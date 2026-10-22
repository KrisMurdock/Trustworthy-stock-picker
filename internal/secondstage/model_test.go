package secondstage

import (
	"encoding/json"
	"testing"
)

func TestValidateTreeRejectsCyclesAndUnreachableNodes(t *testing.T) {
	cycle := Tree{Nodes: []Node{
		{Feature: 0, Threshold: 1, Left: 1, Right: 1},
		{Feature: 0, Threshold: 1, Left: 0, Right: 0},
	}}
	if err := validateTree(cycle, len(FeatureNames)); err == nil {
		t.Fatal("tree cycle accepted")
	}
	unreachable := Tree{Nodes: []Node{
		{Leaf: true, Value: 1},
		{Leaf: true, Value: 2},
	}}
	if err := validateTree(unreachable, len(FeatureNames)); err == nil {
		t.Fatal("unreachable tree node accepted")
	}
}

func TestModelPredictRejectsFeatureCountMismatch(t *testing.T) {
	model, err := DefaultModel()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Predict(make([]float64, len(FeatureNames)-1)); err == nil {
		t.Fatal("feature count mismatch accepted")
	}
}

func TestParseModelAcceptsV2FeatureContract(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(modelArtifact, &document); err != nil {
		t.Fatal(err)
	}
	document["feature_version"] = FeatureVersionV2
	document["feature_names"] = FeatureNamesV2
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	model, err := parseModel(data, "")
	if err != nil {
		t.Fatal(err)
	}
	if model.FeatureVersion != FeatureVersionV2 || len(model.FeatureNames) != len(FeatureNamesV2) {
		t.Fatalf("v2 feature contract was not retained: %+v", model)
	}
	document["feature_names"] = FeatureNamesV2[:len(FeatureNamesV2)-1]
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseModel(data, ""); err == nil {
		t.Fatal("incomplete v2 feature contract accepted")
	}
}
