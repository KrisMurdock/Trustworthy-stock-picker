package fairvalue

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func testSnapshot() market.FundamentalSnapshot {
	return market.FundamentalSnapshot{
		Version: 1, Code: "sh600519", Source: "sina", FetchedAt: time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC),
		EPS: 1, EPSPrior: 0.8, BookValuePerShare: 10, TTMNetProfitYi: 0.0001, Shares: 10000,
		GrowthKnown: true, FinancialStatementBasis: "trailing_four_quarters",
	}
}

func TestCalculateProducesOrderedResearchBand(t *testing.T) {
	snapshot := testSnapshot()
	estimate := Calculate(config.Stock{Code: "sh600519", Sector: "白酒"}, 20, "2026-10-31", snapshot)
	if err := estimate.Validate(); err != nil {
		t.Fatal(err)
	}
	if !estimate.Available() || estimate.Lower >= estimate.Central || estimate.Central >= estimate.Upper {
		t.Fatalf("invalid band: %+v", estimate)
	}
	if math.Abs(estimate.GrowthPct-25) > 1e-9 || math.Abs(estimate.ROEPct-10) > 1e-9 || math.Abs(estimate.PE-20) > 1e-9 || math.Abs(estimate.PB-2) > 1e-9 {
		t.Fatalf("unexpected multiples: %+v", estimate)
	}
	if estimate.FairPE < 20 || estimate.FairPE > 30 || estimate.FairPB < 1.5 || estimate.FairPB > 6 || estimate.Confidence != "medium" {
		t.Fatalf("unexpected fair multiples: %+v", estimate)
	}
}

func TestCalculateUsesFinancialSectorCaps(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Code = "sh600000"
	estimate := Calculate(config.Stock{Code: snapshot.Code, Sector: "股份制银行"}, 5, "2026-10-31", snapshot)
	if !estimate.Available() || estimate.FairPE < 5 || estimate.FairPE > 10 || estimate.FairPB < 0.4 || estimate.FairPB > 1.2 {
		t.Fatalf("financial caps not applied: %+v", estimate)
	}
}

func TestCalculateRejectsNegativeEarnings(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.EPS = -1
	snapshot.TTMNetProfitYi = -0.0001
	estimate := Calculate(config.Stock{Code: snapshot.Code}, 10, "2026-10-31", snapshot)
	if estimate.Available() || !strings.Contains(estimate.Reason, "每股收益非正") {
		t.Fatalf("negative earnings produced a range: %+v", estimate)
	}
}

func TestCalculateRejectsMissingBookValue(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.BookValuePerShare = 0
	estimate := Calculate(config.Stock{Code: snapshot.Code}, 20, "2026-10-31", snapshot)
	if estimate.Available() || !contains(estimate.Warnings, "book_value_non_positive") || !strings.Contains(estimate.Reason, "每股净资产") {
		t.Fatalf("missing book value produced a PE-only range: %+v", estimate)
	}
}

func TestCalculateMarksMissingPriorEpsAsLowConfidence(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.EPSPrior = 0
	snapshot.GrowthKnown = false
	estimate := Calculate(config.Stock{Code: snapshot.Code}, 20, "2026-10-31", snapshot)
	if !estimate.Available() || estimate.GrowthPct != 0 || !contains(estimate.Warnings, "prior_year_eps_missing") || estimate.Confidence != "low" {
		t.Fatalf("growth fallback failed: %+v", estimate)
	}
	if width := (estimate.Upper - estimate.Lower) / estimate.Central; width < 0.59 {
		t.Fatalf("missing growth did not widen the band: width=%.2f estimate=%+v", width, estimate)
	}
}

func TestCalculateUsesIndependentBookValueAnchor(t *testing.T) {
	estimate := Calculate(config.Stock{Code: "sh600519", Sector: "白酒"}, 20, "2026-10-31", testSnapshot())
	if !estimate.BookValueAnchored || estimate.FairPB <= 0 {
		t.Fatalf("book value anchor not recorded: %+v", estimate)
	}
	// The PB branch must not collapse to EPS multiplied by a constant.
	if math.Abs(estimate.FairPB-2.0) > 0.4 {
		t.Fatalf("ROE-adjusted PB anchor is implausible: %+v", estimate)
	}
}

func TestCalculateRejectsAbnormalROE(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.BookValuePerShare = 1.5
	estimate := Calculate(config.Stock{Code: snapshot.Code}, 20, "2026-10-31", snapshot)
	if estimate.Available() || !contains(estimate.Warnings, "abnormal_roe") || !strings.Contains(estimate.Reason, "净资产收益率") {
		t.Fatalf("abnormal ROE produced a displayed range: %+v", estimate)
	}
}

func TestCalculateRejectsAbnormalPB(t *testing.T) {
	estimate := Calculate(config.Stock{Code: "sh600519"}, 200.01, "2026-10-31", testSnapshot())
	if estimate.Available() || !contains(estimate.Warnings, "abnormal_pb") || !strings.Contains(estimate.Reason, "市净率") {
		t.Fatalf("abnormal PB produced a displayed range: %+v", estimate)
	}
}

func TestCalculateDowngradesNearEPSReconciliationTolerance(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.TTMNetProfitYi = 0.000108
	estimate := Calculate(config.Stock{Code: snapshot.Code}, 20, "2026-10-31", snapshot)
	if !estimate.Available() || estimate.Confidence != "low" || !contains(estimate.Warnings, "eps_reconciliation_near_tolerance") {
		t.Fatalf("near EPS tolerance was not downgraded: %+v", estimate)
	}
}

func TestCalculateRejectsEPSOutsideReconciliationTolerance(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.TTMNetProfitYi = 0.000111
	estimate := Calculate(config.Stock{Code: snapshot.Code}, 20, "2026-10-31", snapshot)
	if estimate.Available() || estimate.Reason == "" {
		t.Fatalf("EPS outside reconciliation tolerance produced a band: %+v", estimate)
	}
}

type stubFundamentalSource struct {
	snapshot market.FundamentalSnapshot
	err      error
}

func (s stubFundamentalSource) FetchFundamentals(context.Context, config.Stock) (market.FundamentalSnapshot, error) {
	return s.snapshot, s.err
}

func TestEstimatorPropagatesSourceFailure(t *testing.T) {
	want := errors.New("upstream unavailable")
	estimator := NewEstimator(stubFundamentalSource{err: want})
	_, err := estimator.Estimate(context.Background(), config.Stock{Code: "sh600519"}, 20, "2026-10-31")
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestEstimatorRejectsInvalidPriceWithoutFetching(t *testing.T) {
	estimator := NewEstimator(stubFundamentalSource{snapshot: testSnapshot()})
	estimate, err := estimator.Estimate(context.Background(), config.Stock{Code: "sh600519"}, 0, "2026-10-31")
	if err != nil || estimate.Available() || estimate.Reason == "" {
		t.Fatalf("invalid price accepted: %+v err=%v", estimate, err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
