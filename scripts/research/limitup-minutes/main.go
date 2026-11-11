package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

type request struct {
	Code string `json:"code"`
	Date string `json:"date"`
}

type sample struct {
	Version int       `json:"schema_version"`
	Records []request `json:"records"`
}

type result struct {
	Code    string                `json:"code"`
	Date    string                `json:"date"`
	Status  string                `json:"status"`
	Reason  string                `json:"reason,omitempty"`
	Archive *market.MinuteArchive `json:"archive,omitempty"`
}

type minuteFetcher interface {
	FetchMinuteRange(context.Context, config.Stock, string, string) (market.MinuteArchive, error)
}

func failureReason(err error) string {
	var failure *market.Failure
	var invalid *market.DataError
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &failure) && failure.Kind == "baostock_network":
		return "network"
	case errors.As(err, &invalid):
		return "invalid_data"
	default:
		return "upstream_error"
	}
}

func validateSample(input sample) error {
	if input.Version != 1 || len(input.Records) == 0 || len(input.Records) > 100 {
		return errors.New("invalid sample version or size")
	}
	seen := make(map[request]bool, len(input.Records))
	for _, record := range input.Records {
		date, err := time.Parse("2006-01-02", record.Date)
		growth := strings.HasPrefix(record.Code, "sz300") || strings.HasPrefix(record.Code, "sz301") ||
			strings.HasPrefix(record.Code, "sz302") || strings.HasPrefix(record.Code, "sh688")
		if err != nil || date.Format("2006-01-02") != record.Date || date.Year() < 2021 || date.Year() > 2024 ||
			!config.ValidCode(record.Code) || !growth || seen[record] {
			return errors.New("invalid, duplicated or non-development sample event")
		}
		seen[record] = true
	}
	return nil
}

func download(ctx context.Context, fetcher minuteFetcher, records []request, output io.Writer) error {
	encoder := json.NewEncoder(output)
	consecutiveFailures := 0
	failed := 0
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		requestContext, cancel := context.WithTimeout(ctx, 20*time.Second)
		archive, err := fetcher.FetchMinuteRange(requestContext, config.Stock{Code: record.Code}, record.Date, record.Date)
		cancel()
		entry := result{Code: record.Code, Date: record.Date, Status: "ok", Archive: &archive}
		if err != nil {
			entry.Status, entry.Archive = "fetch_failed", nil
			entry.Reason = failureReason(err)
		} else if len(archive.Bars) == 0 {
			entry.Status = "empty"
		}
		if entry.Status == "ok" {
			consecutiveFailures = 0
		} else {
			failed++
			consecutiveFailures++
		}
		if err := encoder.Encode(entry); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if consecutiveFailures >= 3 {
			return errors.New("three consecutive minute requests failed; partial output retained")
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d minute requests failed; partial output retained", failed)
	}
	return nil
}

func run() error {
	samplePath := flag.String("sample", "", "frozen development sample JSON")
	outputPath := flag.String("output", "", "new minute archive JSONL")
	flag.Parse()
	if *samplePath == "" || *outputPath == "" || flag.NArg() != 0 {
		return errors.New("sample and output are required")
	}
	inputFile, err := os.Open(*samplePath)
	if err != nil {
		return err
	}
	defer inputFile.Close()
	payload, err := io.ReadAll(io.LimitReader(inputFile, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(payload) > 1<<20 {
		return errors.New("sample is too large")
	}
	var input sample
	if err := json.Unmarshal(payload, &input); err != nil {
		return err
	}
	if err := validateSample(input); err != nil {
		return err
	}
	output, err := os.OpenFile(*outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer output.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	client := market.NewBaoStockClient(10*time.Second, time.Second)
	defer client.Close()
	downloadErr := download(ctx, client, input.Records, output)
	return errors.Join(downloadErr, output.Sync())
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
