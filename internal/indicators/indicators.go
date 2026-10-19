// Package indicators computes the shared daily trend metrics.
package indicators

import (
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"math"
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
