package predictor

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
	"time"
)

func testArtifact() Artifact {
	leaf := Tree{Nodes: []Node{{Leaf: true, Value: 0.01}}}
	return Artifact{
		SchemaVersion: ArtifactSchemaVersion,
		Info: ModelInfo{
			SchemaVersion:  ArtifactSchemaVersion,
			CreatedAt:      time.Date(2026, 10, 20, 15, 30, 0, 0, time.UTC),
			DataFrom:       "2026-09-18",
			DataTo:         "2026-10-19",
			ValidFor:       "2026-10-20",
			FeatureVersion: FeatureVersion,
			FeatureNames:   []string{"f1"},
		},
		Hyperparameters: Hyperparameters{Trees: 1, MaxDepth: 1, MinLeaf: 1, LearningRate: 1, Seed: 1, Bins: 4},
		Base:            map[Target]float64{TargetHigh: 0, TargetLow: 0, TargetClose: 0},
		Trees:           map[Target][]Tree{TargetHigh: {leaf}, TargetLow: {leaf}, TargetClose: {leaf}},
		LabelClip:       map[Target]float64{TargetHigh: 0.05, TargetLow: 0.05, TargetClose: 0.05},
	}
}

func TestModelArtifactRoundTripAndChecksum(t *testing.T) {
	artifact := testArtifact()
	data, err := SealArtifact(&artifact)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Info.ModelID == "" || artifact.Info.Checksum == "" {
		t.Fatal("artifact was not sealed")
	}
	model, err := LoadModel(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := model.Predict([]float64{1})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range Targets {
		if got[target] != 0.01 {
			t.Fatalf("%s = %v, want 0.01", target, got[target])
		}
	}
}

func TestModelRejectsChecksumMismatch(t *testing.T) {
	artifact := testArtifact()
	data, err := SealArtifact(&artifact)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Replace(data, []byte(`"value": 0.01`), []byte(`"value": 0.02`), 1)
	if bytes.Equal(corrupt, data) {
		t.Fatal("fixture did not contain a leaf value")
	}
	if _, err := LoadModel(corrupt); err == nil {
		t.Fatal("corrupt artifact was accepted")
	}
}

func TestMissingFeatureFollowsTreeDirection(t *testing.T) {
	artifact := testArtifact()
	tree := Tree{Nodes: []Node{
		{Feature: 0, Threshold: 0.5, Left: 1, Right: 2, MissingLeft: true},
		{Leaf: true, Value: 0.01},
		{Leaf: true, Value: -0.02},
	}}
	artifact.Trees = map[Target][]Tree{TargetHigh: {tree}, TargetLow: {tree}, TargetClose: {tree}}
	artifact.Hyperparameters.LearningRate = 1
	data, err := SealArtifact(&artifact)
	if err != nil {
		t.Fatal(err)
	}
	model, err := LoadModel(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := model.Predict([]float64{math.NaN()})
	if err != nil {
		t.Fatal(err)
	}
	if got[TargetHigh] != 0.01 {
		t.Fatalf("missing value routed to %v, want 0.01", got[TargetHigh])
	}
}

func TestSealedArtifactIsDeterministicJSON(t *testing.T) {
	a := testArtifact()
	b := testArtifact()
	dataA, err := SealArtifact(&a)
	if err != nil {
		t.Fatal(err)
	}
	dataB, err := SealArtifact(&b)
	if err != nil {
		t.Fatal(err)
	}
	if a.Info.Checksum != b.Info.Checksum {
		t.Fatal("same artifact produced different checksums")
	}
	var decoded map[string]any
	if err := json.Unmarshal(dataA, &decoded); err != nil || len(decoded) == 0 {
		t.Fatalf("artifact is not JSON: %v", err)
	}
	_ = dataB
}

func TestResealArtifactKeepsModelIDAndLoads(t *testing.T) {
	artifact := testArtifact()
	first, err := SealArtifact(&artifact)
	if err != nil {
		t.Fatal(err)
	}
	modelID := artifact.Info.ModelID
	artifact.Info.DataTo = "2026-10-20"
	resealed, err := ResealArtifact(&artifact)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Info.ModelID != modelID {
		t.Fatalf("model ID changed from %q to %q", modelID, artifact.Info.ModelID)
	}
	if bytes.Equal(first, resealed) {
		t.Fatal("reseal did not update metadata or checksum")
	}
	loaded, err := LoadModel(resealed)
	if err != nil {
		t.Fatalf("load resealed artifact: %v", err)
	}
	if loaded.Artifact.Info.DataTo != "2026-10-20" || loaded.Artifact.Info.ModelID != modelID {
		t.Fatalf("unexpected resealed metadata: %+v", loaded.Artifact.Info)
	}
}
