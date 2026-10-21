package dayrange

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
)

func sample(date, code string) Observation {
	at, _ := time.Parse(time.RFC3339, date+"T09:00:00+08:00")
	return Observation{Code: code, Date: date, Mode: ModePremarket, AsOf: at, ReferencePrice: 100, ScalePrice: 2,
		Forecast: Forecast{
			High: Estimate{Point: 110, Quantiles: []Quantile{{Level: .1, Price: 108}, {Level: .5, Price: 110}, {Level: .9, Price: 112}}, Interval: &Interval{Lower: 108, Upper: 112, NominalCoverage: .8, Method: "fixture"}},
			Low:  Estimate{Point: 90, Quantiles: []Quantile{{Level: .1, Price: 88}, {Level: .5, Price: 90}, {Level: .9, Price: 92}}, Interval: &Interval{Lower: 88, Upper: 92, NominalCoverage: .8, Method: "fixture"}},
		}, Actual: &Extremes{High: 114, Low: 89}, Baseline: &Extremes{High: 108, Low: 92}}
}
func options() Options {
	return Options{Subject: modeleval.ModelSubject("daily-v1"), Baseline: "historical_range"}
}
func evaluate(t *testing.T, samples ...Observation) Report {
	t.Helper()
	r, err := Evaluate(context.Background(), samples, options())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range r.Metrics {
		if err := m.Validate(); err != nil {
			t.Fatalf("invalid metric %+v: %v", m, err)
		}
		k, _ := m.CanonicalKey()
		if seen[k] {
			t.Fatalf("duplicate metric key %s", k)
		}
		seen[k] = true
	}
	return r
}
func metric(t *testing.T, r Report, id, target string, space modeleval.Space, aggregation modeleval.Aggregation, extra ...modeleval.SliceDimension) modeleval.MetricRecord {
	t.Helper()
	want := append([]modeleval.SliceDimension{{Name: "as_of", Value: "09:00"}, {Name: "mode", Value: string(ModePremarket)}}, extra...)
	for _, m := range r.Metrics {
		if m.MetricID != id || m.Target != target || m.Space != space || m.Aggregation != aggregation || len(m.Slice) != len(want) {
			continue
		}
		found := true
		for _, s := range want {
			ok := false
			for _, v := range m.Slice {
				if v == s {
					ok = true
				}
			}
			found = found && ok
		}
		if found {
			return m
		}
	}
	t.Fatalf("missing metric %s/%s/%s/%s %v", id, target, space, aggregation, extra)
	return modeleval.MetricRecord{}
}
func closeValue(t *testing.T, m modeleval.MetricRecord, want float64) {
	t.Helper()
	if m.Value == nil || math.Abs(*m.Value-want) > 1e-8 {
		t.Fatalf("%s got %v want %v", m.MetricID, m.Value, want)
	}
}
func intervalDims() []modeleval.SliceDimension {
	return []modeleval.SliceDimension{{Name: "coverage", Value: "0.8"}, {Name: "interval_method", Value: "fixture"}}
}

func TestHandCalculatedMetrics(t *testing.T) {
	r := evaluate(t, sample("2026-10-19", "sh600000"))
	closeValue(t, metric(t, r, modeleval.MetricPointMAE, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual), 4)
	closeValue(t, metric(t, r, modeleval.MetricPointRMSE, "high", modeleval.SpaceReturn, modeleval.AggregationDayEqual), 400)
	closeValue(t, metric(t, r, modeleval.MetricPointBias, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual), -4)
	closeValue(t, metric(t, r, modeleval.MetricPointMAE, "high", modeleval.SpaceNone, modeleval.AggregationDayEqual, modeleval.SliceDimension{Name: "normalization", Value: "volatility"}), 2)
	closeValue(t, metric(t, r, modeleval.MetricIntervalPinballLoss, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual, modeleval.SliceDimension{Name: "quantile", Value: "0.1"}), .6)
	closeValue(t, metric(t, r, modeleval.MetricIntervalPinballLoss, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual, modeleval.SliceDimension{Name: "quantile", Value: "0.9"}), 1.8)
	dims := intervalDims()
	closeValue(t, metric(t, r, modeleval.MetricIntervalEmpiricalCoverage, "high", modeleval.SpaceNone, modeleval.AggregationDayEqual, dims...), 0)
	closeValue(t, metric(t, r, modeleval.MetricIntervalEmpiricalCoverage, "low", modeleval.SpaceNone, modeleval.AggregationDayEqual, dims...), 1)
	closeValue(t, metric(t, r, modeleval.MetricIntervalScore, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual, dims...), 24)
	closeValue(t, metric(t, r, modeleval.MetricIntervalWidthMean, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual, dims...), 4)
	closeValue(t, metric(t, r, modeleval.MetricIntervalUpperBreachRate, "high", modeleval.SpaceNone, modeleval.AggregationDayEqual, dims...), 1)
	closeValue(t, metric(t, r, modeleval.MetricIntervalUpperExceedanceMean, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual, dims...), 2)
	closeValue(t, metric(t, r, modeleval.MetricPointSkillMAE, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual), 1-4.0/6)
	m := metric(t, r, modeleval.MetricPointMAE, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual)
	if m.Samples != 1 || m.Days != 1 || m.Stocks != 1 || m.CI == nil || m.CI.Applicability != modeleval.ApplicabilityInsufficientData {
		t.Fatalf("bad evidence %+v", m)
	}
}

func TestJointCoverageIsNotMeanOfMarginals(t *testing.T) {
	a, b := sample("2026-10-19", "a"), sample("2026-10-19", "b")
	a.Actual = &Extremes{High: 114, Low: 90}
	b.Actual = &Extremes{High: 110, Low: 86}
	r := evaluate(t, a, b)
	dims := intervalDims()
	for _, target := range []string{"high", "low"} {
		closeValue(t, metric(t, r, modeleval.MetricIntervalEmpiricalCoverage, target, modeleval.SpaceNone, modeleval.AggregationDayEqual, dims...), .5)
	}
	closeValue(t, metric(t, r, modeleval.MetricIntervalJointCoverage, "all", modeleval.SpaceNone, modeleval.AggregationDayEqual, dims...), 0)
	// Both extremes can lie in the outer envelope while missing their own intervals.
	a.Actual = &Extremes{High: 106, Low: 94}
	r = evaluate(t, a)
	closeValue(t, metric(t, r, modeleval.MetricIntervalJointCoverage, "all", modeleval.SpaceNone, modeleval.AggregationDayEqual, dims...), 0)
	closeValue(t, metric(t, r, modeleval.MetricIntervalEnvelopeCoverage, "all", modeleval.SpaceNone, modeleval.AggregationDayEqual, dims...), 1)
}

func TestIntervalBoundariesAndWidthPenalty(t *testing.T) {
	a := sample("2026-10-19", "a")
	a.Actual = &Extremes{High: 112, Low: 88}
	r := evaluate(t, a)
	dims := intervalDims()
	closeValue(t, metric(t, r, modeleval.MetricIntervalJointCoverage, "all", modeleval.SpaceNone, modeleval.AggregationDayEqual, dims...), 1)
	closeValue(t, metric(t, r, modeleval.MetricIntervalLowerBreachRate, "low", modeleval.SpaceNone, modeleval.AggregationDayEqual, dims...), 0)
	closeValue(t, metric(t, r, modeleval.MetricIntervalScore, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual, dims...), 4)
	a.Forecast.High.Interval.Lower = 100
	a.Forecast.High.Interval.Upper = 120
	r = evaluate(t, a)
	closeValue(t, metric(t, r, modeleval.MetricIntervalScore, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual, dims...), 20)
	a.Actual.Low = 86
	r = evaluate(t, a)
	closeValue(t, metric(t, r, modeleval.MetricIntervalLowerExceedanceMean, "low", modeleval.SpacePrice, modeleval.AggregationDayEqual, dims...), 2)
}

func TestDayEqualAggregationAndUnresolved(t *testing.T) {
	a, b, c := sample("2026-10-19", "a"), sample("2026-10-19", "b"), sample("2026-10-20", "a")
	a.Actual.High = 110
	b.Actual.High = 110
	c.Actual.High = 116
	unresolved := sample("2026-10-20", "c")
	unresolved.Actual = nil
	unresolved.UnresolvedReason = "missing_final_bar"
	r := evaluate(t, a, b, c, unresolved)
	closeValue(t, metric(t, r, modeleval.MetricPointMAE, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual), 3)
	closeValue(t, metric(t, r, modeleval.MetricPointMAE, "high", modeleval.SpacePrice, modeleval.AggregationPooled), 2)
	if r.Submitted != 4 || r.Resolved != 3 || r.Unresolved != 1 {
		t.Fatalf("bad totals %+v", r)
	}
	m := metric(t, r, modeleval.MetricPointMAE, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual)
	if m.Samples != 3 || m.Days != 2 || m.Stocks != 2 {
		t.Fatalf("bad counts %+v", m)
	}
}

func TestEmptyAndPointOnlyReports(t *testing.T) {
	r := evaluate(t)
	if len(r.Metrics) != 0 || r.Status != "insufficient_data" {
		t.Fatalf("bad empty report %+v", r)
	}
	a := sample("2026-10-19", "a")
	a.Forecast.High.Interval = nil
	a.Forecast.Low.Interval = nil
	a.Forecast.High.Quantiles = nil
	a.Forecast.Low.Quantiles = nil
	a.Actual = nil
	a.UnresolvedReason = "suspended"
	r = evaluate(t, a)
	m := metric(t, r, modeleval.MetricPointMAE, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual)
	if m.Value != nil || m.Applicability != modeleval.ApplicabilityInsufficientData {
		t.Fatalf("missing actual became zero: %+v", m)
	}
	m = metric(t, r, modeleval.MetricIntervalEmpiricalCoverage, "high", modeleval.SpaceNone, modeleval.AggregationDayEqual)
	if m.Value != nil || m.Applicability != modeleval.ApplicabilityNotApplicable {
		t.Fatalf("point forecast became interval: %+v", m)
	}
	payload, err := json.Marshal(r)
	if err != nil || !strings.Contains(string(payload), `"value":null`) {
		t.Fatalf("null JSON: %s %v", payload, err)
	}
}

func TestBaselineUsesOnlyMatchedSamplesAndZeroDenominator(t *testing.T) {
	a, b := sample("2026-10-19", "a"), sample("2026-10-19", "b")
	b.Baseline = nil
	b.Actual.High = 200
	r := evaluate(t, a, b)
	m := metric(t, r, modeleval.MetricPointSkillMAE, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual)
	closeValue(t, m, 1-4.0/6)
	if m.Samples != 1 || m.Baseline != "historical_range" {
		t.Fatalf("unpaired comparison %+v", m)
	}
	a.Baseline.High = a.Actual.High
	r = evaluate(t, a)
	m = metric(t, r, modeleval.MetricPointSkillMAE, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual)
	if m.Value != nil || m.Applicability != modeleval.ApplicabilityNotApplicable {
		t.Fatalf("zero baseline denominator %+v", m)
	}
}

func TestModeTimeAndDimensionSlices(t *testing.T) {
	a, b, c := sample("2026-10-19", "a"), sample("2026-10-19", "b"), sample("2026-10-19", "c")
	b.Mode = ModeRemaining
	b.AsOf = b.AsOf.Add(90 * time.Minute)
	c.Mode = ModeFullDay
	c.AsOf = c.AsOf.Add(90 * time.Minute)
	a.Slices = []modeleval.SliceDimension{{Name: "regime", Value: "down"}}
	r := evaluate(t, a, b, c)
	modes := map[string]bool{}
	for _, m := range r.Metrics {
		if m.MetricID == modeleval.MetricPointMAE && m.Target == "high" && m.Space == modeleval.SpacePrice && m.Aggregation == modeleval.AggregationDayEqual && len(m.Slice) == 2 {
			for _, s := range m.Slice {
				if s.Name == "mode" {
					modes[s.Value] = true
				}
			}
		}
	}
	if len(modes) != 3 {
		t.Fatalf("modes mixed: %v", modes)
	}
	closeValue(t, metric(t, r, modeleval.MetricPointMAE, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual, modeleval.SliceDimension{Name: "regime", Value: "down"}), 4)
	closeValue(t, metric(t, r, modeleval.MetricPointMAE, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual, modeleval.SliceDimension{Name: "month", Value: "2026-10"}), 4)
}

func TestValidationAndCancellation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Observation)
	}{
		{"zero reference", func(s *Observation) { s.ReferencePrice = 0 }},
		{"nan prediction", func(s *Observation) { s.Forecast.High.Point = math.NaN() }},
		{"infinite actual", func(s *Observation) { s.Actual.Low = math.Inf(1) }},
		{"inverted actual", func(s *Observation) { s.Actual.Low = 200 }},
		{"inverted point", func(s *Observation) { s.Forecast.Low.Point = 200 }},
		{"inverted interval", func(s *Observation) { s.Forecast.High.Interval.Lower = 120 }},
		{"invalid coverage", func(s *Observation) { s.Forecast.High.Interval.NominalCoverage = 1 }},
		{"missing interval pair", func(s *Observation) { s.Forecast.Low.Interval = nil }},
		{"mixed interval level", func(s *Observation) { s.Forecast.Low.Interval.NominalCoverage = .9 }},
		{"crossing quantiles", func(s *Observation) { s.Forecast.High.Quantiles[0].Price = 120 }},
		{"duplicate quantile", func(s *Observation) { s.Forecast.High.Quantiles[1].Level = .1 }},
		{"invalid date", func(s *Observation) { s.Date = "2026-02-30" }},
		{"date mismatch", func(s *Observation) { s.AsOf = s.AsOf.AddDate(0, 0, 1) }},
		{"missing as of", func(s *Observation) { s.AsOf = time.Time{} }},
		{"late premarket", func(s *Observation) { s.AsOf = s.AsOf.Add(time.Hour) }},
		{"close already known", func(s *Observation) { s.Mode = ModeRemaining; s.AsOf = s.AsOf.Add(6 * time.Hour) }},
		{"lunch", func(s *Observation) { s.Mode = ModeRemaining; s.AsOf = s.AsOf.Add(3 * time.Hour) }},
		{"unknown mode", func(s *Observation) { s.Mode = "unknown" }},
		{"missing unresolved reason", func(s *Observation) { s.Actual = nil }},
		{"resolved with reason", func(s *Observation) { s.UnresolvedReason = "missing" }},
		{"reserved slice", func(s *Observation) { s.Slices = []modeleval.SliceDimension{{Name: "mode", Value: "fake"}} }},
		{"negative scale", func(s *Observation) { s.ScalePrice = -1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := sample("2026-10-19", "a")
			tc.mutate(&s)
			if _, err := Evaluate(context.Background(), []Observation{s}, options()); err == nil {
				t.Fatal("invalid sample accepted")
			}
		})
	}
	a := sample("2026-10-19", "a")
	if _, err := Evaluate(context.Background(), []Observation{a, a}, options()); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err := Evaluate(context.Background(), []Observation{a}, Options{}); err == nil {
		t.Fatal("missing subject accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Evaluate(ctx, []Observation{a}, options()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestDeterministicJSONAndDayBlockConfidence(t *testing.T) {
	var samples []Observation
	for i := 0; i < 12; i++ {
		date := time.Date(2026, 9, 1+i, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
		s := sample(date, "a")
		s.Actual.High = 110 + float64(i)
		samples = append(samples, s)
	}
	before, _ := json.Marshal(samples)
	r := evaluate(t, samples...)
	after, _ := json.Marshal(samples)
	if string(before) != string(after) {
		t.Fatal("evaluator mutated input")
	}
	m := metric(t, r, modeleval.MetricPointMAE, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual)
	if m.CI == nil || m.CI.Applicability != modeleval.ApplicabilityOK || m.CI.Lower == nil || m.CI.Upper == nil || *m.CI.Lower > *m.Value || *m.CI.Upper < *m.Value {
		t.Fatalf("invalid CI %+v", m)
	}
	for i, j := 0, len(samples)-1; i < j; i, j = i+1, j-1 {
		samples[i], samples[j] = samples[j], samples[i]
	}
	r2 := evaluate(t, samples...)
	j1, _ := json.Marshal(r)
	j2, _ := json.Marshal(r2)
	if string(j1) != string(j2) {
		t.Fatal("input order changed report")
	}
	// Replicating a whole day's cross-section must not manufacture more independent days.
	var replicated []Observation
	for _, s := range samples {
		replicated = append(replicated, s)
		copy := s
		copy.Code = "b"
		replicated = append(replicated, copy)
	}
	r3 := evaluate(t, replicated...)
	m3 := metric(t, r3, modeleval.MetricPointMAE, "high", modeleval.SpacePrice, modeleval.AggregationDayEqual)
	if !reflect.DeepEqual(m.CI, m3.CI) {
		t.Fatalf("same-day replication changed CI: %+v %+v", m.CI, m3.CI)
	}
}
