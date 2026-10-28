package yangpullback

import (
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

// YearlyStats is a descriptive calendar-year comparison. Pattern and control
// are independent cohorts, so the increment is not a paired setup effect.
type YearlyStats struct {
	Year      string          `json:"year"`
	Pattern   Stats           `json:"pattern"`
	Control   Stats           `json:"control"`
	Increment float64         `json:"win_rate_increment"`
	Bootstrap BootstrapResult `json:"bootstrap_vs_control"`
}

// HorizonPoint is one holding period in the 1..10 day curve.
type HorizonPoint struct {
	Horizon   int             `json:"horizon_trading_days"`
	Pattern   Stats           `json:"pattern"`
	Control   Stats           `json:"control"`
	Increment float64         `json:"win_rate_increment"`
	Bootstrap BootstrapResult `json:"bootstrap_vs_control"`
}

type Robustness struct {
	Years                       []YearlyStats  `json:"years"`
	Horizons                    []HorizonPoint `json:"horizons"`
	YearsPatternAboveControl    int            `json:"years_pattern_above_control"`
	MedianYearlyIncrement       float64        `json:"median_yearly_increment"`
	BestHorizonByIncrement      int            `json:"best_horizon_by_increment"`
	BestHorizonIncrement        float64        `json:"best_horizon_increment"`
	HorizonCurvePatternBest     int            `json:"horizon_curve_pattern_best"`
	HorizonCurvePatternBestRate float64        `json:"horizon_curve_pattern_best_win_rate"`
}

func appendRobustness(result *Evaluation, pairsByCode map[string]pairedHistory, patternSignals, controlSignals []Signal, patternTrades, controlTrades []Trade, s config.Strategy, cal *calendar.Calendar, opts EvalOptions) {
	patternByYear := tradesByYear(patternTrades)
	controlByYear := tradesByYear(controlTrades)
	years := unionYears(patternByYear, controlByYear)
	increments := make([]float64, 0, len(years))
	for _, year := range years {
		pattern := summarizeTrades(patternByYear[year], 0)
		control := summarizeTrades(controlByYear[year], 0)
		increment := pattern.WinRateResolved - control.WinRateResolved
		if pattern.Resolved > 0 && control.Resolved > 0 {
			increments = append(increments, increment)
		}
		bootstrap := bootstrapComparison(patternByYear[year], controlByYear[year], opts.Bootstrap, opts.Seed, "hold_only_same_year", opts.Horizon+2)
		result.Robustness.Years = append(result.Robustness.Years, YearlyStats{
			Year:      year,
			Pattern:   pattern,
			Control:   control,
			Increment: increment,
			Bootstrap: bootstrap,
		})
		if pattern.Resolved > 0 && control.Resolved > 0 && pattern.WinRateResolved > control.WinRateResolved {
			result.Robustness.YearsPatternAboveControl++
		}
	}
	result.Robustness.MedianYearlyIncrement = medianFloat(increments)

	bestIncrement := 0.0
	bestHorizon := 0
	bestPatternRate := 0.0
	bestPatternHorizon := 0
	for horizon := 1; horizon <= 10; horizon++ {
		pattern := fixedHoldTrades(pairsByCode, patternSignals, s, cal, horizon)
		control := fixedHoldTrades(pairsByCode, controlSignals, s, cal, horizon)
		patternStats := summarizeTrades(pattern, 0)
		controlStats := summarizeTrades(control, 0)
		increment := patternStats.WinRateResolved - controlStats.WinRateResolved
		bootstrap := bootstrapComparison(pattern, control, opts.Bootstrap, opts.Seed, "hold_only", horizon+2)
		result.Robustness.Horizons = append(result.Robustness.Horizons, HorizonPoint{
			Horizon:   horizon,
			Pattern:   patternStats,
			Control:   controlStats,
			Increment: increment,
			Bootstrap: bootstrap,
		})
		if bestHorizon == 0 || increment > bestIncrement {
			bestHorizon, bestIncrement = horizon, increment
		}
		if bestPatternHorizon == 0 || patternStats.WinRateResolved > bestPatternRate {
			bestPatternHorizon, bestPatternRate = horizon, patternStats.WinRateResolved
		}
	}
	result.Robustness.BestHorizonByIncrement = bestHorizon
	result.Robustness.BestHorizonIncrement = bestIncrement
	result.Robustness.HorizonCurvePatternBest = bestPatternHorizon
	result.Robustness.HorizonCurvePatternBestRate = bestPatternRate

	result.Data["robustness_years_pattern_above_control"] = result.Robustness.YearsPatternAboveControl
	result.Data["robustness_median_yearly_increment"] = result.Robustness.MedianYearlyIncrement
	result.Data["robustness_best_horizon_by_increment"] = result.Robustness.BestHorizonByIncrement
	result.Data["robustness_best_horizon_increment"] = result.Robustness.BestHorizonIncrement
	result.Data["robustness_scope"] = "descriptive calendar-year and 1..10 day horizon curves; same data, overlapping trades, no selection"
}

func tradesByYear(trades []Trade) map[string][]Trade {
	out := map[string][]Trade{}
	for _, trade := range trades {
		date := trade.Signal.Date
		if len(date) < 4 {
			continue
		}
		year := date[:4]
		out[year] = append(out[year], trade)
	}
	return out
}

func unionYears(left, right map[string][]Trade) []string {
	seen := map[string]bool{}
	for year := range left {
		seen[year] = true
	}
	for year := range right {
		seen[year] = true
	}
	out := make([]string, 0, len(seen))
	for year := range seen {
		out = append(out, year)
	}
	sort.Strings(out)
	return out
}
