package fourlightstudy

import (
	"context"
	"errors"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const PriceEpisodePolicy = "four_light_price_episode_v1"

// PriceEpisodeContext describes the uninterrupted observed price-three-red
// segment containing AsOf. All prior fields exclude AsOf. A censored start
// never becomes known merely because more red observations arrive. Observing
// all four lights red can prove a later repeat even when its original
// transition was unknown and cannot enter PriorConfirmedTriggers.
type PriceEpisodeContext struct {
	Version                  int    `json:"schema_version"`
	Policy                   string `json:"policy"`
	AsOf                     string `json:"as_of"`
	StartDate                string `json:"start_date,omitempty"`
	ObservedStartDate        string `json:"observed_start_date"`
	StartKnown               bool   `json:"start_known"`
	CensorReason             string `json:"censor_reason,omitempty"`
	PriorAllRedObserved      bool   `json:"prior_all_red_observed"`
	PriorConfirmedTriggers   int    `json:"prior_confirmed_triggers"`
	ExactPriorTriggers       *int   `json:"exact_prior_triggers"`
	ExactPriorTriggersReason string `json:"exact_prior_triggers_reason,omitempty"`
	LastObservedTriggerDate  string `json:"last_observed_trigger_date,omitempty"`
	TriggerGapSessions       *int   `json:"trigger_gap_sessions"`
	TriggerGapReason         string `json:"trigger_gap_reason,omitempty"`
	Group                    string `json:"group"`
	GroupReason              string `json:"group_reason,omitempty"`
}

// AnalyzePriceEpisodes computes each historical light from its complete
// available prefix, without the trigger-age feature's 60-session run cap.
// calendarDays is the frozen trading calendar. Only calendar/bar prefixes
// through the inclusive boundary are validated or used; future prices cannot
// change prior results. Missing sessions and price-unknown states break
// confirmed continuity. There is no future-wave input or selection threshold.
func AnalyzePriceEpisodes(ctx context.Context, bars []market.Bar, calendarDays []string, through string) (map[string]PriceEpisodeContext, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !snapshotDateValid(through) {
		return nil, errors.New("price episode requires a valid through date")
	}
	end := sort.SearchStrings(calendarDays, through)
	if end >= len(calendarDays) || calendarDays[end] != through {
		return nil, errors.New("price episode through date must be a frozen trading session")
	}
	days := calendarDays[:end+1]
	validDays := make(map[string]bool, len(days))
	for i, day := range days {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !snapshotDateValid(day) || (i > 0 && day <= days[i-1]) {
			return nil, errors.New("price episode calendar must have strictly increasing valid dates")
		}
		validDays[day] = true
	}
	barEnd := sort.Search(len(bars), func(i int) bool { return bars[i].Date > through })
	bars = bars[:barEnd]
	for i, bar := range bars {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !snapshotDateValid(bar.Date) || (i > 0 && bar.Date <= bars[i-1].Date) || (bar.Date >= days[0] && !validDays[bar.Date]) {
			return nil, errors.New("price episode bars must have strictly increasing valid trading dates")
		}
	}
	rows := make(map[string]PriceEpisodeContext)
	var tracker priceEpisodeTracker
	index := 0
	for di, day := range days {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for index < len(bars) && bars[index].Date < day {
			index++
		}
		if index >= len(bars) || bars[index].Date != day {
			tracker.advance(day, di, nil)
			continue
		}
		evidence := strategy.AnalyzeFourLight(bars[:index+1])
		state := strategy.FourLightState{Trend: evidence.Trend, Energy: evidence.Energy, MidTerm: evidence.MidTerm, ShortTerm: evidence.ShortTerm}
		if value := tracker.advance(day, di, &state); value != nil {
			rows[day] = *value
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

type priceEpisodeTracker struct {
	previous       *strategy.FourLightState
	seenKnownPrice bool
	boundaryReason string
	segment        priceEpisodeSegment
}

type priceEpisodeSegment struct {
	observedStart    string
	startKnown       bool
	censorReason     string
	allRedObserved   bool
	confirmed        int
	countExact       bool
	countReason      string
	lastTrigger      string
	lastTriggerIndex int
	lastTriggerKnown bool
}

// advance consumes one frozen calendar session. A nil state represents a
// missing bar. State and output pointers are detached from callers and future
// advances so reports cannot be revised after emission.
func (t *priceEpisodeTracker) advance(date string, index int, current *strategy.FourLightState) *PriceEpisodeContext {
	if current == nil {
		t.segment, t.previous = priceEpisodeSegment{}, nil
		t.boundaryReason = "session_missing"
		if !t.seenKnownPrice {
			t.boundaryReason = "left_censored"
		}
		return nil
	}
	previous := t.previous
	copyCurrent := *current
	t.previous = &copyCurrent
	price := triggerPriceColor(*current)
	if price != strategy.FourLightRed {
		t.segment = priceEpisodeSegment{}
		if price == strategy.FourLightGreen {
			t.seenKnownPrice, t.boundaryReason = true, ""
		} else {
			t.boundaryReason = "price_light_unknown"
			if !t.seenKnownPrice {
				t.boundaryReason = "left_censored"
			}
		}
		return nil
	}
	t.seenKnownPrice = true
	if t.segment.observedStart == "" {
		knownStart := previous != nil && triggerPriceColor(*previous) == strategy.FourLightGreen
		reason := t.boundaryReason
		if !knownStart && reason == "" {
			reason = "left_censored"
		}
		t.segment = priceEpisodeSegment{observedStart: date, startKnown: knownStart, countExact: knownStart}
		if !knownStart {
			t.segment.censorReason, t.segment.countReason = reason, reason
		}
	}
	s := &t.segment
	currentKnown := triggerStateKnown(*current)
	previousKnown := previous != nil && triggerStateKnown(*previous)
	allRed := triggerAllRed(*current)
	confirmed := currentKnown && allRed && previousKnown && !triggerAllRed(*previous)
	value := PriceEpisodeContext{Version: 1, Policy: PriceEpisodePolicy, AsOf: date,
		ObservedStartDate: s.observedStart, StartKnown: s.startKnown, CensorReason: s.censorReason,
		PriorAllRedObserved: s.allRedObserved, PriorConfirmedTriggers: s.confirmed,
		LastObservedTriggerDate: s.lastTrigger, ExactPriorTriggersReason: s.countReason,
		Group: "not_applicable", TriggerGapReason: "not_confirmed_trigger"}
	if s.startKnown {
		value.StartDate = s.observedStart
	}
	if s.countExact {
		prior := s.confirmed
		value.ExactPriorTriggers = &prior
	}
	if confirmed {
		switch {
		case s.allRedObserved:
			value.Group = "repeat"
		case s.startKnown && s.countExact:
			value.Group = "first"
		default:
			value.Group, value.GroupReason = "unknown", s.countReason
		}
		switch {
		case s.lastTrigger == "":
			value.TriggerGapReason = "no_prior_confirmed_trigger"
		case !s.lastTriggerKnown:
			value.TriggerGapReason = "intervening_trigger_unknown"
		default:
			gap := index - s.lastTriggerIndex
			value.TriggerGapSessions, value.TriggerGapReason = &gap, ""
		}
	} else {
		switch {
		case !currentKnown:
			value.GroupReason = "current_energy_unknown"
		case !allRed:
			value.GroupReason = "current_not_all_red"
		case !previousKnown:
			value.GroupReason = "previous_state_unknown"
		default:
			value.GroupReason = "continuing_all_red"
		}
	}
	// Mutations below are deliberately after the detached prior-only output.
	// Any unknown energy or unconfirmed red transition irreversibly removes
	// cumulative precision for this segment, but a later confirmed trigger can
	// recover the nearest-trigger date without repairing historical totals.
	if !currentKnown || (allRed && !previousKnown) {
		if s.countExact {
			s.countReason = "energy_history_unknown"
			if currentKnown {
				s.countReason = "trigger_transition_unknown"
			}
		}
		s.countExact, s.lastTriggerKnown = false, false
	}
	if allRed {
		s.allRedObserved = true
	}
	if confirmed {
		s.confirmed++
		s.lastTrigger, s.lastTriggerIndex, s.lastTriggerKnown = date, index, true
	}
	return &value
}
