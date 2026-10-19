package strategy

import (
	"context"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"sort"
	"time"
)

// ExperimentTrade retains the exact signal features and simulated execution.
// Selected=false records a rejected original top-N candidate for attribution.
type ExperimentTrade struct {
	ID        string        `json:"id"`
	Code      string        `json:"code"`
	Date      string        `json:"date"`
	Rank      int           `json:"rank"`
	Selected  bool          `json:"selected"`
	Features  Features      `json:"features"`
	Outcome   Outcome       `json:"outcome"`
	EntryZone *EntryZone    `json:"entry_zone,omitempty"`
	Chan      *ChanEvidence `json:"chan,omitempty"`
}
type DevelopmentResult struct {
	Research       Research          `json:"research"`
	Trades         []ExperimentTrade `json:"trades"`
	HoldoutFrom    string            `json:"holdout_from"`
	HoldoutThrough string            `json:"holdout_through"`
	Selection      *SignalSelection  `json:"signal_selection,omitempty"`
}

type SignalSelection struct {
	Rule                string `json:"rule"`
	Candidates          int    `json:"candidates"`
	Eligible            int    `json:"eligible"`
	Selected            int    `json:"selected"`
	FilteredOriginalTop int    `json:"filtered_original_top"`
}
type EntryPolicy struct {
	MaxUpperWick    *float64
	RequireEvidence bool
	NextDayRule     string
	Range           *BacktestRange
}

type developmentOptions struct {
	boundary        string
	maxUpperWick    *float64
	requireEvidence bool
	nextDayRule     string
	rangeFilter     *BacktestRange
	holdout         bool
	holdoutFrom     string
	holdoutThrough  string
	contaminated    bool
	trades          []ExperimentTrade
}

// AnalyzeDevelopment never evaluates the final reserved interval. The policy
// only filters eligible top-N entries; training and baselines stay identical.
func AnalyzeDevelopment(ctx context.Context, c config.Config, histories, raw []market.History, asof string, policy EntryPolicy) (DevelopmentResult, error) {
	result := DevelopmentResult{Trades: []ExperimentTrade{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if policy.MaxUpperWick != nil && !(*policy.MaxUpperWick >= 0 && *policy.MaxUpperWick <= 1) {
		return result, errors.New("upper wick threshold must be between zero and one")
	}
	if !validNextDayRule(policy.NextDayRule) {
		return result, errors.New("unknown next-day entry rule")
	}
	horizon := c.Strategy.MaxHoldingDays
	if policy.NextDayRule != "" {
		horizon = 1
	}
	if err := policy.Range.Validate(c, asof, horizon); err != nil {
		return result, err
	}
	data, err := datasets(c, histories, raw, asof)
	if err != nil {
		return result, err
	}
	cal := calendar.New(c)
	if err := requireCalendarCoverage(cal, data); err != nil {
		return result, err
	}
	seen := map[string]bool{}
	for _, d := range data {
		h, r := d.Adjusted, d.Raw
		if special(h.Name) || len(h.Bars) < 60 || h.Bars[len(h.Bars)-1].Date != asof || r.Bars[len(r.Bars)-1].Volume <= 0 {
			continue
		}
		for i := 59; i+c.Strategy.MaxHoldingDays+1 < len(h.Bars); i++ {
			date := h.Bars[i].Date
			if ok, err := cal.Trading(date); err == nil && ok {
				seen[date] = true
			}
		}
	}
	dates := make([]string, 0, len(seen))
	for date := range seen {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	if policy.Range == nil && len(dates) <= c.Strategy.MinTrainingDays+c.Strategy.HoldoutDays+c.Strategy.MaxHoldingDays+1 {
		return result, errors.New("insufficient signal dates for training and a reserved final interval")
	}
	if policy.Range == nil {
		result.HoldoutFrom = dates[len(dates)-c.Strategy.HoldoutDays]
		result.HoldoutThrough = asof
	}
	// Preserve the snapshot's original reserved boundary before shortening the
	// exit horizon. A shorter horizon must not consume previously held-out dates.
	if policy.NextDayRule != "" {
		c.Strategy.MaxHoldingDays = 1
	}
	options := &developmentOptions{boundary: result.HoldoutFrom, maxUpperWick: policy.MaxUpperWick, requireEvidence: policy.RequireEvidence, nextDayRule: policy.NextDayRule, rangeFilter: policy.Range, trades: []ExperimentTrade{}}
	x, err := analyze(ctx, c, histories, raw, asof, options)
	if err != nil {
		return result, err
	}
	result.Research, result.Trades = x.Research, options.trades
	return result, nil
}

// AnalyzeHoldout consumes the final reserved interval once with the rule frozen
// from the completed development experiment. It never re-trains inside the
// interval: ranking evidence is cut off strictly before holdoutFrom.
func AnalyzeHoldout(ctx context.Context, c config.Config, histories, raw []market.History, asof string, policy EntryPolicy, from, through string, contaminated bool) (DevelopmentResult, error) {
	result := DevelopmentResult{Trades: []ExperimentTrade{}, HoldoutFrom: from, HoldoutThrough: through}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if policy.Range != nil {
		return result, errors.New("holdout cannot reuse an explicit signal range")
	}
	if err := validateHoldoutRange(c, asof, from, through); err != nil {
		return result, err
	}
	if policy.MaxUpperWick != nil && !(*policy.MaxUpperWick >= 0 && *policy.MaxUpperWick <= 1) {
		return result, errors.New("upper wick threshold must be between zero and one")
	}
	if !validNextDayRule(policy.NextDayRule) {
		return result, errors.New("unknown next-day entry rule")
	}
	data, err := datasets(c, histories, raw, asof)
	if err != nil {
		return result, err
	}
	cal := calendar.New(c)
	if err := requireCalendarCoverage(cal, data); err != nil {
		return result, err
	}
	if policy.NextDayRule != "" {
		c.Strategy.MaxHoldingDays = 1
	}
	options := &developmentOptions{
		maxUpperWick: policy.MaxUpperWick, requireEvidence: policy.RequireEvidence,
		nextDayRule: policy.NextDayRule, holdout: true, holdoutFrom: from,
		holdoutThrough: through, contaminated: contaminated, trades: []ExperimentTrade{},
	}
	x, err := analyze(ctx, c, histories, raw, asof, options)
	if err != nil {
		return result, err
	}
	result.Research, result.Trades = x.Research, options.trades
	return result, nil
}

func SummarizeExperimentTrades(trades []ExperimentTrade, parameters config.Strategy) Stats {
	stats := Stats{Rule: "experiment_top_n", Target: parameters.TargetNetPct, Horizon: parameters.MaxHoldingDays}
	for _, t := range trades {
		if t.Selected {
			add(&stats, Sample{Date: t.Date}, t.Outcome)
		}
	}
	return finalize(stats)
}

// SummarizeExperimentTradesWithCohort keeps the legacy per-trade statistics
// intact and attaches the signal-day cohort view used by acceptance gates.
func SummarizeExperimentTradesWithCohort(trades []ExperimentTrade, parameters config.Strategy, topN int) Stats {
	stats := SummarizeExperimentTrades(trades, parameters)
	stats.Cohort = SummarizeExperimentCohorts(trades, parameters, topN)
	return stats
}

// validateHoldoutRange checks the frozen signal window only. The caller already
// built samples with complete exits, so reserve-through may equal as-of without
// pretending the final sessions have future exits.
func validateHoldoutRange(c config.Config, asof, from, through string) error {
	for _, value := range []string{from, through} {
		t, err := time.Parse("2006-01-02", value)
		if err != nil || t.Format("2006-01-02") != value {
			return errors.New("holdout requires YYYY-MM-DD dates")
		}
	}
	if from > through || from < c.Calendar.ValidFrom || through > c.Calendar.ValidThrough || through > asof {
		return errors.New("holdout range is reversed or outside the saved data/calendar")
	}
	return nil
}
