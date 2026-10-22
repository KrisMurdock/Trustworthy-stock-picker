package predictor

import (
	"math"
	"testing"
)

func directionArtifact(closeValue, weight, threshold float64) *Artifact {
	artifact := testArtifact()
	artifact.Trees[TargetClose] = []Tree{{Nodes: []Node{{Leaf: true, Value: closeValue}}}}
	artifact.Direction = &DirectionModel{
		Bias:                0,
		Weights:             []float64{weight},
		Means:               []float64{0},
		Stds:                []float64{1},
		ConfidenceThreshold: threshold,
		Samples:             100,
		UpRate:              0.5,
	}
	return &artifact
}

func TestModelAppliesDirectionSignAboveThreshold(t *testing.T) {
	artifact := directionArtifact(-0.01, 10, DefaultDirectionThreshold)
	data, err := SealArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	model, err := LoadModel(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := model.Predict([]float64{1})
	if err != nil {
		t.Fatal(err)
	}
	if got[TargetClose] != 0.01 {
		t.Fatalf("close = %v, want corrected positive 0.01", got[TargetClose])
	}
	if got[TargetHigh] != 0.01 || got[TargetLow] != 0.01 {
		t.Fatalf("direction correction changed high/low: %+v", got)
	}
}

func TestModelKeepsCloseSignBelowDirectionThreshold(t *testing.T) {
	artifact := directionArtifact(-0.01, 0, DefaultDirectionThreshold)
	data, err := SealArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	model, err := LoadModel(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := model.Predict([]float64{1})
	if err != nil {
		t.Fatal(err)
	}
	if got[TargetClose] != -0.01 {
		t.Fatalf("close = %v, want unchanged -0.01", got[TargetClose])
	}
}

func TestDirectionThresholdOneDisablesCorrection(t *testing.T) {
	artifact := directionArtifact(-0.01, 10, 1)
	data, err := SealArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	model, err := LoadModel(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := model.Predict([]float64{1})
	if err != nil {
		t.Fatal(err)
	}
	if got[TargetClose] != -0.01 {
		t.Fatalf("close = %v, want unchanged -0.01 when threshold is 1", got[TargetClose])
	}
}

func TestDirectionProbabilityTreatsNonFiniteFeaturesAsMissing(t *testing.T) {
	artifact := directionArtifact(-0.01, 10, DefaultDirectionThreshold)
	data, err := SealArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	model, err := LoadModel(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		got, err := model.Predict([]float64{value})
		if err != nil {
			t.Fatalf("predict with feature %v: %v", value, err)
		}
		if got[TargetClose] != -0.01 {
			t.Fatalf("close with missing feature %v = %v, want -0.01", value, got[TargetClose])
		}
	}
}

func TestModelRejectsInvalidDirectionModel(t *testing.T) {
	artifact := directionArtifact(-0.01, 10, DefaultDirectionThreshold)
	artifact.Direction.Weights = []float64{10, 20}
	data, err := SealArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadModel(data); err == nil {
		t.Fatal("invalid direction model was accepted")
	}
}

func TestTrainFitsDirectionModelDeterministically(t *testing.T) {
	samples := predGBDTSamples(5, 24)
	opts := predGBDTOptions()
	first, err := Train(samples, predGBDTInfo(), opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Train(predGBDTReverse(samples), predGBDTInfo(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Direction == nil || second.Direction == nil {
		t.Fatal("training did not fit a direction model")
	}
	if first.Direction.Samples != second.Direction.Samples || first.Direction.UpRate != second.Direction.UpRate {
		t.Fatalf("direction metadata changed with input order: %+v / %+v", first.Direction, second.Direction)
	}
	for _, sample := range samples[:10] {
		firstProbability, err := first.Direction.probability(sample.Features)
		if err != nil {
			t.Fatal(err)
		}
		secondProbability, err := second.Direction.probability(sample.Features)
		if err != nil {
			t.Fatal(err)
		}
		if firstProbability != secondProbability {
			t.Fatalf("direction probability changed with input order: %v != %v", firstProbability, secondProbability)
		}
	}
}
