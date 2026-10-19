package strategy

import (
	"math"
	"stock-watcher/internal/config"
	"testing"
)

func TestEvidenceGateBoundaries(t *testing.T) {
	p := config.DefaultStrategy()
	baseline := Stats{HitRate: .4}
	valid := Stats{Samples: 60, Dates: 40, Resolved: 60, MeanNet: .5, HitRate: .5, WorstNet: -2.99, LossLimitPct: 3}
	cases := []struct {
		name, code string
		change     func(*Stats)
	}{
		{"sample boundary", "samples", func(s *Stats) { s.Samples = 59 }},
		{"date boundary", "signal_dates", func(s *Stats) { s.Dates = 39 }},
		{"unknown outcome", "unresolved", func(s *Stats) { s.Unresolved = 1 }},
		{"zero mean", "mean_net", func(s *Stats) { s.MeanNet = 0 }},
		{"equal baseline", "target_rate", func(s *Stats) { s.HitRate = .4 }},
		{"exact loss boundary", "loss_limit", func(s *Stats) { s.WorstNet = -3 }},
		{"recorded loss", "loss_limit", func(s *Stats) { s.LossBreaches = 1 }},
	}
	if got := EvidenceFailures(valid, baseline, p); len(got) != 0 {
		t.Fatalf("valid evidence rejected: %+v", got)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := valid
			tc.change(&s)
			got := EvidenceFailures(s, baseline, p)
			if len(got) != 1 || got[0].Code != tc.code {
				t.Fatalf("wrong gate: %+v", got)
			}
		})
	}
}

func TestHoldoutGateRetainsMinimumSamplesAndLossPolicy(t *testing.T) {
	valid := Evaluation{Method: "frozen_holdout", Recommended: Stats{Samples: 30, Dates: 30, Resolved: 30, WorstNet: -2.99, MeanNet: .5, HitRate: .5, LossLimitPct: 3}, PoolBaseline: Stats{MeanNet: .4, HitRate: .4}}
	if got := HoldoutFailures(valid); len(got) != 0 {
		t.Fatalf("valid validation rejected: %+v", got)
	}
	cases := []struct {
		name, code string
		change     func(*Evaluation)
	}{
		{"no held-out period", "holdout_unavailable", func(e *Evaluation) { e.Method = "purged_walk_forward" }},
		{"too few entries", "samples", func(e *Evaluation) { e.Recommended.Samples = 29 }},
		{"too few dates", "signal_dates", func(e *Evaluation) { e.Recommended.Dates = 29 }},
		{"unresolved", "unresolved", func(e *Evaluation) { e.Recommended.Unresolved = 1 }},
		{"no exits", "resolved_samples", func(e *Evaluation) { e.Recommended.Resolved = 0 }},
		{"exact loss limit", "loss_limit", func(e *Evaluation) { e.Recommended.WorstNet = -3 }},
		{"no baseline advantage", "baseline_mean_net", func(e *Evaluation) { e.Recommended.MeanNet = .4 }},
		{"invalid mean", "mean_net", func(e *Evaluation) { e.Recommended.MeanNet = math.NaN() }},
		{"invalid rate", "target_rate", func(e *Evaluation) { e.Recommended.HitRate = math.NaN() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := valid
			tc.change(&e)
			found := false
			for _, f := range HoldoutFailures(e) {
				found = found || f.Code == tc.code
			}
			if !found {
				t.Fatalf("lost holdout rejection %s", tc.code)
			}
		})
	}
}
