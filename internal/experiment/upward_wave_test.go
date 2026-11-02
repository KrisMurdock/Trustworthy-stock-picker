package experiment

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func TestUpwardWaveVerdictDoesNotReuseProfitGate(t *testing.T) {
	rate, coverage := .2, .8
	r := Record{Status: "completed", Spec: Spec{FourLightRule: "all-red"}, Result: &Results{Development: strategy.DevelopmentResult{
		Research:   strategy.Research{Development: strategy.Evaluation{TopRanked: strategy.Stats{LossBreaches: 10}}},
		UpwardWave: &strategy.UpwardWaveSummary{Model: strategy.UpwardWaveModel, Windows: []strategy.UpwardWaveStats{{Days: 5, Known: 100, Success: 20, SuccessRate: &rate, Coverage: &coverage}, {Days: 10}}},
	}}}
	applyUpwardWaveFindings(&r, nil)
	if r.Verdict != "wave_observed" {
		t.Fatal("old trading loss overrode wave objective", r.Verdict)
	}
	b, _ := json.Marshal(r.Result.Findings)
	if !strings.Contains(string(b), "10 笔") || !strings.Contains(string(b), "Top N") {
		t.Fatal("risk/selection limitations hidden", string(b))
	}
	r.Result.Development.UpwardWave.Windows[0].Known = 0
	applyUpwardWaveFindings(&r, nil)
	if r.Verdict != "wave_unverified" {
		t.Fatal("empty evidence shown as observed")
	}
}

func TestUpwardWaveUpgradeStartsComparableBaseline(t *testing.T) {
	spec, _ := Specification("baseline")
	parent, _ := NewRecord(Snapshot{}, spec, nil, "old-build")
	parent.Status = "completed"
	parent.Engine = "indicator_selection_v7"
	next, err := NewRecord(Snapshot{}, spec, &parent, "new-build")
	if err != nil || next.BaselineID != next.ID || next.ParentID != parent.ID {
		t.Fatal("upgrade retained incompatible root", next, err)
	}
	next.Status = "completed"
	four, _ := Specification("four-light-all-red")
	child, err := NewRecord(Snapshot{}, four, &next, "new-build")
	if err != nil || child.BaselineID != next.ID {
		t.Fatal("new contrast lost new baseline", child, err)
	}
	child.Status = "completed"
	if !Comparable(child, next) || Comparable(child, parent) {
		t.Fatal("mixed engine comparison")
	}
	child.Stage = "holdout"
	if Comparable(child, next) {
		t.Fatal("development compared to holdout")
	}
	child.Stage, next.Stage = "", "development"
	if !Comparable(child, next) {
		t.Fatal("legacy empty stage no longer means development")
	}
	if _, err := NewHoldoutRecord(Snapshot{}, parent, false); err == nil {
		t.Fatal("old engine allowed to consume holdout under a new build")
	}
}
