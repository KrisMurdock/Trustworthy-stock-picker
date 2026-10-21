package firststage

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	ReviewSchemaVersion = 1

	ReviewStatusOK          = "ok"
	ReviewStatusEmpty       = "empty"
	ReviewStatusUnavailable = "unavailable"

	PushLimit = 30
)

type ReviewOptions struct {
	SignalDate     string
	EvaluationDate string
	Adjust         string
	SelectedLimit  int
	ThresholdPct   float64
	Now            time.Time
}

type ReviewCandidate struct {
	Rank           int     `json:"rank"`
	Code           string  `json:"code"`
	Name           string  `json:"name"`
	SignalClose    float64 `json:"signal_close"`
	Close          float64 `json:"evaluation_close"`
	High           float64 `json:"evaluation_high"`
	CloseReturnPct float64 `json:"close_return_pct"`
	HighReturnPct  float64 `json:"high_return_pct"`
	Correct        bool    `json:"correct"`
	Touch          bool    `json:"touch"`
	Status         string  `json:"status"`
}

type ReviewReport struct {
	Version              int               `json:"schema_version"`
	Status               string            `json:"status"`
	SignalDate           string            `json:"signal_date"`
	EvaluationDate       string            `json:"evaluation_date"`
	GeneratedAt          time.Time         `json:"generated_at"`
	Criterion            string            `json:"criterion"`
	ThresholdPct         float64           `json:"threshold_pct"`
	SelectionLimit       int               `json:"selection_limit"`
	Selected             int               `json:"selected"`
	Evaluated            int               `json:"evaluated"`
	Unavailable          int               `json:"unavailable"`
	Hits                 int               `json:"hits"`
	Misses               int               `json:"misses"`
	TouchHits            int               `json:"touch_hits"`
	PoolSize             int               `json:"pool_size"`
	PoolEvaluated        int               `json:"pool_evaluated"`
	PoolUnavailable      int               `json:"pool_unavailable"`
	PoolHits             int               `json:"pool_hits"`
	PoolTouchHits        int               `json:"pool_touch_hits"`
	SelectionHitRatePct  float64           `json:"selection_hit_rate_pct"`
	RecallPct            float64           `json:"recall_pct"`
	RecallAvailable      bool              `json:"recall_available"`
	TouchHitRatePct      float64           `json:"touch_hit_rate_pct"`
	TouchRecallPct       float64           `json:"touch_recall_pct"`
	TouchRecallAvailable bool              `json:"touch_recall_available"`
	Candidates           []ReviewCandidate `json:"candidates"`
	Reason               string            `json:"reason,omitempty"`
}

type reviewPrices struct {
	signalClose float64
	close       float64
	high        float64
	status      string
}

func EvaluateNextDay(previous Report, histories []market.History, pool []config.Stock, opts ReviewOptions) (ReviewReport, error) {
	report := reviewBaseReport(opts)
	if previous.AsOf != opts.SignalDate || previous.Target != opts.EvaluationDate {
		return failReview(report, errors.New("first-stage review dates do not match the previous report"))
	}
	if previous.Status != StatusOK || len(previous.Candidates) == 0 {
		report.Status = ReviewStatusEmpty
		report.Reason = "previous first-stage report has no candidates"
		return report, nil
	}
	if report.SelectionLimit < 1 || report.SelectionLimit > 500 {
		return failReview(report, errors.New("first-stage review selection limit must be between 1 and 500"))
	}
	if report.ThresholdPct <= 0 || report.ThresholdPct > 100 {
		return failReview(report, errors.New("first-stage review threshold must be between 0 and 100"))
	}
	poolCodes := make(map[string]bool, len(pool))
	for _, stock := range pool {
		if !config.ValidCode(stock.Code) || poolCodes[stock.Code] {
			return failReview(report, errors.New("first-stage review pool contains an invalid or duplicate code"))
		}
		poolCodes[stock.Code] = true
	}
	byCode := make(map[string]market.History, len(histories))
	for _, history := range histories {
		if !poolCodes[history.Code] {
			continue
		}
		if _, exists := byCode[history.Code]; exists {
			return failReview(report, errors.New("first-stage review histories contain a duplicate code"))
		}
		byCode[history.Code] = history
	}

	report.PoolSize = len(pool)
	prices := make(map[string]reviewPrices, len(pool))
	for _, stock := range pool {
		history, ok := byCode[stock.Code]
		if !ok {
			report.PoolUnavailable++
			continue
		}
		value := reviewPrice(history, opts)
		prices[stock.Code] = value
		if value.status != "" {
			report.PoolUnavailable++
			continue
		}
		report.PoolEvaluated++
		if pct(value.signalClose, value.close) >= report.ThresholdPct {
			report.PoolHits++
		}
		if pct(value.signalClose, value.high) >= report.ThresholdPct {
			report.PoolTouchHits++
		}
	}

	limit := min(report.SelectionLimit, len(previous.Candidates))
	report.Selected = limit
	report.Candidates = make([]ReviewCandidate, 0, limit)
	for _, candidate := range previous.Candidates[:limit] {
		item := ReviewCandidate{Rank: candidate.Rank, Code: candidate.Code, Name: candidate.Name}
		value, ok := prices[candidate.Code]
		if ok && value.status == "" {
			item.SignalClose = value.signalClose
			item.Close = value.close
			item.High = value.high
			item.CloseReturnPct = pct(value.signalClose, value.close)
			item.HighReturnPct = pct(value.signalClose, value.high)
			item.Correct = item.CloseReturnPct >= report.ThresholdPct
			item.Touch = item.HighReturnPct >= report.ThresholdPct
			item.Status = "miss"
			if item.Correct {
				item.Status = "hit"
				report.Hits++
			} else {
				report.Misses++
			}
			if item.Touch {
				report.TouchHits++
			}
			report.Evaluated++
		} else {
			item.Status = "missing_history"
			if ok && value.status != "" {
				item.Status = value.status
			}
			report.Unavailable++
		}
		report.Candidates = append(report.Candidates, item)
	}
	sort.SliceStable(report.Candidates, func(i, j int) bool { return report.Candidates[i].Rank < report.Candidates[j].Rank })

	if report.Evaluated > 0 {
		report.SelectionHitRatePct = float64(report.Hits) / float64(report.Evaluated) * 100
		report.TouchHitRatePct = float64(report.TouchHits) / float64(report.Evaluated) * 100
	}
	if report.PoolHits > 0 {
		report.RecallAvailable = true
		report.RecallPct = float64(report.Hits) / float64(report.PoolHits) * 100
	}
	if report.PoolTouchHits > 0 {
		report.TouchRecallAvailable = true
		report.TouchRecallPct = float64(report.TouchHits) / float64(report.PoolTouchHits) * 100
	}
	if report.Evaluated == 0 {
		report.Status = ReviewStatusUnavailable
		report.Reason = "no selected stock has both signal and evaluation bars"
	}
	return report, nil
}

func reviewBaseReport(opts ReviewOptions) ReviewReport {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	limit := opts.SelectedLimit
	if limit == 0 {
		limit = PushLimit
	}
	threshold := opts.ThresholdPct
	if threshold == 0 {
		threshold = TouchThresholdPct
	}
	return ReviewReport{
		Version:        ReviewSchemaVersion,
		Status:         ReviewStatusOK,
		SignalDate:     opts.SignalDate,
		EvaluationDate: opts.EvaluationDate,
		GeneratedAt:    now.In(market.Shanghai),
		Criterion:      fmt.Sprintf("evaluation close return >= %.2f%%", threshold),
		ThresholdPct:   threshold,
		SelectionLimit: limit,
		Candidates:     []ReviewCandidate{},
	}
}

func failReview(report ReviewReport, err error) (ReviewReport, error) {
	report.Status = ReviewStatusUnavailable
	report.Reason = err.Error()
	report.Candidates = []ReviewCandidate{}
	return report, err
}

func reviewPrice(history market.History, opts ReviewOptions) reviewPrices {
	value := reviewPrices{}
	if history.Adjust != opts.Adjust {
		value.status = "adjustment_mismatch"
		return value
	}
	signal, ok := barOn(history.Bars, opts.SignalDate)
	if !ok || signal.Close <= 0 {
		value.status = "missing_signal"
		return value
	}
	evaluation, ok := barOn(history.Bars, opts.EvaluationDate)
	if !ok || evaluation.Close <= 0 {
		value.status = "missing_evaluation"
		return value
	}
	value.signalClose = signal.Close
	value.close = evaluation.Close
	value.high = evaluation.High
	return value
}

func barOn(bars []market.Bar, day string) (market.Bar, bool) {
	index := sort.Search(len(bars), func(i int) bool { return bars[i].Date >= day })
	if index >= len(bars) || bars[index].Date != day {
		return market.Bar{}, false
	}
	return bars[index], true
}

func pct(base, value float64) float64 {
	if base <= 0 {
		return 0
	}
	return (value/base - 1) * 100
}
