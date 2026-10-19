package strategy

import "github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"

const (
	MinCohortSignalDates = 30
	MinCohortTrades      = 30
)

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
	dates, completed, meanNet, incomplete := x.Dates, x.Samples, x.MeanNet, 0
	if x.Cohort.Version != 0 {
		dates, completed, meanNet, incomplete = x.Cohort.SignalDates, x.Cohort.FilledSlots, x.Cohort.MeanNet, x.Cohort.IncompleteDates
	}
	if x.LossBreaches > 0 || BreachesLossLimit(x.WorstNet) {
		out = append(out, GateFailure{"loss_limit", x.WorstNet, -MaxTradeLossPct, ">"})
	}
	requiredTrades := max(MinCohortTrades, s.MinEvidence)
	if completed < requiredTrades {
		out = append(out, GateFailure{"samples", float64(completed), float64(requiredTrades), ">="})
	}
	requiredDates := max(MinCohortSignalDates, s.MinTrainingDays)
	if dates < requiredDates {
		out = append(out, GateFailure{"signal_dates", float64(dates), float64(requiredDates), ">="})
	}
	if x.Unresolved > 0 {
		out = append(out, GateFailure{"unresolved", float64(x.Unresolved), 0, "="})
	}
	if incomplete > 0 {
		out = append(out, GateFailure{"incomplete_signal_date", float64(incomplete), 0, "="})
	}
	if meanNet <= 0 || x.Cohort.Bootstrap.Iterations > 0 && x.Cohort.Bootstrap.MeanNetLower <= 0 {
		out = append(out, GateFailure{"mean_net", meanNet, 0, ">"})
	}
	if x.Cohort.IncrementBootstrap != nil && x.Cohort.IncrementBootstrap.MeanNetLower <= 0 {
		out = append(out, GateFailure{"baseline_mean_net", x.Cohort.MeanNetIncrement, 0, ">"})
	}
	if x.Cohort.IncrementBootstrap != nil && x.Cohort.PairedDates < MinCohortSignalDates {
		out = append(out, GateFailure{"baseline_signal_dates", float64(x.Cohort.PairedDates), MinCohortSignalDates, ">="})
	}
	if x.HitRate <= baseline.HitRate {
		out = append(out, GateFailure{"target_rate", x.HitRate * 100, baseline.HitRate * 100, ">"})
	}
	return out
}

func HoldoutFailures(hold Evaluation) []GateFailure {
	var out []GateFailure
	x := hold.Recommended.Cohort
	dates, completed, meanNet := hold.Recommended.Dates, hold.Recommended.Samples, hold.Recommended.MeanNet
	incomplete := 0
	if x.Version != 0 {
		dates, completed, meanNet, incomplete = x.SignalDates, x.FilledSlots, x.MeanNet, x.IncompleteDates
	}
	if hold.Method != "frozen_holdout" {
		out = append(out, GateFailure{Code: "holdout_unavailable"})
	}
	if completed < MinCohortTrades {
		out = append(out, GateFailure{"samples", float64(completed), MinCohortTrades, ">="})
	}
	if dates < MinCohortSignalDates {
		out = append(out, GateFailure{"signal_dates", float64(dates), MinCohortSignalDates, ">="})
	}
	if incomplete > 0 {
		out = append(out, GateFailure{"incomplete_signal_date", float64(incomplete), 0, "="})
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
	if !(meanNet > 0) || x.Version != 0 && x.Bootstrap.Iterations > 0 && x.Bootstrap.MeanNetLower <= 0 {
		out = append(out, GateFailure{"mean_net", meanNet, 0, ">"})
	}
	if !(hold.Recommended.HitRate > hold.PoolBaseline.HitRate) {
		out = append(out, GateFailure{"target_rate", hold.Recommended.HitRate * 100, hold.PoolBaseline.HitRate * 100, ">"})
	}
	if x.Version != 0 {
		if x.IncrementBootstrap == nil || x.IncrementBootstrap.MeanNetLower <= 0 {
			out = append(out, GateFailure{"baseline_mean_net", x.MeanNetIncrement, 0, ">"})
		}
		if x.PairedDates < MinCohortSignalDates {
			out = append(out, GateFailure{"baseline_signal_dates", float64(x.PairedDates), MinCohortSignalDates, ">="})
		}
	} else if !(hold.Recommended.MeanNet > hold.PoolBaseline.MeanNet) {
		out = append(out, GateFailure{"baseline_mean_net", hold.Recommended.MeanNet, hold.PoolBaseline.MeanNet, ">"})
	}
	return out
}
