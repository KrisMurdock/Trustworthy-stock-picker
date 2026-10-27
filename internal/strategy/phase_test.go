package strategy

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func phaseSeries(count int, trend, amplitude float64) []market.Bar {
	bars := make([]market.Bar, 0, count)
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, market.Shanghai)
	for i, day := 0, start; i < count; day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		price := 10 + trend*float64(i) + amplitude*math.Sin(2*math.Pi*float64(i)/20)
		bars = append(bars, market.Bar{
			Date: day.Format("2006-01-02"),
			Open: price - .05, Close: price, High: price + .15, Low: price - .15, Volume: 1000,
		})
		i++
	}
	return bars
}

func phasePercentSeries(base, dailyPct, amplitudePct float64, count int) []market.Bar {
	bars := make([]market.Bar, 0, count)
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, market.Shanghai)
	for i, day := 0, start; i < count; day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		closePrice := base * math.Pow(1+dailyPct/100, float64(i)) * (1 + amplitudePct*math.Sin(2*math.Pi*float64(i)/20))
		bars = append(bars, market.Bar{
			Date: day.Format("2006-01-02"), Open: closePrice * .998, Close: closePrice,
			High: closePrice * 1.01, Low: closePrice * .99, Volume: 1000,
		})
		i++
	}
	return bars
}

func TestAnalyzePhaseClassifiesAdvanceRangeAndDecline(t *testing.T) {
	cases := []struct {
		name  string
		bars  []market.Bar
		phase string
	}{
		{name: "advance", bars: phaseSeries(160, .03, .1), phase: PhaseAdvance},
		{name: "range", bars: phaseSeries(160, 0, .4), phase: PhaseRange},
		{name: "decline", bars: phaseSeries(160, -.03, .1), phase: PhaseDecline},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			features, err := Extract(tc.bars)
			if err != nil {
				t.Fatal(err)
			}
			wave := AnalyzeWave(tc.bars)
			phase := AnalyzePhase(tc.bars, features, &wave)
			if phase.Phase != tc.phase {
				t.Fatalf("phase = %s (%s), score=%d strength=%d, want %s: %+v", phase.Phase, phase.Label, phase.Score, phase.Strength, tc.phase, phase)
			}
			if phase.AsOf != tc.bars[len(tc.bars)-1].Date || len(phase.Steps) != 7 || phase.Evidence == "" {
				t.Fatalf("phase evidence is incomplete: %+v", phase)
			}
			if phase.ConfidenceScore < 0 || phase.ConfidenceScore > 100 {
				t.Fatalf("phase confidence is outside 0-100: %+v", phase)
			}
			if phase.Phase == PhaseAdvance && phase.Invalidation <= 0 {
				t.Fatalf("advance phase missing invalidation: %+v", phase)
			}
			if again := AnalyzePhase(tc.bars, features, &wave); again.Phase != phase.Phase || again.Score != phase.Score || again.Evidence != phase.Evidence {
				t.Fatalf("phase evidence is not deterministic: first=%+v second=%+v", phase, again)
			}
		})
	}
}

func TestAnalyzePhaseReturnsUnknownWithoutEnoughBars(t *testing.T) {
	bars := phaseSeries(phaseMinBars-1, .03, .4)
	features, err := Extract(bars)
	if err == nil {
		t.Fatal("expected Extract to reject short history")
	}
	phase := AnalyzePhase(bars, features, nil)
	if phase.Phase != PhaseUnknown || phase.Label != "未知" || len(phase.Steps) != 1 || phase.Steps[0].Verdict != "不足" {
		t.Fatalf("short history produced a fabricated phase: %+v", phase)
	}
	if phase.Limitation == "" || phase.Evidence == "" {
		t.Fatalf("unknown phase missing boundary text: %+v", phase)
	}
	if phase.ConfidenceScore != 0 {
		t.Fatalf("unknown phase fabricated confidence: %+v", phase)
	}
}

func TestEvaluatePhaseValidationUsesOnlySignalDayAndEarlierBars(t *testing.T) {
	stable := phaseSeries(phaseMinBars+phaseValidationHorizon, .03, .1)
	reversed := append([]market.Bar(nil), stable...)
	for i := phaseMinBars; i < len(reversed); i++ {
		closePrice := reversed[i-1].Close * .95
		reversed[i] = market.Bar{
			Date: reversed[i].Date, Open: closePrice * 1.01, Close: closePrice,
			High: closePrice * 1.01, Low: closePrice * .99, Volume: 1000,
		}
	}
	signalDate := stable[phaseMinBars-1].Date
	stableReport := EvaluatePhaseValidation(context.Background(), []market.History{{
		Code: "sz000001", AsOf: stable[len(stable)-1].Date, Bars: stable,
	}}, nil, stable[len(stable)-1].Date)
	reversedReport := EvaluatePhaseValidation(context.Background(), []market.History{{
		Code: "sz000001", AsOf: reversed[len(reversed)-1].Date, Bars: reversed,
	}}, nil, reversed[len(reversed)-1].Date)

	if stableReport.Predicted != 1 || reversedReport.Predicted != 1 {
		t.Fatalf("expected one phase prediction each: stable=%+v reversed=%+v", stableReport, reversedReport)
	}
	if stableReport.From != signalDate || stableReport.To != signalDate || reversedReport.From != signalDate || reversedReport.To != signalDate {
		t.Fatalf("validation window used future dates: stable=%s/%s reversed=%s/%s want=%s", stableReport.From, stableReport.To, reversedReport.From, reversedReport.To, signalDate)
	}
	if stableReport.Dates != 1 || stableReport.Status != "insufficient" {
		t.Fatalf("single signal date must not be reported as sufficient validation: %+v", stableReport)
	}
	if !reflect.DeepEqual(phasePredictedSamples(stableReport.Classes), phasePredictedSamples(reversedReport.Classes)) {
		t.Fatalf("future bars changed phase inference: stable=%+v reversed=%+v", stableReport.Classes, reversedReport.Classes)
	}
}

func phasePredictedSamples(classes []PhaseClassValidation) map[string]int {
	counts := make(map[string]int, len(classes))
	for _, class := range classes {
		counts[class.Phase] = class.Predicted
	}
	return counts
}

func TestAnalyzePhaseIsPriceScaleInvariant(t *testing.T) {
	var baseline PhaseEvidence
	for _, base := range []float64{10, 100, 1000} {
		bars := phasePercentSeries(base, .3, .02, 160)
		features, err := Extract(bars)
		if err != nil {
			t.Fatal(err)
		}
		wave := AnalyzeWave(bars)
		phase := AnalyzePhase(bars, features, &wave)
		if phase.Phase != PhaseAdvance {
			t.Fatalf("base %.0f phase = %s, want advance: %+v", base, phase.Phase, phase)
		}
		if base == 10 {
			baseline = phase
			continue
		}
		if phase.Phase != baseline.Phase || phase.Score != baseline.Score || phase.Strength != baseline.Strength || phase.ConfidenceScore != baseline.ConfidenceScore {
			t.Fatalf("price scale changed phase evidence: base10=%+v base%.0f=%+v", baseline, base, phase)
		}
		if math.Abs(phase.RegressionSlopePct-baseline.RegressionSlopePct) > 1e-9 {
			t.Fatalf("price scale changed regression slope: base10=%g base%.0f=%g", baseline.RegressionSlopePct, base, phase.RegressionSlopePct)
		}
	}
}

func TestAnalyzePhaseRangeConfidenceIsNotHigh(t *testing.T) {
	bars := phasePercentSeries(100, 0, .01, 160)
	features, err := Extract(bars)
	if err != nil {
		t.Fatal(err)
	}
	wave := AnalyzeWave(bars)
	phase := AnalyzePhase(bars, features, &wave)
	if phase.Phase != PhaseRange {
		t.Fatalf("flat series phase = %s, want range: %+v", phase.Phase, phase)
	}
	if phase.ConfidenceScore >= 75 || phase.Confidence == "high" {
		t.Fatalf("range without directional evidence must not be high confidence: %+v", phase)
	}
}

func TestEvaluatePhaseValidationTruncatesAtAsOf(t *testing.T) {
	bars := phaseSeries(160, .03, .1)
	asof := bars[100].Date
	report := EvaluatePhaseValidation(context.Background(), []market.History{{
		Code: "sz000001", AsOf: bars[len(bars)-1].Date, Bars: bars,
	}}, nil, asof)
	if report.Predicted == 0 {
		t.Fatalf("expected predictions before asof: %+v", report)
	}
	if report.To > asof || report.From > asof {
		t.Fatalf("validation used bars after asof: from=%s to=%s asof=%s", report.From, report.To, asof)
	}
}

func TestEvaluatePhaseValidationCoversFullPoolAndPriorYear(t *testing.T) {
	bars := phasePercentSeries(100, .03, .02, 360)
	histories := make([]market.History, 0, 30)
	for i := 0; i < 30; i++ {
		histories = append(histories, market.History{
			Code: fmt.Sprintf("sz%06d", i), AsOf: bars[len(bars)-1].Date, Bars: bars,
		})
	}
	report := EvaluatePhaseValidation(context.Background(), histories, nil, bars[len(bars)-1].Date)
	if report.Universe != 30 || report.Stocks != 30 {
		t.Fatalf("validation did not cover the full pool: universe=%d stocks=%d", report.Universe, report.Stocks)
	}
	if report.Predicted < phaseValidationMinSamples || report.Dates < phaseValidationMinDates {
		t.Fatalf("validation sample is too small to be credible: %+v", report)
	}
	if report.Status != "ok" {
		t.Fatalf("full-pool prior-year validation should pass the gate: %+v", report)
	}
}

func TestEvaluatePhaseValidationRejectsDuplicateDates(t *testing.T) {
	bars := phaseSeries(160, .03, .1)
	bars[80].Date = bars[79].Date
	report := EvaluatePhaseValidation(context.Background(), []market.History{{
		Code: "sz000001", AsOf: bars[len(bars)-1].Date, Bars: bars,
	}}, nil, bars[len(bars)-1].Date)
	if report.Stocks != 0 || report.Predicted != 0 || report.Status != "unavailable" {
		t.Fatalf("duplicate dates produced validation samples: %+v", report)
	}
}

func TestEvaluatePhaseValidationPrefersUnadjustedLabels(t *testing.T) {
	bars := phaseSeries(160, .03, .1)
	asof := bars[len(bars)-1].Date
	adjusted := []market.History{{Code: "sz000001", AsOf: asof, Bars: bars}}
	raw := []market.History{{Code: "sz000001", AsOf: asof, Adjust: "none", Bars: bars}}
	report := EvaluatePhaseValidation(context.Background(), adjusted, raw, asof)
	if report.LabelSource != "unadjusted_close" {
		t.Fatalf("label source = %q, want unadjusted_close: %+v", report.LabelSource, report)
	}
	fallback := EvaluatePhaseValidation(context.Background(), adjusted, nil, asof)
	if fallback.LabelSource != "adjusted_close_fallback" {
		t.Fatalf("fallback label source = %q, want adjusted_close_fallback: %+v", fallback.LabelSource, fallback)
	}
}

func TestAnalyzePhaseRejectsInvalidBars(t *testing.T) {
	bars := phaseSeries(phaseMinBars, .03, .4)
	bars[len(bars)-1].High = bars[len(bars)-1].Low - 1
	phase := AnalyzePhase(bars, Features{}, nil)
	if phase.Phase != PhaseUnknown || len(phase.Steps) != 1 || phase.Steps[0].Verdict != "无效" {
		t.Fatalf("invalid bar produced a phase: %+v", phase)
	}
}

func TestAnalyzeAttachesPhaseEvidenceToCandidates(t *testing.T) {
	c, histories, raw := researchFixture()
	result, err := Analyze(context.Background(), c, histories, raw, histories[0].AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Ranking) != 1 || result.Ranking[0].Phase == nil {
		t.Fatalf("phase evidence was not attached to the ranking: %+v", result.Ranking)
	}
	phase := result.Ranking[0].Phase
	if phase.Phase == "" || phase.Label == "" || len(phase.Steps) == 0 || phase.Limitation == "" {
		t.Fatalf("attached phase evidence is incomplete: %+v", phase)
	}
}
