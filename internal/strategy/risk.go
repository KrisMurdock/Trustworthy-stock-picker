package strategy

import "math"

// MaxTradeLossPct is measured against the actual buy cost, including fees.
// It is an acceptance constraint, never a promise of a broker execution price.
const MaxTradeLossPct = 3.0
const LossPolicy = "net_loss_strictly_below_3_v1"

func BreachesLossLimit(net float64) bool {
	return math.IsNaN(net) || math.IsInf(net, 0) || net <= -MaxTradeLossPct+1e-9
}

type LossAssessment struct {
	Policy     string  `json:"policy"`
	LimitPct   float64 `json:"limit_pct"`
	Status     string  `json:"status"`
	Breaches   int     `json:"breaches"`
	ExactCount bool    `json:"exact_count"`
	Unresolved int     `json:"unresolved"`
	Resolved   int     `json:"resolved"`
}

func AssessLoss(s Stats) LossAssessment {
	a := LossAssessment{Policy: LossPolicy, LimitPct: MaxTradeLossPct, Status: "unverified", Breaches: s.LossBreaches, ExactCount: s.LossLimitPct == MaxTradeLossPct, Unresolved: s.Unresolved, Resolved: s.Resolved}
	if s.Resolved > 0 && (s.LossBreaches > 0 || BreachesLossLimit(s.WorstNet)) {
		a.Status = "breached"
		if a.Breaches == 0 {
			a.Breaches, a.ExactCount = 1, false // Legacy statistics lack the exact count.
		}
	} else if s.Resolved > 0 && s.Unresolved == 0 {
		a.Status = "observed_pass"
	}
	return a
}

func ResolvedOutcome(out Outcome) bool {
	return out.Status == "target" || out.Status == "stop" || out.Status == "stop_gap" || out.Status == "timeout"
}
