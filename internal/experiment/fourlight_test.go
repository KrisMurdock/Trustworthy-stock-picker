package experiment

import "testing"

func TestFourLightPoliciesAreFrozenAndComparable(t *testing.T) {
	base, err := Specification("baseline")
	if err != nil {
		t.Fatal(err)
	}
	baseRecord, err := NewRecord(Snapshot{}, base, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	baseRecord.Status = "completed"
	for _, rule := range []string{"all-red", "flip-any", "flip-all"} {
		spec, err := Specification("four-light-" + rule)
		if err != nil || spec.FourLightRule != rule || spec.NextDayRule != "" || spec.Title == "" {
			t.Fatalf("policy %s unavailable: %+v %v", rule, spec, err)
		}
		record, err := NewRecord(Snapshot{}, spec, nil, "test")
		if err != nil {
			t.Fatal(err)
		}
		record.Status = "completed"
		if !Comparable(record, baseRecord) {
			t.Fatal("four-light policy changed the comparable execution horizon")
		}
	}
	if _, err := Specification("four-light-typo"); err == nil {
		t.Fatal("unknown four-light policy accepted")
	}
}
