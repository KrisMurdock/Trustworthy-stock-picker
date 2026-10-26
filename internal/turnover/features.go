// Package turnover derives point-in-time liquidity features from daily turnover
// rate and amount observations. It deliberately does not infer float shares
// from price or volume; missing observations remain missing.
package turnover

import (
	"errors"
	"math"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	ShortWindow = 5
	LongWindow  = 20
	MinHistory  = LongWindow + 1
)

// FeatureNames is the frozen order used by the research models and reports.
var FeatureNames = []string{
	"log_turnover_rate",
	"log_turnover_ratio_5",
	"log_turnover_ratio_20",
	"log_turnover_5_20",
	"turnover_z_20",
	"turnover_cv_20",
	"log_amount",
	"log_amount_ratio_5",
	"amount_z_20",
	"log_float_mcap",
}

type Features struct {
	LogTurnoverRate    float64 `json:"log_turnover_rate"`
	LogTurnoverRatio5  float64 `json:"log_turnover_ratio_5"`
	LogTurnoverRatio20 float64 `json:"log_turnover_ratio_20"`
	LogTurnover5To20   float64 `json:"log_turnover_5_20"`
	TurnoverZ20        float64 `json:"turnover_z_20"`
	TurnoverCV20       float64 `json:"turnover_cv_20"`
	LogAmount          float64 `json:"log_amount"`
	LogAmountRatio5    float64 `json:"log_amount_ratio_5"`
	AmountZ20          float64 `json:"amount_z_20"`
	LogFloatMcap       float64 `json:"log_float_mcap"`
}

func (f Features) Values() []float64 {
	return []float64{f.LogTurnoverRate, f.LogTurnoverRatio5, f.LogTurnoverRatio20, f.LogTurnover5To20,
		f.TurnoverZ20, f.TurnoverCV20, f.LogAmount, f.LogAmountRatio5, f.AmountZ20, f.LogFloatMcap}
}

// Extract uses one observation on date plus at most the previous LongWindow
// observations. It rejects a stale final observation and insufficient history.
func Extract(metrics []market.DailyMetric, date string) (Features, error) {
	if date == "" {
		return Features{}, errors.New("turnover feature date is required")
	}
	end := -1
	for i := range metrics {
		if metrics[i].Date > date {
			break
		}
		end = i
	}
	if end < MinHistory-1 || metrics[end].Date != date {
		return Features{}, errors.New("turnover history is missing the signal date or is too short")
	}
	window := metrics[end-LongWindow+1 : end+1]
	current := window[len(window)-1]
	if current.TurnoverRatePct <= 0 || current.AmountCNY <= 0 {
		return Features{}, errors.New("turnover observation is not positive")
	}
	turnoverShort, err := meanPositive(window[len(window)-ShortWindow:])
	if err != nil {
		return Features{}, err
	}
	turnoverLong, err := meanPositive(window)
	if err != nil {
		return Features{}, err
	}
	amountShort, err := meanAmount(window[len(window)-ShortWindow:])
	if err != nil {
		return Features{}, err
	}
	mean, deviation, err := stats(window)
	if err != nil {
		return Features{}, err
	}
	amountMean, amountDeviation, err := amountStats(window)
	if err != nil {
		return Features{}, err
	}
	return Features{
		LogTurnoverRate:    math.Log1p(current.TurnoverRatePct),
		LogTurnoverRatio5:  math.Log(current.TurnoverRatePct / turnoverShort),
		LogTurnoverRatio20: math.Log(current.TurnoverRatePct / turnoverLong),
		LogTurnover5To20:   math.Log(turnoverShort / turnoverLong),
		TurnoverZ20:        (current.TurnoverRatePct - mean) / deviation,
		TurnoverCV20:       deviation / mean,
		LogAmount:          math.Log1p(current.AmountCNY),
		LogAmountRatio5:    math.Log(current.AmountCNY / amountShort),
		AmountZ20:          (current.AmountCNY - amountMean) / amountDeviation,
		// Float market value is amount / (turnover rate / 100). It is a
		// point-in-time size control, not an independently observed share count.
		LogFloatMcap: math.Log(current.AmountCNY * 100 / current.TurnoverRatePct),
	}, nil
}

func meanPositive(metrics []market.DailyMetric) (float64, error) {
	sum := 0.0
	for _, metric := range metrics {
		if metric.TurnoverRatePct <= 0 {
			return 0, errors.New("turnover history contains a non-positive value")
		}
		sum += metric.TurnoverRatePct
	}
	return sum / float64(len(metrics)), nil
}

func meanAmount(metrics []market.DailyMetric) (float64, error) {
	sum := 0.0
	for _, metric := range metrics {
		if metric.AmountCNY <= 0 {
			return 0, errors.New("amount history contains a non-positive value")
		}
		sum += metric.AmountCNY
	}
	return sum / float64(len(metrics)), nil
}

func stats(metrics []market.DailyMetric) (float64, float64, error) {
	values := make([]float64, len(metrics))
	for i, metric := range metrics {
		if metric.TurnoverRatePct <= 0 {
			return 0, 0, errors.New("turnover history contains a non-positive value")
		}
		values[i] = metric.TurnoverRatePct
	}
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	variance := 0.0
	for _, value := range values {
		delta := value - mean
		variance += delta * delta
	}
	variance /= float64(len(values))
	if mean <= 0 || variance <= 0 {
		return 0, 0, errors.New("turnover history has no usable dispersion")
	}
	return mean, math.Sqrt(variance), nil
}

func amountStats(metrics []market.DailyMetric) (float64, float64, error) {
	mean := 0.0
	for _, metric := range metrics {
		if metric.AmountCNY <= 0 {
			return 0, 0, errors.New("amount history contains a non-positive value")
		}
		mean += metric.AmountCNY
	}
	mean /= float64(len(metrics))
	variance := 0.0
	for _, metric := range metrics {
		delta := metric.AmountCNY - mean
		variance += delta * delta
	}
	variance /= float64(len(metrics))
	if mean <= 0 || variance <= 0 {
		return 0, 0, errors.New("amount history has no usable dispersion")
	}
	return mean, math.Sqrt(variance), nil
}
