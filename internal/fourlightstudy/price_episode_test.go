package fourlightstudy

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func TestPriceEpisodeCompleteCycleAndRepeatedVolumeTriggers(t *testing.T) {
	bars, dates, _ := triggerFixture(t, 90, 65)
	for i := range bars {
		bars[i].Volume = 100
	}
	bars[67].Volume, bars[68].Volume, bars[70].Volume = 200, 300, 250
	rows, err := AnalyzePriceEpisodes(context.Background(), bars, dates, dates[90])
	if err != nil {
		t.Fatal(err)
	}
	first, continuation, repeat := rows[dates[67]], rows[dates[68]], rows[dates[70]]
	if first.Version != 1 || first.Policy != PriceEpisodePolicy || first.StartDate != dates[65] || first.ObservedStartDate != dates[65] || !first.StartKnown || first.Group != "first" || first.PriorAllRedObserved || first.PriorConfirmedTriggers != 0 || first.ExactPriorTriggers == nil || *first.ExactPriorTriggers != 0 || first.TriggerGapSessions != nil {
		t.Fatalf("first trigger contains current/future or lacks known start: %+v", first)
	}
	if continuation.Group != "not_applicable" || continuation.PriorConfirmedTriggers != 1 || continuation.TriggerGapSessions != nil {
		t.Fatalf("continuing all-red was counted as a trigger: %+v", continuation)
	}
	if repeat.Group != "repeat" || !repeat.PriorAllRedObserved || repeat.PriorConfirmedTriggers != 1 || repeat.ExactPriorTriggers == nil || *repeat.ExactPriorTriggers != 1 || repeat.LastObservedTriggerDate != dates[67] || repeat.TriggerGapSessions == nil || *repeat.TriggerGapSessions != 3 {
		t.Fatalf("volume green/red failed to repeat inside same price episode: %+v", repeat)
	}
	if rows[dates[71]].PriorConfirmedTriggers != 2 || *rows[dates[71]].ExactPriorTriggers != 2 {
		t.Fatal("current trigger was not carried into next date")
	}
	// A price green closes the old episode. Its next price-red segment starts
	// afresh even if the earlier segment had several all-red observations.
	bars[75].Close = 80
	bars[77].Volume = 400
	rows, err = AnalyzePriceEpisodes(context.Background(), bars, dates, dates[90])
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := rows[dates[75]]; exists {
		t.Fatal("price green emitted an episode row")
	}
	reentered := rows[dates[77]]
	if reentered.Group != "first" || reentered.StartDate != dates[76] || reentered.PriorConfirmedTriggers != 0 || reentered.PriorAllRedObserved {
		t.Fatalf("old episode leaked through confirmed price green: %+v", reentered)
	}
}

func TestPriceEpisodeHistoryBeforeResearchAndNoSixtySessionCap(t *testing.T) {
	bars, dates, _ := triggerFixture(t, 149, 65)
	for i := range bars {
		bars[i].Volume = 100
	}
	bars[67].Volume, bars[148].Volume = 200, 200
	rows, err := AnalyzePriceEpisodes(context.Background(), bars, dates, dates[149])
	if err != nil {
		t.Fatal(err)
	}
	x := rows[dates[148]]
	if x.Group != "repeat" || x.StartDate != dates[65] || x.PriorConfirmedTriggers != 1 || x.TriggerGapSessions == nil || *x.TriggerGapSessions != 81 {
		t.Fatalf("research start or old 60-day cap erased earlier trigger: %+v", x)
	}
}

func TestPriceEpisodeMissingSessionBreaksRepeatEvidence(t *testing.T) {
	bars, dates, _ := triggerFixture(t, 90, 65)
	for i := range bars {
		bars[i].Volume = 100
	}
	bars[67].Volume, bars[72].Volume, bars[75].Volume = 200, 200, 200
	bars = append(append([]market.Bar(nil), bars[:70]...), bars[71:]...)
	rows, err := AnalyzePriceEpisodes(context.Background(), bars, dates, dates[90])
	if err != nil {
		t.Fatal(err)
	}
	firstAfterGap, later := rows[dates[72]], rows[dates[75]]
	if firstAfterGap.Group != "unknown" || firstAfterGap.StartKnown || firstAfterGap.StartDate != "" || firstAfterGap.ObservedStartDate != dates[71] || firstAfterGap.CensorReason != "session_missing" || firstAfterGap.PriorAllRedObserved || firstAfterGap.PriorConfirmedTriggers != 0 || firstAfterGap.ExactPriorTriggers != nil || firstAfterGap.TriggerGapSessions != nil {
		t.Fatalf("gap admitted earlier all-red into censored episode: %+v", firstAfterGap)
	}
	if later.Group != "repeat" || later.ExactPriorTriggers != nil || later.PriorConfirmedTriggers != 1 || later.TriggerGapSessions == nil || *later.TriggerGapSessions != 3 {
		t.Fatalf("new confirmed trigger did not recover local repeat/gap: %+v", later)
	}
}

func TestPriceEpisodeRealUnknownBarsRespectSeparatePriceAndEnergyContinuity(t *testing.T) {
	bars, dates, _ := triggerFixture(t, 149, 65)
	for i := range bars {
		bars[i].Volume = 100
	}
	bars[67].Volume, bars[70].Volume, bars[73].Volume = 200, 0, 200
	rows, err := AnalyzePriceEpisodes(context.Background(), bars, dates, dates[149])
	if err != nil {
		t.Fatal(err)
	}
	energyUnknown, repeat := rows[dates[70]], rows[dates[73]]
	if energyUnknown.StartDate != dates[65] || energyUnknown.GroupReason != "current_energy_unknown" || repeat.Group != "repeat" || repeat.StartDate != dates[65] || repeat.PriorConfirmedTriggers != 1 || repeat.ExactPriorTriggers != nil || repeat.TriggerGapSessions != nil {
		t.Fatalf("zero-volume energy unknown split price episode or hid uncertainty: unknown=%+v repeat=%+v", energyUnknown, repeat)
	}
	// An invalid close makes the frozen formula's price states unknown until
	// its 60-bar calculation window no longer includes that bad observation.
	// The recovered run must not inherit the earlier episode's all-red state.
	bars[70].Close = math.NaN()
	bars[132].Volume, bars[135].Volume = 200, 200
	rows, err = AnalyzePriceEpisodes(context.Background(), bars, dates, dates[149])
	if err != nil {
		t.Fatal(err)
	}
	for i := 70; i < 130; i++ {
		if _, exists := rows[dates[i]]; exists {
			t.Fatalf("price-unknown date %s emitted a price-red episode", dates[i])
		}
	}
	recovered := rows[dates[132]]
	if recovered.Group != "unknown" || recovered.StartKnown || recovered.ObservedStartDate != dates[130] || recovered.CensorReason != "price_light_unknown" || recovered.PriorAllRedObserved || recovered.PriorConfirmedTriggers != 0 {
		t.Fatalf("formula recovery bridged unknown price segment: %+v", recovered)
	}
	if rows[dates[135]].Group != "repeat" || rows[dates[135]].PriorConfirmedTriggers != 1 {
		t.Fatal("observed recovered segment cannot establish its own repeat")
	}
}

func episodeState(price, energy string) strategy.FourLightState {
	return strategy.FourLightState{Trend: price, Energy: energy, MidTerm: price, ShortTerm: price}
}

func episodeStep(s *priceEpisodeTracker, index int, state strategy.FourLightState) *PriceEpisodeContext {
	date := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, index).Format(time.DateOnly)
	return s.advance(date, index, &state)
}

func TestPriceEpisodeEnergyUnknownPreservesPriceSegmentButCensorsCounts(t *testing.T) {
	var tracker priceEpisodeTracker
	episodeStep(&tracker, 0, episodeState("green", "green"))
	episodeStep(&tracker, 1, episodeState("red", "unknown"))
	unknownConversion := episodeStep(&tracker, 2, episodeState("red", "red"))
	if unknownConversion.Group != "not_applicable" || !unknownConversion.StartKnown || unknownConversion.ExactPriorTriggers != nil || unknownConversion.PriorAllRedObserved {
		t.Fatalf("unknown conversion was called confirmed: %+v", unknownConversion)
	}
	episodeStep(&tracker, 3, episodeState("red", "green"))
	repeat := episodeStep(&tracker, 4, episodeState("red", "red"))
	if repeat.Group != "repeat" || !repeat.PriorAllRedObserved || repeat.PriorConfirmedTriggers != 0 || repeat.ExactPriorTriggers != nil || repeat.LastObservedTriggerDate != "" || repeat.TriggerGapSessions != nil {
		t.Fatalf("observed all-red must prove repeat without inventing a trigger: %+v", repeat)
	}
	episodeStep(&tracker, 5, episodeState("red", "green"))
	later := episodeStep(&tracker, 6, episodeState("red", "red"))
	if later.Group != "repeat" || later.PriorConfirmedTriggers != 1 || later.ExactPriorTriggers != nil || later.TriggerGapSessions == nil || *later.TriggerGapSessions != 2 {
		t.Fatalf("local gap did not recover independently from lifetime precision: %+v", later)
	}
	episodeStep(&tracker, 7, episodeState("red", "unknown"))
	episodeStep(&tracker, 8, episodeState("red", "green"))
	afterUnknown := episodeStep(&tracker, 9, episodeState("red", "red"))
	if afterUnknown.TriggerGapSessions != nil || afterUnknown.LastObservedTriggerDate != later.AsOf || afterUnknown.PriorConfirmedTriggers != 2 {
		t.Fatalf("possibly intervening trigger did not invalidate nearest gap: %+v", afterUnknown)
	}
}

func TestPriceEpisodePriceUnknownResetsAndKnownGreenRecovers(t *testing.T) {
	var tracker priceEpisodeTracker
	episodeStep(&tracker, 0, episodeState("green", "green"))
	episodeStep(&tracker, 1, episodeState("red", "red"))
	if x := episodeStep(&tracker, 2, episodeState("unknown", "red")); x != nil {
		t.Fatal("unknown price emitted a price-red row")
	}
	episodeStep(&tracker, 3, episodeState("red", "green"))
	x := episodeStep(&tracker, 4, episodeState("red", "red"))
	if x.Group != "unknown" || x.StartKnown || x.PriorAllRedObserved || x.PriorConfirmedTriggers != 0 || x.CensorReason != "price_light_unknown" {
		t.Fatalf("unknown price retained old episode: %+v", x)
	}
	// One known price green proves that price-all-red ended, regardless of
	// another unknown price light and unknown energy on that same session.
	green := strategy.FourLightState{Trend: "green", MidTerm: "unknown", ShortTerm: "red", Energy: "unknown"}
	episodeStep(&tracker, 5, green)
	start := episodeStep(&tracker, 6, episodeState("red", "red"))
	if !start.StartKnown || start.Group != "not_applicable" || start.ExactPriorTriggers == nil || *start.ExactPriorTriggers != 0 {
		t.Fatalf("known price-green boundary lost, or current count included: %+v", start)
	}
	episodeStep(&tracker, 7, episodeState("red", "green"))
	repeat := episodeStep(&tracker, 8, episodeState("red", "red"))
	if repeat.Group != "repeat" || repeat.PriorConfirmedTriggers != 0 || repeat.ExactPriorTriggers != nil || repeat.TriggerGapSessions != nil {
		t.Fatalf("unconfirmed initial conversion acquired exact counting: %+v", repeat)
	}
}

func TestPriceEpisodeLeftCensorAndEnergyUnknownWithoutPriorAllRed(t *testing.T) {
	var tracker priceEpisodeTracker
	left := episodeStep(&tracker, 0, episodeState("red", "green"))
	x := episodeStep(&tracker, 1, episodeState("red", "red"))
	if left.StartKnown || left.CensorReason != "left_censored" || x.Group != "unknown" || x.ExactPriorTriggers != nil || x.PriorAllRedObserved {
		t.Fatalf("first observed date treated as episode start: left=%+v later=%+v", left, x)
	}
	var known priceEpisodeTracker
	episodeStep(&known, 0, episodeState("green", "green"))
	episodeStep(&known, 1, episodeState("red", "unknown"))
	episodeStep(&known, 2, episodeState("red", "green"))
	x = episodeStep(&known, 3, episodeState("red", "red"))
	if x.Group != "unknown" || !x.StartKnown || x.PriorAllRedObserved || x.ExactPriorTriggers != nil {
		t.Fatalf("unknown energy was assumed never all-red: %+v", x)
	}
}

func TestPriceEpisodePrefixIsUnaffectedByFutureAndJSONKeepsUnknownNull(t *testing.T) {
	bars, dates, _ := triggerFixture(t, 90, 0)
	for i := range bars {
		bars[i].Volume = 100
	}
	bars[70].Volume = 200
	before, err := AnalyzePriceEpisodes(context.Background(), bars[:81], dates[:81], dates[80])
	if err != nil {
		t.Fatal(err)
	}
	for i := 81; i < len(bars); i++ {
		bars[i].Close, bars[i].Volume = math.NaN(), -1
	}
	after, err := AnalyzePriceEpisodes(context.Background(), bars, dates, dates[80])
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("future rows changed prefix", err)
	}
	x := before[dates[70]]
	encoded, err := json.Marshal(x)
	if err != nil || !strings.Contains(string(encoded), `"exact_prior_triggers":null`) || !strings.Contains(string(encoded), `"trigger_gap_sessions":null`) {
		t.Fatal("unknown serialized as zero or disappeared", string(encoded), err)
	}
	var decoded PriceEpisodeContext
	if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(decoded, x) {
		t.Fatal("JSON evidence failed to round trip", err)
	}
}

type episodeCancelContext struct {
	context.Context
	checks int
	after  int
}

func (c *episodeCancelContext) Err() error {
	c.checks++
	if c.checks >= c.after {
		return context.Canceled
	}
	return nil
}

func TestPriceEpisodeCancellationAndInvalidCalendarReturnNoPartialResult(t *testing.T) {
	bars, dates, _ := triggerFixture(t, 90, 65)
	ctx := &episodeCancelContext{Context: context.Background(), after: 200}
	rows, err := AnalyzePriceEpisodes(ctx, bars, dates, dates[90])
	if !errors.Is(err, context.Canceled) || rows != nil {
		t.Fatal("canceled run returned partial success", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if rows, err := AnalyzePriceEpisodes(canceled, bars, dates, dates[90]); !errors.Is(err, context.Canceled) || rows != nil {
		t.Fatal("initial cancellation ignored", err)
	}
	broken := append([]string(nil), dates...)
	broken[70] = broken[69]
	if rows, err := AnalyzePriceEpisodes(context.Background(), bars, broken, dates[90]); err == nil || rows != nil {
		t.Fatal("invalid frozen calendar accepted")
	}
}
