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

// BootstrapResult contains a date-cluster bootstrap. The interval is for the
// fixed-horizon win rate; the difference interval pairs pattern and baseline
// observations on the same signal dates.
type BootstrapResult struct {
	Method              string  `json:"method"`
	Iterations          int     `json:"iterations"`
	Seed                int64   `json:"seed"`
	Confidence          float64 `json:"confidence"`
	Available           bool    `json:"available"`
	WinRate             float64 `json:"win_rate"`
	WinRateLower        float64 `json:"win_rate_95_lower"`
	WinRateUpper        float64 `json:"win_rate_95_upper"`
	Baseline            string  `json:"baseline,omitempty"`
	BaselineWinRate     float64 `json:"baseline_win_rate"`
	Difference          float64 `json:"difference"`
	DifferenceAvailable bool    `json:"difference_available"`
	DifferenceLower     float64 `json:"difference_95_lower"`
	DifferenceUpper     float64 `json:"difference_95_upper"`
	PairedDates         int     `json:"paired_dates"`
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

func bootstrapComparison(pattern, baseline []Trade, iterations int, seed int64, baselineName string) BootstrapResult {
	out := BootstrapResult{
		Method:     "signal_date_cluster_bootstrap",
		Iterations: iterations,
		Seed:       seed,
		Confidence: 0.95,
		Baseline:   baselineName,
	}
	patternGroups := groupBySignalDate(pattern)
	if iterations <= 0 || len(patternGroups) == 0 {
		return out
	}

	patternDates := sortedKeys(patternGroups)
	out.WinRate = aggregateWinRate(patternGroups, patternDates)
	draws := make([]float64, 0, iterations)
	random := rand.New(rand.NewSource(seed))
	for i := 0; i < iterations; i++ {
		wins, signals := 0, 0
		for range patternDates {
			observation := patternGroups[patternDates[random.Intn(len(patternDates))]]
			wins += observation.wins
			signals += observation.signals
		}
		if signals > 0 {
			draws = append(draws, float64(wins)/float64(signals))
		}
	}
	if len(draws) > 0 {
		sort.Float64s(draws)
		out.Available = true
		out.WinRateLower = bootstrapQuantile(draws, 0.025)
		out.WinRateUpper = bootstrapQuantile(draws, 0.975)
	}

	baselineGroups := groupBySignalDate(baseline)
	baselineDates := sortedKeys(baselineGroups)
	if len(baselineDates) > 0 {
		out.BaselineWinRate = aggregateWinRate(baselineGroups, baselineDates)
	}
	pairedDates := intersectDates(patternDates, baselineDates)
	out.PairedDates = len(pairedDates)
	if out.PairedDates == 0 {
		return out
	}
	out.DifferenceAvailable = true
	out.Difference = aggregateWinRate(patternGroups, pairedDates) - aggregateWinRate(baselineGroups, pairedDates)

	draws = draws[:0]
	for i := 0; i < iterations; i++ {
		patternWins, patternSignals := 0, 0
		baselineWins, baselineSignals := 0, 0
		for range pairedDates {
			date := pairedDates[random.Intn(len(pairedDates))]
			p := patternGroups[date]
			b := baselineGroups[date]
			patternWins += p.wins
			patternSignals += p.signals
			baselineWins += b.wins
			baselineSignals += b.signals
		}
		if patternSignals > 0 && baselineSignals > 0 {
			draws = append(draws, float64(patternWins)/float64(patternSignals)-float64(baselineWins)/float64(baselineSignals))
		}
	}
	if len(draws) > 0 {
		sort.Float64s(draws)
		out.DifferenceLower = bootstrapQuantile(draws, 0.025)
		out.DifferenceUpper = bootstrapQuantile(draws, 0.975)
	} else {
		out.DifferenceAvailable = false
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

func intersectDates(left, right []string) []string {
	rightSet := make(map[string]bool, len(right))
	for _, date := range right {
		rightSet[date] = true
	}
	out := make([]string, 0, len(left))
	for _, date := range left {
		if rightSet[date] {
			out = append(out, date)
		}
	}
	return out
}

func bootstrapQuantile(sorted []float64, probability float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	probability = math.Max(0, math.Min(1, probability))
	index := int(math.Floor(probability * float64(len(sorted)-1)))
	return sorted[index]
}
