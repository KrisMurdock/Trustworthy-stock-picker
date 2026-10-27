package yangpullback

import (
	"math"
	"math/rand"
	"sort"
)

// Stats summarizes one fixed-horizon cohort. Rates and concentration shares
// are fractions in [0, 1]; net returns remain percentage points.
type Stats struct {
	Signals         int            `json:"signals"`
	Resolved        int            `json:"resolved"`
	Wins            int            `json:"wins"`
	WinRate         float64        `json:"win_rate"`
	WinRateResolved float64        `json:"win_rate_resolved"`
	MeanNet         float64        `json:"mean_net_pct"`
	MedianNet       float64        `json:"median_net_pct"`
	ProfitFactor    *float64       `json:"profit_factor"`
	TouchRate       float64        `json:"touch_rate"`
	PriceTouchRate  float64        `json:"price_touch_rate"`
	TargetFirst     int            `json:"target_first"`
	StopFirst       int            `json:"stop_first"`
	Ambiguous       int            `json:"ambiguous"`
	Unresolved      int            `json:"unresolved"`
	EntrySkipped    int            `json:"entry_skipped"`
	Purged          int            `json:"purged"`
	Dates           int            `json:"dates"`
	Stocks          int            `json:"stocks"`
	MaxStockShare   float64        `json:"max_stock_share"`
	MaxYearShare    float64        `json:"max_year_share"`
	ByYear          map[string]int `json:"by_year"`
	StatusCounts    map[string]int `json:"status_counts"`
}

// BootstrapResult contains independent signal-date moving-block bootstraps for
// the pattern and its control. The difference is descriptive and is not a
// same-setup paired increment.
type BootstrapResult struct {
	Method              string  `json:"method"`
	Iterations          int     `json:"iterations"`
	Seed                int64   `json:"seed"`
	Confidence          float64 `json:"confidence"`
	BlockLength         int     `json:"block_length"`
	Available           bool    `json:"available"`
	WinRate             float64 `json:"win_rate"`
	WinRateLower        float64 `json:"win_rate_95_lower"`
	WinRateUpper        float64 `json:"win_rate_95_upper"`
	Baseline            string  `json:"baseline,omitempty"`
	BaselineAvailable   bool    `json:"baseline_available"`
	BaselineWinRate     float64 `json:"baseline_win_rate"`
	BaselineLower       float64 `json:"baseline_win_rate_95_lower"`
	BaselineUpper       float64 `json:"baseline_win_rate_95_upper"`
	Difference          float64 `json:"difference"`
	DifferenceAvailable bool    `json:"difference_available"`
	DifferenceLower     float64 `json:"difference_95_lower"`
	DifferenceUpper     float64 `json:"difference_95_upper"`
	IndependentCohorts  bool    `json:"independent_cohorts"`
	Paired              bool    `json:"paired"`
	PatternDates        int     `json:"pattern_signal_dates"`
	BaselineDates       int     `json:"baseline_signal_dates"`
	ComparisonNote      string  `json:"comparison_note"`
}

type dateObservation struct {
	signals int
	wins    int
}

func summarizeTrades(trades []Trade, purged int) Stats {
	byYear := map[string]int{}
	byStock := map[string]int{}
	byDate := map[string]bool{}
	byCode := map[string]bool{}
	nets := make([]float64, 0, len(trades))
	profit, loss := 0.0, 0.0
	touches := 0
	priceTouches := 0
	statusCounts := map[string]int{}

	out := Stats{Purged: purged, ByYear: byYear, StatusCounts: statusCounts}
	for _, trade := range trades {
		out.Signals++
		if trade.Status != "" {
			statusCounts[trade.Status]++
		}
		date := trade.Signal.Date
		if date != "" {
			byDate[date] = true
			byYear[yearOf(date)]++
		}
		if trade.Signal.Code != "" {
			byCode[trade.Signal.Code] = true
			byStock[trade.Signal.Code]++
		}
		switch {
		case trade.EntrySkipped:
			out.EntrySkipped++
		case trade.Resolved:
			out.Resolved++
			nets = append(nets, trade.NetPct)
			if trade.NetPct > 0 {
				out.Wins++
			}
			if trade.NetPct > 0 {
				profit += trade.NetPct
			} else if trade.NetPct < 0 {
				loss -= trade.NetPct
			}
			if trade.MFE > 0 {
				touches++
			}
			if trade.PriceTouch {
				priceTouches++
			}
			if trade.TargetFirst && !trade.Ambiguous {
				out.TargetFirst++
			}
			if trade.StopFirst && !trade.Ambiguous {
				out.StopFirst++
			}
			if trade.Ambiguous {
				out.Ambiguous++
			}
		}
	}

	out.Unresolved = out.Signals - out.Resolved - out.EntrySkipped
	if out.Unresolved < 0 {
		out.Unresolved = 0
	}
	out.Dates = len(byDate)
	out.Stocks = len(byCode)
	if out.Signals > 0 {
		out.WinRate = float64(out.Wins) / float64(out.Signals)
		out.MaxStockShare = maxCountShare(byStock, out.Signals)
		out.MaxYearShare = maxCountShare(byYear, out.Signals)
	}
	if out.Resolved > 0 {
		out.WinRateResolved = float64(out.Wins) / float64(out.Resolved)
		out.MeanNet = meanFloat(nets)
		out.MedianNet = medianFloat(nets)
		out.TouchRate = float64(touches) / float64(out.Resolved)
		out.PriceTouchRate = float64(priceTouches) / float64(out.Resolved)
	}
	if loss > 0 {
		value := profit / loss
		out.ProfitFactor = &value
	}
	return out
}

func maxCountShare(counts map[string]int, total int) float64 {
	if total == 0 {
		return 0
	}
	max := 0
	for _, count := range counts {
		if count > max {
			max = count
		}
	}
	return float64(max) / float64(total)
}

func meanFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func medianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[middle-1] + sorted[middle]) / 2
	}
	return sorted[middle]
}

func yearOf(date string) string {
	if len(date) >= 4 {
		return date[:4]
	}
	return ""
}

func bootstrapComparison(pattern, baseline []Trade, iterations int, seed int64, baselineName string, blockLength int) BootstrapResult {
	if blockLength < 1 {
		blockLength = 1
	}
	out := BootstrapResult{
		Method:             "independent_signal_date_moving_block_bootstrap",
		Iterations:         iterations,
		Seed:               seed,
		Confidence:         0.95,
		BlockLength:        blockLength,
		Baseline:           baselineName,
		IndependentCohorts: true,
		Paired:             false,
		ComparisonNote:     "Independent cohorts with different signal timing; descriptive contrast only, not a paired setup increment.",
	}
	patternGroups := groupBySignalDate(pattern)
	if iterations <= 0 || len(patternGroups) == 0 {
		return out
	}

	patternDates := sortedKeys(patternGroups)
	out.WinRate = aggregateWinRate(patternGroups, patternDates)
	out.PatternDates = len(patternDates)
	patternDraws := bootstrapDateWinRates(patternGroups, patternDates, iterations, blockLength, rand.New(rand.NewSource(seed)))
	if len(patternDraws) > 0 {
		sorted := append([]float64(nil), patternDraws...)
		sort.Float64s(sorted)
		out.Available = true
		out.WinRateLower = bootstrapQuantile(sorted, 0.025)
		out.WinRateUpper = bootstrapQuantile(sorted, 0.975)
	}

	baselineGroups := groupBySignalDate(baseline)
	baselineDates := sortedKeys(baselineGroups)
	if len(baselineDates) > 0 {
		out.BaselineWinRate = aggregateWinRate(baselineGroups, baselineDates)
		out.BaselineDates = len(baselineDates)
	}
	baselineDraws := bootstrapDateWinRates(baselineGroups, baselineDates, iterations, blockLength, rand.New(rand.NewSource(seed^int64(0x5A17))))
	if len(baselineDraws) > 0 {
		sorted := append([]float64(nil), baselineDraws...)
		sort.Float64s(sorted)
		out.BaselineAvailable = true
		out.BaselineLower = bootstrapQuantile(sorted, 0.025)
		out.BaselineUpper = bootstrapQuantile(sorted, 0.975)
	}
	if !out.Available || !out.BaselineAvailable {
		return out
	}
	out.Difference = out.WinRate - out.BaselineWinRate
	differences := make([]float64, 0, min(len(patternDraws), len(baselineDraws)))
	for i := 0; i < len(patternDraws) && i < len(baselineDraws); i++ {
		differences = append(differences, patternDraws[i]-baselineDraws[i])
	}
	if len(differences) > 0 {
		sort.Float64s(differences)
		out.DifferenceAvailable = true
		out.DifferenceLower = bootstrapQuantile(differences, 0.025)
		out.DifferenceUpper = bootstrapQuantile(differences, 0.975)
	}
	return out
}

func bootstrapDateWinRates(groups map[string]dateObservation, dates []string, iterations, blockLength int, random *rand.Rand) []float64 {
	if iterations <= 0 || len(dates) == 0 {
		return nil
	}
	draws := make([]float64, 0, iterations)
	for i := 0; i < iterations; i++ {
		wins, signals := 0, 0
		for _, index := range movingBlockIndices(len(dates), blockLength, random) {
			observation := groups[dates[index]]
			wins += observation.wins
			signals += observation.signals
		}
		if signals > 0 {
			draws = append(draws, float64(wins)/float64(signals))
		}
	}
	return draws
}

func movingBlockIndices(count, blockLength int, random *rand.Rand) []int {
	out := make([]int, 0, count)
	for len(out) < count {
		start := random.Intn(count)
		for offset := 0; offset < blockLength && len(out) < count; offset++ {
			out = append(out, (start+offset)%count)
		}
	}
	return out
}

func groupBySignalDate(trades []Trade) map[string]dateObservation {
	groups := map[string]dateObservation{}
	for _, trade := range trades {
		if trade.Signal.Date == "" || !trade.Resolved || trade.EntrySkipped {
			continue
		}
		observation := groups[trade.Signal.Date]
		observation.signals++
		if trade.NetPct > 0 {
			observation.wins++
		}
		groups[trade.Signal.Date] = observation
	}
	return groups
}

func sortedKeys(values map[string]dateObservation) []string {
	out := make([]string, 0, len(values))
	for date := range values {
		out = append(out, date)
	}
	sort.Strings(out)
	return out
}

func aggregateWinRate(groups map[string]dateObservation, dates []string) float64 {
	wins, signals := 0, 0
	for _, date := range dates {
		observation := groups[date]
		wins += observation.wins
		signals += observation.signals
	}
	if signals == 0 {
		return 0
	}
	return float64(wins) / float64(signals)
}

func bootstrapQuantile(sorted []float64, probability float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	probability = math.Max(0, math.Min(1, probability))
	index := int(math.Floor(probability * float64(len(sorted)-1)))
	return sorted[index]
}
