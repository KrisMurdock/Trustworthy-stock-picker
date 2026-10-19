package experiment

import (
	"stock-watcher/internal/market"
	"testing"
)

func TestIndicatorPoliciesCompareOnlyOnCompatibleFrozenBaseline(t *testing.T) {
	snapshot := Snapshot{Minutes: []market.MinuteHistory{{}}}
	base, _ := Specification("conditional-entry")
	old, err := NewRecord(snapshot, base, nil, "previous-version")
	if err != nil {
		t.Fatal(err)
	}
	old.Engine, old.Status = "conditional_entry_v5", "completed"
	current, err := NewRecord(snapshot, base, &old, "new-version")
	if err != nil || current.BaselineID != current.ID || current.ParentID != old.ID {
		t.Fatal("new baseline lost provenance", err)
	}
	current.Status = "completed"
	if Comparable(current, old) {
		t.Fatal("different engines compared")
	}
	for _, rule := range []string{"rsi", "macd", "bollinger", "confluence", "all-three", "guard", "confluence-guard"} {
		spec, err := Specification("conditional-" + rule)
		if err != nil || spec.SignalRule != rule || spec.EntryMode != base.EntryMode {
			t.Fatalf("missing policy %s", rule)
		}
		child, err := NewRecord(snapshot, spec, &current, "new-version")
		if err != nil || child.BaselineID != current.ID {
			t.Fatal("indicator policy changed baseline", err)
		}
		child.Status = "completed"
		if !Comparable(current, child) {
			t.Fatal("indicator change should be comparable")
		}
	}
	if _, err := Specification("conditional-unknown"); err == nil {
		t.Fatal("unknown indicator accepted")
	}
}
