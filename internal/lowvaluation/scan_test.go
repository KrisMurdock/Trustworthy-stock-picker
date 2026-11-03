package lowvaluation

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

type fakeQuotes struct {
	quotes []Quote
	err    error
}

func (f fakeQuotes) LatestQuotes(context.Context, string) ([]Quote, error) {
	return f.quotes, f.err
}

type fakeFundamentals struct {
	snapshots map[string]market.FundamentalSnapshot
	errs      map[string]error
}

func (f fakeFundamentals) FetchFundamentals(_ context.Context, stock config.Stock) (market.FundamentalSnapshot, error) {
	if err := f.errs[stock.Code]; err != nil {
		return market.FundamentalSnapshot{}, err
	}
	snapshot := f.snapshots[stock.Code]
	snapshot.Code = stock.Code
	return snapshot, nil
}

func testSnapshot() market.FundamentalSnapshot {
	return market.FundamentalSnapshot{
		Version: 1, Source: "sina", FetchedAt: time.Date(2026, 10, 31, 18, 0, 0, 0, market.Shanghai),
		EPS: 1, EPSPrior: 0.9, BookValuePerShare: 10, GrowthKnown: true,
		FinancialStatementBasis: "trailing_four_quarters",
	}
}

func TestScanRanksClosestLowerBoundAndFilters(t *testing.T) {
	quotes := fakeQuotes{quotes: []Quote{
		{Code: "sh600001", Name: "接近区间", PriceDate: "2026-10-31", Close: 13, Volume: 1000},
		{Code: "sh600002", Name: "深度折价", PriceDate: "2026-10-31", Close: 10, Volume: 1000},
		{Code: "sh600003", Name: "区间上方", PriceDate: "2026-10-31", Close: 15, Volume: 1000},
		{Code: "sh600004", Name: "停牌旧价", PriceDate: "2026-10-30", Close: 8, Volume: 1000},
	}}
	fundamentals := fakeFundamentals{snapshots: map[string]market.FundamentalSnapshot{
		"sh600001": testSnapshot(), "sh600002": testSnapshot(), "sh600003": testSnapshot(),
	}}
	rec, err := Scan(context.Background(), quotes, fundamentals, Options{
		AsOf: "2026-10-31", Target: "2026-11-02", TopN: 2, Workers: 2,
		Sectors: map[string]string{"sh600001": "制造业", "sh600002": "制造业", "sh600003": "制造业"},
		Now:     func() time.Time { return time.Date(2026, 10, 31, 19, 0, 0, 0, market.Shanghai) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.UniverseTotal != 4 || rec.Priced != 3 || rec.Valued != 3 || rec.Eligible != 2 || rec.Selected != 2 {
		t.Fatalf("counts = %+v", rec)
	}
	if len(rec.Candidates) != 2 || rec.Candidates[0].Code != "sh600001" || rec.Candidates[1].Code != "sh600002" {
		t.Fatalf("ranking = %+v", rec.Candidates)
	}
	if rec.Candidates[0].GapToLowerPct <= 0 || rec.Candidates[0].GapToLowerPct >= rec.Candidates[1].GapToLowerPct {
		t.Fatalf("gap ranking = %+v", rec.Candidates)
	}
	if rec.Candidates[0].Estimate.Confidence != "medium" || rec.Candidates[0].Sector != "制造业" {
		t.Fatalf("candidate valuation = %+v", rec.Candidates[0])
	}
	gotFiltered := map[string]int{}
	for _, item := range rec.Filtered {
		gotFiltered[item.Reason] = item.Count
	}
	if gotFiltered["not_below_lower"] != 1 || gotFiltered["stale_or_suspended"] != 1 {
		t.Fatalf("filtered = %+v", rec.Filtered)
	}
	if rec.SectorCoveragePct != 100 || rec.ID != "low_valuation-2026-10-31-2" {
		t.Fatalf("metadata = %+v", rec)
	}
}

func TestScanKeepsFundamentalFailuresOutOfCandidates(t *testing.T) {
	quotes := fakeQuotes{quotes: []Quote{
		{Code: "sh600001", Name: "失败", PriceDate: "2026-10-31", Close: 10, Volume: 1000},
	}}
	fundamentals := fakeFundamentals{errs: map[string]error{"sh600001": errors.New("upstream timeout")}}
	rec, err := Scan(context.Background(), quotes, fundamentals, Options{AsOf: "2026-10-31", Target: "2026-11-02", TopN: 20})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Priced != 1 || rec.Valued != 0 || rec.Selected != 0 || len(rec.Candidates) != 0 {
		t.Fatalf("result = %+v", rec)
	}
	if len(rec.Filtered) != 1 || rec.Filtered[0].Reason != "fundamentals_error" || rec.Filtered[0].Count != 1 {
		t.Fatalf("filtered = %+v", rec.Filtered)
	}
}

func TestScanRejectsInvalidDatesAndTopN(t *testing.T) {
	quotes := fakeQuotes{}
	fundamentals := fakeFundamentals{}
	for _, options := range []Options{
		{AsOf: "bad", Target: "2026-11-02", TopN: 20},
		{AsOf: "2026-10-31", Target: "2026-10-31", TopN: 20},
		{AsOf: "2026-10-31", Target: "2026-11-02", TopN: 0},
	} {
		if _, err := Scan(context.Background(), quotes, fundamentals, options); err == nil {
			t.Fatalf("options %+v were accepted", options)
		}
	}
}

func TestScanRejectsEmptyFreshUniverse(t *testing.T) {
	quotes := fakeQuotes{quotes: []Quote{
		{Code: "sh600001", Name: "停牌", PriceDate: "2026-10-30", Close: 10, Volume: 1000},
	}}
	_, err := Scan(context.Background(), quotes, fakeFundamentals{}, Options{AsOf: "2026-10-31", Target: "2026-11-02", TopN: 20})
	if err == nil {
		t.Fatal("empty fresh universe produced a publishable recommendation")
	}
}

func TestScanRejectsInsufficientFundamentalCoverage(t *testing.T) {
	quotes := make([]Quote, 0, minCoveragePriced)
	fundamentals := fakeFundamentals{errs: map[string]error{}}
	for index := 0; index < minCoveragePriced; index++ {
		code := fmt.Sprintf("sh6%05d", index)
		quotes = append(quotes, Quote{Code: code, Name: code, PriceDate: "2026-10-31", Close: 10, Volume: 1000})
		fundamentals.errs[code] = errors.New("upstream timeout")
	}
	rec, err := Scan(context.Background(), fakeQuotes{quotes: quotes}, fundamentals, Options{AsOf: "2026-10-31", Target: "2026-11-02", TopN: 20})
	if err == nil {
		t.Fatal("wholesale fundamental failure produced a publishable recommendation")
	}
	if rec.Priced != minCoveragePriced || rec.Valued != 0 {
		t.Fatalf("coverage failure result = %+v", rec)
	}
}
