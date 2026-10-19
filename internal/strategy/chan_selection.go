package strategy

import (
	"context"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
)

// AnalyzeWithChan adds the fixed 5/15/30-minute Chan check to the daily
// ranking. Chan is an eligibility gate for recommendations, not a replacement
// for the daily opportunity score or its full-pool relative strength.
func AnalyzeWithChan(ctx context.Context, c config.Config, histories, raw []market.History, minutes []market.MinuteHistory, asof string) (Result, error) {
	result, err := analyze(ctx, c, histories, raw, asof, nil)
	if err != nil {
		return result, err
	}
	applyChanGate(&result, raw, minutes, asof)
	return result, nil
}

// applyChanGate is intentionally separate from scoring so the pushed report
// can state both facts: the score was computed by the daily ranking rule, and
// the recommendation was or was not filtered by Chan structure.
func applyChanGate(result *Result, raw []market.History, minutes []market.MinuteHistory, asof string) {
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
		if evidence.Status == chanPass {
			continue
		}
		candidate.Recommended = false
		risk := "chan_filtered"
		if evidence.Status == chanUnknown {
			risk = "chan_unknown"
		}
		candidate.Risks = append(candidate.Risks, risk)
	}
}

func unavailableChanEvidence(asof, reason string) ChanEvidence {
	levels := make([]ChanLevelEvidence, 0, 3)
	for _, period := range []int{chanContextPeriod, chanSetupPeriod, chanTriggerPeriod} {
		levels = append(levels, ChanLevelEvidence{Period: period, Status: chanUnknown, Reason: reason})
	}
	return ChanEvidence{Version: 1, Rule: ChanSignalRule, AsOf: asof, Status: chanUnknown, Reason: reason, Levels: levels}
}
