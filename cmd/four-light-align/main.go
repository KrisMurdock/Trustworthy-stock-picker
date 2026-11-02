// four-light-align is an offline research utility. It compares a continuous
// target-software light sample with the local price/volume approximation and
// never writes market data, reports or notifications.
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
	"sort"
	"syscall"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fourlightalign"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

var version = "development"

const maxReferenceBytes = 16 << 20

type output struct {
	CodeVersion     string                `json:"code_version"`
	ReferenceSHA256 string                `json:"reference_sha256"`
	Alignment       fourlightalign.Report `json:"alignment"`
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("four-light-align", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "stock-watcher data directory containing market.db (required)")
	reference := flags.String("reference", "", "continuous target light CSV (required)")
	software := flags.String("software", "", "target software name (required)")
	softwareVersion := flags.String("software-version", "", "target software version (required)")
	period := flags.String("period", "", "target period; only daily is supported (required)")
	adjust := flags.String("adjust", "", "target price basis; only qfq is supported (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("no positional arguments are allowed")
	}
	if *dataDir == "" || *reference == "" || *software == "" || *softwareVersion == "" || *period == "" || *adjust == "" {
		return errors.New("data-dir, reference, software, software-version, period and adjust are required")
	}
	if *period != "daily" || *adjust != "qfq" {
		return errors.New("four-light alignment currently supports daily qfq samples only")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	file, err := os.Open(*reference)
	if err != nil {
		return errors.New("reference sample cannot be opened")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxReferenceBytes+1))
	if err != nil {
		return errors.New("reference sample cannot be read")
	}
	if len(data) > maxReferenceBytes {
		return errors.New("reference sample exceeds 16 MiB")
	}
	observations, err := fourlightalign.ReadCSV(ctx, bytes.NewReader(data))
	if err != nil {
		return err
	}

	database, err := storage.OpenMarketView(*dataDir, false)
	if err != nil {
		return err
	}
	defer database.Close()
	if !database.HasDatabase() {
		return errors.New("market database is not initialized")
	}
	latestByCode := map[string]string{}
	for _, observation := range observations {
		if latestByCode[observation.Code] < observation.Date {
			latestByCode[observation.Code] = observation.Date
		}
	}
	codes := make([]string, 0, len(latestByCode))
	for code := range latestByCode {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	histories := make(map[string]market.History, len(codes))
	for _, code := range codes {
		history, err := database.LoadLatestHistory(ctx, code, "qfq", latestByCode[code])
		if err != nil {
			return fmt.Errorf("cannot load qfq history for %s: %w", code, err)
		}
		histories[code] = history
	}
	report, err := fourlightalign.Compare(observations, fourlightalign.Target{
		Software: *software, Version: *softwareVersion, Period: *period, Adjust: *adjust,
	}, histories)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output{CodeVersion: version, ReferenceSHA256: hex.EncodeToString(sum[:]), Alignment: report})
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
