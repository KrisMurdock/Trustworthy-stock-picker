package storage

import (
	"context"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fundamentals"
)

func TestFundamentalsFailedSyncPreservesSuccessfulSummary(t *testing.T) {
	ctx := context.Background()
	db, err := InitFundamentals(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fetched := time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)
	period := fundamentalsPeriodFixture("sh600519", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-sync", fetched)
	if _, err := db.AppendSeries(ctx, fundamentalsSeriesFixture(period)); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordSync(ctx, FundamentalsSyncRecord{
		Code: "sh600519", Source: fundamentals.SourceTushare, Status: "failed", LastError: "upstream unavailable",
	}); err != nil {
		t.Fatal(err)
	}
	syncs, err := db.ListSync(ctx)
	if err != nil || len(syncs) != 1 {
		t.Fatalf("list sync: %+v %v", syncs, err)
	}
	got := syncs[0]
	if got.Status != "failed" || got.LastError != "upstream unavailable" || got.Periods != 1 ||
		got.LastReportEnd != "2024-12-31" || got.LastFetchedAt == "" {
		t.Fatalf("failed sync erased successful summary: %+v", got)
	}
}
