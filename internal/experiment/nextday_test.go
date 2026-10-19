package experiment

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"testing"
)

func TestNextDayPoliciesSeparateObservationWindowAndLineage(t *testing.T) {
	old, _ := Specification("baseline")
	snapshot := Snapshot{}
	parent, err := NewRecord(snapshot, old, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	parent.Status = "completed"
	baseline, err := Specification("next-day-baseline")
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRecord(snapshot, baseline, &parent, "test")
	if err != nil {
		t.Fatal(err)
	}
	r.Status = "completed"
	if r.BaselineID != r.ID || r.ParentID != parent.ID || Comparable(r, parent) {
		t.Fatal("next-day baseline reused a five-day comparison")
	}
	for _, rule := range []string{"upper-wick", "low-volatility", "pullback", "breakout", "momentum", "volume-recovery", "balanced", "risk-qualified"} {
		spec, err := Specification("next-day-" + rule)
		if err != nil || spec.NextDayRule != rule {
			t.Fatalf("policy %s unavailable: %v", rule, err)
		}
		child, err := NewRecord(snapshot, spec, &r, "test")
		if err != nil {
			t.Fatal(err)
		}
		child.Status = "completed"
		if !Comparable(r, child) || child.BaselineID != r.ID {
			t.Fatal("same-window comparison lost")
		}
	}
	if _, err := Specification("next-day-typo"); err == nil {
		t.Fatal("unknown policy accepted")
	}
	r.Spec.Range = &strategy.BacktestRange{From: "2026-04-01", Through: "2026-05-01"}
	child := r
	child.Spec.Range = &strategy.BacktestRange{From: "2026-04-02", Through: "2026-05-01"}
	if Comparable(r, child) {
		t.Fatal("different backtest dates compared")
	}
	child.Spec.Range = &strategy.BacktestRange{From: "2026-04-01", Through: "2026-05-01"}
	if !Comparable(r, child) {
		t.Fatal("equal ranges not comparable")
	}
}
