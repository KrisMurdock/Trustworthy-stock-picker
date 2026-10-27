package selection

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const articleChartLimit = 10

// attachArticleCharts keeps the persisted payload bounded. Only stocks that
// can appear in the premarket or second-stage article receive a chart.
func attachArticleCharts(report *Report, histories []market.History) {
	if report == nil || len(report.Ranking) == 0 || len(histories) == 0 {
		return
	}
	wanted := map[string]bool{}
	for index := 0; index < len(report.Ranking) && index < articleChartLimit; index++ {
		wanted[report.Ranking[index].Code] = true
	}
	if report.SecondStage != nil {
		for _, candidate := range report.SecondStage.Candidates {
			if candidate.Code != "" {
				wanted[candidate.Code] = true
			}
		}
	}
	byCode := make(map[string]market.History, len(histories))
	for _, history := range histories {
		byCode[history.Code] = history
	}
	for index := range report.Ranking {
		candidate := &report.Ranking[index]
		if !wanted[candidate.Code] || candidate.Chart != nil {
			continue
		}
		history, ok := byCode[candidate.Code]
		if !ok {
			continue
		}
		var zone *strategy.ChanZoneEvidence
		if candidate.StructureGuard != nil {
			zone = candidate.StructureGuard.DailyZone
		}
		candidate.Chart = strategy.BuildArticleChart(history, candidate.Wave, zone)
	}
}
