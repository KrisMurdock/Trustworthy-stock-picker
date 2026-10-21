package secondstage

import "testing"

func TestValidateTreeRejectsCyclesAndUnreachableNodes(t *testing.T) {
	cycle := Tree{Nodes: []Node{
		{Feature: 0, Threshold: 1, Left: 1, Right: 1},
		{Feature: 0, Threshold: 1, Left: 0, Right: 0},
	}}
	if err := validateTree(cycle); err == nil {
		t.Fatal("tree cycle accepted")
	}
	unreachable := Tree{Nodes: []Node{
		{Leaf: true, Value: 1},
		{Leaf: true, Value: 2},
	}}
	if err := validateTree(unreachable); err == nil {
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
