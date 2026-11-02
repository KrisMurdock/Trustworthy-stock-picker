package fairvalue

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fundamentals"
)

func normalizedSeriesFixture(t *testing.T) fundamentals.Series {
	t.Helper()
	fetched := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	var periods []fundamentals.Period
	for year := 2019; year <= 2025; year++ {
		announcement := fmt.Sprintf("%d-04-01", year+1)
		periods = append(periods, fundamentals.Period{
			SchemaVersion: fundamentals.Version, Code: "sh600519", Source: fundamentals.SourceTushare,
			ReportEnd: fmt.Sprintf("%d-12-31", year), AnnouncementDate: announcement,
			AvailableFrom:                fmt.Sprintf("%d-04-02", year+1),
			CashDividendAnnouncementDate: announcement, CashDividendAvailableFrom: fmt.Sprintf("%d-04-02", year+1),
			StatementBasis: fundamentals.StatementAnnualConsolidated,
			ReportType:     "1", CompType: 1, UpdateFlag: 0, Revision: "v0", FetchedAt: fetched,
			RowHash: fmt.Sprintf("row-%d", year), NetProfit: float64Ptr(100), Equity: float64Ptr(1000),
			TotalShares: float64Ptr(100), CashDividend: float64Ptr(30),
		})
	}
	series := fundamentals.Series{
		SchemaVersion: fundamentals.Version, Code: "sh600519", Source: fundamentals.SourceTushare,
		FetchedAt: fetched, Periods: periods,
	}
	if err := series.Validate(); err != nil {
		t.Fatalf("series fixture is invalid: %v", err)
	}
	return series
}

func normalizedFixture(t *testing.T) fundamentals.Normalized {
	t.Helper()
	series := normalizedSeriesFixture(t)
	normalized := fundamentals.Normalize(series, "2026-11-01")
	if err := normalized.Validate(); err != nil {
		t.Fatalf("normalized fixture is invalid: %v: %+v", err, normalized)
	}
	return normalized
}

func float64Ptr(value float64) *float64 { return &value }

func TestCalculateV3UsesNormalizedEarningsAndPayout(t *testing.T) {
	normalized := normalizedFixture(t)
	estimate := CalculateV3(config.Stock{Code: "sh600519", Sector: "白酒"}, 10, "2026-11-01", normalized)
	if err := estimate.Validate(); err != nil {
		t.Fatalf("valid v3 estimate rejected: %v: %+v", err, estimate)
	}
	if !estimate.Available() || estimate.DataQuality != DataQualityNormalized || estimate.Confidence != ConfidenceLowV3 {
		t.Fatalf("unexpected v3 availability or quality: %+v", estimate)
	}
	if math.Abs(estimate.EPSNormalized-1) > 1e-9 || math.Abs(estimate.BookValuePerShare-10) > 1e-9 ||
		math.Abs(estimate.ROEPct-10) > 1e-9 || math.Abs(estimate.PayoutPct-30) > 1e-9 {
		t.Fatalf("normalized inputs were not carried through: %+v", estimate)
	}
	if estimate.SustainableGrowthPct != 6 || estimate.CostOfEquityPct != 9.5 {
		t.Fatalf("unexpected growth or cost of equity: %+v", estimate)
	}
	if math.Abs(estimate.FairPE-estimate.FairPB/(estimate.ROEPct/100)) > 1e-9 {
		t.Fatalf("v3 PE/PB are inconsistent: %+v", estimate)
	}
	if estimate.Lower > estimate.Central || estimate.Central > estimate.Upper || len(estimate.Scenarios) == 0 {
		t.Fatalf("invalid v3 range or scenarios: %+v", estimate)
	}
	if !containsString(estimate.Warnings, "model_uncalibrated") {
		t.Fatalf("uncalibrated model warning missing: %+v", estimate.Warnings)
	}
}

func TestCalculateV3RequiresObservedPayout(t *testing.T) {
	normalized := normalizedFixture(t)
	normalized.PayoutYears = 0
	normalized.PayoutPct = 0
	normalized.Warnings = append(normalized.Warnings, "payout_missing")
	// Validate accepts unavailable fixtures only; keep the input valid as a
	// normalized surface but make the v3 formula refuse to invent a payout.
	if err := normalized.Validate(); err != nil {
		t.Fatalf("fixture with missing payout should remain a valid normalized surface: %v", err)
	}
	estimate := CalculateV3(config.Stock{Code: "sh600519"}, 10, "2026-11-01", normalized)
	if estimate.Available() || estimate.DataQuality != DataQualityUnavailable || !strings.Contains(estimate.Reason, "现金分红") {
		t.Fatalf("missing payout was fabricated: %+v", estimate)
	}
	if err := estimate.Validate(); err != nil {
		t.Fatalf("explained unavailable v3 failed validation: %v", err)
	}
}

func TestCalculateV3RejectsGrowthAtOrAboveROE(t *testing.T) {
	normalized := normalizedFixture(t)
	normalized.ROEPct = 5
	normalized.PayoutPct = 0
	estimate := CalculateV3(config.Stock{Code: "sh600519"}, 10, "2026-11-01", normalized)
	if estimate.Available() || !strings.Contains(estimate.Reason, "可持续增长率") {
		t.Fatalf("growth at ROE produced a valuation: %+v", estimate)
	}
}

func TestCalculateV3RejectsUnavailableNormalizedInput(t *testing.T) {
	normalized := normalizedFixture(t)
	normalized.Available = false
	normalized.Confidence = fundamentals.ConfidenceUnavailable
	normalized.Reason = "insufficient_visible_years"
	estimate := CalculateV3(config.Stock{Code: "sh600519"}, 10, "2026-11-01", normalized)
	if estimate.Available() || estimate.DataQuality != DataQualityUnavailable || estimate.Reason != normalized.Reason {
		t.Fatalf("unavailable normalized input was accepted: %+v", estimate)
	}
	if err := estimate.Validate(); err != nil {
		t.Fatalf("unavailable v3 failed validation: %v", err)
	}
}

func TestCalculateV3InvalidPriceUsesUnavailableQuality(t *testing.T) {
	estimate := CalculateV3(config.Stock{Code: "sh600519"}, 0, "2026-11-01", normalizedFixture(t))
	if estimate.Available() || estimate.DataQuality != DataQualityUnavailable || estimate.Reason == "" {
		t.Fatalf("invalid price was accepted: %+v", estimate)
	}
}

func TestCalculateV3RejectsStaleOrMismatchedNormalizedInput(t *testing.T) {
	estimate := CalculateV3(config.Stock{Code: "sz000001"}, 10, "2026-11-01", normalizedFixture(t))
	if estimate.Available() || !strings.Contains(estimate.Reason, "不一致") {
		t.Fatalf("mismatched stock was accepted: %+v", estimate)
	}
	estimate = CalculateV3(config.Stock{Code: "sh600519"}, 10, "2026-10-31", normalizedFixture(t))
	if estimate.Available() || !strings.Contains(estimate.Reason, "不一致") {
		t.Fatalf("mismatched as-of was accepted: %+v", estimate)
	}
}

func TestEstimateV3ValidateRejectsBrokenContracts(t *testing.T) {
	valid := CalculateV3(config.Stock{Code: "sh600519", Sector: "白酒"}, 10, "2026-11-01", normalizedFixture(t))
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	badRange := valid
	badRange.Central = valid.Upper + 1
	badRange.Reason = "forced unavailable"
	badRange.DataQuality = DataQualityUnavailable
	badRange.Confidence = ConfidenceUnavailableV3
	if err := badRange.Validate(); err == nil {
		t.Fatal("unordered v3 range was accepted")
	}

	badHash := valid
	badHash.InputDigest = "short"
	if err := badHash.Validate(); err == nil {
		t.Fatal("invalid v3 digest was accepted")
	}

	badScenario := valid
	badScenario.Scenarios = append([]ScenarioV2(nil), valid.Scenarios...)
	badScenario.Scenarios[0].GrowthPct = valid.ROEPct
	if err := badScenario.Validate(); err == nil {
		t.Fatal("scenario with growth equal to ROE was accepted")
	}

	badPayout := valid
	badPayout.PayoutYears = 0
	if err := badPayout.Validate(); err == nil {
		t.Fatal("available v3 without payout was accepted")
	}

	if err := (EstimateV3{Version: VersionV3, Code: "sh600519", AsOf: "2026-11-01", Method: MethodV3}).Validate(); err == nil {
		t.Fatal("unavailable v3 without reason was accepted")
	}
}

func TestEstimateV3JSONContract(t *testing.T) {
	estimate := CalculateV3(config.Stock{Code: "sh600519", Sector: "白酒"}, 10, "2026-11-01", normalizedFixture(t))
	encoded, err := json.Marshal(estimate)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, key := range []string{
		`"schema_version"`, `"code"`, `"as_of"`, `"source"`, `"source_version"`, `"input_mode"`,
		`"method"`, `"statement_basis"`, `"confidence"`, `"data_quality"`, `"input_digest"`,
		`"window_start"`, `"window_end"`, `"fiscal_years"`, `"payout_years"`, `"latest_report_end"`,
		`"eps_normalized"`, `"book_value_per_share"`, `"roe_norm_pct"`, `"payout_norm_pct"`,
		`"sustainable_growth_pct"`, `"cost_of_equity_pct"`, `"fair_pb"`, `"fair_pe"`, `"lower"`,
		`"central"`, `"upper"`, `"upside_pct"`, `"scenarios"`,
	} {
		if !strings.Contains(text, key) {
			t.Fatalf("JSON contract missing %s: %s", key, text)
		}
	}
}
