package strategy

import (
	"context"
	"errors"
	"math"
	"sort"
	"stock-watcher/internal/calendar"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
)

// AnalyzeConditional uses one preregistered entry rule. It never chooses stocks
// based on whether tomorrow's limit or rebound eventually triggers.
func AnalyzeConditional(ctx context.Context, c config.Config, histories, raw []market.History, minutes []market.MinuteHistory, asof string, interval *BacktestRange, rule string) (DevelopmentResult, error) {
	result := DevelopmentResult{Trades: []ExperimentTrade{}, HoldoutThrough: asof, Selection: &SignalSelection{Rule: rule}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !validSignalRule(rule) {
		return result, errors.New("unknown conditional signal rule")
	}
	if err := interval.Validate(c, asof, 1); err != nil {
		return result, err
	}
	data, err := datasets(c, histories, raw, asof)
	if err != nil {
		return result, err
	}
	if len(data) == 0 {
		return result, errors.New("no aligned daily data for conditional entry")
	}
	if len(minutes) == 0 {
		return result, errors.New("conditional entry requires saved minute paths")
	}
	byCode := map[string]map[string][]market.MinuteBar{}
	minuteByCode := map[string]market.MinuteHistory{}
	days := map[string]bool{}
	for _, h := range minutes {
		if err := h.Validate(); err != nil {
			return result, err
		}
		if _, ok := byCode[h.Code]; ok {
			return result, errors.New("duplicate minute history")
		}
		minuteByCode[h.Code] = h
		byCode[h.Code] = map[string][]market.MinuteBar{}
		for _, b := range h.Bars {
			date := b.End.In(market.Shanghai).Format("2006-01-02")
			if date <= asof {
				byCode[h.Code][date] = append(byCode[h.Code][date], b)
			}
		}
		for date, bars := range byCode[h.Code] {
			if len(bars) == 48 {
				days[date] = true
			}
		}
	}
	dates := []string{}
	for date := range days {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	if interval == nil && len(dates) <= 3 {
		return result, errors.New("conditional entry requires more than three complete minute sessions")
	}
	if interval == nil {
		result.HoldoutFrom = dates[len(dates)-3]
	} else {
		result.HoldoutThrough = ""
	}
	s := c.Strategy
	s.MaxHoldingDays = 1
	research := Research{Version: 1, Strategy: ConditionalEntryMode, AsOf: asof, Parameters: s, Skipped: map[string]int{}, Limitations: []string{
		"Fixed-rule minute pilot only; reserve the latest three full sessions and purge trades crossing that boundary.",
		"Short minute coverage cannot establish an 80% future success rate; no parameter optimization or independent validation claim.",
		"Earlier minute dates may overlap a previous daily experiment's holdout; once inspected they are not unseen data for that older strategy.",
		"Entry uses next five-minute open after a completed rebound; entry-day close is an outcome, never an entry filter.",
		"Same-candle target/stop ordering remains conservative; mark-to-close reserves exit fees and slippage but is not a T+0 sale.",
	}}
	if rule == ChanSignalRule {
		research.Limitations = append(research.Limitations,
			"Chan rule uses fixed 5m entry, 15m setup and 30m context levels; 15m/30m are derived from saved 5m bars at each signal-day close.",
			"Only completed 5m bars are used; missing minute paths, an incomplete signal session or insufficient fractals/strokes/zones remain unknown and never pass the filter.",
			"Chan structure confirms prior price movement, not future direction; the fixed rule has not been optimized or independently validated.")
	}
	research.Limitations = append(research.Limitations,
		"Relative strength and the opportunity score are computed within the signal-rule-eligible pool; an excluded stock cannot change the eligible pool's cross-sectional mean.",
		"The original full-pool Top N is retained only to attribute which samples the rule removed, not to score or replace eligible candidates.")
	signals := map[string][]conditionalSignal{}
	cal := calendar.New(c)
	for _, d := range data {
		h, r := d.Adjusted, d.Raw
		if special(h.Name) {
			continue
		}
		for i := 59; i < len(h.Bars); i++ {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			date := h.Bars[i].Date
			if !interval.Includes(date) {
				continue
			}
			entryDate, err := cal.Next(date)
			if err != nil {
				continue
			}
			exitDate, err := cal.Next(entryDate)
			if err != nil || (interval == nil && !days[entryDate]) || (result.HoldoutFrom != "" && exitDate >= result.HoldoutFrom) {
				continue
			}
			f, err := extractSignalFeatures(h.Bars[:i+1])
			if err != nil {
				return result, err
			}
			var chanEvidence *ChanEvidence
			if rule == ChanSignalRule {
				evidence, err := chanSignalEvidence(minuteByCode[h.Code], date, r.Bars[i])
				if err != nil {
					return result, err
				}
				chanEvidence = &evidence
			}
			end := min(i+3, len(r.Bars))
			future := r.Bars[i+1 : end]
			corporate := false
			factor := h.Bars[i].Close / r.Bars[i].Close
			for j := i + 1; j < end; j++ {
				if math.Abs(h.Bars[j].Close/r.Bars[j].Close/factor-1) > .002 {
					corporate = true
				}
			}
			signals[date] = append(signals[date], conditionalSignal{h.Code, f, r.Bars[i], future, corporate, chanEvidence})
		}
	}
	signalDates := []string{}
	for date := range signals {
		signalDates = append(signalDates, date)
	}
	sort.Strings(signalDates)
	evaluation := Evaluation{Method: "fixed_rules_minute_pilot"}
	if interval != nil {
		evaluation.Method = "explicit_range_minute_backtest"
		research.Limitations[0] = "Explicit signal-date interval; no independent holdout declared. Missing minute entry paths remain unknown, never safe skips."
	}
	for _, date := range signalDates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		group := signals[date]
		if evaluation.From == "" {
			evaluation.From = date
		}
		evaluation.Through = date
		evaluation.SignalDates++
		selected, filtered, eligible := selectConditionalSignals(group, rule, c.Selection.TopN)
		result.Selection.Candidates += len(group)
		result.Selection.Eligible += eligible
		result.Selection.FilteredOriginalTop += len(filtered)
		for _, item := range filtered {
			sample := item.signal
			zone := ConditionalZone(sample.bar.Close)
			status := "signal_filtered"
			if rule == ChanSignalRule {
				status = "chan_filtered"
				if sample.chanEvidence == nil || sample.chanEvidence.Status == chanUnknown {
					status = "chan_unknown"
				}
			}
			result.Trades = append(result.Trades, ExperimentTrade{ID: date + "/" + sample.code, Code: sample.code, Date: date, Rank: item.rank, Features: item.candidate.Features, Outcome: Outcome{Status: status}, EntryZone: &zone, Chan: sample.chanEvidence})
		}
		for _, item := range selected {
			sample := item.signal
			zone := ConditionalZone(sample.bar.Close)
			result.Selection.Selected++
			entryDate, _ := cal.Next(date)
			exitDate, _ := cal.Next(entryDate)
			out, err := SimulateConditional(s, cal, sample.code, sample.bar, sample.future, byCode[sample.code][entryDate], byCode[sample.code][exitDate])
			if err != nil {
				return result, err
			}
			if sample.corporate && out.EntryDate != "" {
				out.Status = "corporate_action"
			}
			result.Trades = append(result.Trades, ExperimentTrade{ID: date + "/" + sample.code, Code: sample.code, Date: date, Rank: item.rank, Selected: true, Features: item.candidate.Features, Outcome: out, EntryZone: &zone, Chan: sample.chanEvidence})
		}
	}
	evaluation.TopRanked = SummarizeExperimentTrades(result.Trades, s)
	research.Development = evaluation
	research.SampleCount = len(result.Trades)
	result.Research = research
	return result, nil
}
