package strategy

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestPhaseSlopeStatsUsesLogPriceOLS(t *testing.T) {
	points := make([]PhaseLabelPoint, phaseLabelMaxHorizon+1)
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, market.Shanghai)
	for i := range points {
		points[i] = PhaseLabelPoint{
			Date:  phaseTradingDate(start, i).Format("2006-01-02"),
			Close: 10 * math.Exp(0.01*float64(i)),
		}
	}
	stats := phaseSlopeStatsForPoints(points, 20)
	if !stats.Valid {
		t.Fatal("expected valid slope stats")
	}
	if math.Abs(stats.SlopePct-1.005) > 0.001 {
		t.Fatalf("daily slope pct = %.6f, want 1.005", stats.SlopePct)
	}
	if math.Abs(stats.ExpectedPct-22.14) > 0.05 {
		t.Fatalf("expected 20-day move = %.2f%%, want about 22.14%%", stats.ExpectedPct)
	}
	if stats.R2 < 0.999 {
		t.Fatalf("log-linear series R2 = %.6f, want about 1", stats.R2)
	}
	if math.Abs(stats.TStat) < 100 {
		t.Fatalf("perfect trend t statistic = %.2f, want a large finite value", stats.TStat)
	}
}

func TestPhaseSlopeLabelSeparatesTrendAndRange(t *testing.T) {
	spec := PhaseLabelSpec{Horizon: 10, ATRMultiple: 1, TCritical: phaseLabelTCritical(10), R2Minimum: 0.5}
	cases := []struct {
		name  string
		path  func(int) float64
		phase string
	}{
		{name: "advance", path: func(i int) float64 { return 10 * math.Exp(0.01*float64(i)) }, phase: PhaseAdvance},
		{name: "decline", path: func(i int) float64 { return 10 * math.Exp(-0.01*float64(i)) }, phase: PhaseDecline},
		{name: "range", path: func(i int) float64 { return 10 * (1 + 0.02*math.Sin(float64(i)*1.7)) }, phase: PhaseRange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sample := phaseLabelTestSample("sz000001", "2026-01-05", tc.phase, 2, tc.path)
			if got, ok := phaseSlopeLabel(sample, spec); !ok || got != tc.phase {
				t.Fatalf("slope label = %s, ok=%v; want %s", got, ok, tc.phase)
			}
		})
	}
}

func TestComparePhaseLabelsSelectsOnDevelopmentOnly(t *testing.T) {
	samples := phaseLabelFixtureSamples(35, 110)
	first := ComparePhaseLabels(samples)
	if first == nil || first.Candidates != 48 {
		t.Fatalf("unexpected comparison: %+v", first)
	}
	if first.Baseline.Development.Status != "ok" || first.Baseline.Holdout.Status != "ok" || first.Slope.Holdout.Status != "ok" {
		t.Fatalf("fixture should produce credible development and holdout samples: %+v", first)
	}
	if first.Verdict != "candidate" {
		t.Fatalf("slope candidate should beat the FTH baseline in fixture: %+v", first)
	}

	holdoutStart := first.HoldoutFrom
	changed := clonePhaseLabelSamples(samples)
	for i := range changed {
		if changed[i].Date < holdoutStart {
			continue
		}
		changed[i].Phase = PhaseAdvance
		for j := range changed[i].Forward {
			changed[i].Forward[j].Close = changed[i].StartClose * math.Exp(0.008*float64(j))
		}
	}
	second := ComparePhaseLabels(changed)
	if second.Spec != first.Spec {
		t.Fatalf("holdout labels changed development-selected spec: first=%+v second=%+v", first.Spec, second.Spec)
	}
	if second.Verdict == "candidate" {
		t.Fatalf("rewritten holdout must not be used to select or validate the candidate: %+v", second)
	}
}

func TestComparePhaseLabelsMarksShortHoldoutInsufficient(t *testing.T) {
	samples := phaseLabelFixtureSamples(35, 25)
	comparison := ComparePhaseLabels(samples)
	if comparison == nil {
		t.Fatal("missing comparison")
	}
	if comparison.SlopeVerdict != "insufficient" || comparison.Verdict != "insufficient" {
		t.Fatalf("short holdout verdict = %s/%s, want insufficient: %+v", comparison.SlopeVerdict, comparison.Verdict, comparison)
	}
}

func TestComparePhaseLabelsRejectsConcentratedCandidateLabels(t *testing.T) {
	samples := phaseLabelFixtureSamples(35, 110)
	for i := range samples {
		samples[i].Phase = PhaseRange
		amplitude := 0.025
		if i%3 == 2 {
			amplitude = 0.001
		}
		sign := 1.0
		if i%3 == 1 {
			sign = -1
		}
		for j := range samples[i].Forward {
			samples[i].Forward[j].Close = samples[i].StartClose * (1 + sign*amplitude*math.Sin(float64(j)*2.8))
		}
	}
	comparison := ComparePhaseLabels(samples)
	if comparison == nil {
		t.Fatal("missing comparison")
	}
	if comparison.Slope.Holdout.ClassCoverage == "ok" {
		t.Fatalf("fixture should collapse slope labels into one class: %+v", comparison.Slope.Holdout.Distribution)
	}
	if comparison.SlopeVerdict != "unstable" || comparison.Verdict != "unstable" {
		t.Fatalf("concentrated labels must not become a candidate: %+v", comparison)
	}
}

func TestComparePhaseLabelsHasEnoughDevelopmentDatesAtProductionWindow(t *testing.T) {
	bars := phasePercentSeries(100, .03, .02, 500)
	histories := make([]market.History, 0, 30)
	for i := 0; i < 30; i++ {
		histories = append(histories, market.History{
			Code: "sz" + phaseCodeSuffix(i), AsOf: bars[len(bars)-1].Date, Bars: bars,
		})
	}
	report := EvaluatePhaseValidation(context.Background(), histories, nil, bars[len(bars)-1].Date)
	if report.LabelComparison == nil {
		t.Fatal("missing label comparison")
	}
	comparison := report.LabelComparison
	if comparison.Baseline.Development.Status != "ok" || comparison.Slope.Development.Status != "ok" {
		t.Fatalf("production-sized history did not fill development gates: baseline=%+v slope=%+v", comparison.Baseline.Development, comparison.Slope.Development)
	}
	if comparison.Verdict == "insufficient" {
		t.Fatalf("production-sized history was incorrectly marked insufficient: %+v", comparison)
	}
}

func TestEvaluatePhaseValidationAttachesLabelComparison(t *testing.T) {
	bars := phasePercentSeries(100, .03, .02, 360)
	histories := make([]market.History, 0, 30)
	for i := 0; i < 30; i++ {
		histories = append(histories, market.History{
			Code: "sz" + phaseCodeSuffix(i), AsOf: bars[len(bars)-1].Date, Bars: bars,
		})
	}
	report := EvaluatePhaseValidation(context.Background(), histories, nil, bars[len(bars)-1].Date)
	if report.LabelComparison == nil {
		t.Fatal("phase validation did not attach label comparison")
	}
	if report.LabelComparison.Version != PhaseLabelComparisonVersion || len(report.LabelComparison.Baseline.Development.Distribution) != 3 {
		t.Fatalf("label comparison is incomplete: %+v", report.LabelComparison)
	}
}

func phaseLabelFixtureSamples(stocks, signalCount int) []phaseValidationSample {
	daily := phaseTradingDates(time.Date(2025, 1, 6, 0, 0, 0, 0, market.Shanghai), signalCount*5+phaseLabelMaxHorizon+1)
	samples := make([]phaseValidationSample, 0, stocks*signalCount)
	for stock := 0; stock < stocks; stock++ {
		phase := []string{PhaseAdvance, PhaseRange, PhaseDecline}[stock%3]
		for signal := 0; signal < signalCount; signal++ {
			start := daily[signal*5]
			path := func(i int) float64 { return 10 * math.Exp(0.01*float64(i)) }
			switch phase {
			case PhaseDecline:
				path = func(i int) float64 { return 10 * math.Exp(-0.01*float64(i)) }
			case PhaseRange:
				switch signal % 3 {
				case 0:
					path = func(i int) float64 { return 10 * (1 + 0.02*math.Sin(float64(i)*1.7)) }
				case 1:
					path = func(i int) float64 { return 10 * (1 - 0.02*math.Sin(float64(i)*1.7)) }
				default:
					path = func(i int) float64 { return 10 * (1 + 0.001*math.Sin(float64(i)*1.7)) }
				}
			}
			forward := make([]PhaseLabelPoint, phaseLabelMaxHorizon+1)
			for i := range forward {
				forward[i] = PhaseLabelPoint{Date: daily[signal*5+i].Format("2006-01-02"), Close: path(i)}
			}
			samples = append(samples, phaseValidationSample{
				Code: "sz" + phaseCodeSuffix(stock), Date: start.Format("2006-01-02"), Phase: phase,
				ATRPct: 2, StartClose: forward[0].Close, Forward: forward,
			})
		}
	}
	return samples
}

func phaseLabelTestSample(code, date, phase string, atrPct float64, path func(int) float64) phaseValidationSample {
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, market.Shanghai)
	forward := make([]PhaseLabelPoint, phaseLabelMaxHorizon+1)
	for i := range forward {
		forward[i] = PhaseLabelPoint{Date: phaseTradingDate(start, i).Format("2006-01-02"), Close: path(i)}
	}
	return phaseValidationSample{Code: code, Date: date, Phase: phase, ATRPct: atrPct, StartClose: forward[0].Close, Forward: forward}
}

func clonePhaseLabelSamples(samples []phaseValidationSample) []phaseValidationSample {
	out := make([]phaseValidationSample, len(samples))
	for i, sample := range samples {
		out[i] = sample
		out[i].Forward = append([]PhaseLabelPoint(nil), sample.Forward...)
	}
	return out
}

func phaseTradingDate(start time.Time, offset int) time.Time {
	date := start
	for offset > 0 {
		date = date.AddDate(0, 0, 1)
		if date.Weekday() != time.Saturday && date.Weekday() != time.Sunday {
			offset--
		}
	}
	return date
}

func phaseTradingDates(start time.Time, count int) []time.Time {
	out := make([]time.Time, 0, count)
	for date := start; len(out) < count; date = date.AddDate(0, 0, 1) {
		if date.Weekday() != time.Saturday && date.Weekday() != time.Sunday {
			out = append(out, date)
		}
	}
	return out
}

func phaseCodeSuffix(index int) string {
	const digits = "0123456789"
	value := index
	out := []byte("000000")
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = digits[value%10]
		value /= 10
	}
	return string(out)
}
