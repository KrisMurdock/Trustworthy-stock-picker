package strategy

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"math"
	"testing"
)

func TestStrictLossBoundaryAndCensoredOutcomes(t *testing.T) {
	for _, tc := range []struct {
		net    float64
		breach bool
	}{{-2.99, false}, {-3, true}, {-3.01, true}, {0, false}, {math.NaN(), true}, {math.Inf(1), true}} {
		if BreachesLossLimit(tc.net) != tc.breach {
			t.Fatal("incorrect strict boundary", tc.net)
		}
	}
	s := Stats{}
	for _, out := range []Outcome{
		{Status: "stop", EntryDate: "2026-01-05", NetPct: -2.99},
		{Status: "stop", EntryDate: "2026-01-05", NetPct: -3},
		{Status: "stop_gap", EntryDate: "2026-01-05", NetPct: -10},
		{Status: "target", EntryDate: "2026-01-05", NetPct: 3.1},
		{Status: "timeout", EntryDate: "2026-01-05", NetPct: 2.5},
		{Status: "corporate_action", EntryDate: "2026-01-05", ExitDate: "2026-01-06", NetPct: -90},
		{Status: "entry_skipped", NetPct: -99},
	} {
		add(&s, Sample{Date: "2026-01-02"}, out)
	}
	s = finalize(s)
	if s.LossBreaches != 2 || s.Resolved != 5 || s.Unresolved != 1 || s.Profit25 != 2 || s.Profit30 != 1 || s.Profit25Rate != 2.0/6 || s.WorstNet != -10 || AssessLoss(s).Status != "breached" {
		t.Fatalf("invalid risk statistics: %+v", s)
	}
	for _, tc := range []struct {
		stats  Stats
		status string
	}{
		{Stats{}, "unverified"},
		{Stats{Resolved: 30, Unresolved: 1, WorstNet: -1}, "unverified"},
		{Stats{Resolved: 30, WorstNet: -3}, "breached"},
		{Stats{Resolved: 30, WorstNet: -2.99}, "observed_pass"},
	} {
		if AssessLoss(tc.stats).Status != tc.status {
			t.Fatal(tc)
		}
	}
}

func TestProfitableHistoricalRuleCannotOverrideLossBreach(t *testing.T) {
	s := config.DefaultStrategy()
	s.MinRankScore = 0
	f := Features{ClosePosition: .9, Return1: 1, VolumeRatio: 1.4, MA20Distance: 2, MA20Slope: 1, ATR14Pct: 2}
	stats := []Stats{{Rule: "baseline", Target: 3, Horizon: 5, HitRate: .2}, {Rule: "volume_recovery", Target: 3, Horizon: 5, Samples: 100, Resolved: 100, Dates: 60, MeanNet: 5, HitRate: .9, LowerBound: .8, WorstNet: -3}}
	if rank("sz000001", "", f, stats, s).Recommended {
		t.Fatal("high profit bypassed hard loss constraint")
	}
	stats[1].WorstNet = -2.99
	if !rank("sz000001", "", f, stats, s).Recommended {
		t.Fatal("historically qualified rule rejected")
	}
}
