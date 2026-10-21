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
	ReviewSchemaVersion = 3

	ReviewStatusOK          = "ok"
	ReviewStatusEmpty       = "empty"
	ReviewStatusUnavailable = "unavailable"

	PushLimit = 30
)

type ReviewOptions struct {
	SignalDate     string
	EvaluationDate string
	HorizonDates   []string
	Adjust         string
	SelectedLimit  int
	ThresholdPct   float64
	Now            time.Time
}

type ReviewCandidate struct {
	Rank                int     `json:"rank"`
	Code                string  `json:"code"`
	Name                string  `json:"name"`
	SignalClose         float64 `json:"signal_close"`
	WindowHigh          float64 `json:"window_high"`
	WindowHighDate      string  `json:"window_high_date"`
	HorizonClose        float64 `json:"horizon_close"`
	WindowHighReturnPct float64 `json:"window_high_return_pct"`
	HorizonCloseReturn  float64 `json:"horizon_close_return_pct"`
	Correct             bool    `json:"correct"`
	Status              string  `json:"status"`
}

type ReviewReport struct {
	Version              int               `json:"schema_version"`
	Status               string            `json:"status"`
	SignalDate           string            `json:"signal_date"`
	WindowStart          string            `json:"window_start"`
	EvaluationDate       string            `json:"evaluation_date"`
	HorizonDays          int               `json:"horizon_days"`
	GeneratedAt          time.Time         `json:"generated_at"`
	Criterion            string            `json:"criterion"`
	ThresholdPct         float64           `json:"threshold_pct"`
	SelectionLimit       int               `json:"selection_limit"`
	Selected             int               `json:"selected"`
	Evaluated            int               `json:"evaluated"`
	Unavailable          int               `json:"unavailable"`
	Hits                 int               `json:"hits"`
	Misses               int               `json:"misses"`
	CandidateCount       int               `json:"candidate_count"`
	CandidateEvaluated   int               `json:"candidate_evaluated"`
	CandidateUnavailable int               `json:"candidate_unavailable"`
	CandidateHits        int               `json:"candidate_hits"`
	PoolSize             int               `json:"pool_size"`
	PoolEvaluated        int               `json:"pool_evaluated"`
	PoolUnavailable      int               `json:"pool_unavailable"`
	PoolHits             int               `json:"pool_hits"`
	TopHitRatePct        float64           `json:"top_hit_rate_pct"`
	CandidateRecallPct   float64           `json:"candidate_recall_pct"`
	TopCoveragePct       float64           `json:"top_coverage_pct"`
	RecallAvailable      bool              `json:"recall_available"`
	Candidates           []ReviewCandidate `json:"candidates"`
	Reason               string            `json:"reason,omitempty"`
}

type reviewPrices struct {
	signalClose    float64
	windowHigh     float64
	windowHighDate string
	horizonClose   float64
	status         string
}

func EvaluateHorizon(previous Report, histories []market.History, pool []config.Stock, opts ReviewOptions) (ReviewReport, error) {
	report := reviewBaseReport(opts)
	if len(opts.HorizonDates) != HorizonDays {
		return failReview(report, errors.New("first-stage review requires exactly three horizon dates"))
	}
	if err := validateHorizonDates(opts.SignalDate, opts.EvaluationDate, opts.HorizonDates); err != nil {
		return failReview(report, err)
	}
	if previous.AsOf != opts.SignalDate || previous.Target != opts.HorizonDates[0] {
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
		if pct(value.signalClose, value.windowHigh) >= report.ThresholdPct {
			report.PoolHits++
		}
	}

	limit := min(report.SelectionLimit, len(previous.Candidates))
	report.Selected = limit
	report.CandidateCount = len(previous.Candidates)
	report.Candidates = make([]ReviewCandidate, 0, len(previous.Candidates))
	for index, candidate := range previous.Candidates {
		item := ReviewCandidate{Rank: candidate.Rank, Code: candidate.Code, Name: candidate.Name}
		value, ok := prices[candidate.Code]
		if ok && value.status == "" {
			item.SignalClose = value.signalClose
			item.WindowHigh = value.windowHigh
			item.WindowHighDate = value.windowHighDate
			item.HorizonClose = value.horizonClose
			item.WindowHighReturnPct = pct(value.signalClose, value.windowHigh)
			item.HorizonCloseReturn = pct(value.signalClose, value.horizonClose)
			item.Correct = item.WindowHighReturnPct >= report.ThresholdPct
			item.Status = "miss"
			report.CandidateEvaluated++
			if item.Correct {
				item.Status = "hit"
				report.CandidateHits++
			}
			if index < limit {
				if item.Correct {
					report.Hits++
				} else {
					report.Misses++
				}
				report.Evaluated++
			}
		} else {
			item.Status = "missing_history"
			if ok && value.status != "" {
				item.Status = value.status
			}
			report.CandidateUnavailable++
			if index < limit {
				report.Unavailable++
			}
		}
		report.Candidates = append(report.Candidates, item)
	}
	sort.SliceStable(report.Candidates, func(i, j int) bool { return report.Candidates[i].Rank < report.Candidates[j].Rank })

	if report.Evaluated > 0 {
		report.TopHitRatePct = float64(report.Hits) / float64(report.Evaluated) * 100
	}
	if report.PoolHits > 0 {
		report.RecallAvailable = true
		report.CandidateRecallPct = float64(report.CandidateHits) / float64(report.PoolHits) * 100
		report.TopCoveragePct = float64(report.Hits) / float64(report.PoolHits) * 100
	}
	if report.Evaluated == 0 {
		report.Status = ReviewStatusUnavailable
		report.Reason = "no selected stock has signal and three-day horizon bars"
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
	windowStart := ""
	evaluationDate := opts.EvaluationDate
	if len(opts.HorizonDates) > 0 {
		windowStart = opts.HorizonDates[0]
		evaluationDate = opts.HorizonDates[len(opts.HorizonDates)-1]
	}
	return ReviewReport{
		Version:        ReviewSchemaVersion,
		Status:         ReviewStatusOK,
		SignalDate:     opts.SignalDate,
		WindowStart:    windowStart,
		EvaluationDate: evaluationDate,
		HorizonDays:    len(opts.HorizonDates),
		GeneratedAt:    now.In(market.Shanghai),
		Criterion:      fmt.Sprintf("three-day maximum high return >= %.2f%%", threshold),
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
	horizonDates := make(map[string]bool, len(opts.HorizonDates))
	for _, day := range opts.HorizonDates {
		horizonDates[day] = true
	}
	value.signalClose = signal.Close
	for _, bar := range history.Bars {
		if !horizonDates[bar.Date] || bar.High <= 0 || bar.Close <= 0 {
			continue
		}
		if bar.High > value.windowHigh {
			value.windowHigh = bar.High
			value.windowHighDate = bar.Date
		}
		value.horizonClose = bar.Close
	}
	if value.windowHigh <= 0 || value.horizonClose <= 0 {
		value.status = "missing_horizon"
	}
	return value
}

func barOn(bars []market.Bar, day string) (market.Bar, bool) {
	index := sort.Search(len(bars), func(i int) bool { return bars[i].Date >= day })
	if index >= len(bars) || bars[index].Date != day {
		return market.Bar{}, false
	}
	return bars[index], true
}

func validateHorizonDates(signalDate, evaluationDate string, dates []string) error {
	signal, err := time.Parse("2006-01-02", signalDate)
	if err != nil || signal.Format("2006-01-02") != signalDate {
		return errors.New("invalid first-stage signal date")
	}
	previous := signal
	for _, day := range dates {
		value, err := time.Parse("2006-01-02", day)
		if err != nil || value.Format("2006-01-02") != day || !value.After(previous) {
			return errors.New("first-stage horizon dates must be valid and strictly increasing")
		}
		previous = value
	}
	if dates[len(dates)-1] != evaluationDate {
		return errors.New("first-stage evaluation date must match the horizon end")
	}
	return nil
}

func pct(base, value float64) float64 {
	if base <= 0 {
		return 0
	}
	return (value/base - 1) * 100
}
