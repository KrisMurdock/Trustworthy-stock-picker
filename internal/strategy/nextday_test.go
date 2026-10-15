package strategy

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNextDayCountsDoNotCountLaterAmbiguousOrUnresolvedTouches(t *testing.T) {
	s := Stats{}
	for i, out := range []Outcome{
		{Status: "timeout", EntryDate: "2026-01-05", NetPct: -1, HighTouch: &HighTouch{Version: 1, ObservedDays: 1, Day25: 1, Day30: 1}},
		{Status: "target", EntryDate: "2026-01-05", NetPct: 3, HighTouch: &HighTouch{Version: 1, ObservedDays: 2, Day25: 2, Day30: 2}},
		{Status: "stop", EntryDate: "2026-01-05", NetPct: -2.9, HighTouch: &HighTouch{Version: 1, ObservedDays: 1, Day25: 1, Day30: 1, Day25Unknown: true, Day30Unknown: true}},
		{Status: "corporate_action", EntryDate: "2026-01-05", HighTouch: &HighTouch{Version: 1, ObservedDays: 1, Day25: 1, Day30: 1}},
		{Status: "timeout", EntryDate: "2026-01-05"}, // Old record: unknown.
		{Status: "entry_skipped"},
	} {
		add(&s, Sample{Date: string(rune('a' + i))}, out)
	}
	s = finalize(s)
	if s.Samples != 5 || s.NextDay25 != 1 || s.NextDay30 != 1 || s.NextDay25Rate != .2 || s.NextDay30Rate != .2 || s.HighTouch25 != 2 || s.HighTouchUnknown != 2 || s.Profit30 != 1 {
		t.Fatalf("next-day counts confused later high or exit P&L: %+v", s)
	}
}

func TestNextDayGoalDoesNotWaiveRiskOrEvidence(t *testing.T) {
	base := Stats{Samples: 100, Resolved: 100, Dates: 20, WorstNet: -2.9, MeanNet: 1, HighTouchSamples: 100, NextDay25Rate: .85, NextDay30Rate: .8}
	for _, tc := range []struct {
		want   string
		change func(*Stats)
	}{
		{"goal_observed", func(s *Stats) {}},
		{"goal_25_only", func(s *Stats) { s.NextDay30Rate = .79 }},
		{"below_goal", func(s *Stats) { s.NextDay25Rate = .79 }},
		{"risk_rejected", func(s *Stats) { s.WorstNet = -3 }},
		{"risk_unverified", func(s *Stats) { s.Unresolved = 1 }},
		{"risk_unverified", func(s *Stats) { *s = Stats{} }},
		{"insufficient", func(s *Stats) { s.Resolved = 5; s.NextDay25Rate = 1; s.NextDay30Rate = 1 }},
		{"insufficient", func(s *Stats) { s.Dates = 9 }},
		{"insufficient", func(s *Stats) { s.HighTouchSamples = 99 }},
		{"below_goal", func(s *Stats) { s.MeanNet = -1 }},
	} {
		s := base
		tc.change(&s)
		if got := NextDayVerdict(s); got != tc.want {
			t.Fatalf("got %s want %s for %+v", got, tc.want, s)
		}
	}
}

func TestNextDayHorizonPreservesReservedBoundaryAndFutureIsolation(t *testing.T) {
	c, h, raw := researchFixture()
	old, err := AnalyzeDevelopment(context.Background(), c, h, raw, h[0].AsOf, EntryPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	next, err := AnalyzeDevelopment(context.Background(), c, h, raw, h[0].AsOf, EntryPolicy{NextDayRule: "baseline"})
	if err != nil {
		t.Fatal(err)
	}
	if next.HoldoutFrom != old.HoldoutFrom || next.Research.Parameters.MaxHoldingDays != 1 || next.Research.Development.TopRanked.Samples == 0 {
		t.Fatal("shorter horizon moved the holdout or produced no evidence")
	}
	for _, trade := range next.Trades {
		if trade.Outcome.HoldingDays > 1 || trade.Outcome.ExitDate >= next.HoldoutFrom {
			t.Fatal("next-day experiment counted a later day or consumed holdout")
		}
	}
	before, _ := json.Marshal(next)
	for i, b := range h[0].Bars {
		if b.Date >= next.HoldoutFrom {
			h[0].Bars[i].Open *= 2
			h[0].Bars[i].Close *= 2
			h[0].Bars[i].High *= 2
			h[0].Bars[i].Low *= 2
			raw[0].Bars[i] = h[0].Bars[i]
		}
	}
	after, err := AnalyzeDevelopment(context.Background(), c, h, raw, h[0].AsOf, EntryPolicy{NextDayRule: "baseline"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(after)
	if string(before) != string(encoded) {
		t.Fatal("reserved prices changed next-day evidence")
	}
	if _, err = AnalyzeDevelopment(context.Background(), c, h, raw, h[0].AsOf, EntryPolicy{NextDayRule: "typo"}); err == nil {
		t.Fatal("unknown filter accepted")
	}
}
