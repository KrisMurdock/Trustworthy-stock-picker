package predictor

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func evalSyntheticSamples(days, perDay int) []Sample {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, market.Shanghai)
	out := make([]Sample, 0, days*perDay)
	for d := 0; d < days; d++ {
		date := start.AddDate(0, 0, d).Format("2006-01-02")
		for i := 0; i < perDay; i++ {
			index := d*perDay + i
			x := math.Sin(float64(index) * 0.21)
			features := make([]float64, len(FeatureNames()))
			features[0] = x
			features[1] = math.Cos(float64(index) * 0.13)
			closeLabel := 0.004*x + 0.001*features[1]
			inputEnd := time.Date(2026, 9, 1+d, 10, 0, 0, 0, market.Shanghai).Add(time.Duration(i) * 5 * time.Minute)
			out = append(out, Sample{
				Code: "sh600000", Date: date, InputEnd: inputEnd, TargetEnd: inputEnd.Add(5 * time.Minute),
				InputOpen: 10, InputHigh: 10.1, InputLow: 9.9, InputClose: 10, BaselineRange: 0.01,
				Features: features,
				Labels: map[Target]float64{
					TargetHigh: closeLabel + 0.002, TargetLow: closeLabel - 0.002, TargetClose: closeLabel,
				},
			})
		}
	}
	return out
}

func evalDates(days int) []string {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, market.Shanghai)
	out := make([]string, days)
	for i := range out {
		out[i] = start.AddDate(0, 0, i).Format("2006-01-02")
	}
	return out
}

func evalOptions(t *testing.T, dates []string) TrainOptions {
	t.Helper()
	return TrainOptions{
		Hyperparameters: Hyperparameters{Trees: 8, MaxDepth: 2, MinLeaf: 1, LearningRate: 0.2, Seed: 1949, Bins: 8},
		LabelClip:       TargetMetrics{High: 0.05, Low: 0.05, Close: 0.05},
	}
}

func evalFindStrategy(t *testing.T, report EvaluationReport, name string) StrategyResult {
	t.Helper()
	for _, strategy := range report.Strategies {
		if strategy.Name == name {
			return strategy
		}
	}
	t.Fatalf("strategy %s not found", name)
	return StrategyResult{}
}

func TestEvaluateRollingWindowsCommonDates(t *testing.T) {
	days := evalDates(8)
	samples := evalSyntheticSamples(8, 6)
	provider := func(date string) (EvaluationDay, error) {
		return EvaluationDay{Date: date, Samples: samples, Codes: []string{"sh600000"}, ReportRevision: "rev-" + date}, nil
	}
	opts := EvaluationOptions{
		AsOf:            days[len(days)-1],
		Windows:         []WindowSpec{{Days: 2}, {Days: 3}, {Days: 4}},
		TradingDays:     days,
		MarketStates:    map[string]string{days[6]: MarketUp, days[7]: MarketRange},
		MinTrainingDays: 2,
		HalfLifeDays:    60,
		TrainOptions:    evalOptions(t, days),
	}
	report, err := EvaluateRollingWindows(context.Background(), days[6:], provider, opts)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.Status != "ok" || report.ValidationDays != 2 || report.ValidationFrom != days[6] || report.ValidationTo != days[7] {
		t.Fatalf("unexpected report header: %+v", report)
	}
	if len(report.Windows) != 3 {
		t.Fatalf("windows=%d want 3", len(report.Windows))
	}
	if sum := report.Windows[0].Weight + report.Windows[1].Weight + report.Windows[2].Weight; math.Abs(sum-1) > 1e-12 {
		t.Fatalf("weights sum to %v", sum)
	}
	if report.Windows[0].Weight <= report.Windows[1].Weight || report.Windows[1].Weight <= report.Windows[2].Weight {
		t.Fatalf("weights are not descending: %+v", report.Windows)
	}
	for _, name := range []string{"B0", "N0-last", "N0-range", "E0", "E1", "E1-filtered", "B0-filtered", "window-2", "window-3", "window-4"} {
		if name == "window-2" || name == "window-3" || name == "window-4" {
			continue
		}
		strategy := evalFindStrategy(t, report, name)
		if !strategy.Available || strategy.Metrics.Samples == 0 {
			t.Fatalf("strategy %s unavailable: %+v", name, strategy)
		}
	}
	e1 := evalFindStrategy(t, report, "E1")
	if e1.Filter == nil || e1.Comparison == nil || e1.Comparison.Baseline != "B0" {
		t.Fatalf("E1 comparison/filter missing: %+v", e1)
	}
	if len(report.Regimes) != 2 {
		t.Fatalf("regimes=%+v want up and range", report.Regimes)
	}
	if len(report.Universes) != 2 || report.Universes[0].ReportRevision == "" {
		t.Fatalf("universe audit metadata missing: %+v", report.Universes)
	}

	first, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	secondReport, err := EvaluateRollingWindows(context.Background(), days[6:], provider, opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(secondReport)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("evaluation output is not deterministic")
	}
}

func TestEvaluateRollingWindowsInsufficientHistory(t *testing.T) {
	days := evalDates(4)
	samples := evalSyntheticSamples(4, 6)
	provider := func(date string) (EvaluationDay, error) {
		return EvaluationDay{Date: date, Samples: samples, Codes: []string{"sh600000"}}, nil
	}
	report, err := EvaluateRollingWindows(context.Background(), days[2:], provider, EvaluationOptions{
		AsOf: days[3], Windows: []WindowSpec{{Days: 4}}, TradingDays: days, MinTrainingDays: 1, TrainOptions: evalOptions(t, days),
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.Status != "insufficient_history" || report.ValidationDays != 0 {
		t.Fatalf("unexpected insufficient report: %+v", report)
	}
	if len(report.Skipped) == 0 {
		t.Fatal("insufficient history must record a skip reason")
	}
}

func TestEvaluateRollingWindowsRejectsMissingTradingDays(t *testing.T) {
	_, err := EvaluateRollingWindows(context.Background(), []string{"2026-09-01"}, nil, EvaluationOptions{})
	if err == nil {
		t.Fatal("missing provider must fail")
	}
}

func TestWindowCombinationAndFilterHelpers(t *testing.T) {
	windows := []WindowSpec{{Days: 2}, {Days: 4}}
	weights := []float64{0.75, 0.25}
	preds := map[int][]map[Target]float64{
		2: {{TargetHigh: 0.02, TargetLow: -0.01, TargetClose: 0.01}},
		4: {{TargetHigh: 0.00, TargetLow: -0.03, TargetClose: -0.01}},
	}
	equal, exponential := combineWindowPredictions(preds, windows, weights, 0)
	if equal[TargetClose] != 0 || equal[TargetHigh] != 0.01 {
		t.Fatalf("equal combination = %+v", equal)
	}
	if math.Abs(exponential[TargetClose]-0.005) > 1e-12 {
		t.Fatalf("exponential close = %v", exponential[TargetClose])
	}
	if got := windowCloseDispersion(preds, windows, 0); math.Abs(got-0.02) > 1e-12 {
		t.Fatalf("close dispersion = %v", got)
	}
	if unanimousCloseDirection(preds, windows, 0) {
		t.Fatal("opposite close directions must not be unanimous")
	}
	preds[4][0][TargetClose] = 0.02
	if !unanimousCloseDirection(preds, windows, 0) {
		t.Fatal("same-direction close predictions must be unanimous")
	}
}
