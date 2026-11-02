package selection

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

// attachTrappedSignals adds a turnover-adjusted overhead-supply diagnostic to
// every ranking candidate. Cost uses the unadjusted history; adjusted history
// is never substituted because it would distort historical price levels around
// corporate actions. A missing raw history still produces an explicit unknown
// signal so every recommendation remains renderable.
func attachTrappedSignals(report *Report, histories, raw []market.History, metrics map[string]market.DailyMetricHistory) {
	if report == nil || len(report.Ranking) == 0 {
		return
	}
	rawByCode := make(map[string]market.History, len(raw))
	for _, history := range raw {
		if history.Adjust == "none" {
			rawByCode[history.Code] = history
		}
	}
	adjustedByCode := make(map[string]market.History, len(histories))
	for _, history := range histories {
		if history.Adjust == "none" {
			adjustedByCode[history.Code] = history
		}
	}
	signals := make(map[string]*strategy.TrappedSignal, len(report.Ranking))
	for index := range report.Ranking {
		code := report.Ranking[index].Code
		history, ok := rawByCode[code]
		if !ok {
			history, ok = adjustedByCode[code]
		}
		if !ok {
			signals[code] = strategy.TrappedSignalUnavailable("unadjusted_history_unavailable")
			report.Ranking[index].Trapped = signals[code]
			continue
		}
		var metric *market.DailyMetricHistory
		if value, found := metrics[code]; found {
			copy := value
			metric = &copy
		}
		signal := strategy.AnalyzeTrappedSignalV3(history, metric)
		signals[code] = signal
		report.Ranking[index].Trapped = signal
	}
	// Keep Selected aligned with Ranking for callers that read the compact
	// recommendation list instead of the full ranking.
	for index := range report.Selected {
		if report.Selected[index].Ranking == nil {
			continue
		}
		if signal, ok := signals[report.Selected[index].Code]; ok {
			report.Selected[index].Ranking.Trapped = signal
		}
	}
}
