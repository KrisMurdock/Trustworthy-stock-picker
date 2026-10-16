// Package bottomstudy measures daily price responses to causal bottom signals.
// It does not simulate orders, staged positions or net trading returns.
package bottomstudy

import (
	"context"
	"math"
	"stock-watcher/internal/indicators"
	"stock-watcher/internal/market"
)

const warmup = 60

var rules = []string{"rsi_divergence", "macd_divergence", "rsi_persistence", "macd_contraction", "bollinger_reentry", "any"}

type Signal struct {
	Date       string   `json:"date"`
	Close      float64  `json:"close"`
	Events     []string `json:"events"`
	States     []string `json:"states"`
	PivotDate  string   `json:"pivot_date,omitempty"`
	PriorPivot string   `json:"prior_pivot_date,omitempty"`
}

type metric struct {
	rsi, dif, hist, ma20, lower float64
}

// detect uses only bars available at each day's close. Pivots are emitted two
// bars after the low; the low's price is never used as an entry reference.
func detect(ctx context.Context, bars []market.Bar) ([]Signal, error) {
	metrics := make([]metric, len(bars))
	for i := 34; i < len(bars); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m, err := indicators.Score(bars[:i+1])
		if err != nil {
			return nil, err
		}
		variance := 0.0
		for _, b := range bars[i-19 : i+1] {
			variance += (b.Close - m.MA20) * (b.Close - m.MA20)
		}
		metrics[i] = metric{m.RSI14, m.DIF, 2 * (m.DIF - m.DEA), m.MA20, m.MA20 - 2*math.Sqrt(variance/20)}
	}
	return detectMetrics(bars, metrics), nil
}

func detectMetrics(bars []market.Bar, metrics []metric) []Signal {
	out := make([]Signal, len(bars))
	pivots := []int{}
	previousPersistence, previousContraction := false, false
	for i, b := range bars {
		s := Signal{Date: b.Date, Close: b.Close, Events: []string{}, States: []string{}}
		if i < 37 {
			out[i] = s
			continue
		}
		m := metrics[i]
		persistence := m.rsi <= 30 && metrics[i-1].rsi <= 30 && metrics[i-2].rsi <= 30
		contraction := m.hist < 0 && m.hist > metrics[i-1].hist && metrics[i-1].hist > metrics[i-2].hist && metrics[i-2].hist > metrics[i-3].hist && b.Close < m.ma20
		for _, state := range []struct {
			name         string
			on, previous bool
		}{{"rsi_persistence", persistence, previousPersistence}, {"macd_contraction", contraction, previousContraction}} {
			if state.on {
				s.States = append(s.States, state.name)
				if !state.previous {
					s.Events = append(s.Events, state.name)
				}
			}
		}
		previousPersistence, previousContraction = persistence, contraction
		if bars[i-1].Close < metrics[i-1].lower && b.Close >= m.lower {
			s.Events = append(s.Events, "bollinger_reentry")
			s.States = append(s.States, "bollinger_reentry")
		}
		p := i - 2
		isLow := true
		for j := p - 2; j <= p+2; j++ {
			if j != p && bars[p].Low >= bars[j].Low {
				isLow = false
			}
		}
		if isLow {
			for k := len(pivots) - 1; k >= 0; k-- {
				q := pivots[k]
				if p-q < 5 {
					continue
				}
				if p-q <= 60 && bars[p].Low <= bars[q].Low*0.995 {
					a, z := metrics[q], metrics[p]
					if a.rsi <= 45 && z.rsi <= 45 && z.rsi-a.rsi >= 3 {
						s.Events = append(s.Events, "rsi_divergence")
						s.States = append(s.States, "rsi_divergence")
					}
					if a.dif < 0 && z.dif < 0 && z.dif > a.dif {
						s.Events = append(s.Events, "macd_divergence")
						s.States = append(s.States, "macd_divergence")
					}
					if has(s.Events, "rsi_divergence") || has(s.Events, "macd_divergence") {
						s.PivotDate, s.PriorPivot = bars[p].Date, bars[q].Date
					}
				}
				break // Nearest eligible prior pivot, never the most favorable one.
			}
			pivots = append(pivots, p)
		}
		if i < warmup-1 {
			s.Events, s.States = []string{}, []string{}
		}
		out[i] = s
	}
	return out
}

func has(names []string, rule string) bool {
	if rule == "any" {
		return len(names) > 0
	}
	for _, name := range names {
		if name == rule {
			return true
		}
	}
	return false
}
