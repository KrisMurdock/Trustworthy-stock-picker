package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

type fakeFetcher struct {
	archive market.MinuteArchive
	err     error
}

type blockingFetcher struct{ started chan struct{} }

func (f blockingFetcher) FetchMinuteRange(ctx context.Context, _ config.Stock, _, _ string) (market.MinuteArchive, error) {
	close(f.started)
	<-ctx.Done()
	return market.MinuteArchive{}, ctx.Err()
}

func (f fakeFetcher) FetchMinuteRange(ctx context.Context, _ config.Stock, _, _ string) (market.MinuteArchive, error) {
	if err := ctx.Err(); err != nil {
		return market.MinuteArchive{}, err
	}
	return f.archive, f.err
}

func TestValidateSampleRejectsDuplicatesAndReservedYears(t *testing.T) {
	record := request{Code: "sz300031", Date: "2023-04-28"}
	for _, input := range []sample{
		{Version: 1, Records: []request{record, record}},
		{Version: 1, Records: []request{{Code: "sz300031", Date: "2025-01-02"}}},
		{Version: 1, Records: []request{{Code: "sh600001", Date: "2023-04-28"}}},
		{Version: 1, Records: []request{{Code: "sz300031", Date: "2023-02-30"}}},
	} {
		if err := validateSample(input); err == nil {
			t.Fatalf("accepted invalid sample: %#v", input)
		}
	}
	if err := validateSample(sample{Version: 1, Records: []request{record}}); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadRecordsSuccessAndStopsOnRepeatedFailures(t *testing.T) {
	records := []request{{Code: "sz300031", Date: "2023-04-28"}}
	var output bytes.Buffer
	archive := market.MinuteArchive{Bars: []market.MinuteBar{{Open: 1}}}
	if err := download(context.Background(), fakeFetcher{archive: archive}, records, &output); err != nil {
		t.Fatal(err)
	}
	var entry result
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil || entry.Status != "ok" || entry.Archive == nil {
		t.Fatalf("unexpected successful entry: %#v, %v", entry, err)
	}
	output.Reset()
	failedRecords := append(append(append([]request{}, records...), records...), records...)
	failedRecords = append(failedRecords, records...)
	err := download(context.Background(), fakeFetcher{err: errors.New("upstream error")}, failedRecords, &output)
	if err == nil || strings.Count(output.String(), "\n") != 3 || strings.Contains(output.String(), "upstream error") {
		t.Fatalf("unexpected failure behavior: %v, %q", err, output.String())
	}
}

func TestDownloadHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	err := download(ctx, fakeFetcher{}, []request{{Code: "sz300031", Date: "2023-04-28"}}, &output)
	if !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatalf("expected cancellation with no output, got %v and %q", err, output.String())
	}
}

func TestDownloadCancelsAnInFlightRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	completed := make(chan error, 1)
	var output bytes.Buffer
	go func() {
		completed <- download(ctx, blockingFetcher{started: started}, []request{{Code: "sz300031", Date: "2023-04-28"}}, &output)
	}()
	<-started
	cancel()
	select {
	case err := <-completed:
		if !errors.Is(err, context.Canceled) || !strings.Contains(output.String(), `"reason":"cancelled"`) {
			t.Fatalf("expected recorded cancellation, got %v and %q", err, output.String())
		}
	case <-time.After(time.Second):
		t.Fatal("download did not stop after cancellation")
	}
}

func TestFailureReasonDoesNotExposeRawErrors(t *testing.T) {
	if got := failureReason(context.DeadlineExceeded); got != "timeout" {
		t.Fatalf("timeout classified as %q", got)
	}
	if got := failureReason(&market.Failure{Kind: "baostock_network"}); got != "network" {
		t.Fatalf("network failure classified as %q", got)
	}
	if got := failureReason(errors.New("credential-looking detail")); got != "upstream_error" {
		t.Fatalf("raw error leaked as %q", got)
	}
}
