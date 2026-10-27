package selection

import (
	"fmt"
	"math"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const (
	phaseDecisionVersion = 2
	treeModelWeight      = 0.6
	jevModelWeight       = 0.4
	phaseConfidenceDelta = 4.0
)

// attachPhaseDecisions completes the phase pipeline after the second-stage
// artifacts have been attached. The daily rule chain defines the phase. Tree
// and JEV predict short-horizon upside ranking rather than a three-class trend,
// so they never flip the phase; they only provide labelled same-direction or
// conflicting evidence and a small confidence adjustment. Missing models never
// fabricate a vote.
func attachPhaseDecisions(report *Report) {
	if report == nil || len(report.Ranking) == 0 {
		return
	}
	byCode := map[string]secondstage.Candidate{}
	second := report.SecondStage
	if second != nil {
		for _, candidate := range second.Candidates {
			byCode[candidate.Code] = candidate
		}
	}
	treePercentiles := phaseTreePercentiles(second)
	for index := range report.Ranking {
		candidate := &report.Ranking[index]
		if candidate.Phase == nil || candidate.Phase.Phase == strategy.PhaseUnknown {
			continue
		}
		phase := candidate.Phase
		basePhase := phase.RulePhase
		if basePhase == "" {
			basePhase = phase.Phase
		}
		baseConfidence := phase.BaseConfidenceScore
		if baseConfidence <= 0 {
			baseConfidence = phase.ConfidenceScore
		}

		models := []strategy.PhaseModelEvidence{}
		modelCandidate, hasModelCandidate := byCode[candidate.Code]
		if hasModelCandidate && second != nil {
			treeStatus := "missing"
			treeScore := 0.0
			treeWeight := 0.0
			treeDetail := "树模型缺失，不参与置信度调整"
			percentile, hasTree := treePercentiles[candidate.Code]
			if second.ModelID != "" && hasTree && finitePrediction(modelCandidate.PredictedMaxHigh) {
				treeStatus = "ok"
				treeScore = clampUnit((percentile - 0.5) * 2)
				treeWeight = treeModelWeight
				treeDetail = fmt.Sprintf("未来3日最高涨幅预测 %.2f%%，组内百分位 %.0f%%", modelCandidate.PredictedMaxHigh, percentile*100)
			}
			models = append(models, strategy.PhaseModelEvidence{Name: "tree", Status: treeStatus, Score: treeScore, Weight: treeWeight, Detail: treeDetail})

			jevStatus := second.JEVStatus
			if jevStatus == "" {
				jevStatus = "missing"
			}
			if !modelCandidate.JEVPresent && jevStatus == secondstage.JEVStatusOK {
				jevStatus = "missing"
			}
			jevScore := 0.0
			jevWeight := 0.0
			jevDetail := "JEV 缺失或无效，不参与置信度调整"
			if modelCandidate.JEVPresent && jevStatus == secondstage.JEVStatusOK {
				percentile := modelCandidate.JEVPercentile
				if signal, ok := modelCandidate.Models[secondstage.ModelJEV]; ok && signal.Present {
					percentile = signal.Percentile
				}
				if finitePrediction(percentile) {
					jevStatus = "ok"
					jevScore = clampUnit((percentile - 0.5) * 2)
					jevWeight = jevModelWeight
					jevDetail = fmt.Sprintf("组内百分位 %.0f%%，adjacent15 %.2f", percentile*100, modelCandidate.JEVAdjacent15)
				}
			}
			models = append(models, strategy.PhaseModelEvidence{Name: "jev", Status: jevStatus, Score: jevScore, Weight: jevWeight, Detail: jevDetail})
		}

		composite, weightSum := compositeModelScore(models)
		modelComplete := true
		for _, model := range models {
			if model.Status != "ok" || model.Weight <= 0 {
				modelComplete = false
				break
			}
		}
		confidence := baseConfidence
		agreement := "missing"
		if weightSum > 0 {
			switch basePhase {
			case strategy.PhaseAdvance:
				agreement = phaseAgreement(basePhase, composite)
				confidence += composite * phaseConfidenceDelta
			case strategy.PhaseDecline:
				agreement = phaseAgreement(basePhase, composite)
				confidence -= composite * phaseConfidenceDelta
			case strategy.PhaseRange:
				agreement = "not_applicable"
			}
			confidence = clampScore(confidence)
		}
		if weightSum == 0 || !modelComplete {
			confidence = math.Min(confidence, 65)
		}

		phase.Phase = basePhase
		phase.Label = strategy.PhaseLabel(basePhase)
		phase.ConfidenceScore = math.Max(0, math.Min(100, math.Round(confidence)))
		phase.Confidence = strategy.PhaseConfidenceFromScore(phase.ConfidenceScore)
		phase.Models = models
		phase.ModelAgreement = agreement
		if weightSum > 0 && basePhase != strategy.PhaseRange {
			phase.DecisionStage = "rule+model-confidence"
		} else {
			phase.DecisionStage = "rule"
		}
	}
}

func compositeModelScore(models []strategy.PhaseModelEvidence) (float64, float64) {
	weighted := 0.0
	weight := 0.0
	for _, model := range models {
		if model.Weight <= 0 || model.Status != "ok" {
			continue
		}
		weighted += model.Score * model.Weight
		weight += model.Weight
	}
	if weight == 0 {
		return 0, 0
	}
	return weighted / weight, weight
}

func phaseAgreement(phase string, composite float64) string {
	switch phase {
	case strategy.PhaseAdvance:
		if composite > 0.15 {
			return "supportive"
		}
		if composite < -0.15 {
			return "conflicting"
		}
	case strategy.PhaseDecline:
		if composite < -0.15 {
			return "supportive"
		}
		if composite > 0.15 {
			return "conflicting"
		}
	}
	return "neutral"
}

func phaseTreePercentiles(second *secondstage.Report) map[string]float64 {
	if second == nil || second.ModelID == "" || len(second.Candidates) == 0 {
		return nil
	}
	type prediction struct {
		code  string
		value float64
	}
	values := make([]prediction, 0, len(second.Candidates))
	for _, candidate := range second.Candidates {
		if finitePrediction(candidate.PredictedMaxHigh) {
			values = append(values, prediction{code: candidate.Code, value: candidate.PredictedMaxHigh})
		}
	}
	if len(values) == 0 {
		return nil
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].value == values[j].value {
			return values[i].code < values[j].code
		}
		return values[i].value < values[j].value
	})
	out := make(map[string]float64, len(values))
	denominator := len(values) - 1
	if denominator < 1 {
		denominator = 1
	}
	for i, current := range values {
		first := i
		for first > 0 && values[first-1].value == current.value {
			first--
		}
		last := i
		for last+1 < len(values) && values[last+1].value == current.value {
			last++
		}
		percentile := 0.5
		if len(values) > 1 {
			percentile = (float64(first) + float64(last)) / 2 / float64(denominator)
		}
		out[current.code] = percentile
	}
	return out
}

func finitePrediction(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func clampUnit(value float64) float64 {
	return math.Max(-1, math.Min(1, value))
}

func clampScore(value float64) float64 {
	return math.Max(5, math.Min(98, value))
}
