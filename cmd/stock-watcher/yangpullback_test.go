package main

import "testing"

func TestStudyHistoryStartUsesFrozenTenYearWindow(t *testing.T) {
	tests := []struct {
		asof string
		want string
	}{
		{"2026-10-27", "2016-10-27"},
		{"2024-02-29", "2014-02-28"},
	}
	for _, test := range tests {
		got, err := studyHistoryStart(test.asof)
		if err != nil {
			t.Fatalf("studyHistoryStart(%s): %v", test.asof, err)
		}
		if got != test.want {
			t.Fatalf("studyHistoryStart(%s) = %s, want %s", test.asof, got, test.want)
		}
	}
	if _, err := studyHistoryStart("bad-date"); err == nil {
		t.Fatal("invalid as-of date was accepted")
	}
}
