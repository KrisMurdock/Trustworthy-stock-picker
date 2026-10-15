package strategy

// Fixed hypotheses, chosen before inspecting this round's results. All inputs
// are signal-day features; no future outcome participates in entry selection.
func validNextDayRule(rule string) bool {
	switch rule {
	case "", "baseline", "upper-wick", "low-volatility", "pullback", "breakout", "momentum", "volume-recovery", "balanced", "risk-qualified":
		return true
	}
	return false
}

func matchesNextDay(rule string, f Features) bool {
	switch rule {
	case "", "baseline", "risk-qualified":
		return true
	case "upper-wick":
		return f.UpperWick <= .3
	case "low-volatility":
		return f.ATR14Pct <= 2
	case "balanced":
		return f.ATR14Pct <= 3 && f.UpperWick <= .3 && f.MA20Slope > 0 && f.Return5 <= 10 && f.MA20Distance <= 8
	default:
		matched := rule
		if rule == "volume-recovery" {
			matched = "volume_recovery"
		}
		for _, name := range Matches(f) {
			if name == matched {
				return true
			}
		}
		return false
	}
}

const NextDayGoalRate = .8

// NextDayVerdict describes observed development evidence, not a future win
// probability. A higher rate cannot waive loss, uncertainty or sample gates.
func NextDayVerdict(s Stats) string {
	switch AssessLoss(s).Status {
	case "breached":
		return "risk_rejected"
	case "unverified":
		return "risk_unverified"
	}
	if s.Resolved < 30 || s.Dates < 10 || s.HighTouchSamples != s.Samples {
		return "insufficient"
	}
	if s.MeanNet <= 0 || s.NextDay25Rate < NextDayGoalRate {
		return "below_goal"
	}
	if s.NextDay30Rate < NextDayGoalRate {
		return "goal_25_only"
	}
	return "goal_observed"
}
