// bottom-study is an offline research utility. It reads an immutable experiment
// snapshot and writes a deterministic JSON report; it never opens a live store.
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
	"stock-watcher/internal/bottomstudy"
	"stock-watcher/internal/experiment"
	"syscall"
	"time"
)

var version = "development"

type output struct {
	SnapshotID   string `json:"snapshot_id"`
	SnapshotSHA  string `json:"snapshot_file_sha256"`
	CodeVersion  string `json:"code_version"`
	PoolSize     int    `json:"pool_size"`
	Excluded     int    `json:"excluded_stocks"`
	SnapshotAsOf string `json:"snapshot_as_of"`
	bottomstudy.Report
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("bottom-study", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("snapshot", "", "immutable experiment snapshot JSON (required)")
	from := flags.String("from", "", "first signal date YYYY-MM-DD (required)")
	through := flags.String("through", "", "last observation date YYYY-MM-DD (required)")
	optimize := flags.Bool("optimize", false, "run the fixed net-return, batch-count and stop grid")
	contextPath := flags.String("compare-context", "", "frozen sector/market JSON for the two-arm comparison")
	trainThrough := flags.String("train-through", "", "last training outcome date (required with optimize or compare-context)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" || *from == "" || *through == "" || flags.NArg() != 0 {
		return errors.New("snapshot, from and through are required; no positional arguments allowed")
	}
	if *optimize && *contextPath != "" {
		return errors.New("optimize and compare-context are mutually exclusive")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 32<<20 {
		return errors.New("snapshot exceeds 32 MiB")
	}
	var snapshot experiment.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return errors.New("invalid snapshot JSON")
	}
	if err := experiment.ValidateSnapshot(snapshot); err != nil {
		return fmt.Errorf("invalid snapshot: %w", err)
	}
	if *contextPath != "" {
		contextFile, err := os.Open(*contextPath)
		if err != nil {
			return err
		}
		defer contextFile.Close()
		contextData, err := io.ReadAll(io.LimitReader(contextFile, (32<<20)+1))
		if err != nil || len(contextData) > 32<<20 {
			return errors.New("context cannot be read or exceeds 32 MiB")
		}
		var background bottomstudy.MarketContext
		decoder := json.NewDecoder(bytes.NewReader(contextData))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&background) != nil || decoder.Decode(new(any)) != io.EOF {
			return errors.New("invalid context JSON or unsupported fields")
		}
		report, err := bottomstudy.CompareContext(ctx, snapshot, background, bottomstudy.GridOptions{Options: bottomstudy.Options{From: *from, Through: *through}, TrainThrough: *trainThrough})
		if err != nil {
			return err
		}
		stockHash, contextHash := sha256.Sum256(data), sha256.Sum256(contextData)
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(struct {
			CodeVersion string `json:"code_version"`
			StockSHA    string `json:"stock_snapshot_file_sha256"`
			ContextSHA  string `json:"context_file_sha256"`
			bottomstudy.ComparisonReport
		}{version, hex.EncodeToString(stockHash[:]), hex.EncodeToString(contextHash[:]), report})
	}
	if *optimize {
		report, err := bottomstudy.Optimize(ctx, snapshot, bottomstudy.GridOptions{Options: bottomstudy.Options{From: *from, Through: *through}, TrainThrough: *trainThrough})
		if err != nil {
			return err
		}
		hash := sha256.Sum256(data)
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(struct {
			CodeVersion string `json:"code_version"`
			SnapshotSHA string `json:"snapshot_file_sha256"`
			bottomstudy.GridReport
		}{version, hex.EncodeToString(hash[:]), report})
	}
	if *trainThrough != "" {
		return errors.New("train-through requires optimize or compare-context")
	}
	report, err := bottomstudy.Analyze(ctx, snapshot.Adjusted, bottomstudy.Options{From: *from, Through: *through})
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output{snapshot.ID, hex.EncodeToString(hash[:]), version, snapshot.PoolSize, len(snapshot.Excluded), snapshot.AsOf, report})
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, stop := context.WithTimeout(ctx, 2*time.Minute)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
