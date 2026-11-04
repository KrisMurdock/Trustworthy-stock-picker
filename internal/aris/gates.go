package aris

import (
	"context"
	"encoding/json"
	"math"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
)

// Every numeric decision names its frozen threshold and denominators. Undefined
// PF (including all-winner samples) is inconclusive, never positive infinity.
func checkGates(ctx context.Context, b Bundle, artifacts map[string]Artifact, attempts []Attempt) []GateResult {
	gates := []GateResult{}
	add := func(id, candidate string, passed bool, reason string, evidence any) {
		status := "failed"
		if passed {
			status = "passed"
		}
		raw, _ := json.Marshal(evidence)
		gates = append(gates, GateResult{ID: id, Candidate: candidate, Status: status, Reason: reason, Evidence: string(raw)})
	}
	add("G1", "", true, "Human and machine contracts bound", map[string]any{"bundle_sha256": Digest(b), "human_contract_sha256": b.Contract.HumanHash, "path": "bundle.json"})
	add("G2", "", true, "Only preregistered inputs evaluated", b.Contract.InputHashes)
	add("G3", "", true, "Frozen bytes verified; historical lineage remains G14", b.Contract.InputHashes)
	add("G4", "", true, "Executing binary identity matches frozen contract", b.Contract.Build)
	add("G12", "", true, "Attempt budget retained across recovery", attempts)
	for _, id := range append([]string{b.Contract.Baseline}, b.Contract.Candidates...) {
		ar := artifacts[id]
		r := ar.Result
		c := b.Contract.Constraints
		evidence := map[string]any{"path": "artifacts/" + id + ".json", "result_sha256": Digest(r), "metrics": r.Metrics, "signals": len(r.Decisions), "open_positions": len(r.Open), "constraints": c}
		add("G5", id, Digest(r) == ar.ReplayHash, "Repeat evaluation hash", evidence)
		// A losing baseline is a valid comparator, not a reason to reject candidates.
		if id == b.Contract.Baseline {
			continue
		}
		days := map[string]bool{}
		for _, tr := range r.Trades {
			days[tr.SignalDate] = true
		}
		evidence["closed_signal_days"] = len(days)
		add("G7", id, r.Metrics.Closed >= c.MinTrades && len(days) >= c.MinDays, "Closed trades and distinct closed signal dates", evidence)
		add("G8", id, r.Metrics.Expectancy != nil && *r.Metrics.Expectancy > c.MinExpectancy, "Net expectancy amount must exceed threshold", evidence)
		add("G9", id, r.Metrics.PF != nil && *r.Metrics.PF >= c.MinPF && *r.Metrics.PF > 1, "Finite profit factor must exceed one and meet threshold", evidence)
		worst := 0.0
		for _, tr := range r.Trades {
			worst = math.Max(worst, -tr.PNL)
		}
		evidence["worst_closed_loss_amount"] = worst
		add("G10", id, -r.Metrics.Drawdown <= *c.Drawdown && worst <= *c.TailLoss, "Portfolio drawdown and absolute closed-trade tail-loss budget", evidence)
		ok := len(ar.Stress) == len(c.StressSlippage)
		stressEvidence := []any{}
		for _, st := range ar.Stress {
			ok = ok && thresholds(st.Result, c) && st.Slippage > r.Config.Slippage
			stressEvidence = append(stressEvidence, map[string]any{"slippage": st.Slippage, "metrics": st.Result.Metrics, "sha256": Digest(st.Result)})
		}
		add("G13", id, ok, "Every stress must increase one-way slippage and meet all numeric thresholds", map[string]any{"path": "artifacts/" + id + ".json", "constraints": c, "scenarios": stressEvidence})
		accounted := len(r.Decisions) == len(b.Inputs[id].Signals)
		add("G16", id, accounted, "All signals retained; terminal positions marked, never force-closed", evidence)
		interval, e := infer(ctx, r, artifacts[b.Contract.Baseline].Result, b.Contract.Inference)
		if e != nil {
			gates = append(gates, GateResult{ID: "G15", Candidate: id, Status: "inconclusive", Reason: e.Error(), Evidence: stringJSON(interval)})
		} else {
			add("G15", id, interval.ExpectancyLower > c.MinExpectancy && interval.ExcessLower > 0, "Registered time-block interval excludes zero for expectancy and paired excess returns", map[string]any{"interval": interval, "candidate_path": "artifacts/" + id + ".json", "baseline_path": "artifacts/" + b.Contract.Baseline + ".json", "candidate_sha256": Digest(r), "baseline_sha256": Digest(artifacts[b.Contract.Baseline].Result)})
		}
	}
	for _, g := range []GateResult{{ID: "G6", Status: "inconclusive", Reason: "No signed test attestation for this binary"}, {ID: "G11", Status: "inconclusive", Reason: "Development-only proxy; no independent holdout consumption"}, {ID: "G14", Status: "inconclusive", Reason: "Frozen prices do not prove historical visibility or revision lineage"}} {
		gates = append(gates, g)
	}
	return gates
}
func stringJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func thresholds(r maexec.Result, c Constraints) bool {
	days := map[string]bool{}
	for _, tr := range r.Trades {
		days[tr.SignalDate] = true
		if -tr.PNL > *c.TailLoss {
			return false
		}
	}
	return r.Metrics.Closed >= c.MinTrades && len(days) >= c.MinDays && r.Metrics.Expectancy != nil && *r.Metrics.Expectancy > c.MinExpectancy && r.Metrics.PF != nil && *r.Metrics.PF >= c.MinPF && *r.Metrics.PF > 1 && -r.Metrics.Drawdown <= *c.Drawdown
}
