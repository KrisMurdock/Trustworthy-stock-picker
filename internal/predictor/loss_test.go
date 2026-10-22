package predictor

import (
	"bytes"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
)

func lossTestSamples() []Sample {
	const count = 400
	out := make([]Sample, 0, count)
	start := time.Date(2026, 9, 1, 10, 0, 0, 0, market.Shanghai)
	for i := 0; i < count; i++ {
		label := 0.0
		if i%20 == 0 {
			label = 0.04
		}
		end := start.Add(time.Duration(i) * 5 * time.Minute)
		out = append(out, Sample{
			Code:          fmt.Sprintf("sh%06d", i%20+1),
			Date:          end.Format("2006-01-02"),
			InputEnd:      end,
			TargetEnd:     end.Add(5 * time.Minute),
			InputOpen:     100,
			InputHigh:     100.2,
			InputLow:      99.8,
			InputClose:    100,
			BaselineRange: 0.01,
			Features:      []float64{0, 0},
			Labels: map[Target]float64{
				TargetHigh:  0,
				TargetLow:   0,
				TargetClose: label,
			},
		})
	}
	return out
}

func TestTrainL1UsesMedianBaseAndImprovesMAE(t *testing.T) {
	samples := lossTestSamples()
	opts := predGBDTOptions()
	opts.Trees = 1
	opts.MaxDepth = 1
	opts.MinLeaf = 5
	opts.LearningRate = 0.1
	opts.Bins = 4

	l2Artifact, err := Train(samples, predGBDTInfo(), opts)
	if err != nil {
		t.Fatalf("Train L2: %v", err)
	}
	opts.Loss = LossL1
	l1Artifact, err := Train(samples, predGBDTInfo(), opts)
	if err != nil {
		t.Fatalf("Train L1: %v", err)
	}
	if l1Artifact.Info.Loss != string(LossL1) {
		t.Fatalf("L1 artifact loss = %q", l1Artifact.Info.Loss)
	}
	if math.Abs(l2Artifact.Base[TargetClose]) < 1e-9 {
		t.Fatalf("L2 base = %v, want a positive mean", l2Artifact.Base[TargetClose])
	}
	if math.Abs(l1Artifact.Base[TargetClose]) > 1e-9 {
		t.Fatalf("L1 base = %v, want the zero median", l1Artifact.Base[TargetClose])
	}

	l2Records, _, err := Evaluate(&Model{Artifact: *l2Artifact}, samples)
	if err != nil {
		t.Fatalf("Evaluate L2: %v", err)
	}
	l1Records, _, err := Evaluate(&Model{Artifact: *l1Artifact}, samples)
	if err != nil {
		t.Fatalf("Evaluate L1: %v", err)
	}
	l2MAE := metricValue(t, l2Records, "point.mae", "close")
	l1MAE := metricValue(t, l1Records, "point.mae", "close")
	if l1MAE >= l2MAE {
		t.Fatalf("L1 close MAE = %v, want < L2 %v", l1MAE, l2MAE)
	}
}

func TestTrainL1Deterministic(t *testing.T) {
	samples := predGBDTSamples(5, 24)
	opts := predGBDTOptions()
	opts.Loss = LossL1
	opts.Trees = 10
	opts.MaxDepth = 2
	first, err := Train(samples, predGBDTInfo(), opts)
	if err != nil {
		t.Fatalf("Train first: %v", err)
	}
	second, err := Train(predGBDTReverse(samples), predGBDTInfo(), opts)
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
		t.Fatal("L1 training is not deterministic across input order")
	}
}

func TestLoadModelRejectsUnknownLoss(t *testing.T) {
	artifact, err := Train(predGBDTSamples(5, 24), predGBDTInfo(), predGBDTOptions())
	if err != nil {
		t.Fatalf("Train: %v", err)
	}
	artifact.Info.Loss = "huber"
	raw, err := SealArtifact(artifact)
	if err != nil {
		t.Fatalf("SealArtifact: %v", err)
	}
	if _, err := LoadModel(raw); err == nil {
		t.Fatal("unknown loss was accepted")
	}
}

func metricValue(t *testing.T, records []modeleval.MetricRecord, metricID, target string) float64 {
	t.Helper()
	for _, record := range records {
		if record.MetricID == metricID && record.Target == target && record.Space == modeleval.SpaceReturn &&
			record.Subject == "model:candidate" && record.Value != nil {
			return *record.Value
		}
	}
	t.Fatalf("metric %s target %s not found", metricID, target)
	return 0
}

func TestTrainCloseLossOverrideOnlyAffectsClose(t *testing.T) {
	samples := lossTestSamples()
	baselineOpts := predGBDTOptions()
	baseline, err := Train(samples, predGBDTInfo(), baselineOpts)
	if err != nil {
		t.Fatalf("Train baseline: %v", err)
	}
	opts := baselineOpts
	opts.Loss = LossL2
	opts.CloseLoss = LossL1
	artifact, err := Train(samples, predGBDTInfo(), opts)
	if err != nil {
		t.Fatalf("Train: %v", err)
	}
	if artifact.Info.Loss != string(LossL2) || artifact.Info.CloseLoss != string(LossL1) {
		t.Fatalf("loss metadata = %q/%q", artifact.Info.Loss, artifact.Info.CloseLoss)
	}
	if math.Abs(artifact.Base[TargetClose]) > 1e-9 {
		t.Fatalf("close base = %v, want the L1 median", artifact.Base[TargetClose])
	}
	if artifact.Base[TargetHigh] != 0 || artifact.Base[TargetLow] != 0 {
		t.Fatalf("high/low base = %v/%v, want L2 means", artifact.Base[TargetHigh], artifact.Base[TargetLow])
	}
	if !reflect.DeepEqual(artifact.Trees[TargetHigh], baseline.Trees[TargetHigh]) || !reflect.DeepEqual(artifact.Trees[TargetLow], baseline.Trees[TargetLow]) {
		t.Fatal("close-loss override changed the high/low ensembles")
	}
	if reflect.DeepEqual(artifact.Trees[TargetClose], baseline.Trees[TargetClose]) {
		t.Fatal("close-loss override did not change the close ensemble")
	}
}

func TestProductionTrainOptionsUseL1Close(t *testing.T) {
	opts := ProductionTrainOptions()
	if opts.Loss != LossL2 || opts.CloseLoss != LossL1 {
		t.Fatalf("production losses = %q/%q, want l2/l1", opts.Loss, opts.CloseLoss)
	}
	if defaults := DefaultTrainOptions(); defaults.CloseLoss != "" {
		t.Fatalf("evaluation defaults changed close loss to %q", defaults.CloseLoss)
	}
}
