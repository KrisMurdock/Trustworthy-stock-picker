package fourlightstudy

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func triggerFixture(t *testing.T, signal, priceStart int) ([]market.Bar, []string, strategy.FourLightEvidence) {
	t.Helper()
	m, _ := studyFixture(t)
	bars := make([]market.Bar, signal+1)
	for i := range bars {
		price := 100.0
		if i >= priceStart {
			price += float64(i - priceStart + 1)
		}
		bars[i] = market.Bar{Date: m.Calendar.Days[i], Open: price, High: price + .1, Low: price - .1, Close: price, Volume: 100}
	}
	bars[signal].Volume = 200
	return bars, m.Calendar.Days, strategy.AnalyzeFourLight(bars)
}

func TestTriggerPriorAgeExcludesSignalAndRequiresKnownTermination(t *testing.T) {
	for _, tc := range []struct {
		prior int
		group string
	}{{0, "0"}, {1, "1-2"}, {2, "1-2"}, {3, "3-5"}, {5, "3-5"}, {6, "6+"}, {12, "6+"}} {
		bars, dates, evidence := triggerFixture(t, 80, 80-tc.prior)
		got := AnalyzeTriggerContext(bars, dates, evidence)
		run := got.PriorRedRuns.PriceAllRed
		if got.Version != 1 || got.Policy != TriggerContextPolicy || got.LookbackSessions != 60 || got.PriorThrough != dates[79] || got.Lifecycle != "first_all_red" || got.PriceAgeGroup != tc.group {
			t.Fatalf("prior=%d context=%+v", tc.prior, got)
		}
		if run.ExactDays == nil || *run.ExactDays != tc.prior || run.ObservedDays != tc.prior || run.Censored || run.Reason != "" {
			t.Fatalf("signal included or termination lost: prior=%d run=%+v", tc.prior, run)
		}
		if got.PriorRedRuns.Energy.ExactDays == nil || *got.PriorRedRuns.Energy.ExactDays != 0 {
			t.Fatal("newly red signal must not count as one prior red day")
		}
		wantStage := "price_preexisting"
		if tc.prior == 0 {
			wantStage = "multi_light_trigger"
		}
		if !reflect.DeepEqual(got.TriggerStages, []string{wantStage}) {
			t.Fatal("wrong trigger", got.TriggerStages, wantStage)
		}
	}
}

func TestTriggerStagesCanOverlapWithoutChangingAgePartition(t *testing.T) {
	bars, dates, _ := triggerFixture(t, 80, 80)
	bars[79].Volume = 150
	got := AnalyzeTriggerContext(bars, dates, strategy.AnalyzeFourLight(bars))
	if got.PriceAgeGroup != "0" || !reflect.DeepEqual(got.TriggerStages, []string{"energy_preexisting", "multi_light_trigger"}) {
		t.Fatal("energy-first and multi-light evidence must coexist", got)
	}
	if got.PriorRedRuns.Energy.ExactDays == nil || *got.PriorRedRuns.Energy.ExactDays != 1 {
		t.Fatal("energy age should end before the signal", got.PriorRedRuns.Energy)
	}
}

func TestTriggerMissingSessionAndUnknownLightsAreCensoredIndependently(t *testing.T) {
	for _, tc := range []struct {
		missing, observed int
		group             string
	}{{79, 0, "unknown"}, {75, 4, "unknown"}, {73, 6, "6+"}} {
		bars, dates, _ := triggerFixture(t, 80, 60)
		bars = append(append([]market.Bar(nil), bars[:tc.missing]...), bars[tc.missing+1:]...)
		got := AnalyzeTriggerContext(bars, dates, strategy.AnalyzeFourLight(bars))
		run := got.PriorRedRuns.PriceAllRed
		if run.ExactDays != nil || !run.Censored || run.ObservedDays != tc.observed || run.Reason != "prior_session_missing" || got.PriceAgeGroup != tc.group {
			t.Fatalf("gap turned into a known age: missing=%d context=%+v", tc.missing, got)
		}
		if tc.missing == 79 && (got.Lifecycle != "unknown" || len(got.TriggerStages) != 0) {
			t.Fatal("stale previous session invented first trigger")
		}
	}
	bars, dates, _ := triggerFixture(t, 80, 73)
	bars[79].Volume = 0
	got := AnalyzeTriggerContext(bars, dates, strategy.AnalyzeFourLight(bars))
	if got.PriorRedRuns.Energy.ExactDays != nil || got.PriorRedRuns.Energy.Reason != "prior_light_unknown" || got.PriorRedRuns.Trend.ExactDays == nil || *got.PriorRedRuns.Trend.ExactDays != 7 || got.PriorRedRuns.PriceAllRed.ExactDays == nil {
		t.Fatal("one unknown light erased independent price ages", got.PriorRedRuns)
	}
	if got.Lifecycle != "unknown" || got.PriceAgeGroup != "unknown" {
		t.Fatal("unknown previous energy invented a known first event", got)
	}
}

func TestTriggerLeftBoundaryAndLookbackCapAreLowerBounds(t *testing.T) {
	for _, tc := range []struct {
		signal   int
		observed int
		reason   string
		group    string
	}{{60, 1, "prior_light_unknown", "unknown"}, {65, 6, "prior_light_unknown", "6+"}, {119, 60, "lookback_limit", "6+"}, {149, 60, "lookback_limit", "6+"}} {
		bars, dates, evidence := triggerFixture(t, tc.signal, 0)
		got := AnalyzeTriggerContext(bars, dates, evidence)
		run := got.PriorRedRuns.PriceAllRed
		if run.ExactDays != nil || !run.Censored || run.ObservedDays != tc.observed || run.Reason != tc.reason || got.PriceAgeGroup != tc.group {
			t.Fatalf("censored run called exact: signal=%d got=%+v", tc.signal, got)
		}
	}
	bars, dates, evidence := triggerFixture(t, 59, 0)
	got := AnalyzeTriggerContext(bars, dates, evidence)
	if got.Lifecycle != "unknown" || got.PriceAgeGroup != "unknown" || got.PriorRedRuns.PriceAllRed.ObservedDays != 0 {
		t.Fatal("first calculable current state invented a previous signal", got)
	}
}

func TestTriggerNonFirstEventsHaveNoAgeCandidate(t *testing.T) {
	bars, dates, _ := triggerFixture(t, 80, 60)
	bars[79].Volume = 150
	got := AnalyzeTriggerContext(bars, dates, strategy.AnalyzeFourLight(bars))
	if got.Lifecycle != "continuing_all_red" || got.PriceAgeGroup != "not_applicable" || len(got.TriggerStages) != 0 {
		t.Fatal("continuing state became first trigger", got)
	}
	bars[80].Volume = 100
	got = AnalyzeTriggerContext(bars, dates, strategy.AnalyzeFourLight(bars))
	if got.Lifecycle != "non_all_red" || got.PriceAgeGroup != "not_applicable" || len(got.TriggerStages) != 0 {
		t.Fatal("non-all-red state became first trigger", got)
	}
}

func TestTriggerPriorReturnsUsePreviousCloseAndExactTwentyTwoSessions(t *testing.T) {
	for _, tc := range []struct {
		start20, start5, previous float64
		group                     string
	}{
		{90, 95, 100, "both_positive"}, {90, 100, 100, "positive_then_nonpositive"},
		{110, 95, 100, "nonpositive_then_positive"}, {100, 100, 100, "both_nonpositive"},
		{110, 105, 100, "both_nonpositive"},
	} {
		bars, dates, _ := triggerFixture(t, 80, 0)
		bars[59].Close, bars[74].Close, bars[79].Close, bars[80].Close = tc.start20, tc.start5, tc.previous, 200
		got := AnalyzeTriggerContext(bars, dates, strategy.AnalyzeFourLight(bars))
		want5, want20 := (tc.previous/tc.start5-1)*100, (tc.previous/tc.start20-1)*100
		if got.PriorPathGroup != tc.group || got.Prior5Pct == nil || got.Prior20Pct == nil || *got.Prior5Pct != want5 || *got.Prior20Pct != want20 || got.SignalChangePct == nil || *got.SignalChangePct != 100 {
			t.Fatalf("signal close leaked into prior returns: %+v", got)
		}
		bars[65].Close = math.NaN()
		got = AnalyzeTriggerContext(bars, dates, strategy.AnalyzeFourLight(bars))
		if got.Prior5Pct != nil || got.Prior20Pct != nil || got.PriorPathGroup != "unknown" || got.PriorPathReason != "prior_path_price_invalid" {
			t.Fatal("5-day return bypassed the frozen shared 22-session coverage rule", got)
		}
	}
	bars, dates, _ := triggerFixture(t, 80, 0)
	bars = append(append([]market.Bar(nil), bars[:65]...), bars[66:]...)
	got := AnalyzeTriggerContext(bars, dates, strategy.AnalyzeFourLight(bars))
	if got.PriorPathReason != "prior_path_session_missing" || got.PriorPathGroup != "unknown" || got.Prior5Pct != nil || got.Prior20Pct != nil {
		t.Fatal("missing middle session bypassed prior-path coverage", got)
	}
}

func TestTriggerFutureAppendDoesNotChangeFeaturesOrInputs(t *testing.T) {
	bars, dates, evidence := triggerFixture(t, 80, 73)
	beforeBars := append([]market.Bar(nil), bars...)
	beforeEvidence := evidence
	before := AnalyzeTriggerContext(bars, dates, evidence)
	for i := 81; i < 100; i++ {
		bars = append(bars, market.Bar{Date: dates[i], Close: math.NaN(), Volume: -1})
	}
	after := AnalyzeTriggerContext(bars, dates, evidence)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(bars[:81], beforeBars) || !reflect.DeepEqual(evidence, beforeEvidence) {
		t.Fatal("future bars changed signal features or inputs were mutated")
	}
	encoded, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TriggerContext
	if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(decoded, before) {
		t.Fatal("trigger evidence failed JSON round-trip", err)
	}
}
