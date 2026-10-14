package strategy

import (
	"errors"
	"math"
	"stock-watcher/internal/indicators"
	"stock-watcher/internal/market"
)

type Features struct {
	Return1        float64            `json:"return_1_pct"`
	Return5        float64            `json:"return_5_pct"`
	Return10       float64            `json:"return_10_pct"`
	MA20Distance   float64            `json:"ma20_distance_pct"`
	MA20Slope      float64            `json:"ma20_slope_5_pct"`
	VolumeRatio    float64            `json:"volume_ratio"`
	ATR14Pct       float64            `json:"atr14_pct"`
	ClosePosition  float64            `json:"close_position"`
	UpperWick      float64            `json:"upper_wick_ratio"`
	High20Distance float64            `json:"high20_distance_pct"`
	Relative5      float64            `json:"relative_pool_5_pct"`
	Metrics        indicators.Metrics `json:"trend_metrics"`
}

func Extract(bars []market.Bar) (Features, error) {
	f := Features{}
	if len(bars) < 60 {
		return f, errors.New("60 daily bars required for strategy features")
	}
	m, err := indicators.Score(bars)
	if err != nil {
		return f, err
	}
	f.Metrics = m
	last := bars[len(bars)-1]
	close := last.Close
	f.Return1 = (close/bars[len(bars)-2].Close - 1) * 100
	f.Return5 = (close/bars[len(bars)-6].Close - 1) * 100
	f.Return10 = (close/bars[len(bars)-11].Close - 1) * 100
	f.MA20Distance = (close/m.MA20 - 1) * 100
	previousMA := 0.0
	for _, b := range bars[len(bars)-25 : len(bars)-5] {
		previousMA += b.Close
	}
	previousMA /= 20
	f.MA20Slope = (m.MA20/previousMA - 1) * 100
	f.VolumeRatio = m.VolumeRatio
	for i := len(bars) - 14; i < len(bars); i++ {
		b, prev := bars[i], bars[i-1].Close
		f.ATR14Pct += math.Max(b.High-b.Low, math.Max(math.Abs(b.High-prev), math.Abs(b.Low-prev)))
	}
	f.ATR14Pct = f.ATR14Pct / 14 / close * 100
	if last.High > last.Low {
		f.ClosePosition = (close - last.Low) / (last.High - last.Low)
		f.UpperWick = (last.High - math.Max(last.Open, close)) / (last.High - last.Low)
	}
	high := 0.0
	for _, b := range bars[len(bars)-21 : len(bars)-1] {
		high = math.Max(high, b.High)
	}
	f.High20Distance = (close/high - 1) * 100
	return f, nil
}

// The rule vocabulary is fixed before holdout evaluation; no parameter search on test data.
var ruleNames = []string{"baseline", "breakout", "pullback", "momentum", "volume_recovery"}

func Matches(f Features) []string {
	rules := []string{"baseline"}
	if f.High20Distance >= -0.5 && f.High20Distance <= 3 && f.VolumeRatio >= 1.2 && f.ClosePosition >= 0.65 && f.UpperWick <= 0.3 && f.MA20Slope > 0 {
		rules = append(rules, "breakout")
	}
	if f.MA20Slope > 0 && f.MA20Distance >= -1 && f.MA20Distance <= 3 && f.Return1 > 0 && f.ClosePosition >= 0.6 && f.VolumeRatio <= 1.5 {
		rules = append(rules, "pullback")
	}
	if f.Metrics.MA5 > f.Metrics.MA10 && f.Metrics.MA10 > f.Metrics.MA20 && f.Return5 > 0 && f.Return5 <= 10 && f.Relative5 > 0 && f.Metrics.RSI14 >= 50 && f.Metrics.RSI14 <= 75 {
		rules = append(rules, "momentum")
	}
	if f.VolumeRatio >= 1.2 && f.VolumeRatio <= 2.5 && f.Return1 >= 0 && f.Return1 <= 4 && f.ClosePosition >= 0.7 && f.MA20Distance >= -2 && f.MA20Distance <= 8 {
		rules = append(rules, "volume_recovery")
	}
	return rules
}

func opportunity(f Features) float64 {
	value := float64(f.Metrics.Score)*0.25 + 25
	if f.MA20Slope > 0 {
		value += 10
	}
	if f.Relative5 > 0 {
		value += 10
	}
	if f.ClosePosition >= 0.65 {
		value += 10
	}
	if f.VolumeRatio >= 1 && f.VolumeRatio <= 2.5 {
		value += 10
	}
	if f.ATR14Pct >= 1 && f.ATR14Pct <= 4 {
		value += 10
	}
	if f.MA20Distance > 10 || f.Return5 > 12 {
		value -= 20
	}
	if f.UpperWick > 0.5 {
		value -= 10
	}
	return math.Max(0, math.Min(100, value))
}
