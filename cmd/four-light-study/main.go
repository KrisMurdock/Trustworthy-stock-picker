// four-light-study evaluates a frozen all-market SQLite backup. It never
// contacts data providers or writes production/experiment databases.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fourlightstudy"
)

var version = "development"

type output struct {
	PriceEpisodes        *fourlightstudy.PriceEpisodeReport  `json:"price_episodes,omitempty"`
	TriggerTiming        *fourlightstudy.TriggerTimingReport `json:"trigger_timing,omitempty"`
	ClassificationSHA256 string                              `json:"classification_file_sha256,omitempty"`
	SectorContext        *fourlightstudy.SectorContextReport `json:"sector_context,omitempty"`
	CodeVersion          string                              `json:"code_version"`
	ManifestSHA256       string                              `json:"manifest_file_sha256"`
	DatabaseSHA256       string                              `json:"database_sha256"`
	DatabaseSchema       int                                 `json:"database_schema"`
	Report               fourlightstudy.Report               `json:"report"`
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("four-light-study", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "frozen research manifest JSON (required; at most 1 MiB)")
	databasePath := flags.String("database", "", "standalone frozen all-market SQLite backup (required)")
	classificationPath := flags.String("classification", "", "frozen industry classification JSON (optional; at most 8 MiB; requires manifest v2)")
	classificationSHA := flags.String("classification-sha256", "", "expected lowercase SHA256 of classification JSON (required with -classification)")
	triggerTiming := flags.Bool("trigger-timing", false, "diagnose first-all-red timing with frozen classification; requires manifest v2")
	priceEpisodes := flags.Bool("price-episodes", false, "compare first and repeated triggers in continuous price-red episodes; includes trigger-timing")
	eventsPath := flags.String("events", "", "new JSONL file for all-red events; existing files are never overwritten")
	timeout := flags.Duration("timeout", 10*time.Minute, "complete run timeout, greater than zero and at most 1h")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *manifestPath == "" || *databasePath == "" || flags.NArg() != 0 {
		return errors.New("manifest and database are required; no positional arguments allowed")
	}
	if (*classificationPath == "") != (*classificationSHA == "") {
		return errors.New("classification and classification-sha256 must be supplied together")
	}
	if *triggerTiming && *classificationPath == "" {
		return errors.New("trigger-timing requires classification and classification-sha256")
	}
	if *priceEpisodes && *classificationPath == "" {
		return errors.New("price-episodes requires classification and classification-sha256")
	}
	if *timeout <= 0 || *timeout > time.Hour {
		return errors.New("timeout must be greater than zero and at most 1h")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	manifest, manifestSHA, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	var sectors fourlightstudy.SectorSnapshot
	if *classificationPath != "" {
		if manifest.Version != 2 {
			return errors.New("sector context requires manifest version 2")
		}
		sectors, err = readClassification(*classificationPath, *classificationSHA)
		if err != nil {
			return err
		}
	}
	var events *eventOutput
	if *eventsPath != "" {
		events, err = newEventOutput(*eventsPath, *manifestPath, *databasePath, *classificationPath)
		if err != nil {
			return err
		}
		defer events.cleanup()
	}
	source, err := fourlightstudy.OpenSnapshot(ctx, *databasePath, manifest)
	if err != nil {
		return err
	}
	defer source.Close()
	var emit func(fourlightstudy.Event) error
	if events != nil {
		encoder := json.NewEncoder(events.file)
		emit = func(event fourlightstudy.Event) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return encoder.Encode(event)
		}
	}
	var report fourlightstudy.Report
	var sectorContext *fourlightstudy.SectorContextReport
	var triggerReport *fourlightstudy.TriggerTimingReport
	var episodeReport *fourlightstudy.PriceEpisodeReport
	if *priceEpisodes {
		report, sectorContext, triggerReport, episodeReport, err = fourlightstudy.AnalyzePriceEpisodeStudy(ctx, manifest, source, sectors, emit)
	} else if *triggerTiming {
		report, sectorContext, triggerReport, err = fourlightstudy.AnalyzeTriggerTiming(ctx, manifest, source, sectors, emit)
	} else if *classificationPath != "" {
		report, sectorContext, err = fourlightstudy.AnalyzeSectorContext(ctx, manifest, source, sectors, emit)
	} else {
		report, err = fourlightstudy.Analyze(ctx, manifest, source, emit)
	}
	if err != nil {
		return err
	}
	result := output{
		CodeVersion: version, ManifestSHA256: manifestSHA, DatabaseSHA256: source.DatabaseSHA256(),
		DatabaseSchema: source.SchemaVersion(), Report: report,
		ClassificationSHA256: *classificationSHA, SectorContext: sectorContext, TriggerTiming: triggerReport,
		PriceEpisodes: episodeReport,
	}
	if err := source.Close(); err != nil {
		return fmt.Errorf("close snapshot: %w", err)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if events != nil {
		if err := events.publish(); err != nil {
			return err
		}
	}
	data = append(data, '\n')
	if written, err := stdout.Write(data); err != nil {
		return err
	} else if written != len(data) {
		return io.ErrShortWrite
	}
	if events != nil {
		events.complete = true
	}
	return nil
}

func readManifest(path string) (fourlightstudy.Manifest, string, error) {
	var manifest fourlightstudy.Manifest
	file, err := os.Open(path)
	if err != nil {
		return manifest, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return manifest, "", errors.New("manifest must be a regular JSON file")
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return manifest, "", err
	}
	if len(data) > 1<<20 {
		return manifest, "", errors.New("manifest exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, "", fmt.Errorf("invalid manifest JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return manifest, "", errors.New("manifest must contain exactly one JSON object")
	}
	if err := manifest.Validate(); err != nil {
		return manifest, "", fmt.Errorf("invalid manifest: %w", err)
	}
	hash := sha256.Sum256(data)
	return manifest, hex.EncodeToString(hash[:]), nil
}

func readClassification(path, expected string) (fourlightstudy.SectorSnapshot, error) {
	var snapshot fourlightstudy.SectorSnapshot
	hash, err := hex.DecodeString(expected)
	if err != nil || len(hash) != sha256.Size || strings.ToLower(expected) != expected {
		return snapshot, errors.New("classification-sha256 must be a lowercase SHA256 digest")
	}
	file, err := os.Open(path)
	if err != nil {
		return snapshot, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return snapshot, errors.New("classification must be a regular JSON file")
	}
	data, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil {
		return snapshot, err
	}
	if len(data) > 8<<20 {
		return snapshot, errors.New("classification exceeds 8 MiB")
	}
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != expected {
		return snapshot, errors.New("classification SHA256 mismatch")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return snapshot, fmt.Errorf("invalid classification JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return snapshot, errors.New("classification must contain exactly one JSON object")
	}
	if err := snapshot.Validate(); err != nil {
		return snapshot, fmt.Errorf("invalid classification: %w", err)
	}
	return snapshot, nil
}

type eventOutput struct {
	file      *os.File
	temporary string
	final     string
	published bool
	complete  bool
}

func newEventOutput(path string, inputs ...string) (*eventOutput, error) {
	final, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	for _, input := range inputs {
		if input == "" {
			continue
		}
		absolute, err := filepath.Abs(input)
		if err != nil {
			return nil, err
		}
		if absolute == final {
			return nil, errors.New("events output must not replace an input file")
		}
	}
	if _, err := os.Lstat(final); err == nil {
		return nil, errors.New("events output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.CreateTemp(filepath.Dir(final), ".four-light-events-*.tmp")
	if err != nil {
		return nil, err
	}
	return &eventOutput{file: file, temporary: file.Name(), final: final}, nil
}

func (e *eventOutput) publish() error {
	if err := e.file.Sync(); err != nil {
		return fmt.Errorf("sync events: %w", err)
	}
	if err := e.file.Close(); err != nil {
		return fmt.Errorf("close events: %w", err)
	}
	// A same-directory hard link publishes atomically without overwriting a file
	// created since validation. Rename would silently overwrite that file.
	if err := os.Link(e.temporary, e.final); err != nil {
		return fmt.Errorf("publish events without replacement: %w", err)
	}
	e.published = true
	return nil
}

func (e *eventOutput) cleanup() {
	_ = e.file.Close()
	if e.published && !e.complete {
		original, originalErr := os.Stat(e.temporary)
		current, currentErr := os.Lstat(e.final)
		if originalErr == nil && currentErr == nil && os.SameFile(original, current) {
			_ = os.Remove(e.final)
		}
	}
	_ = os.Remove(e.temporary)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
