package strategy

import (
	"context"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"sort"
)

// AnalyzeWithChan attaches the legacy 5/15/30-minute Chan diagnostic and the
// auditable structure guard to the daily ranking. The legacy path never changes
// the formal recommendation; only enforce mode can promote guard-passing
// candidates to executable recommendations.
func AnalyzeWithChan(ctx context.Context, c config.Config, histories, raw []market.History, minutes []market.MinuteHistory, asof string) (Result, error) {
	result, err := analyze(ctx, c, histories, raw, asof, nil)
	if err != nil {
		return result, err
	}
	mode := c.StructureGuard.Mode
	if mode == "" {
		mode = StructureGuardShadow
	}
	attachLegacyChanEvidence(&result, raw, minutes, asof)
	applyStructureGuard(&result, c, histories, raw, minutes, asof)
	return result, nil
}

// applyStructureGuard attaches the auditable structure gate and derives the
// eligible-pool ranking. Shadow mode records both views but never promotes a
// candidate to executable. Enforce mode makes the eligible ranking the only
// source of formal recommendations.
func applyStructureGuard(result *Result, c config.Config, histories, raw []market.History, minutes []market.MinuteHistory, asof string) {
	if result == nil {
		return
	}
	mode := c.StructureGuard.Mode
	if mode == "" {
		mode = StructureGuardShadow
	}
	adjustedByCode := map[string]market.History{}
	for _, history := range histories {
		adjustedByCode[history.Code] = history
	}
	rawByCode := map[string]market.History{}
	for _, history := range raw {
		rawByCode[history.Code] = history
	}

	observation := append([]Candidate(nil), result.Ranking...)
	eligible := []Candidate{}
	for i := range observation {
		candidate := observation[i]
		candidate.ObservationRank = i + 1
		candidate.Rank = i + 1
		candidate.ObservationScore = candidate.Score
		candidate.ObservationScoreBreakdown = candidate.ScoreBreakdown
		candidate.EligibleRank = 0
		candidate.EligibleScore = 0
		candidate.EligibleScoreBreakdown = nil
		candidate.EligibleRecommended = false
		adjusted, hasAdjusted := adjustedByCode[candidate.Code]
		rawHistory, hasRaw := rawByCode[candidate.Code]
		evidence := StructureGuardEvidence{Version: 1, Rule: StructureGuardRule, Mode: mode, AsOf: asof, DataStatus: guardDataInvalid, GuardStatus: guardUnknown, SignalStatus: "not_evaluated", ReasonCode: "daily_missing", Reason: "日线数据缺失", Levels: []StructureLevelEvidence{}}
		if hasAdjusted && hasRaw {
			evidence = evaluateStructureGuard(mode, candidate.Code, asof, adjusted, rawHistory, minutes)
		}
		candidate.StructureGuard = &evidence
		if hasAdjusted && hasRaw && evidence.GuardStatus == guardPass {
			eligible = append(eligible, candidate)
		}
		observation[i] = candidate
	}
	result.GuardPassed = len(eligible)

	if len(eligible) > 0 {
		mean := 0.0
		for _, candidate := range eligible {
			mean += candidate.Features.Return5
		}
		mean /= float64(len(eligible))
		for i := range eligible {
			base := eligible[i]
			features := base.Features
			features.Relative5 = features.Return5 - mean
			reranked := rank(base.Code, base.Name, features, result.Research.Rules, c.Strategy)
			reranked.Close = base.Close
			reranked.DayHigh = base.DayHigh
			reranked.DayLow = base.DayLow
			reranked.Plan = base.Plan
			reranked.Chan = base.Chan
			reranked.StructureGuard = base.StructureGuard
			reranked.Wave = base.Wave
			reranked.ObservationRank = base.ObservationRank
			reranked.Rank = base.Rank
			reranked.ObservationScore = base.ObservationScore
			reranked.ObservationScoreBreakdown = base.ObservationScoreBreakdown
			reranked.DailyRecommended = base.DailyRecommended
			reranked.EligibleScore = reranked.Score
			reranked.EligibleScoreBreakdown = reranked.ScoreBreakdown
			hardGate := containsString(base.Risks, "holdout_validation_not_passed") || containsString(base.Risks, "cash_insufficient")
			reranked.EligibleRecommended = base.DailyRecommended && !hardGate
			if hardGate {
				for _, risk := range []string{"holdout_validation_not_passed", "cash_insufficient"} {
					if containsString(base.Risks, risk) && !containsString(reranked.Risks, risk) {
						reranked.Risks = append(reranked.Risks, risk)
					}
				}
			}
			eligible[i] = reranked
		}
		sort.SliceStable(eligible, func(i, j int) bool {
			if eligible[i].EligibleScore == eligible[j].EligibleScore {
				return eligible[i].Code < eligible[j].Code
			}
			return eligible[i].EligibleScore > eligible[j].EligibleScore
		})
		for i := range eligible {
			eligible[i].EligibleRank = i + 1
		}
		eligibleByCode := make(map[string]Candidate, len(eligible))
		for _, candidate := range eligible {
			eligibleByCode[candidate.Code] = candidate
		}
		for i := range observation {
			reranked, ok := eligibleByCode[observation[i].Code]
			if !ok {
				continue
			}
			observation[i].EligibleRank = reranked.EligibleRank
			observation[i].EligibleScore = reranked.EligibleScore
			observation[i].EligibleScoreBreakdown = reranked.EligibleScoreBreakdown
			observation[i].EligibleRecommended = reranked.EligibleRecommended
		}
	}

	result.ExecutionMode = mode
	if mode != StructureGuardEnforce {
		for i := range observation {
			observation[i].Recommended = false
		}
		result.Ranking = observation
		return
	}
	nonEligible := make([]Candidate, 0, len(observation))
	for i := range observation {
		candidate := &observation[i]
		if candidate.StructureGuard == nil {
			nonEligible = append(nonEligible, *candidate)
			continue
		}
		if candidate.StructureGuard.GuardStatus == guardPass {
			continue
		}
		candidate.Recommended = false
		switch candidate.StructureGuard.GuardStatus {
		case guardFail:
			candidate.Risks = append(candidate.Risks, "structure_guard_fail")
		case guardUnknown:
			candidate.Risks = append(candidate.Risks, "structure_guard_unknown")
		}
		nonEligible = append(nonEligible, *candidate)
	}
	for i := range eligible {
		candidate := &eligible[i]
		candidate.Recommended = candidate.EligibleRecommended
		candidate.Score = candidate.EligibleScore
		candidate.ScoreBreakdown = candidate.EligibleScoreBreakdown
	}
	result.Ranking = append(append([]Candidate(nil), eligible...), nonEligible...)
}

// attachLegacyChanEvidence keeps the retired 5/15/30 gate as report evidence
// only. It must not change Recommended or add filtering risks; those decisions
// belong to the structure guard.
func attachLegacyChanEvidence(result *Result, raw []market.History, minutes []market.MinuteHistory, asof string) {
	if result == nil || len(minutes) == 0 {
		return
	}
	daily := make(map[string]market.Bar, len(raw))
	for _, history := range raw {
		if len(history.Bars) > 0 && history.Bars[len(history.Bars)-1].Date == asof {
			daily[history.Code] = history.Bars[len(history.Bars)-1]
		}
	}
	minuteByCode := make(map[string]market.MinuteHistory, len(minutes))
	contextByCode := make(map[string]market.MinuteHistory, len(minutes))
	for _, history := range minutes {
		if history.Period == 30 {
			contextByCode[history.Code] = history
			continue
		}
		if history.Period != 5 {
			continue
		}
		if _, exists := minuteByCode[history.Code]; exists {
			continue
		}
		minuteByCode[history.Code] = history
	}
	for i := range result.Ranking {
		candidate := &result.Ranking[i]
		evidence := unavailableChanEvidence(asof, "无5分钟数据")
		if history, ok := minuteByCode[candidate.Code]; ok {
			if err := history.Validate(); err != nil {
				evidence = unavailableChanEvidence(asof, "5分钟数据无效")
			} else if context, exists := contextByCode[candidate.Code]; exists && market.ValidateMinutePair(history, context) != nil {
				evidence = unavailableChanEvidence(asof, "30m/5m 分钟快照不一致")
			} else if bar, exists := daily[candidate.Code]; exists {
				if value, err := chanSignalEvidence(history, asof, bar); err != nil {
					evidence = unavailableChanEvidence(asof, "缠论计算失败")
				} else {
					evidence = value
				}
			} else {
				evidence = unavailableChanEvidence(asof, "无当日日线")
			}
		}
		candidate.Chan = &evidence
	}
}

func unavailableChanEvidence(asof, reason string) ChanEvidence {
	levels := make([]ChanLevelEvidence, 0, 3)
	for _, period := range []int{chanContextPeriod, chanSetupPeriod, chanTriggerPeriod} {
		levels = append(levels, ChanLevelEvidence{Period: period, Status: chanUnknown, Reason: reason})
	}
	return ChanEvidence{Version: 1, Rule: ChanSignalRule, AsOf: asof, Status: chanUnknown, Reason: reason, Levels: levels}
}
