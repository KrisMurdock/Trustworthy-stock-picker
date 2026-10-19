package strategy

import "stock-watcher/internal/config"

// GateFailure preserves measured values for explanations without changing the
// acceptance policy. Percentage values use percentage points, not fractions.
type GateFailure struct {
	Code       string  `json:"code"`
	Actual     float64 `json:"actual"`
	Required   float64 `json:"required"`
	Comparison string  `json:"comparison"`
}

func EvidenceFailures(x, baseline Stats, s config.Strategy) []GateFailure {
	var out []GateFailure
	if x.LossBreaches > 0 || BreachesLossLimit(x.WorstNet) {
		out = append(out, GateFailure{"loss_limit", x.WorstNet, -MaxTradeLossPct, ">"})
	}
	if x.Samples < s.MinEvidence {
		out = append(out, GateFailure{"samples", float64(x.Samples), float64(s.MinEvidence), ">="})
	}
	if x.Dates < s.MinTrainingDays {
		out = append(out, GateFailure{"signal_dates", float64(x.Dates), float64(s.MinTrainingDays), ">="})
	}
	if x.Unresolved > 0 {
		out = append(out, GateFailure{"unresolved", float64(x.Unresolved), 0, "="})
	}
	if x.MeanNet <= 0 {
		out = append(out, GateFailure{"mean_net", x.MeanNet, 0, ">"})
	}
	if x.HitRate <= baseline.HitRate {
		out = append(out, GateFailure{"target_rate", x.HitRate * 100, baseline.HitRate * 100, ">"})
	}
	return out
}

func HoldoutFailures(hold Evaluation) []GateFailure {
	var out []GateFailure
	if hold.Method != "frozen_holdout" {
		out = append(out, GateFailure{Code: "holdout_unavailable"})
	}
	if hold.Recommended.Samples < 30 {
		out = append(out, GateFailure{"samples", float64(hold.Recommended.Samples), 30, ">="})
	}
	if hold.Recommended.Dates < 10 {
		out = append(out, GateFailure{"signal_dates", float64(hold.Recommended.Dates), 10, ">="})
	}
	loss := AssessLoss(hold.Recommended)
	if loss.Status != "observed_pass" {
		if loss.Status == "breached" {
			out = append(out, GateFailure{"loss_limit", hold.Recommended.WorstNet, -MaxTradeLossPct, ">"})
		}
		if loss.Unresolved > 0 {
			out = append(out, GateFailure{"unresolved", float64(loss.Unresolved), 0, "="})
		}
		if loss.Resolved == 0 {
			out = append(out, GateFailure{"resolved_samples", 0, 0, ">"})
		}
	}
	if !(hold.Recommended.MeanNet > 0) {
		out = append(out, GateFailure{"mean_net", hold.Recommended.MeanNet, 0, ">"})
	}
	if !(hold.Recommended.HitRate > hold.PoolBaseline.HitRate) {
		out = append(out, GateFailure{"target_rate", hold.Recommended.HitRate * 100, hold.PoolBaseline.HitRate * 100, ">"})
	}
	if !(hold.Recommended.MeanNet > hold.PoolBaseline.MeanNet) {
		out = append(out, GateFailure{"baseline_mean_net", hold.Recommended.MeanNet, hold.PoolBaseline.MeanNet, ">"})
	}
	return out
}
