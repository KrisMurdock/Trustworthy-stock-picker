package experiment

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"testing"
)

func TestRiskPrecedesProfitAndShorterHolding(t *testing.T) {
	base := strategy.Stats{Samples: 100, Resolved: 100, Dates: 50, WorstNet: -2.8, MeanNet: 1, MeanDays: 3, Profit25Rate: .5, Profit30Rate: .4, LossLimitPct: 3, HighTouchSamples: 100, HighTouch25: 50, HighTouch30: 40, HighTouch25Rate: .5, HighTouch30Rate: .4, HighTouch25Days: 3, HighTouch30Days: 3}
	parent := Record{Engine: Engine, Status: "completed", Result: &Results{Development: strategy.DevelopmentResult{Research: strategy.Research{Development: strategy.Evaluation{TopRanked: base}}}}}
	for _, tc := range []struct {
		name, want string
		change     func(*strategy.Stats)
	}{
		{"high_profit_cannot_cover_loss", "risk_rejected", func(s *strategy.Stats) { s.WorstNet = -3; s.MeanNet = 20; s.MeanDays = 1 }},
		{"unresolved_is_not_zero_risk", "risk_unverified", func(s *strategy.Stats) { s.Unresolved = 1 }},
		{"no_trades_is_not_safe", "risk_unverified", func(s *strategy.Stats) { *s = strategy.Stats{} }},
		{"too_few_trades", "insufficient", func(s *strategy.Stats) { s.Resolved = 5 }},
		{"quicker_same_return", "promising", func(s *strategy.Stats) { s.HighTouch25Days = 2; s.HighTouch30Days = 2 }},
		{"more_profit_same_duration", "promising", func(s *strategy.Stats) { s.HighTouch25Rate = .6; s.HighTouch30Rate = .5 }},
		{"quicker_but_lower_profit", "tradeoff", func(s *strategy.Stats) { s.HighTouch25Days = 2; s.HighTouch30Days = 2; s.HighTouch25Rate = .4 }},
		{"realized_return_alone_is_not_high_touch_improvement", "no_improvement", func(s *strategy.Stats) { s.MeanNet = 5; s.MeanDays = 1 }},
		{"same_result", "no_improvement", func(s *strategy.Stats) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.change(&s)
			r := parent
			r.Result = &Results{Development: strategy.DevelopmentResult{Research: strategy.Research{Development: strategy.Evaluation{TopRanked: s}}}}
			if got := developmentVerdict(r, &parent); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
