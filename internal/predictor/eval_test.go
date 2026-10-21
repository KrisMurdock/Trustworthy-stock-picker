package predictor

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
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
		if !strategy.Available {
			t.Fatalf("strategy %s unavailable: %+v", name, strategy)
		}
	}
	e1 := evalFindStrategy(t, report, "E1")
	if e1.Filter == nil {
		t.Fatalf("E1 filter missing: %+v", e1)
	}
	if report.SchemaVersion != EvaluationSchemaVersion || len(report.Metrics) == 0 {
		t.Fatalf("report metrics missing: schema=%d records=%d", report.SchemaVersion, len(report.Metrics))
	}
	if samples, days, ok := metricSampleSummary(report.Metrics, "strategy:E1"); !ok || samples == 0 || days != 2 {
		t.Fatalf("strategy metric summary = %d samples, %d days, ok=%v", samples, days, ok)
	}
	if samples, days, ok := metricSampleSummary(report.Metrics, "window:2"); !ok || samples == 0 || days != 2 {
		t.Fatalf("window metric summary = %d samples, %d days, ok=%v", samples, days, ok)
	}
	if len(report.Days) != 2 || report.Days[0].ValidationSamples == 0 || report.Days[0].TrainingSamples == 0 {
		t.Fatalf("daily fold metadata missing: %+v", report.Days)
	}
	foundDaily := false
	for _, record := range report.Metrics {
		if record.Subject == "strategy:B0" && record.MetricID == modeleval.MetricPointMAE &&
			record.Target == modeleval.TargetClose && record.Aggregation == modeleval.AggregationDayEqual &&
			len(record.Slice) == 1 && record.Slice[0].Name == "date" && record.Slice[0].Value == days[6] {
			foundDaily = record.Value != nil && record.Samples > 0
		}
	}
	if !foundDaily {
		t.Fatal("daily metric slice missing for B0")
	}
	for _, subject := range []string{"strategy:E1", "window:2"} {
		reserved := findMetricRecord(t, report.Metrics, modeleval.MetricIntervalEmpiricalCoverage, subject, modeleval.TargetAll, modeleval.SpaceNone, modeleval.AggregationPooled)
		if reserved.Applicability != modeleval.ApplicabilityNotApplicable || reserved.Value != nil ||
			reserved.Reason != "point estimate model has no calibrated interval" {
			t.Fatalf("%s reserved metric = %+v", subject, reserved)
		}
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

func TestEvaluateBaselineOnlySkipsRollingWindows(t *testing.T) {
	days := evalDates(8)
	samples := evalSyntheticSamples(8, 6)
	provider := func(date string) (EvaluationDay, error) {
		return EvaluationDay{Date: date, Samples: samples, Codes: []string{"sh600000"}}, nil
	}
	report, err := EvaluateRollingWindows(context.Background(), days[6:], provider, EvaluationOptions{
		AsOf: days[7], BaselineOnly: true, TradingDays: days, MinTrainingDays: 2,
		TrainOptions: evalOptions(t, days),
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.Mode != "baseline" || report.ValidationDays != 2 || len(report.Windows) != 0 {
		t.Fatalf("unexpected baseline report: %+v", report)
	}
	if len(report.Strategies) != 3 {
		t.Fatalf("baseline strategies=%+v want B0 and two N0 baselines", report.Strategies)
	}
	for _, name := range []string{"B0", "N0-last", "N0-range"} {
		if strategy := evalFindStrategy(t, report, name); !strategy.Available {
			t.Fatalf("baseline strategy %s unavailable", name)
		}
	}
	for _, name := range []string{"E0", "E1", "B0-filtered"} {
		for _, strategy := range report.Strategies {
			if strategy.Name == name {
				t.Fatalf("baseline-only unexpectedly emitted %s", name)
			}
		}
	}
}

func addMetricTestSample(accumulator *metricAccumulator, date, code string, actualClose, predictedClose float64) {
	inputClose := 100.0
	sample := Sample{
		Code:          code,
		Date:          date,
		InputClose:    inputClose,
		InputHigh:     inputClose * 1.002,
		InputLow:      inputClose * 0.998,
		BaselineRange: 0.01,
		Labels: map[Target]float64{
			TargetHigh:  actualClose,
			TargetLow:   actualClose,
			TargetClose: actualClose,
		},
	}
	accumulator.add(sample, map[Target]float64{
		TargetHigh:  predictedClose,
		TargetLow:   predictedClose,
		TargetClose: predictedClose,
	})
}

func TestMetricDayEqualAndPooledAggregation(t *testing.T) {
	accumulator := newMetricAccumulator()
	addMetricTestSample(accumulator, "2026-09-01", "sh600000", 0.01, 0)
	for index := 0; index < 3; index++ {
		addMetricTestSample(accumulator, "2026-09-02", "sh600000", 0.01, 0.01)
	}
	records := accumulator.records("model:candidate", nil, func(sample metricSample) TargetMetrics { return sample.Predicted }, false)
	dayEqual := findMetricRecord(t, records, modeleval.MetricPointMAE, "model:candidate", modeleval.TargetClose, modeleval.SpaceReturn, modeleval.AggregationDayEqual)
	pooled := findMetricRecord(t, records, modeleval.MetricPointMAE, "model:candidate", modeleval.TargetClose, modeleval.SpaceReturn, modeleval.AggregationPooled)
	if dayEqual.Value == nil || math.Abs(*dayEqual.Value-50) > 1e-9 {
		t.Fatalf("day-equal MAE = %v bps, want 50", dayEqual.Value)
	}
	if pooled.Value == nil || math.Abs(*pooled.Value-25) > 1e-9 {
		t.Fatalf("pooled MAE = %v bps, want 25", pooled.Value)
	}
	if dayEqual.Samples != 4 || dayEqual.Days != 2 || dayEqual.Stocks != 1 {
		t.Fatalf("unexpected day-equal counts: %+v", dayEqual)
	}
	if dayEqual.CI == nil || dayEqual.CI.Applicability != modeleval.ApplicabilityInsufficientData || dayEqual.CI.Reason != "fewer than 10 validation days" {
		t.Fatalf("short-run CI = %+v", dayEqual.CI)
	}
}

func TestMetricNonzeroDirectionTreatsPredictedZeroAsMiss(t *testing.T) {
	accumulator := newMetricAccumulator()
	addMetricTestSample(accumulator, "2026-09-01", "sh600000", 0.01, 0)
	records := accumulator.records("model:candidate", nil, func(sample metricSample) TargetMetrics { return sample.Predicted }, false)
	accuracy := findMetricRecord(t, records, modeleval.MetricDirectionAccuracy, "model:candidate", modeleval.TargetClose, modeleval.SpaceNone, modeleval.AggregationDayEqual)
	if accuracy.Value == nil || *accuracy.Value != 0 {
		t.Fatalf("predicted zero accuracy = %v, want 0", accuracy.Value)
	}
	if accuracy.Spec == nil || accuracy.Spec.Label == nil || accuracy.Spec.Label.Mode != "nonzero_sign" {
		t.Fatalf("direction label spec = %+v", accuracy.Spec)
	}
}

func TestMetricMajorityBaseline(t *testing.T) {
	accumulator := newMetricAccumulator()
	addMetricTestSample(accumulator, "2026-09-01", "sh600000", 0.01, 0.01)
	addMetricTestSample(accumulator, "2026-09-02", "sh600000", 0.01, -0.01)
	addMetricTestSample(accumulator, "2026-09-03", "sh600000", 0.01, 0.01)
	addMetricTestSample(accumulator, "2026-09-04", "sh600000", -0.01, -0.01)
	records := accumulator.majorityRecords(modeleval.BaselineSubject(modeleval.BaselineMajority), nil)
	accuracy := findMetricRecord(t, records, modeleval.MetricDirectionAccuracy, modeleval.BaselineSubject(modeleval.BaselineMajority), modeleval.TargetClose, modeleval.SpaceNone, modeleval.AggregationDayEqual)
	if accuracy.Value == nil || math.Abs(*accuracy.Value-0.75) > 1e-12 {
		t.Fatalf("majority accuracy = %v, want 0.75", accuracy.Value)
	}
	if accuracy.Spec == nil || accuracy.Spec.Label == nil || accuracy.Spec.Label.Mode != "nonzero_sign" {
		t.Fatalf("majority label spec = %+v", accuracy.Spec)
	}
}

func TestMetricSkillFormula(t *testing.T) {
	accumulator := newMetricAccumulator()
	addMetricTestSample(accumulator, "2026-09-01", "sh600000", 0.02, 0.01)
	records := accumulator.records("model:candidate", nil, func(sample metricSample) TargetMetrics { return sample.Predicted }, false)
	skill := findMetricRecord(t, records, modeleval.MetricPointSkillMAE, "model:candidate", modeleval.TargetClose, modeleval.SpaceReturn, modeleval.AggregationDayEqual)
	if skill.Value == nil || math.Abs(*skill.Value-0.5) > 1e-12 {
		t.Fatalf("skill MAE = %v, want 0.5", skill.Value)
	}
	if skill.Baseline != modeleval.BaselineLastPrice || skill.Spec == nil || skill.Spec.Comparison == nil ||
		skill.Spec.Comparison.Formula != "1 - candidate / baseline" || skill.Spec.Comparison.BaselineMetricID != modeleval.MetricPointMAE {
		t.Fatalf("skill comparison spec = %+v", skill.Spec)
	}
}

func TestMetricNAAndReservedRecords(t *testing.T) {
	accumulator := newMetricAccumulator()
	addMetricTestSample(accumulator, "2026-09-01", "sh600000", 0.01, 0.01)
	records := accumulator.records("model:candidate", nil, func(sample metricSample) TargetMetrics { return sample.Predicted }, true)
	r2 := findMetricRecord(t, records, modeleval.MetricPointR2, "model:candidate", modeleval.TargetClose, modeleval.SpacePrice, modeleval.AggregationPooled)
	if r2.Value != nil || r2.Applicability != modeleval.ApplicabilityInsufficientData || r2.Reason == "" {
		t.Fatalf("constant-price R2 = %+v", r2)
	}
	if !r2.HigherIsBetter {
		t.Fatal("point.r2 must be marked higher-is-better")
	}
	reserved := findMetricRecord(t, records, modeleval.MetricIntervalEmpiricalCoverage, "model:candidate", modeleval.TargetAll, modeleval.SpaceNone, modeleval.AggregationPooled)
	if reserved.Value != nil || reserved.Applicability != modeleval.ApplicabilityNotApplicable || reserved.Reason != "point estimate model has no calibrated interval" {
		t.Fatalf("reserved interval record = %+v", reserved)
	}
	foundQuantile := false
	for _, record := range records {
		if record.Spec != nil && record.Spec.Label != nil && record.Spec.Label.Mode == "quantile_30_70" {
			foundQuantile = true
			if record.Applicability != modeleval.ApplicabilityInsufficientData || record.Value != nil ||
				record.Reason != "quantile thresholds require training-window fit" {
				t.Fatalf("quantile record = %+v", record)
			}
		}
	}
	if !foundQuantile {
		t.Fatal("quantile direction records are missing")
	}
	reservedCount := 0
	for _, record := range records {
		if record.Family == modeleval.FamilyInterval || record.Family == modeleval.FamilyRanking || record.Family == modeleval.FamilyTrading {
			reservedCount++
		}
		if err := record.Validate(); err != nil {
			t.Fatalf("invalid metric record %+v: %v", record, err)
		}
	}
	if reservedCount != 11 {
		t.Fatalf("reserved records = %d, want 11", reservedCount)
	}
	if samples, days, ok := metricSampleSummary(records, "model:candidate"); !ok || samples != 1 || days != 1 {
		t.Fatalf("metric sample summary = %d samples, %d days, ok=%v", samples, days, ok)
	}
}

func TestMetricRecordsDeterministic(t *testing.T) {
	forward := newMetricAccumulator()
	reverse := newMetricAccumulator()
	samples := [][2]float64{{0.01, 0.005}, {-0.02, -0.01}, {0.03, 0.04}, {0, 0.01}}
	for index, sample := range samples {
		addMetricTestSample(forward, evalDates(4)[index], "sh600000", sample[0], sample[1])
	}
	for index := len(samples) - 1; index >= 0; index-- {
		addMetricTestSample(reverse, evalDates(4)[index], "sh600000", samples[index][0], samples[index][1])
	}
	selector := func(sample metricSample) TargetMetrics { return sample.Predicted }
	first := forward.records("model:candidate", nil, selector, true)
	second := reverse.records("model:candidate", nil, selector, true)
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("metric records depend on sample insertion order")
	}
	previousKey := ""
	for _, record := range first {
		key, err := record.CanonicalKey()
		if err != nil {
			t.Fatal(err)
		}
		if previousKey != "" && key <= previousKey {
			t.Fatalf("metric records are not strictly sorted/unique at %s", key)
		}
		previousKey = key
	}
}

func TestMetricConfidenceIntervalWithEnoughDays(t *testing.T) {
	accumulator := newMetricAccumulator()
	for index := 0; index < 10; index++ {
		addMetricTestSample(accumulator, evalDates(10)[index], "sh600000", 0.01, 0.005+float64(index)*0.0001)
	}
	records := accumulator.records("model:candidate", nil, func(sample metricSample) TargetMetrics { return sample.Predicted }, false)
	mae := findMetricRecord(t, records, modeleval.MetricPointMAE, "model:candidate", modeleval.TargetClose, modeleval.SpaceReturn, modeleval.AggregationDayEqual)
	if mae.CI == nil || mae.CI.Applicability != modeleval.ApplicabilityOK || mae.CI.Lower == nil || mae.CI.Upper == nil {
		t.Fatalf("MAE CI = %+v", mae.CI)
	}
	if mae.CI.Method != "paired_day_block_bootstrap" || mae.CI.Confidence != 0.95 || mae.CI.Resamples != 10000 || mae.CI.Seed != 1949 {
		t.Fatalf("MAE CI contract = %+v", mae.CI)
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
