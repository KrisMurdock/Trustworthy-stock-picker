package strategy

import "github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"

// EntryDay is a cost-reserved mark at the entry day's close, not a T+0 sale.
// Missing legacy observations remain unknown; never infer a fill from the low.
type EntryDay struct {
	Version     int     `json:"schema_version"`
	Mode        string  `json:"mode"`
	EntryAt     string  `json:"entry_at,omitempty"`
	Fill        float64 `json:"fill_price"`
	BuyCost     float64 `json:"buy_cost"`
	Shares      int     `json:"shares"`
	Close       float64 `json:"close_price"`
	CloseNetPct float64 `json:"close_net_pct"`
}

func entryDayValue(s config.Strategy, p Plan, close float64, at, mode string) *EntryDay {
	return &EntryDay{Version: 1, Mode: mode, EntryAt: at, Fill: p.EntryPrice, BuyCost: p.BuyCost, Shares: p.Shares, Close: close, CloseNetPct: netPct(s, p, tickDown(close*(1-s.SlippageRate)))}
}

func JointVerdict(s Stats) string {
	switch AssessLoss(s).Status {
	case "breached":
		return "risk_rejected"
	case "unverified":
		return "risk_unverified"
	}
	if s.EntryDayNonPositive > 0 {
		return "entry_day_rejected"
	}
	if s.UnknownEntries > 0 {
		return "entry_day_unverified"
	}
	if s.EntryDayKnown != s.Samples || s.HighTouchSamples != s.Samples {
		return "entry_day_unverified"
	}
	if s.Resolved < 30 || s.Dates < 10 {
		return "insufficient"
	}
	if s.MeanNet <= 0 || s.Joint25Rate < NextDayGoalRate {
		return "below_goal"
	}
	if s.Joint30Rate < NextDayGoalRate {
		return "goal_25_only"
	}
	return "goal_observed"
}
