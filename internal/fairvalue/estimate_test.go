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
		EPS: 1, EPSPrior: 0.8, BookValuePerShare: 10, TTMNetProfitCNY: 0.0001, Shares: 10000,
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
	if estimate.FairPE < 20 || estimate.FairPB < 1.9 || estimate.Confidence != "medium" {
		t.Fatalf("unexpected fair multiples: %+v", estimate)
	}
}

func TestCalculateUsesFinancialSectorCaps(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Code = "sh600000"
	estimate := Calculate(config.Stock{Code: snapshot.Code, Sector: "股份制银行"}, 5, "2026-10-31", snapshot)
	if !estimate.Available() || estimate.FairPE > 10 || estimate.FairPB > 1.2 {
		t.Fatalf("financial caps not applied: %+v", estimate)
	}
}

func TestCalculateRejectsNegativeEarnings(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.EPS = -1
	snapshot.TTMNetProfitCNY = -0.0001
	estimate := Calculate(config.Stock{Code: snapshot.Code}, 10, "2026-10-31", snapshot)
	if estimate.Available() || !strings.Contains(estimate.Reason, "每股收益为负") {
		t.Fatalf("negative earnings produced a range: %+v", estimate)
	}
}

func TestCalculateFallsBackToPeWhenBookValueIsUnavailable(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.BookValuePerShare = 0
	estimate := Calculate(config.Stock{Code: snapshot.Code}, 20, "2026-10-31", snapshot)
	if !estimate.Available() || estimate.FairPB != 0 || !contains(estimate.Warnings, "book_value_non_positive") || estimate.Confidence != "low" {
		t.Fatalf("book value fallback failed: %+v", estimate)
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
