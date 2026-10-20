package predictor

import (
	"bytes"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func predGBDTInfo() ModelInfo {
	return ModelInfo{
		FeatureNames: []string{"x0", "x1"},
		CreatedAt:    time.Date(2026, 9, 20, 16, 0, 0, 0, time.UTC),
		ValidFor:     "2026-09-21",
	}
}

func predGBDTOptions() TrainOptions {
	opts := DefaultTrainOptions()
	opts.Trees = 60
	opts.MaxDepth = 3
	opts.MinLeaf = 5
	opts.LearningRate = 0.2
	opts.Bins = 16
	return opts
}

func predGBDTSamples(days, perDay int) []Sample {
	out := make([]Sample, 0, days*perDay)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, market.Shanghai)
	for d := 0; d < days; d++ {
		date := start.AddDate(0, 0, d)
		for i := 0; i < perDay; i++ {
			index := d*perDay + i
			x0 := math.Sin(float64(index) * 0.17)
			x1 := math.Cos(float64(index) * 0.11)
			if index%7 == 0 {
				x1 = math.NaN()
			}
			x1Value := x1
			if math.IsNaN(x1Value) {
				x1Value = 0
			}
			closeLabel := 0.012*x0 - 0.004*x1Value
			highLabel := closeLabel + 0.003 + 0.001*math.Abs(x0)
			lowLabel := closeLabel - 0.003 - 0.001*math.Abs(x1Value)
			inputEnd := time.Date(date.Year(), date.Month(), date.Day(), 10, 0, 0, 0, market.Shanghai).Add(time.Duration(i) * 5 * time.Minute)
			out = append(out, Sample{
				Code:          fmt.Sprintf("sh%06d", i+1),
				Date:          date.Format("2006-01-02"),
				InputEnd:      inputEnd,
				TargetEnd:     inputEnd.Add(5 * time.Minute),
				InputOpen:     100,
				InputHigh:     100.2,
				InputLow:      99.8,
				InputClose:    100,
				BaselineRange: 0.01,
				Features:      []float64{x0, x1},
				Labels: map[Target]float64{
					TargetHigh:  highLabel,
					TargetLow:   lowLabel,
					TargetClose: closeLabel,
				},
			})
		}
	}
	return out
}

func predGBDTReverse(samples []Sample) []Sample {
	out := append([]Sample(nil), samples...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func TestTrainDeterministic(t *testing.T) {
	samples := predGBDTSamples(5, 24)
	info := predGBDTInfo()
	opts := predGBDTOptions()
	first, err := Train(samples, info, opts)
	if err != nil {
		t.Fatalf("Train first: %v", err)
	}
	second, err := Train(predGBDTReverse(samples), info, opts)
	if err != nil {
		t.Fatalf("Train second: %v", err)
	}
	firstBytes, err := SealArtifact(first)
	if err != nil {
		t.Fatalf("SealArtifact first: %v", err)
	}
	secondBytes, err := SealArtifact(second)
	if err != nil {
		t.Fatalf("SealArtifact second: %v", err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("training is not deterministic across input order")
	}
}

func TestArtifactRoundTrip(t *testing.T) {
	samples := predGBDTSamples(5, 24)
	artifact, err := Train(samples, predGBDTInfo(), predGBDTOptions())
	if err != nil {
		t.Fatalf("Train: %v", err)
	}
	raw, err := SealArtifact(artifact)
	if err != nil {
		t.Fatalf("SealArtifact: %v", err)
	}
	loaded, err := LoadModel(raw)
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	original := &Model{Artifact: *artifact}
	for i := range samples {
		want, err := original.Predict(samples[i].Features)
		if err != nil {
			t.Fatalf("original Predict: %v", err)
		}
		got, err := loaded.Predict(samples[i].Features)
		if err != nil {
			t.Fatalf("loaded Predict: %v", err)
		}
		for _, target := range Targets {
			if math.Abs(want[target]-got[target]) > 1e-12 {
				t.Fatalf("round trip changed %s: %v -> %v", target, want[target], got[target])
			}
		}
	}
	if loaded.Artifact.Info.Checksum == "" || loaded.Artifact.Info.ModelID == "" {
		t.Fatal("loaded artifact is missing identity metadata")
	}
}

func TestEvaluateBeatsLastBarBaseline(t *testing.T) {
	samples := predGBDTSamples(6, 30)
	var train, validation []Sample
	for _, sample := range samples {
		if sample.Date < "2026-09-06" {
			train = append(train, sample)
		} else {
			validation = append(validation, sample)
		}
	}
	artifact, err := Train(train, predGBDTInfo(), predGBDTOptions())
	if err != nil {
		t.Fatalf("Train: %v", err)
	}
	metrics, bands, err := Evaluate(&Model{Artifact: *artifact}, validation)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if metrics.Samples != len(validation) {
		t.Fatalf("evaluated %d samples, want %d", metrics.Samples, len(validation))
	}
	if metrics.MAE.Close >= metrics.BaselineLastMAE.Close {
		t.Fatalf("model close MAE %v did not beat last-bar baseline %v", metrics.MAE.Close, metrics.BaselineLastMAE.Close)
	}
	if metrics.DirectionAccuracy < 0.6 {
		t.Fatalf("direction accuracy %v is unexpectedly low", metrics.DirectionAccuracy)
	}
	if bands.High <= 0 || bands.Low <= 0 || bands.Close <= 0 {
		t.Fatalf("residual bands must be positive: %+v", bands)
	}
}

func TestWalkForwardPoolsValidationDays(t *testing.T) {
	samples := predGBDTSamples(6, 30)
	metrics, bands, err := WalkForward(samples, predGBDTInfo(), 2, predGBDTOptions())
	if err != nil {
		t.Fatalf("WalkForward: %v", err)
	}
	if metrics.Samples != 60 {
		t.Fatalf("walk-forward evaluated %d samples, want 60", metrics.Samples)
	}
	if metrics.Days != 2 {
		t.Fatalf("walk-forward covered %d days, want 2", metrics.Days)
	}
	if bands.Close <= 0 {
		t.Fatalf("walk-forward close band must be positive: %+v", bands)
	}
}

func TestTrainRejectsInvalidSamples(t *testing.T) {
	info := predGBDTInfo()
	opts := predGBDTOptions()
	if _, err := Train(nil, info, opts); err == nil {
		t.Fatal("empty samples must fail")
	}
	badFeature := predGBDTSamples(1, 2)
	badFeature[0].Features = []float64{1}
	if _, err := Train(badFeature, info, opts); err == nil {
		t.Fatal("feature count mismatch must fail")
	}
	missingLabel := predGBDTSamples(1, 2)
	delete(missingLabel[0].Labels, TargetLow)
	if _, err := Train(missingLabel, info, opts); err == nil {
		t.Fatal("missing label must fail")
	}
	infinite := predGBDTSamples(1, 2)
	infinite[0].Features[0] = math.Inf(1)
	if _, err := Train(infinite, info, opts); err == nil {
		t.Fatal("infinite feature must fail")
	}
}

func TestEvaluateRejectsNilModel(t *testing.T) {
	if _, _, err := Evaluate(nil, predGBDTSamples(1, 1)); err == nil {
		t.Fatal("nil model must fail")
	}
}

func BenchmarkTrainRealistic(b *testing.B) {
	names := FeatureNames()
	rows := 5000
	samples := make([]Sample, rows)
	state := uint64(1949)
	for i := range samples {
		features := make([]float64, len(names))
		for f := range features {
			state = state*6364136223846793005 + 1442695040888963407
			features[f] = float64(int64(state>>33)%2001-1000) / 1000
		}
		closeLabel := 0.004*features[0] - 0.003*features[1] + 0.002*features[2]
		samples[i] = Sample{
			Code: fmt.Sprintf("sh%06d", i+1), Date: "2026-09-01", InputClose: 100,
			InputHigh: 100.2, InputLow: 99.8, BaselineRange: 0.01,
			Features: features,
			Labels: map[Target]float64{
				TargetHigh:  closeLabel + 0.003,
				TargetLow:   closeLabel - 0.003,
				TargetClose: closeLabel,
			},
		}
	}
	info := ModelInfo{FeatureNames: names, CreatedAt: time.Date(2026, 9, 20, 16, 0, 0, 0, time.UTC)}
	opts := DefaultTrainOptions()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Train(samples, info, opts); err != nil {
			b.Fatal(err)
		}
	}
}
