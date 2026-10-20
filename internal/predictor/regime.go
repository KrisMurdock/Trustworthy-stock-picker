package predictor

import "sort"

const (
	MarketUp      = "up"
	MarketRange   = "range"
	MarketDown    = "down"
	MarketUnknown = "unknown"
)

// MarketBar is the minimal daily index input needed for market-state
// classification. It deliberately does not depend on storage types.
type MarketBar struct {
	Date  string  `json:"date"`
	Close float64 `json:"close"`
}

// BuildMarketRegimes classifies each trading day using only the twenty trading
// sessions strictly before that day. A missing or stale session is unknown.
func BuildMarketRegimes(tradingDays []string, bars []MarketBar) map[string]string {
	out := make(map[string]string, len(tradingDays))
	closes := make(map[string]float64, len(bars))
	for _, bar := range bars {
		if bar.Date == "" || bar.Close <= 0 {
			continue
		}
		closes[bar.Date] = bar.Close
	}
	days := normalizeDays(tradingDays)
	for i, day := range days {
		if i < 20 {
			out[day] = MarketUnknown
			continue
		}
		window := days[i-20 : i]
		sum := 0.0
		valid := true
		for _, session := range window {
			close, ok := closes[session]
			if !ok || close <= 0 {
				valid = false
				break
			}
			sum += close
		}
		if !valid {
			out[day] = MarketUnknown
			continue
		}
		last, ok := closes[window[len(window)-1]]
		if !ok || last <= 0 || sum <= 0 {
			out[day] = MarketUnknown
			continue
		}
		ma20 := sum / 20
		delta := last/ma20 - 1
		switch {
		case delta > 0.01:
			out[day] = MarketUp
		case delta < -0.01:
			out[day] = MarketDown
		default:
			out[day] = MarketRange
		}
	}
	return out
}

func normalizeDays(days []string) []string {
	out := make([]string, 0, len(days))
	seen := map[string]bool{}
	for _, day := range days {
		if day == "" || seen[day] {
			continue
		}
		seen[day] = true
		out = append(out, day)
	}
	sort.Strings(out)
	return out
}
