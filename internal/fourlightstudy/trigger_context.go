package fourlightstudy

import (
	"math"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const (
	TriggerContextPolicy       = "four_light_trigger_timing_v1"
	TriggerRedLookbackSessions = 60
)

// TriggerRedRun ends at the session immediately before the signal. ExactDays
// is available only after a known green terminates the run. ObservedDays is a
// confirmed lower bound when missing history, unknown lights or the fixed
// lookback limit prevents finding that termination. Zero is not a missing value.
type TriggerRedRun struct {
	ExactDays    *int   `json:"exact_days"`
	ObservedDays int    `json:"observed_days"`
	Censored     bool   `json:"censored"`
	Reason       string `json:"reason,omitempty"`
}

type TriggerPriorRedRuns struct {
	Trend       TriggerRedRun `json:"trend"`
	Energy      TriggerRedRun `json:"energy"`
	MidTerm     TriggerRedRun `json:"mid_term"`
	ShortTerm   TriggerRedRun `json:"short_term"`
	PriceAllRed TriggerRedRun `json:"price_all_red"`
}

// TriggerContext is descriptive signal-time evidence, never an entry gate.
// TriggerStages may overlap: energy_preexisting and multi_light_trigger can
// both be true. PriceAgeGroup partitions first-all-red observations only.
// PriorPathGroup is a sign comparison of overlapping prior returns, not proof
// of a reversal or a separate, nonoverlapping two-leg price pattern.
type TriggerContext struct {
	Version            int                 `json:"schema_version"`
	Policy             string              `json:"policy"`
	AsOf               string              `json:"as_of"`
	PriorThrough       string              `json:"prior_through,omitempty"`
	LookbackSessions   int                 `json:"lookback_sessions"`
	Lifecycle          string              `json:"lifecycle"`
	TriggerStages      []string            `json:"trigger_stages"`
	TriggerReason      string              `json:"trigger_reason,omitempty"`
	PriorRedRuns       TriggerPriorRedRuns `json:"prior_red_runs"`
	PriceAgeGroup      string              `json:"price_age_group"`
	PriceAgeReason     string              `json:"price_age_reason,omitempty"`
	Prior5Pct          *float64            `json:"prior5_pct"`
	Prior20Pct         *float64            `json:"prior20_pct"`
	PriorPathGroup     string              `json:"prior_path_group"`
	PriorPathReason    string              `json:"prior_path_reason,omitempty"`
	SignalChangePct    *float64            `json:"signal_change_pct"`
	SignalChangeReason string              `json:"signal_change_reason,omitempty"`
}

// AnalyzeTriggerContext uses the frozen four-light formula on historical
// prefixes. calendarDays must be the frozen trading calendar, and evidence
// must describe the signal's completed daily bar. Only rows through AsOf are
// used, even if a caller supplies future bars. No price, volume or wave-outcome
// threshold is added to selection by this function.
func AnalyzeTriggerContext(bars []market.Bar, calendarDays []string, evidence strategy.FourLightEvidence) TriggerContext {
	c := TriggerContext{Version: 1, Policy: TriggerContextPolicy, AsOf: evidence.AsOf,
		LookbackSessions: TriggerRedLookbackSessions, Lifecycle: "unknown", TriggerStages: []string{},
		PriceAgeGroup: "unknown", PriorPathGroup: "unknown"}
	fail := func(reason string) TriggerContext {
		c.TriggerReason, c.PriceAgeReason = reason, reason
		c.PriorPathReason, c.SignalChangeReason = reason, reason
		c.PriorRedRuns = unknownTriggerRuns(reason)
		return c
	}
	if !snapshotDateValid(evidence.AsOf) {
		return fail("invalid_signal_date")
	}
	end := sort.SearchStrings(calendarDays, evidence.AsOf)
	if end >= len(calendarDays) || calendarDays[end] != evidence.AsOf {
		return fail("signal_not_in_calendar")
	}
	index := sort.Search(len(bars), func(i int) bool { return bars[i].Date >= evidence.AsOf })
	if index >= len(bars) || bars[index].Date != evidence.AsOf {
		return fail("signal_session_missing")
	}
	bars, calendarDays = bars[:index+1], calendarDays[:end+1]
	for i, date := range calendarDays {
		if !snapshotDateValid(date) || (i > 0 && date <= calendarDays[i-1]) {
			return fail("invalid_calendar_order")
		}
	}
	for i, bar := range bars {
		if !snapshotDateValid(bar.Date) || (i > 0 && bar.Date <= bars[i-1].Date) {
			return fail("invalid_bar_order")
		}
	}
	if end > 0 {
		c.PriorThrough = calendarDays[end-1]
	}
	var previous *strategy.FourLightState
	c.PriorRedRuns, previous = triggerPriorRuns(bars, calendarDays)
	c.Prior5Pct, c.Prior20Pct, c.PriorPathReason = triggerPriorReturns(bars, calendarDays)
	if c.PriorPathReason == "" {
		c.PriorPathGroup = triggerPathGroup(*c.Prior20Pct, *c.Prior5Pct)
	}
	c.SignalChangePct, c.SignalChangeReason = triggerSignalChange(bars, calendarDays)
	current := strategy.FourLightState{Trend: evidence.Trend, Energy: evidence.Energy, MidTerm: evidence.MidTerm, ShortTerm: evidence.ShortTerm}
	if !triggerStateKnown(current) || evidence.UnknownLights > 0 {
		c.TriggerReason, c.PriceAgeReason = "current_light_unknown", "current_light_unknown"
		return c
	}
	if !triggerAllRed(current) {
		c.Lifecycle, c.PriceAgeGroup = "non_all_red", "not_applicable"
		return c
	}
	if previous == nil {
		c.TriggerReason, c.PriceAgeReason = c.PriorRedRuns.PriceAllRed.Reason, c.PriorRedRuns.PriceAllRed.Reason
		return c
	}
	if !triggerStateKnown(*previous) {
		c.TriggerReason, c.PriceAgeReason = "previous_light_unknown", "previous_light_unknown"
		return c
	}
	if triggerAllRed(*previous) {
		c.Lifecycle, c.PriceAgeGroup = "continuing_all_red", "not_applicable"
		return c
	}
	c.Lifecycle = "first_all_red"
	if previous.Trend == strategy.FourLightRed && previous.MidTerm == strategy.FourLightRed && previous.ShortTerm == strategy.FourLightRed {
		c.TriggerStages = append(c.TriggerStages, "price_preexisting")
	}
	if previous.Energy == strategy.FourLightRed {
		c.TriggerStages = append(c.TriggerStages, "energy_preexisting")
	}
	flips := 0
	for _, color := range []string{previous.Trend, previous.Energy, previous.MidTerm, previous.ShortTerm} {
		if color == strategy.FourLightGreen {
			flips++
		}
	}
	if flips >= 2 {
		c.TriggerStages = append(c.TriggerStages, "multi_light_trigger")
	}
	c.PriceAgeGroup, c.PriceAgeReason = triggerPriceAge(c.PriorRedRuns.PriceAllRed)
	return c
}

func unknownTriggerRuns(reason string) TriggerPriorRedRuns {
	run := TriggerRedRun{Censored: true, Reason: reason}
	return TriggerPriorRedRuns{Trend: run, Energy: run, MidTerm: run, ShortTerm: run, PriceAllRed: run}
}

func triggerPriorRuns(bars []market.Bar, days []string) (TriggerPriorRedRuns, *strategy.FourLightState) {
	runs := unknownTriggerRuns("lookback_limit")
	fields := []*TriggerRedRun{&runs.Trend, &runs.Energy, &runs.MidTerm, &runs.ShortTerm, &runs.PriceAllRed}
	closed := [5]bool{}
	remaining := len(fields)
	var previous *strategy.FourLightState
	for offset := 1; offset <= TriggerRedLookbackSessions && remaining > 0; offset++ {
		dayIndex := len(days) - 1 - offset
		reason, index := "", 0
		if dayIndex < 0 {
			reason = "calendar_history_missing"
		} else {
			index = sort.Search(len(bars), func(i int) bool { return bars[i].Date >= days[dayIndex] })
			if index >= len(bars) || bars[index].Date != days[dayIndex] {
				reason = "prior_session_missing"
				if len(bars) == 0 || days[dayIndex] < bars[0].Date {
					reason = "prior_history_missing"
				}
			}
		}
		if reason != "" {
			for i, field := range fields {
				if !closed[i] {
					field.Reason = reason
				}
			}
			break
		}
		e := strategy.AnalyzeFourLight(bars[:index+1])
		state := strategy.FourLightState{Trend: e.Trend, Energy: e.Energy, MidTerm: e.MidTerm, ShortTerm: e.ShortTerm}
		if offset == 1 {
			previous = &state
		}
		colors := []string{state.Trend, state.Energy, state.MidTerm, state.ShortTerm, triggerPriceColor(state)}
		for i, field := range fields {
			if closed[i] {
				continue
			}
			switch colors[i] {
			case strategy.FourLightRed:
				field.ObservedDays++
			case strategy.FourLightGreen:
				value := field.ObservedDays
				field.ExactDays, field.Censored, field.Reason = &value, false, ""
				closed[i], remaining = true, remaining-1
			default:
				field.Reason = "prior_light_unknown"
				closed[i], remaining = true, remaining-1
			}
		}
	}
	return runs, previous
}

func triggerPriceColor(s strategy.FourLightState) string {
	// One known green is sufficient to prove the three-price-light run ended,
	// even when another price light is unavailable on that terminating session.
	for _, color := range []string{s.Trend, s.MidTerm, s.ShortTerm} {
		if color == strategy.FourLightGreen {
			return strategy.FourLightGreen
		}
	}
	if s.Trend == strategy.FourLightRed && s.MidTerm == strategy.FourLightRed && s.ShortTerm == strategy.FourLightRed {
		return strategy.FourLightRed
	}
	return strategy.FourLightUnknown
}

func triggerStateKnown(s strategy.FourLightState) bool {
	for _, color := range []string{s.Trend, s.Energy, s.MidTerm, s.ShortTerm} {
		if color != strategy.FourLightRed && color != strategy.FourLightGreen {
			return false
		}
	}
	return true
}

func triggerAllRed(s strategy.FourLightState) bool {
	return s.Energy == strategy.FourLightRed && triggerPriceColor(s) == strategy.FourLightRed
}

func triggerPriceAge(run TriggerRedRun) (string, string) {
	if run.ObservedDays >= 6 {
		return "6+", ""
	}
	if run.ExactDays == nil {
		return "unknown", run.Reason
	}
	switch *run.ExactDays {
	case 0:
		return "0", ""
	case 1, 2:
		return "1-2", ""
	default:
		return "3-5", ""
	}
}

// Both returns share exactly 22 sessions ending at the signal. Their endpoint
// is the previous close; the signal bar establishes coverage but does not
// enter either return or its price validation.
func triggerPriorReturns(bars []market.Bar, days []string) (*float64, *float64, string) {
	if len(bars) < 22 || len(days) < 22 {
		return nil, nil, "prior_path_history_missing"
	}
	window, dates := bars[len(bars)-22:], days[len(days)-22:]
	for i, bar := range window {
		if bar.Date != dates[i] {
			return nil, nil, "prior_path_session_missing"
		}
		if i < 21 && !triggerValidPrice(bar.Close) {
			return nil, nil, "prior_path_price_invalid"
		}
	}
	five, twenty := (window[20].Close/window[15].Close-1)*100, (window[20].Close/window[0].Close-1)*100
	if !triggerFinite(five) || !triggerFinite(twenty) {
		return nil, nil, "prior_path_price_invalid"
	}
	return &five, &twenty, ""
}

func triggerPathGroup(prior20, prior5 float64) string {
	switch {
	case prior20 > 0 && prior5 > 0:
		return "both_positive"
	case prior20 > 0:
		return "positive_then_nonpositive"
	case prior5 > 0:
		return "nonpositive_then_positive"
	default:
		return "both_nonpositive"
	}
}

func triggerSignalChange(bars []market.Bar, days []string) (*float64, string) {
	if len(bars) < 2 || len(days) < 2 {
		return nil, "signal_change_history_missing"
	}
	previous, current := bars[len(bars)-2], bars[len(bars)-1]
	if previous.Date != days[len(days)-2] {
		return nil, "signal_change_session_missing"
	}
	if !triggerValidPrice(previous.Close) || !triggerValidPrice(current.Close) {
		return nil, "signal_change_price_invalid"
	}
	value := (current.Close/previous.Close - 1) * 100
	if !triggerFinite(value) {
		return nil, "signal_change_price_invalid"
	}
	return &value, ""
}

func triggerValidPrice(value float64) bool { return value > 0 && triggerFinite(value) }
func triggerFinite(value float64) bool     { return !math.IsNaN(value) && !math.IsInf(value, 0) }
