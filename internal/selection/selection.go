// Package selection ports the existing trend rubric. It is opt-in, not an approved strategy.
package selection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"strings"
	"time"
)

type Metrics struct {
	Score            int     `json:"score"`
	MAScore          int     `json:"ma_score"`
	MACDScore        int     `json:"macd_score"`
	RSIScore         int     `json:"rsi_score"`
	ConsecutiveScore int     `json:"consecutive_score"`
	VolumeScore      int     `json:"volume_score"`
	MA5              float64 `json:"ma5"`
	MA10             float64 `json:"ma10"`
	MA20             float64 `json:"ma20"`
	DIF              float64 `json:"dif"`
	DEA              float64 `json:"dea"`
	RSI14            float64 `json:"rsi14"`
	VolumeRatio      float64 `json:"volume_ratio"`
	UpDays           int     `json:"up_days"`
}
type Candidate struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Sector    string  `json:"sector,omitempty"`
	Close     float64 `json:"close"`
	ChangePct float64 `json:"change_pct"`
	Metrics   Metrics `json:"metrics"`
}
type Excluded struct {
	Code     string `json:"code"`
	Reason   string `json:"reason"`
	LastDate string `json:"last_date,omitempty"`
}
type Report struct {
	Version          int         `json:"schema_version"`
	ID               string      `json:"id"`
	AsOf             string      `json:"as_of"`
	Target           string      `json:"target_trading_date"`
	GeneratedAt      time.Time   `json:"generated_at"`
	Strategy         string      `json:"strategy"`
	SelectionEnabled bool        `json:"selection_enabled"`
	Adjust           string      `json:"adjust"`
	MinScore         int         `json:"min_score"`
	TopN             int         `json:"top_n"`
	PoolSize         int         `json:"pool_size"`
	Eligible         int         `json:"eligible"`
	Passed           int         `json:"passed"`
	Rules            []string    `json:"rules"`
	Selected         []Candidate `json:"selected"`
	Excluded         []Excluded  `json:"excluded"`
	Filtered         []Excluded  `json:"filtered"`
}

func sma(closes []float64, n int) float64 {
	sum := 0.0
	for _, v := range closes[len(closes)-n:] {
		sum += v
	}
	return sum / float64(n)
}
func ema(values []float64, n int) []float64 {
	out := make([]float64, 0, len(values)-n+1)
	sum := 0.0
	for _, v := range values[:n] {
		sum += v
	}
	last := sum / float64(n)
	out = append(out, last)
	for _, v := range values[n:] {
		last += (v - last) * 2 / float64(n+1)
		out = append(out, last)
	}
	return out
}
func Score(bars []market.Bar) (Metrics, error) {
	if len(bars) < 35 {
		return Metrics{}, errors.New("insufficient history: 35 daily bars required")
	}
	closes := make([]float64, len(bars))
	for i, b := range bars {
		if b.Close <= 0 || math.IsNaN(b.Close) || math.IsInf(b.Close, 0) || b.Volume < 0 || math.IsNaN(b.Volume) || math.IsInf(b.Volume, 0) {
			return Metrics{}, errors.New("invalid indicator input")
		}
		closes[i] = b.Close
	}
	m := Metrics{MA5: sma(closes, 5), MA10: sma(closes, 10), MA20: sma(closes, 20)}
	if m.MA5 > m.MA10 && m.MA10 > m.MA20 {
		m.MAScore = 30
	} else if m.MA5 > m.MA10 {
		m.MAScore = 15
	}
	fast := ema(closes, 12)
	slow := ema(closes, 26)
	dif := make([]float64, len(slow))
	for i := range dif {
		dif[i] = fast[i+14] - slow[i]
	}
	dea := ema(dif, 9)
	m.DIF = dif[len(dif)-1]
	m.DEA = dea[len(dea)-1]
	hist := (m.DIF - m.DEA) * 2
	prevHist := (dif[len(dif)-2] - dea[len(dea)-2]) * 2
	if m.DIF > m.DEA && hist > prevHist {
		m.MACDScore = 25
	} else if m.DIF > m.DEA {
		m.MACDScore = 15
	}
	gain, loss := 0.0, 0.0
	for i := 1; i <= 14; i++ {
		delta := closes[i] - closes[i-1]
		if delta > 0 {
			gain += delta
		} else {
			loss -= delta
		}
	}
	gain /= 14
	loss /= 14
	for i := 15; i < len(closes); i++ {
		delta := closes[i] - closes[i-1]
		gain = (gain*13 + math.Max(delta, 0)) / 14
		loss = (loss*13 + math.Max(-delta, 0)) / 14
	}
	if gain == 0 && loss == 0 {
		m.RSI14 = 50
	} else if loss == 0 {
		m.RSI14 = 100
	} else {
		m.RSI14 = 100 - 100/(1+gain/loss)
	}
	switch {
	case m.RSI14 >= 60 && m.RSI14 <= 80:
		m.RSIScore = 20
	case m.RSI14 >= 50 && m.RSI14 < 60:
		m.RSIScore = 12
	case m.RSI14 >= 40 && m.RSI14 < 50:
		m.RSIScore = 5
	}
	for i := len(closes) - 1; i > 0 && closes[i] > closes[i-1]; i-- {
		m.UpDays++
	}
	switch {
	case m.UpDays >= 3:
		m.ConsecutiveScore = 15
	case m.UpDays == 2:
		m.ConsecutiveScore = 10
	case m.UpDays == 1:
		m.ConsecutiveScore = 4
	}
	volume := 0.0
	for _, b := range bars[len(bars)-6 : len(bars)-1] {
		volume += b.Volume
	}
	volume /= 5
	if volume > 0 {
		m.VolumeRatio = bars[len(bars)-1].Volume / volume
	}
	if m.VolumeRatio >= 1.5 {
		m.VolumeScore = 10
	} else if m.VolumeRatio >= 1 {
		m.VolumeScore = 5
	}
	m.Score = m.MAScore + m.MACDScore + m.RSIScore + m.ConsecutiveScore + m.VolumeScore
	return m, nil
}

func Build(c config.Config, histories []market.History, asof, target string, now time.Time) (Report, error) {
	r := Report{Version: 1, AsOf: asof, Target: target, GeneratedAt: now.In(market.Shanghai), Strategy: "leader_daily_v1", SelectionEnabled: c.Selection.Enabled, Adjust: c.Daily.Adjust, MinScore: c.Selection.MinScore, TopN: c.Selection.TopN, PoolSize: len(c.Stocks), Selected: []Candidate{}, Excluded: []Excluded{}, Filtered: []Excluded{}, Rules: []string{
		"收盘价 > MA20 且 MA5 > MA10 > MA20",
		fmt.Sprintf("量比 ≥ %.2f 且 MACD DIF > DEA", c.Selection.MinVolumeRatio),
		fmt.Sprintf("RSI14 %.0f～%.0f，日涨跌幅 %.1f%%～%.1f%%，排除 ST / 停牌", c.Selection.RSIMin, c.Selection.RSIMax, c.Selection.MinDayChange, c.Selection.MaxDayChange),
	}}
	byCode := map[string]market.History{}
	for _, h := range histories {
		if err := h.Validate(); err != nil {
			return r, fmt.Errorf("invalid persisted history for %s: %w", h.Code, err)
		}
		if _, exists := byCode[h.Code]; exists {
			return r, errors.New("duplicate stock history")
		}
		byCode[h.Code] = h
	}
	ranked := []Candidate{}
	for _, s := range c.Stocks {
		h, ok := byCode[s.Code]
		if !ok || h.Version != 1 || h.Adjust != c.Daily.Adjust || h.AsOf != asof {
			return r, errors.New("incomplete or mismatched pool history")
		}
		if len(h.Bars) == 0 {
			r.Excluded = append(r.Excluded, Excluded{Code: s.Code, Reason: "no_history"})
			continue
		}
		last := h.Bars[len(h.Bars)-1]
		if last.Date != asof {
			r.Excluded = append(r.Excluded, Excluded{Code: s.Code, Reason: "stale_or_suspended", LastDate: last.Date})
			continue
		}
		if last.Volume == 0 {
			r.Excluded = append(r.Excluded, Excluded{Code: s.Code, Reason: "no_trading_volume", LastDate: last.Date})
			continue
		}
		m, err := Score(h.Bars)
		if err != nil {
			r.Excluded = append(r.Excluded, Excluded{Code: s.Code, Reason: "insufficient_or_invalid_history", LastDate: last.Date})
			continue
		}
		r.Eligible++
		change := (last.Close/h.Bars[len(h.Bars)-2].Close - 1) * 100
		name := strings.ToUpper(strings.TrimSpace(h.Name))
		reason := ""
		switch {
		case strings.HasPrefix(name, "ST") || strings.HasPrefix(name, "*ST") || strings.HasPrefix(name, "S*ST") || strings.HasPrefix(name, "SST") || strings.Contains(name, "退"):
			reason = "special_treatment"
		case !(last.Close > m.MA20 && m.MA5 > m.MA10 && m.MA10 > m.MA20):
			reason = "no_bullish_alignment"
		case m.VolumeRatio < c.Selection.MinVolumeRatio || m.DIF <= m.DEA:
			reason = "insufficient_volume_or_macd"
		case m.RSI14 < c.Selection.RSIMin || m.RSI14 > c.Selection.RSIMax || change < c.Selection.MinDayChange || change > c.Selection.MaxDayChange:
			reason = "overheat_or_weakness"
		case m.Score < c.Selection.MinScore:
			reason = "score_below_threshold"
		}
		if reason != "" {
			r.Filtered = append(r.Filtered, Excluded{Code: s.Code, Reason: reason})
			continue
		}
		if c.Selection.Enabled {
			ranked = append(ranked, Candidate{Code: s.Code, Name: h.Name, Sector: s.Sector, Close: last.Close, ChangePct: change, Metrics: m})
		}
	}
	if r.Eligible == 0 {
		return r, errors.New("no stocks have complete eligible daily data")
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Metrics.Score == ranked[j].Metrics.Score {
			return ranked[i].Code < ranked[j].Code
		}
		return ranked[i].Metrics.Score > ranked[j].Metrics.Score
	})
	r.Passed = len(ranked)
	r.Selected = append(r.Selected, ranked[:min(len(ranked), c.Selection.TopN)]...)
	// ID includes configuration, selected results and data date, not wall-clock generation time.
	identity := r
	identity.GeneratedAt = time.Time{}
	identity.ID = ""
	data, _ := json.Marshal(identity)
	sum := sha256.Sum256(data)
	r.ID = hex.EncodeToString(sum[:12])
	return r, nil
}
