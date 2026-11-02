package fundamentals

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestNormalizeSameDayAnnouncementIsUnavailable(t *testing.T) {
	series := seriesForYears(2019, 2023)
	before := Normalize(series, "2024-04-20")
	if before.Available || before.Reason != "insufficient_visible_years" {
		t.Fatalf("same-day result = %+v", before)
	}
	if err := before.Validate(); err != nil {
		t.Fatalf("unavailable result should validate: %v", err)
	}

	after := Normalize(series, "2024-04-21")
	if !after.Available || after.LatestReportEnd != "2023-12-31" {
		t.Fatalf("next-day result = %+v", after)
	}
	if err := after.Validate(); err != nil {
		t.Fatalf("available result should validate: %v", err)
	}
}

func TestNormalizeRevisionSelectionAndFutureStability(t *testing.T) {
	periods := []Period{
		testPeriod(2019, number(10), number(100), nil),
		testPeriod(2020, number(11), number(110), nil),
		testPeriod(2021, number(12), number(120), nil),
		testPeriod(2022, number(13), number(130), nil),
		testPeriodAvailable(2023, number(11.5), number(140), nil, "2024-04-20", "2024-04-21", "rev-a", "row-a"),
		testPeriodAvailable(2023, number(12.5), number(145), nil, "2024-05-31", "2024-06-01", "rev-b", "row-b"),
	}
	series := testSeries(periods...)

	early := Normalize(series, "2024-05-01")
	late := Normalize(series, "2024-07-01")
	if !early.Available || !late.Available {
		t.Fatalf("revision results unavailable: early=%+v late=%+v", early, late)
	}
	if early.NetProfitAnnualMedian != 11.5 || late.NetProfitAnnualMedian != 12 {
		t.Fatalf("revision medians = %v/%v", early.NetProfitAnnualMedian, late.NetProfitAnnualMedian)
	}
	if early.InputDigest == late.InputDigest {
		t.Fatal("different visible revisions produced the same digest")
	}

	series.Periods = append(series.Periods,
		testPeriodAvailable(2023, number(99), number(150), nil, "2025-01-01", "2025-01-02", "rev-future", "row-future"))
	series.FetchedAt = series.Periods[len(series.Periods)-1].FetchedAt
	again := Normalize(series, "2024-05-01")
	if again.InputDigest != early.InputDigest || again.NetProfitAnnualMedian != early.NetProfitAnnualMedian {
		t.Fatalf("future revision polluted early as-of: before=%+v after=%+v", early, again)
	}
}

func TestNormalizeObservedModeRejectsFutureFetches(t *testing.T) {
	series := seriesForYears(2019, 2023)
	for index := range series.Periods {
		series.Periods[index].FetchedAt = time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	}
	series.FetchedAt = series.Periods[len(series.Periods)-1].FetchedAt
	observed := Normalize(series, "2024-05-01")
	if observed.Available || observed.Reason != "no_observed_annual_reports" || observed.VisibilityMode != VisibilityObserved {
		t.Fatalf("observed future-fetch result = %+v", observed)
	}
	if err := observed.Validate(); err != nil {
		t.Fatalf("unavailable observed result should validate: %v", err)
	}
	vendor := NormalizeWithMode(series, "2024-05-01", VisibilityVendorBackfill)
	if !vendor.Available || vendor.VisibilityMode != VisibilityVendorBackfill || vendor.Confidence != ConfidenceLow ||
		!containsWarning(vendor.Warnings, "vendor_backfill_mode") {
		t.Fatalf("vendor backfill result = %+v", vendor)
	}
	if err := vendor.Validate(); err != nil {
		t.Fatalf("vendor backfill result should validate: %v", err)
	}
	if observed.InputDigest == vendor.InputDigest {
		t.Fatal("visibility mode was omitted from the input digest")
	}
	invalid := NormalizeWithMode(series, "2024-05-01", "future")
	if invalid.Available || invalid.Reason != "invalid_visibility_mode" {
		t.Fatalf("invalid mode = %+v", invalid)
	}
}

func TestNormalizeRequiresFiveVisibleAndCompleteYears(t *testing.T) {
	t.Run("visible years", func(t *testing.T) {
		normalized := Normalize(seriesForYears(2020, 2023), "2024-05-01")
		if normalized.Available || normalized.Reason != "insufficient_visible_years" {
			t.Fatalf("four visible years = %+v", normalized)
		}
	})

	t.Run("profit years", func(t *testing.T) {
		series := seriesForYears(2019, 2023)
		series.Periods[4].NetProfit = nil
		normalized := Normalize(series, "2024-05-01")
		if normalized.Available || normalized.Reason != "insufficient_profit_years" {
			t.Fatalf("four profit years = %+v", normalized)
		}
	})

	t.Run("equity years", func(t *testing.T) {
		series := seriesForYears(2019, 2023)
		series.Periods[2].Equity = nil
		normalized := Normalize(series, "2024-05-01")
		if normalized.Available || normalized.Reason != "insufficient_equity_years" {
			t.Fatalf("four equity years = %+v", normalized)
		}
	})
}

func TestNormalizeDistinguishesMissingAndZeroDividend(t *testing.T) {
	series := testSeries(
		testPeriod(2019, number(100), number(1000), nil),
		testPeriod(2020, number(100), number(1000), number(0)),
		testPeriod(2021, number(100), number(1000), number(10)),
		testPeriod(2022, number(100), number(1000), number(20)),
		testPeriod(2023, number(100), number(1000), number(30)),
	)
	normalized := Normalize(series, "2024-05-01")
	if !normalized.Available || normalized.PayoutYears != 4 || normalized.PayoutPct != 15 {
		t.Fatalf("missing/zero payout handling = %+v", normalized)
	}
	if err := normalized.Validate(); err != nil {
		t.Fatalf("missing/zero payout result should validate: %v", err)
	}

	zeroSeries := testSeries(
		testPeriod(2019, number(100), number(1000), number(0)),
		testPeriod(2020, number(100), number(1000), number(0)),
		testPeriod(2021, number(100), number(1000), number(0)),
		testPeriod(2022, number(100), number(1000), number(0)),
		testPeriod(2023, number(100), number(1000), number(0)),
	)
	zero := Normalize(zeroSeries, "2024-05-01")
	if !zero.Available || zero.PayoutYears != 5 || zero.PayoutPct != 0 || containsWarning(zero.Warnings, "payout_missing") {
		t.Fatalf("zero dividend handling = %+v", zero)
	}

	missingSeries := testSeries(
		testPeriod(2019, number(100), number(1000), nil),
		testPeriod(2020, number(100), number(1000), nil),
		testPeriod(2021, number(100), number(1000), nil),
		testPeriod(2022, number(100), number(1000), nil),
		testPeriod(2023, number(100), number(1000), nil),
	)
	missing := Normalize(missingSeries, "2024-05-01")
	if !missing.Available || missing.PayoutYears != 0 || !containsWarning(missing.Warnings, "payout_missing") || missing.Confidence != ConfidenceLow {
		t.Fatalf("missing dividend handling = %+v", missing)
	}
}

func TestNormalizeUsesAverageEquityForROE(t *testing.T) {
	netProfits := []float64{10, 11, 12, 13, 14, 15}
	equities := []float64{100, 110, 120, 130, 140, 150}
	periods := make([]Period, 0, len(netProfits))
	for i := range netProfits {
		periods = append(periods, testPeriod(2018+i, number(netProfits[i]), number(equities[i]), number(0)))
	}
	normalized := Normalize(testSeries(periods...), "2024-05-01")
	if !normalized.Available {
		t.Fatalf("normalization unavailable: %+v", normalized)
	}
	if !almostEqual(normalized.NetProfitAnnualMedian, 12.5) ||
		!almostEqual(normalized.AverageEquityMedian, 125) ||
		!almostEqual(normalized.ROEPct, 10.4) ||
		!almostEqual(normalized.EPSNormalized, 0.0125) ||
		!almostEqual(normalized.BVPS, 0.15) {
		t.Fatalf("unexpected normalized metrics: %+v", normalized)
	}
	if normalized.WindowStart != 2018 || normalized.WindowEnd != 2023 || len(normalized.FiscalYears) != 6 {
		t.Fatalf("unexpected window: %+v", normalized)
	}
	if err := normalized.Validate(); err != nil {
		t.Fatalf("normalized result should validate: %v", err)
	}
}

func TestNormalizePayoutConfidence(t *testing.T) {
	twoYears := testSeries(
		testPeriod(2019, number(100), number(1000), number(10)),
		testPeriod(2020, number(100), number(1000), number(20)),
		testPeriod(2021, number(100), number(1000), nil),
		testPeriod(2022, number(100), number(1000), nil),
		testPeriod(2023, number(100), number(1000), nil),
	)
	normalized := Normalize(twoYears, "2024-05-01")
	if !normalized.Available || normalized.PayoutYears != 2 || normalized.Confidence != ConfidenceLow ||
		!containsWarning(normalized.Warnings, "payout_years_lt_3") {
		t.Fatalf("two payout years = %+v", normalized)
	}

	medium := testSeries(
		testPeriod(2019, number(100), number(1000), number(10)),
		testPeriod(2020, number(100), number(1000), number(20)),
		testPeriod(2021, number(100), number(1000), number(30)),
		testPeriod(2022, number(100), number(1000), number(40)),
		testPeriod(2023, number(100), number(1000), number(50)),
	)
	got := Normalize(medium, "2024-05-01")
	if !got.Available || got.Confidence != ConfidenceMedium || containsWarning(got.Warnings, "payout_missing") {
		t.Fatalf("three-plus payout years = %+v", got)
	}
}

func TestNormalizeAddsStaleReportWarning(t *testing.T) {
	normalized := Normalize(seriesForYears(2017, 2021), "2023-07-02")
	if !normalized.Available || !containsWarning(normalized.Warnings, "stale_report") || normalized.Confidence != ConfidenceLow {
		t.Fatalf("stale report handling = %+v", normalized)
	}
	if err := normalized.Validate(); err != nil {
		t.Fatalf("stale result should validate: %v", err)
	}
}

func TestNormalizeDigestIsDeterministic(t *testing.T) {
	series := seriesForYears(2019, 2023)
	first := Normalize(series, "2024-05-01")
	second := Normalize(series, "2024-05-01")
	if first.InputDigest == "" || first.InputDigest != second.InputDigest {
		t.Fatalf("digest is not deterministic: %q vs %q", first.InputDigest, second.InputDigest)
	}
	if len(first.InputDigest) != 64 || !validDigest(first.InputDigest) {
		t.Fatalf("invalid digest: %q", first.InputDigest)
	}
	differentAsOf := Normalize(series, "2024-05-02")
	if first.InputDigest == differentAsOf.InputDigest {
		t.Fatal("digest did not include as_of")
	}
}

func TestNormalizeInvalidInputs(t *testing.T) {
	t.Run("invalid as of", func(t *testing.T) {
		normalized := Normalize(seriesForYears(2019, 2023), "2024/05/01")
		if normalized.Available || normalized.Reason != "invalid_as_of" {
			t.Fatalf("invalid as-of = %+v", normalized)
		}
		if err := normalized.Validate(); err == nil {
			t.Fatal("invalid as-of normalized result validated")
		}
	})

	t.Run("empty series", func(t *testing.T) {
		normalized := Normalize(testSeries(), "2024-05-01")
		if normalized.Available || normalized.Reason != "empty_series" {
			t.Fatalf("empty series = %+v", normalized)
		}
		if err := normalized.Validate(); err != nil {
			t.Fatalf("empty unavailable result should validate: %v", err)
		}
	})

	t.Run("NaN and negative shares", func(t *testing.T) {
		for name, mutate := range map[string]func(*Period){
			"NaN":             func(period *Period) { period.NetProfit = number(math.NaN()) },
			"negative shares": func(period *Period) { period.TotalShares = number(-1) },
		} {
			t.Run(name, func(t *testing.T) {
				period := testPeriod(2023, number(10), number(100), nil)
				mutate(&period)
				normalized := Normalize(testSeries(period), "2024-05-01")
				if normalized.Available || normalized.Reason != "invalid_series" {
					t.Fatalf("invalid period = %+v", normalized)
				}
			})
		}
	})

	t.Run("duplicate period", func(t *testing.T) {
		period := testPeriod(2023, number(10), number(100), nil)
		normalized := Normalize(testSeries(period, period), "2024-05-01")
		if normalized.Available || normalized.Reason != "invalid_series" {
			t.Fatalf("duplicate period = %+v", normalized)
		}
	})
}

func TestNormalizedValidateRejectsInvalidSurfaces(t *testing.T) {
	valid := Normalize(seriesForYears(2019, 2023), "2024-05-01")
	if err := valid.Validate(); err != nil {
		t.Fatalf("fixture is invalid: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Normalized)
	}{
		{name: "negative shares", mutate: func(n *Normalized) { n.TotalShares = -1 }},
		{name: "infinite EPS", mutate: func(n *Normalized) { n.EPSNormalized = math.Inf(1) }},
		{name: "negative equity", mutate: func(n *Normalized) { n.LatestEquity = -1 }},
		{name: "payout above range", mutate: func(n *Normalized) { n.PayoutPct = 101 }},
		{name: "invalid visibility", mutate: func(n *Normalized) { n.VisibilityMode = "future" }},
		{name: "invalid digest", mutate: func(n *Normalized) { n.InputDigest = "not-a-digest" }},
		{name: "missing reason", mutate: func(n *Normalized) {
			n.Available = false
			n.Confidence = ConfidenceUnavailable
		}},
		{name: "missing payout warning", mutate: func(n *Normalized) {
			n.PayoutYears = 0
			n.PayoutPct = 0
			n.Warnings = removeWarning(n.Warnings, "payout_missing")
		}},
		{name: "medium confidence with short payout history", mutate: func(n *Normalized) {
			n.PayoutYears = 2
			n.Confidence = ConfidenceMedium
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized := valid
			normalized.Warnings = append([]string(nil), valid.Warnings...)
			normalized.FiscalYears = append([]int(nil), valid.FiscalYears...)
			normalized.MissingYears = append([]int(nil), valid.MissingYears...)
			test.mutate(&normalized)
			if err := normalized.Validate(); err == nil {
				t.Fatal("invalid normalized surface was accepted")
			}
		})
	}
}

func removeWarning(warnings []string, want string) []string {
	out := warnings[:0]
	for _, warning := range warnings {
		if !strings.EqualFold(warning, want) && warning != want {
			out = append(out, warning)
		}
	}
	return out
}
