package strategy

import (
	"math"
	"stock-watcher/internal/indicators"
	"stock-watcher/internal/market"
)

// Bollinger uses 20 closes, a simple average and population standard deviation.
// A flat window has no defined %B; it must not create a recovery signal.
type Bollinger struct {
	Middle   float64  `json:"middle"`
	Upper    float64  `json:"upper"`
	Lower    float64  `json:"lower"`
	WidthPct float64  `json:"width_pct"`
	PercentB *float64 `json:"percent_b"`
}

type SignalIndicators struct {
	Version               int       `json:"schema_version"`
	PreviousRSI14         float64   `json:"previous_rsi14"`
	MACDHistogram         float64   `json:"macd_histogram"`
	PreviousMACDHistogram float64   `json:"previous_macd_histogram"`
	Bollinger             Bollinger `json:"bollinger"`
	PreviousPercentB      *float64  `json:"previous_percent_b"`
}

func bollinger(bars []market.Bar) Bollinger {
	window := bars[len(bars)-20:]
	b := Bollinger{}
	for _, x := range window {
		b.Middle += x.Close / 20
	}
	variance := 0.0
	for _, x := range window {
		variance += math.Pow(x.Close-b.Middle, 2) / 20
	}
	deviation := math.Sqrt(variance)
	b.Upper, b.Lower = b.Middle+2*deviation, b.Middle-2*deviation
	width := b.Upper - b.Lower
	b.WidthPct = width / b.Middle * 100
	if width > math.Max(1, math.Abs(b.Middle))*1e-12 {
		position := (window[len(window)-1].Close - b.Lower) / width
		b.PercentB = &position
	}
	return b
}

// Only experiment selection opts into these extra observations. Daily ranking
// and notifications retain their existing indicator and score behavior.
func extractSignalFeatures(bars []market.Bar) (Features, error) {
	f, err := Extract(bars)
	if err != nil {
		return f, err
	}
	previous, err := indicators.Score(bars[:len(bars)-1])
	if err != nil {
		return f, err
	}
	f.SignalIndicators = &SignalIndicators{
		Version: 1, PreviousRSI14: previous.RSI14,
		MACDHistogram: 2 * (f.Metrics.DIF - f.Metrics.DEA), PreviousMACDHistogram: 2 * (previous.DIF - previous.DEA),
		Bollinger: bollinger(bars), PreviousPercentB: bollinger(bars[:len(bars)-1]).PercentB,
	}
	return f, nil
}

func validSignalRule(rule string) bool {
	switch rule {
	case "", "rsi", "macd", "bollinger", "confluence", "all-three", "guard", "confluence-guard", ChanSignalRule:
		return true
	}
	return false
}

// These thresholds are fixed hypotheses, not fitted parameters or independent
// probabilities. Filters run before Top N and before tomorrow's trigger path.
func matchesSignalRule(rule string, f Features) bool {
	if rule == "" {
		return true
	}
	x := f.SignalIndicators
	if x == nil || x.Version != 1 {
		return false
	}
	rsi := f.Metrics.RSI14 >= 40 && f.Metrics.RSI14 <= 65 && f.Metrics.RSI14 > x.PreviousRSI14
	macd := f.Metrics.DIF > f.Metrics.DEA && x.MACDHistogram > x.PreviousMACDHistogram
	band := x.Bollinger.PercentB != nil && x.PreviousPercentB != nil && *x.Bollinger.PercentB >= .1 && *x.Bollinger.PercentB <= .6 && *x.Bollinger.PercentB > *x.PreviousPercentB
	guard := f.ATR14Pct <= 3 && f.MA20Distance >= -5 && f.MA20Distance <= 5 && f.VolumeRatio >= .8 && f.VolumeRatio <= 2.5
	votes := 0
	for _, yes := range []bool{rsi, macd, band} {
		if yes {
			votes++
		}
	}
	switch rule {
	case "rsi":
		return rsi
	case "macd":
		return macd
	case "bollinger":
		return band
	case "confluence":
		return votes >= 2
	case "all-three":
		return votes == 3
	case "guard":
		return guard
	case "confluence-guard":
		return votes >= 2 && guard
	}
	return false
}
