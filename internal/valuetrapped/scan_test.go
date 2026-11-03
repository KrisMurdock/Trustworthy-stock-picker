package valuetrapped

import (
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/lowvaluation"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/trappedscan"
)

func lowCandidateFixture(code string, close, gap float64) lowvaluation.Candidate {
	return lowvaluation.Candidate{
		Code: code, Name: code, Sector: "制造业", PriceDate: "2026-10-31", Close: close,
		GapToLowerPct: gap, UpsideToCentralPct: (16 - close) / close * 100,
		Estimate: fairvalue.Estimate{Code: code, AsOf: "2026-10-31", CurrentPrice: close, Lower: 20, Central: 24, Upper: 28},
	}
}

func lowRecommendationFixture(candidates ...lowvaluation.Candidate) lowvaluation.Recommendation {
	top := candidates
	if len(top) > 2 {
		top = top[:2]
	}
	return lowvaluation.Recommendation{
		Version: lowvaluation.SchemaVersion, ID: "low_valuation-2026-10-31-20", Market: lowvaluation.Market,
		AsOf: "2026-10-31", Target: "2026-11-02", GeneratedAt: time.Date(2026, 10, 31, 19, 0, 0, 0, time.UTC),
		UniverseTotal: 5000, Priced: 4800, Valued: 4300, Eligible: len(candidates), Selected: len(top), TopN: 20,
		Candidates: top, AllCandidates: candidates, SectorCoveragePct: 99,
		Filtered:    []lowvaluation.Filtered{{Reason: "not_below_lower", Count: 4000}},
		Limitations: []string{"财务区间不是目标价。"},
	}
}

func trappedSignalFixture(ratio float64) *strategy.TrappedSignal {
	return &strategy.TrappedSignal{
		Version: strategy.TrappedSignalVersion, Method: strategy.TrappedSignalMethod, Status: strategy.TrappedStatusNone,
		CurrentPrice: 18, TrappedVolumeRatio: ratio, CurrentZone: strategy.TrappedZoneShallow, DominantZone: strategy.TrappedZoneShallow,
	}
}

func trappedResultFixture(candidates ...trappedscan.Candidate) trappedscan.Result {
	return trappedscan.Result{
		SchemaVersion: trappedscan.SchemaVersion, AsOf: "2026-10-31", ValidSignals: len(candidates),
		Top: candidates, Excluded: map[string]int{"low_liquidity": 2},
		UnknownReasons: map[string]int{"turnover_history_unavailable": 1},
	}
}

func TestBuildIntersectsBothScreensAndUsesDeterministicOrder(t *testing.T) {
	low := lowRecommendationFixture(
		lowCandidateFixture("sh600001", 18, 2),
		lowCandidateFixture("sh600002", 18, 5),
		lowCandidateFixture("sh600003", 18, 5),
		lowCandidateFixture("sh600004", 18, 5),
		lowCandidateFixture("sh600005", 18, 5),
		lowCandidateFixture("sh600006", 18, 5),
	)
	trapped := trappedResultFixture(
		trappedscan.Candidate{Code: "sh600001", AvgAmount20CNY: 100_000_000, Trapped: trappedSignalFixture(0.25)},
		trappedscan.Candidate{Code: "sh600002", AvgAmount20CNY: 200_000_000, Trapped: trappedSignalFixture(0.10)},
		trappedscan.Candidate{Code: "sh600003", AvgAmount20CNY: 100_000_000, Trapped: trappedSignalFixture(0.10)},
		trappedscan.Candidate{Code: "sh600004", AvgAmount20CNY: 300_000_000, Trapped: trappedSignalFixture(0.20)},
		trappedscan.Candidate{Code: "sh600005", AvgAmount20CNY: 300_000_000, Trapped: trappedSignalFixture(0.31)},
	)
	rec, err := Build(low, trapped, Options{TopN: 3, MaxTrappedRatio: 0.30,
		Now: func() time.Time { return time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	if rec.LowValuationEligible != 6 || rec.TrappedValid != 5 || rec.Eligible != 4 || rec.Selected != 3 {
		t.Fatalf("counts = %+v", rec)
	}
	want := []string{"sh600001", "sh600002", "sh600003"}
	for index, code := range want {
		if rec.Candidates[index].Code != code || rec.Candidates[index].Rank != index+1 {
			t.Fatalf("ranking = %+v", rec.Candidates)
		}
	}
	if rec.Filtered[len(rec.Filtered)-1].Reason == "" {
		t.Fatalf("filtered = %+v", rec.Filtered)
	}
	filtered := map[string]int{}
	for _, item := range rec.Filtered {
		filtered[item.Reason] = item.Count
	}
	if filtered["not_below_lower"] != 4000 || filtered["low_liquidity"] != 2 || filtered["trapped_ratio_above_limit"] != 1 {
		t.Fatalf("filtered = %+v", rec.Filtered)
	}
	if rec.TrappedUnknownReasons["turnover_history_unavailable"] != 1 || rec.MaxTrappedRatioPct != 30 || rec.ID != "value_trapped-2026-10-31-3" {
		t.Fatalf("metadata = %+v", rec)
	}
}

func TestBuildDoesNotFillFromOneSide(t *testing.T) {
	low := lowRecommendationFixture(lowCandidateFixture("sh600001", 18, 2))
	rec, err := Build(low, trappedResultFixture(), Options{TopN: 20, MaxTrappedRatio: 0.30})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Eligible != 0 || rec.Selected != 0 || len(rec.Candidates) != 0 {
		t.Fatalf("empty intersection was filled: %+v", rec)
	}
	if _, err := Build(low, trappedResultFixture(), Options{TopN: 20, MaxTrappedRatio: 0.30,
		Now: func() time.Time { return time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC) }}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildOnlyIncludesValidStocksFromBothScreens(t *testing.T) {
	low := lowRecommendationFixture(
		lowCandidateFixture("sh600001", 18, 2),
		lowCandidateFixture("sh600002", 18, 3),
	)
	unknown := trappedSignalFixture(0.10)
	unknown.Status = strategy.TrappedStatusUnknown
	trapped := trappedResultFixture(
		trappedscan.Candidate{Code: "sh600001", AvgAmount20CNY: 100_000_000, Trapped: trappedSignalFixture(0.10)},
		trappedscan.Candidate{Code: "sh600002", AvgAmount20CNY: 100_000_000, Trapped: unknown},
		trappedscan.Candidate{Code: "sh600003", AvgAmount20CNY: 100_000_000, Trapped: trappedSignalFixture(0.05)},
	)
	trapped.ValidSignals = 2
	rec, err := Build(low, trapped, Options{TopN: 20, MaxTrappedRatio: 0.30,
		Now: func() time.Time { return time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Eligible != 1 || rec.Selected != 1 || len(rec.Candidates) != 1 || rec.Candidates[0].Code != "sh600001" {
		t.Fatalf("intersection = %+v", rec)
	}
}

func TestBuildRejectsMismatchedDates(t *testing.T) {
	low := lowRecommendationFixture(lowCandidateFixture("sh600001", 18, 2))
	trapped := trappedResultFixture()
	trapped.AsOf = "2026-11-01"
	if _, err := Build(low, trapped, Options{TopN: 20, MaxTrappedRatio: 0.30}); err == nil {
		t.Fatal("mismatched snapshot dates were accepted")
	}
}
