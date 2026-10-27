package selection

import (
	"math"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func TestPhaseDecisionCapsConfidenceWhenModelsAreMissing(t *testing.T) {
	cases := []struct {
		name   string
		second *secondstage.Report
	}{
		{name: "no second stage"},
		{name: "JEV missing", second: phaseDecisionSecondStage(8, false, 0.5, false)},
		{name: "tree missing", second: phaseDecisionSecondStageWithModelID("", 8, true, 0.5)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := phaseDecisionReport(strategy.PhaseAdvance, 3, 92, tc.second)
			attachPhaseDecisions(&report)
			phase := report.Ranking[0].Phase
			if phase.ConfidenceScore > 65 {
				t.Fatalf("missing model confidence = %.0f, want <= 65: %+v", phase.ConfidenceScore, phase)
			}
			if phase.ConfidenceScore < 0 || phase.ConfidenceScore > 100 {
				t.Fatalf("confidence outside 0-100: %+v", phase)
			}
		})
	}
}

func TestPhaseDecisionNeverChangesRulePhase(t *testing.T) {
	cases := []struct {
		name   string
		rule   string
		score  int
		tree   float64
		jev    float64
		want   string
		delta  string
		belief string
	}{
		{name: "advance ignores bearish models", rule: strategy.PhaseAdvance, score: 3, tree: -8, jev: 0, want: strategy.PhaseAdvance, delta: "lower", belief: "conflicting"},
		{name: "decline ignores bullish models", rule: strategy.PhaseDecline, score: -3, tree: 8, jev: 1, want: strategy.PhaseDecline, delta: "lower", belief: "conflicting"},
		{name: "range is not flipped by bullish models", rule: strategy.PhaseRange, score: 2, tree: 8, jev: 1, want: strategy.PhaseRange, delta: "same", belief: "not_applicable"},
		{name: "range is not flipped by bearish models", rule: strategy.PhaseRange, score: -2, tree: -8, jev: 0, want: strategy.PhaseRange, delta: "same", belief: "not_applicable"},
		{name: "aligned models mildly raise confidence", rule: strategy.PhaseAdvance, score: 4, tree: 8, jev: 1, want: strategy.PhaseAdvance, delta: "higher", belief: "supportive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			second := phaseDecisionSecondStage(tc.tree, true, tc.jev, false)
			report := phaseDecisionReport(tc.rule, tc.score, 72, second)
			report.Ranking[0].Phase.Invalidation = 9.5
			attachPhaseDecisions(&report)
			phase := report.Ranking[0].Phase
			if phase.Phase != tc.want || phase.RulePhase != tc.rule {
				t.Fatalf("phase = %s rule=%s, want %s: %+v", phase.Phase, phase.RulePhase, tc.want, phase)
			}
			if phase.ModelAgreement != tc.belief {
				t.Fatalf("model agreement = %s, want %s: %+v", phase.ModelAgreement, tc.belief, phase)
			}
			wantStage := "rule+model-confidence"
			if tc.rule == strategy.PhaseRange {
				wantStage = "rule"
			}
			if phase.DecisionStage != wantStage {
				t.Fatalf("decision stage = %s, want %s: %+v", phase.DecisionStage, wantStage, phase)
			}
			switch tc.delta {
			case "higher":
				if phase.ConfidenceScore <= 72 {
					t.Fatalf("aligned evidence did not raise confidence: %+v", phase)
				}
			case "lower":
				if phase.ConfidenceScore >= 72 {
					t.Fatalf("conflicting evidence did not lower confidence: %+v", phase)
				}
			default:
				if phase.ConfidenceScore != 72 {
					t.Fatalf("range confidence changed without a range model: %+v", phase)
				}
			}
			if phase.Invalidation != 9.5 {
				t.Fatalf("model evidence rewrote rule invalidation: %+v", phase)
			}
		})
	}
}

func TestPhaseDecisionUsesJEVPercentileNotRawGroupSoftmax(t *testing.T) {
	// A five-name JEV softmax has a neutral value near 0.2, not 0.5. The
	// phase layer must use the percentile field so this candidate is neutral.
	second := phaseDecisionSecondStage(8, true, 0.2, false)
	second.Candidates[0].JEVPercentile = 0.5
	second.Candidates[0].Models = map[string]secondstage.ModelSignal{
		secondstage.ModelJEV: {Adjacent15: 0.2, Percentile: 0.5, Present: true},
	}
	report := phaseDecisionReport(strategy.PhaseAdvance, 4, 70, second)
	attachPhaseDecisions(&report)
	phase := report.Ranking[0].Phase
	if phase.ModelAgreement != "neutral" {
		t.Fatalf("JEV percentile 0.5 should be neutral, got %s: %+v", phase.ModelAgreement, phase)
	}
	if phase.ConfidenceScore != 70 {
		t.Fatalf("neutral evidence changed confidence: %+v", phase)
	}
}

func TestPhaseTreePercentilesUseGroupRank(t *testing.T) {
	candidates := []secondstage.Candidate{
		{Code: "a", PredictedMaxHigh: 1},
		{Code: "b", PredictedMaxHigh: 2},
		{Code: "c", PredictedMaxHigh: 2},
		{Code: "d", PredictedMaxHigh: 4},
		{Code: "e", PredictedMaxHigh: 5},
	}
	percentiles := phaseTreePercentiles(&secondstage.Report{ModelID: "tree", Candidates: candidates})
	if math.Abs(percentiles["a"]-0.0) > 1e-9 || math.Abs(percentiles["d"]-0.75) > 1e-9 || math.Abs(percentiles["e"]-1.0) > 1e-9 {
		t.Fatalf("unexpected tree percentiles: %+v", percentiles)
	}
	if math.Abs(percentiles["b"]-0.375) > 1e-9 || math.Abs(percentiles["c"]-0.375) > 1e-9 {
		t.Fatalf("ties must receive the same average percentile: %+v", percentiles)
	}
}

func phaseDecisionReport(rule string, score int, confidence float64, second *secondstage.Report) Report {
	return Report{
		Ranking: []strategy.Candidate{{Code: "sz000001", Phase: &strategy.PhaseEvidence{
			Version: strategy.PhaseVersion, Method: strategy.PhaseMethod,
			Phase: rule, RulePhase: rule, Label: strategy.PhaseLabel(rule),
			Score: score, RuleScore: score,
			ConfidenceScore: confidence, BaseConfidenceScore: confidence,
			Confidence: strategy.PhaseConfidenceFromScore(confidence),
		}}},
		SecondStage: second,
	}
}

func phaseDecisionSecondStage(tree float64, jevPresent bool, jevPercentile float64, _ bool) *secondstage.Report {
	return phaseDecisionSecondStageWithModelID("phase-test-model", tree, jevPresent, jevPercentile)
}

func phaseDecisionSecondStageWithModelID(modelID string, tree float64, jevPresent bool, jevPercentile float64) *secondstage.Report {
	candidate := secondstage.Candidate{
		Code: "sz000001", PredictedMaxHigh: tree, JEVPresent: jevPresent,
		JEVAdjacent15: 0.2, JEVPercentile: jevPercentile,
	}
	if jevPresent {
		candidate.Models = map[string]secondstage.ModelSignal{
			secondstage.ModelJEV: {Adjacent15: candidate.JEVAdjacent15, Percentile: jevPercentile, Present: true},
		}
	}
	return &secondstage.Report{
		ModelID: modelID, JEVStatus: secondstage.JEVStatusOK,
		Candidates: []secondstage.Candidate{candidate},
	}
}
