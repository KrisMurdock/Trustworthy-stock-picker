package calendar

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"stock-watcher/internal/market"
)

func testHistory(t *testing.T, source, from, through string, days []string) History {
	t.Helper()
	history, err := NewHistory(source, from, through, days, time.Date(2026, 10, 19, 12, 0, 0, 0, market.Shanghai))
	if err != nil {
		t.Fatal(err)
	}
	return history
}

func TestHistoryDigestAndHardCoverage(t *testing.T) {
	history := testHistory(t, SourceBaoStock, "2026-01-01", "2026-01-31",
		[]string{"2026-01-05", "2026-01-02", "2026-01-06"})
	if err := history.Validate(); err != nil {
		t.Fatal(err)
	}
	if history.Days[0] != "2026-01-02" || history.Digest == "" {
		t.Fatalf("history was not canonicalized: %+v", history)
	}
	tampered := history
	tampered.Days = append([]string{}, history.Days...)
	tampered.Days[0] = "2026-01-03"
	if tampered.Validate() == nil {
		t.Fatal("tampered calendar digest accepted")
	}

	coverage, err := history.Coverage("2026-01-01", "2026-01-31")
	if err != nil || !coverage.Complete || coverage.Status != CoverageComplete {
		t.Fatalf("complete window rejected: %+v %v", coverage, err)
	}
	coverage, err = history.Coverage("2025-12-30", "2026-02-02")
	if err != nil || coverage.Complete || coverage.Status != CoveragePartial || len(coverage.Missing) != 2 {
		t.Fatalf("partial window not reported: %+v %v", coverage, err)
	}
	if _, err := history.TradingDays("2025-12-30", "2026-01-05"); err == nil {
		t.Fatal("incomplete calendar returned trading days")
	} else {
		var coverageErr *CoverageError
		if !errors.As(err, &coverageErr) || coverageErr.Coverage.Complete {
			t.Fatalf("wrong hard-failure type: %v", err)
		}
	}
	if _, err := NewHistory(SourceBaoStock, "2026-01-01", "2026-01-31", []string{"2026-01-02", "2026-01-02"}, time.Now()); err == nil {
		t.Fatal("duplicate trading day accepted")
	}
}

func TestDatasetFreezesAdjacentWindows(t *testing.T) {
	january := testHistory(t, SourceBaoStock, "2026-01-01", "2026-01-31", []string{"2026-01-02", "2026-01-05"})
	february := testHistory(t, SourceTushare, "2026-02-01", "2026-02-28", []string{"2026-02-02", "2026-02-03"})
	dataset := Dataset{january, february}
	coverage, err := dataset.Coverage("2026-01-01", "2026-02-28")
	if err != nil || !coverage.Complete {
		t.Fatalf("adjacent windows rejected: %+v %v", coverage, err)
	}
	frozen, err := dataset.Freeze("2026-01-01", "2026-02-28")
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Source != "baostock+tushare" || len(frozen.Days) != 4 {
		t.Fatalf("wrong frozen provenance: %+v", frozen)
	}
	if err := frozen.Validate(); err != nil {
		t.Fatal(err)
	}
	coverage, err = dataset.Coverage("2026-01-15", "2026-02-15")
	if err != nil || !coverage.Complete {
		t.Fatalf("subrange inside adjacent windows rejected: %+v %v", coverage, err)
	}
}

func TestOverlappingCalendarWindowsMustAgree(t *testing.T) {
	left := testHistory(t, SourceBaoStock, "2026-01-01", "2026-01-31", []string{"2026-01-02", "2026-01-05"})
	right := testHistory(t, SourceTushare, "2026-01-15", "2026-02-15", []string{"2026-01-16", "2026-01-20"})
	dataset := Dataset{left, right}
	if _, err := dataset.Coverage("2026-01-01", "2026-02-15"); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Fatalf("conflicting overlap was merged: %v", err)
	}
	if _, err := dataset.TradingDays("2026-01-01", "2026-02-15"); err == nil {
		t.Fatal("conflicting overlap returned trading days")
	}
	if _, err := dataset.Freeze("2026-01-01", "2026-02-15"); err == nil {
		t.Fatal("conflicting overlap was frozen into a snapshot calendar")
	}
}

type fakeCalendarSource struct {
	days []string
	err  error
}

func (f fakeCalendarSource) TradingDays(context.Context, string, string) ([]string, error) {
	return f.days, f.err
}

func TestSourcesFallbackAndCancellation(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, market.Shanghai)
	sources := Sources{
		Providers: []Provider{
			{Name: SourceBaoStock, Source: fakeCalendarSource{err: errors.New("primary unavailable")}},
			{Name: SourceTushare, Source: fakeCalendarSource{days: []string{"2026-01-05"}}},
		},
		Now: func() time.Time { return now },
	}
	history, err := sources.Fetch(context.Background(), "2026-01-01", "2026-01-31")
	if err != nil || history.Source != SourceTushare || len(history.Days) != 1 || !history.FetchedAt.Equal(now) {
		t.Fatalf("fallback failed: %+v %v", history, err)
	}

	sources.Providers[0].Source = fakeCalendarSource{days: []string{"2026-01-05", "2026-01-05"}}
	history, err = sources.Fetch(context.Background(), "2026-01-01", "2026-01-31")
	if err != nil || history.Source != SourceTushare {
		t.Fatalf("invalid primary output did not fall back: %+v %v", history, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sources.Fetch(ctx, "2026-01-01", "2026-01-31"); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}

	_, err = Sources{}.Fetch(context.Background(), "2026-01-01", "2026-01-31")
	if err == nil || !strings.Contains(err.Error(), "no trading calendar sources") {
		t.Fatalf("missing provider error: %v", err)
	}
}
