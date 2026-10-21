package dayrange

import (
	"bytes"
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
)

func trainingSamples(count int) []Sample {
	out := make([]Sample, 0, count)
	for i := 0; i < count; i++ {
		values := make([]*float64, len(featureNames))
		for j := range values {
			value := math.Sin(float64(i+1)*float64(j+1)*.017) * .01
			values[j] = &value
		}
		features := make([]Feature, len(featureNames))
		for j, name := range featureNames {
			features[j] = Feature{Name: name, Value: values[j]}
		}
		reference := 10 + float64(i%7)*.1
		snapshot := Snapshot{SchemaVersion: SnapshotSchemaVersion, Code: "sz000001", TargetDate: time.Date(2025, 1, 1, 0, 0, 0, 0, shanghai).AddDate(0, 0, i).Format(time.DateOnly),
			Mode: ModePremarket, ReferencePrice: reference, Features: features, Availability: Availability{Stock: "available"}, FeatureVersion: FeatureVersion}
		out = append(out, Sample{Snapshot: snapshot, Actual: &Extremes{High: reference * (1.02 + float64(i%3)*.002), Low: reference * (.98 - float64(i%4)*.001)}})
	}
	return out
}

func TestTrainPointModelIsDeterministicAndSealed(t *testing.T) {
	samples := trainingSamples(90)
	opts := DefaultTrainOptions()
	opts.Trees, opts.MaxDepth, opts.MinLeaf = 6, 2, 5
	info := ModelInfo{SchemaVersion: ModelSchemaVersion, CodeVersion: "test", CreatedAt: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC),
		TrainFrom: "2025-01-01", TrainTo: "2025-01-30", CalibrationFrom: "2025-01-31", CalibrationTo: "2025-03-01", ValidationFrom: "2025-03-02", ValidationTo: "2025-03-20", ValidFor: "2026-10-22"}
	first, err := TrainPointModel(context.Background(), samples[:60], info, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := Calibrate(first, samples[60:]); err != nil {
		t.Fatal(err)
	}
	if err := SealArtifact(first); err != nil {
		t.Fatal(err)
	}
	second, err := TrainPointModel(context.Background(), samples[:60], info, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := Calibrate(second, samples[60:]); err != nil {
		t.Fatal(err)
	}
	if err := SealArtifact(second); err != nil {
		t.Fatal(err)
	}
	if first.ModelID != second.ModelID || first.Checksum != second.Checksum {
		t.Fatalf("deterministic training changed identity: %s/%s vs %s/%s", first.ModelID, first.Checksum, second.ModelID, second.Checksum)
	}
	model, err := LoadArtifact(mustMarshalArtifact(t, first))
	if err != nil {
		t.Fatal(err)
	}
	forecast, err := model.Predict(samples[0].Snapshot.Values(), samples[0].Snapshot.ReferencePrice)
	if err != nil {
		t.Fatal(err)
	}
	if forecast.High.Point < forecast.Low.Point || forecast.High.Quantiles[0].Price > forecast.High.Quantiles[1].Price || forecast.Low.Interval.Lower > forecast.Low.Interval.Upper {
		t.Fatalf("invalid calibrated forecast: %+v", forecast)
	}
	data := mustMarshalArtifact(t, first)
	tampered := bytes.Replace(data, []byte(first.Checksum), []byte(strings.Repeat("0", len(first.Checksum))), 1)
	if _, err := LoadArtifact(tampered); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered artifact accepted: %v", err)
	}
}

func mustMarshalArtifact(t *testing.T, artifact *Artifact) []byte {
	t.Helper()
	data, err := MarshalArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func metricRecord(id, target string, space modeleval.Space, value float64, days, samples int, extra ...modeleval.SliceDimension) modeleval.MetricRecord {
	slice := []modeleval.SliceDimension{{Name: "as_of", Value: "09:00"}, {Name: "mode", Value: "premarket"}}
	slice = append(slice, extra...)
	return modeleval.MetricRecord{MetricID: id, Target: target, Space: space, Aggregation: modeleval.AggregationDayEqual,
		Value: modeleval.Float(value), Applicability: modeleval.ApplicabilityOK, Days: days, Samples: samples, Slice: slice}
}

func gateReports(candidateMAE, candidateCoverage, baselineWidth float64) (Report, Report) {
	base := []modeleval.MetricRecord{
		metricRecord(modeleval.MetricPointMAE, modeleval.TargetHigh, modeleval.SpacePrice, 10, 20, 100),
		metricRecord(modeleval.MetricPointMAE, modeleval.TargetLow, modeleval.SpacePrice, 10, 20, 100),
		metricRecord(modeleval.MetricIntervalWidthMean, modeleval.TargetHigh, modeleval.SpacePrice, baselineWidth, 20, 100, modeleval.SliceDimension{Name: "coverage", Value: "0.8"}, modeleval.SliceDimension{Name: "interval_method", Value: "baseline"}),
		metricRecord(modeleval.MetricIntervalWidthMean, modeleval.TargetLow, modeleval.SpacePrice, baselineWidth, 20, 100, modeleval.SliceDimension{Name: "coverage", Value: "0.8"}, modeleval.SliceDimension{Name: "interval_method", Value: "baseline"}),
	}
	candidate := []modeleval.MetricRecord{
		metricRecord(modeleval.MetricPointMAE, modeleval.TargetHigh, modeleval.SpacePrice, candidateMAE, 20, 100),
		metricRecord(modeleval.MetricPointMAE, modeleval.TargetLow, modeleval.SpacePrice, candidateMAE, 20, 100),
		metricRecord(modeleval.MetricIntervalEmpiricalCoverage, modeleval.TargetHigh, modeleval.SpaceNone, candidateCoverage, 20, 100, modeleval.SliceDimension{Name: "coverage", Value: "0.8"}, modeleval.SliceDimension{Name: "interval_method", Value: "model"}),
		metricRecord(modeleval.MetricIntervalEmpiricalCoverage, modeleval.TargetLow, modeleval.SpaceNone, candidateCoverage, 20, 100, modeleval.SliceDimension{Name: "coverage", Value: "0.8"}, modeleval.SliceDimension{Name: "interval_method", Value: "model"}),
		metricRecord(modeleval.MetricIntervalWidthMean, modeleval.TargetHigh, modeleval.SpacePrice, 10, 20, 100, modeleval.SliceDimension{Name: "coverage", Value: "0.8"}, modeleval.SliceDimension{Name: "interval_method", Value: "model"}),
		metricRecord(modeleval.MetricIntervalWidthMean, modeleval.TargetLow, modeleval.SpacePrice, 10, 20, 100, modeleval.SliceDimension{Name: "coverage", Value: "0.8"}, modeleval.SliceDimension{Name: "interval_method", Value: "model"}),
		metricRecord(modeleval.MetricIntervalJointCoverage, modeleval.TargetAll, modeleval.SpaceNone, .7, 20, 100, modeleval.SliceDimension{Name: "coverage", Value: "0.8"}, modeleval.SliceDimension{Name: "interval_method", Value: "model"}),
	}
	return Report{Metrics: candidate}, Report{Metrics: base}
}

func hasReason(gate Gate, reason string) bool {
	for _, item := range gate.Reasons {
		if item == reason {
			return true
		}
	}
	return false
}

func TestCheckGateUsesMarginalCoverageWidthAndPointError(t *testing.T) {
	candidate, baseline := gateReports(9, .8, 10)
	if gate := CheckGate(candidate, baseline, 20, 100); !gate.Passed || len(gate.Reasons) != 0 {
		t.Fatalf("valid candidate failed: %+v", gate)
	}
	candidate, baseline = gateReports(9, .9, 10)
	if gate := CheckGate(candidate, baseline, 20, 100); gate.Passed || !hasReason(gate, "marginal_coverage_outside_75_85") {
		t.Fatalf("coverage gate accepted: %+v", gate)
	}
	candidate, baseline = gateReports(9.6, .8, 10)
	if gate := CheckGate(candidate, baseline, 20, 100); gate.Passed || !hasReason(gate, "high_point_not_5pct_better") {
		t.Fatalf("point gate accepted: %+v", gate)
	}
	candidate, baseline = gateReports(9, .8, 0)
	if gate := CheckGate(candidate, baseline, 20, 100); gate.Passed || !hasReason(gate, "missing_required_metrics") {
		t.Fatalf("invalid width ratio was treated as passing: %+v", gate)
	}
}

func TestChronologicalSplitNeverLeaksValidation(t *testing.T) {
	days := weekdayDays("2025-01-01", 220)
	samples := make([]Sample, 0, len(days))
	for _, day := range days {
		samples = append(samples, Sample{Snapshot: Snapshot{TargetDate: day, Code: "sz000001"}, Actual: &Extremes{High: 11, Low: 9}})
	}
	cfg := config.DefaultDayRange()
	split, err := splitChronologically(samples, days[len(days)-1], cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(split.train) < cfg.MinTrainingDays || len(split.calibration) != cfg.CalibrationDays || len(split.validation) != cfg.ValidationDays {
		t.Fatalf("unexpected split sizes: %d/%d/%d", len(split.train), len(split.calibration), len(split.validation))
	}
	if split.calibrationFrom <= split.trainTo || split.validationFrom <= split.calibrationTo {
		t.Fatalf("split windows overlap: %+v", split)
	}
}
