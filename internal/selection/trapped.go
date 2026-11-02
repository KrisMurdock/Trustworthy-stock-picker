package selection

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

// attachTrappedSignals adds a turnover-adjusted overhead-supply diagnostic to
// every ranking or selected candidate. Cost uses the unadjusted history;
// adjusted history is never substituted because it would distort historical
// price levels around corporate actions. A missing raw history still produces
// an explicit unknown signal so every recommendation remains renderable.
func attachTrappedSignals(report *Report, histories, raw, trappedRaw []market.History, metrics map[string]market.DailyMetricHistory) {
	if report == nil || (len(report.Ranking) == 0 && len(report.Selected) == 0) {
		return
	}
	trappedByCode := make(map[string]market.History, len(trappedRaw))
	for _, history := range trappedRaw {
		if history.Adjust == "none" {
			trappedByCode[history.Code] = history
		}
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
	codes := make([]string, 0, len(report.Ranking)+len(report.Selected))
	seenCodes := make(map[string]bool, cap(codes))
	for _, candidate := range report.Ranking {
		if !seenCodes[candidate.Code] {
			seenCodes[candidate.Code] = true
			codes = append(codes, candidate.Code)
		}
	}
	for _, candidate := range report.Selected {
		if !seenCodes[candidate.Code] {
			seenCodes[candidate.Code] = true
			codes = append(codes, candidate.Code)
		}
	}
	signals := make(map[string]*strategy.TrappedSignal, len(codes))
	for _, code := range codes {
		history, ok := trappedByCode[code]
		if !ok {
			history, ok = rawByCode[code]
		}
		if !ok {
			history, ok = adjustedByCode[code]
		}
		if !ok {
			signals[code] = strategy.TrappedSignalUnavailable("unadjusted_history_unavailable")
			continue
		}
		var metric *market.DailyMetricHistory
		if value, found := metrics[code]; found {
			copy := value
			metric = &copy
		}
		signal := strategy.AnalyzeTrappedSignalV3(history, metric)
		signals[code] = signal
	}
	for index := range report.Ranking {
		code := report.Ranking[index].Code
		report.Ranking[index].Trapped = signals[code]
	}
	for index := range report.Selected {
		code := report.Selected[index].Code
		if report.Selected[index].Ranking == nil {
			report.Selected[index].Ranking = &strategy.Candidate{Code: code, Name: report.Selected[index].Name, Close: report.Selected[index].Close, Rank: index + 1}
		}
		signal, ok := signals[code]
		if !ok {
			continue
		}
		report.Selected[index].Ranking.Trapped = signal
	}
}
