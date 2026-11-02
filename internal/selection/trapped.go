package selection

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

// attachTrappedSignals adds an overhead-supply diagnostic to every ranking
// candidate. The calculation uses the same as-of daily history as the ranking
// and does not change scores, recommendations or risk gates.
func attachTrappedSignals(report *Report, histories []market.History) {
	if report == nil || len(report.Ranking) == 0 || len(histories) == 0 {
		return
	}
	byCode := make(map[string]market.History, len(histories))
	for _, history := range histories {
		byCode[history.Code] = history
	}
	for index := range report.Ranking {
		history, ok := byCode[report.Ranking[index].Code]
		if !ok || history.Adjust != report.Adjust {
			continue
		}
		report.Ranking[index].Trapped = strategy.AnalyzeTrappedSignal(history.Bars)
	}
}
