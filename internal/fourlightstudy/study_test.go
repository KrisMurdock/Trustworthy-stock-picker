package fourlightstudy

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

type testSource struct {
	stocks    []config.Stock
	pairs     map[string]Pair
	stocksErr error
	loadErr   error
	loads     int
}

func (s *testSource) Stocks(context.Context) ([]config.Stock, error) { return s.stocks, s.stocksErr }
func (s *testSource) Load(_ context.Context, stock config.Stock) (Pair, error) {
	s.loads++
	return s.pairs[stock.Code], s.loadErr
}

func studyFixture(t *testing.T) (Manifest, *testSource) {
	t.Helper()
	days := []string{}
	start := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	for day := start; len(days) < 150; day = day.AddDate(0, 0, 1) {
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			days = append(days, day.Format(time.DateOnly))
		}
	}
	history, err := calendar.NewHistory("frozen_test", days[0], days[len(days)-1], days, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{Version: 1, DatabaseSHA256: strings.Repeat("a", 64), From: days[80], Through: days[83],
		ObservationThrough: days[100], ReservedFrom: days[110], AsOf: days[120], UniversePolicy: "current_active_sh_sz",
		Calendar: experiment.Calendar{Version: 1, From: days[0], Through: days[len(days)-1], Days: days, Source: history.Source, Digest: history.Digest}, Parameters: config.Defaults().Strategy}
	bars := make([]market.Bar, 101)
	for i := range bars {
		price := 100 + float64(i)*.1
		bars[i] = market.Bar{Date: days[i], Open: price - .05, High: price + .1, Low: price - .1, Close: price, Volume: 1000}
	}
	bars[80].Volume, bars[81].Volume, bars[83].Volume = 2000, 2500, 2500
	stock := config.Stock{Code: "sz000001", Name: "test", ListedFrom: days[0]}
	source := &testSource{stocks: []config.Stock{stock}, pairs: map[string]Pair{stock.Code: {Adjusted: bars, Raw: append([]market.Bar(nil), bars...)}}}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	return m, source
}

func groupWindow(t *testing.T, report Report, group string, days int) strategy.UpwardWaveStats {
	t.Helper()
	for _, row := range report.Groups {
		if row.Name == group {
			for _, window := range row.Wave.Windows {
				if window.Days == days {
					return window
				}
			}
		}
	}
	t.Fatalf("missing group %s window %d", group, days)
	return strategy.UpwardWaveStats{}
}

func TestStudyFirstContinuingAndNonAllRedAreDistinct(t *testing.T) {
	m, source := studyFixture(t)
	events := []Event{}
	r, err := Analyze(context.Background(), m, source, func(event Event) error { events = append(events, event); return nil })
	if err != nil {
		t.Fatal(err)
	}
	for name, selected := range map[string]int{"baseline": 4, "all_red": 3, "first_all_red": 2, "continuing_all_red": 1, "first_energy_only": 2, "first_price_involved": 0, "flip_all": 0, "non_all_red": 1, "all_red_transition_unknown": 0} {
		if got := groupWindow(t, r, name, 5).Selected; got != selected {
			t.Errorf("%s selected=%d want=%d", name, got, selected)
		}
	}
	if r.Status != "complete" || r.Audit.StateKnown != 4 || r.Audit.AllRedEventRows != 3 || len(events) != 3 || source.loads != 1 {
		t.Fatalf("audit %+v, events=%d, loads=%d", r.Audit, len(events), source.loads)
	}
	for _, event := range events {
		if event.FourLight.Rule != strategy.FourLightRule || !event.FourLight.Approximation || event.FourLight.AlignmentStatus != strategy.FourLightAlignmentUnverified {
			t.Fatal("frozen approximation contract changed")
		}
	}
	if len(r.Daily) != 4*len(groupNames) || len(r.Monthly) == 0 {
		t.Fatal("daily or monthly coverage missing")
	}
}

func TestStudyMissingPreviousSessionDoesNotInventFirstOrFlip(t *testing.T) {
	m, source := studyFixture(t)
	pair := source.pairs["sz000001"]
	pair.Adjusted = append(append([]market.Bar{}, pair.Adjusted[:79]...), pair.Adjusted[80:]...)
	source.pairs["sz000001"] = pair
	m.Through = m.From
	var event Event
	r, err := Analyze(context.Background(), m, source, func(e Event) error { event = e; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if groupWindow(t, r, "all_red_transition_unknown", 5).Selected != 1 || groupWindow(t, r, "first_all_red", 5).Selected != 0 || groupWindow(t, r, "continuing_all_red", 5).Selected != 0 {
		t.Fatal("missing previous trading session assigned a lifecycle", r.Groups)
	}
	if event.FourLight.PreviousUnknown != 4 || event.FourLight.FlipCount != 0 || event.FourLight.AllFlipped || event.FourLight.Transitions.Energy {
		t.Fatalf("stale previous row reused: %+v", event.FourLight)
	}
}

func TestStudyFutureUnknownAndRawMissingStayInDenominator(t *testing.T) {
	m, source := studyFixture(t)
	m.Through = m.From
	pair := source.pairs["sz000001"]
	pair.Adjusted, pair.Raw = pair.Adjusted[:83], pair.Raw[:83]
	source.pairs["sz000001"] = pair
	r, err := Analyze(context.Background(), m, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := groupWindow(t, r, "first_all_red", 5)
	if w.Selected != 1 || w.Entered != 1 || w.Unknown != 1 || w.Known != 0 || w.SuccessRate != nil || w.ConservativeRate == nil || *w.ConservativeRate != 0 {
		t.Fatalf("future-unknown entry was dropped or called failed: %+v", w)
	}
	pair.Raw, pair.Problem = nil, "missing_bars_none"
	source.pairs["sz000001"] = pair
	r, err = Analyze(context.Background(), m, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	w = groupWindow(t, r, "first_all_red", 5)
	if w.Selected != 1 || w.EntryUnknown != 1 || w.Entered != 0 || len(r.Audit.StockProblems) != 1 || r.Audit.StateKnown != 1 {
		t.Fatalf("missing raw input removed valid signal: %+v audit=%+v", w, r.Audit)
	}
}

func TestStudyReservedBoundaryKeepsFiveAndTenDayCoverageSeparate(t *testing.T) {
	m, source := studyFixture(t)
	m.Through, m.ObservationThrough, m.ReservedFrom = m.From, m.Calendar.Days[85], m.Calendar.Days[86]
	pair := source.pairs["sz000001"]
	pair.Adjusted, pair.Raw = pair.Adjusted[:86], pair.Raw[:86]
	source.pairs["sz000001"] = pair
	r, err := Analyze(context.Background(), m, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	five, ten := groupWindow(t, r, "first_all_red", 5), groupWindow(t, r, "first_all_red", 10)
	if five.Known != 1 || five.Unknown != 0 || ten.Known != 0 || ten.Unknown != 1 || ten.UnknownReasons["reserved_boundary"] != 1 {
		t.Fatalf("window coverage mixed or reserved window consumed: five=%+v ten=%+v", five, ten)
	}
}

func TestStudyWarmupMissingAndBeforeListingAreAudited(t *testing.T) {
	m, source := studyFixture(t)
	m.From, m.Through = m.Calendar.Days[58], m.Calendar.Days[60]
	pair := source.pairs["sz000001"]
	pair.Adjusted = append(append([]market.Bar{}, pair.Adjusted[:60]...), pair.Adjusted[61:]...)
	source.pairs["sz000001"] = pair
	r, err := Analyze(context.Background(), m, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Audit.StockDates != 3 || r.Audit.WarmupInsufficient != 1 || r.Audit.StateKnown != 1 || r.Audit.SignalMissing != 1 {
		t.Fatalf("wrong warmup accounting: %+v", r.Audit)
	}
	source.stocks[0].ListedFrom = m.Calendar.Days[60]
	r, err = Analyze(context.Background(), m, source, nil)
	if err != nil || r.Audit.BeforeListing != 2 || r.Audit.SignalMissing != 1 {
		t.Fatalf("before-listing dates not separated: %+v err=%v", r.Audit, err)
	}
}

func TestStudySignalsHaveNoFutureInputAndSinkOrderingIsStable(t *testing.T) {
	m, source := studyFixture(t)
	m.Through = m.From
	source.stocks = []config.Stock{{Code: "sz000002"}, source.stocks[0]}
	source.pairs["sz000002"] = source.pairs["sz000001"]
	collect := func() []Event {
		t.Helper()
		events := []Event{}
		if _, err := Analyze(context.Background(), m, source, func(e Event) error { events = append(events, e); return nil }); err != nil {
			t.Fatal(err)
		}
		return events
	}
	before := collect()
	if len(before) != 2 || before[0].Code != "sz000001" || before[1].Code != "sz000002" {
		t.Fatal("event order is not deterministic code/date")
	}
	for _, pair := range source.pairs {
		for i := 81; i < len(pair.Adjusted); i++ {
			pair.Adjusted[i].Close *= 2
			pair.Adjusted[i].Volume *= 5
		}
	}
	after := collect()
	for i := range before {
		if !reflect.DeepEqual(before[i].FourLight, after[i].FourLight) || !reflect.DeepEqual(before[i].Groups, after[i].Groups) {
			t.Fatal("future price/volume changed signal classification")
		}
	}
}

func TestStudySourceSinkAndCancellationFailWithoutSuccess(t *testing.T) {
	boom := errors.New("fixture failure")
	for _, mode := range []string{"universe", "load", "sink", "cancel", "boundary"} {
		t.Run(mode, func(t *testing.T) {
			m, source := studyFixture(t)
			ctx := context.Background()
			var sink func(Event) error
			switch mode {
			case "universe":
				source.stocksErr = boom
			case "load":
				source.loadErr = boom
			case "sink":
				sink = func(Event) error { return boom }
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "boundary":
				pair := source.pairs["sz000001"]
				pair.Raw = append(pair.Raw, market.Bar{Date: m.ReservedFrom})
				source.pairs["sz000001"] = pair
			}
			r, err := Analyze(ctx, m, source, sink)
			if err == nil || r.Status == "complete" {
				t.Fatalf("failed study returned success: %+v %v", r, err)
			}
			if mode != "cancel" && mode != "boundary" && !errors.Is(err, boom) {
				t.Fatalf("lost error context: %v", err)
			}
		})
	}
}

func TestStudyRepeatReportIsDeterministic(t *testing.T) {
	m, source := studyFixture(t)
	a, err := Analyze(context.Background(), m, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Analyze(context.Background(), m, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if string(x) != string(y) {
		t.Fatal("same inputs did not produce byte-identical report")
	}
}

func TestClassifyPriceAndEnergyAttributionCannotOverlap(t *testing.T) {
	for _, tc := range []struct {
		name     string
		previous strategy.FourLightState
		flips    strategy.FourLightTransitions
		all      bool
		want     []string
	}{
		{"energy", strategy.FourLightState{Trend: "red", Energy: "green", MidTerm: "red", ShortTerm: "red"}, strategy.FourLightTransitions{Energy: true}, false, []string{"baseline", "all_red", "first_all_red", "first_energy_only"}},
		{"price_and_energy", strategy.FourLightState{Trend: "green", Energy: "green", MidTerm: "red", ShortTerm: "red"}, strategy.FourLightTransitions{Trend: true, Energy: true}, false, []string{"baseline", "all_red", "first_all_red", "first_price_involved"}},
		{"all", strategy.FourLightState{Trend: "green", Energy: "green", MidTerm: "green", ShortTerm: "green"}, strategy.FourLightTransitions{Trend: true, Energy: true, MidTerm: true, ShortTerm: true}, true, []string{"baseline", "all_red", "first_all_red", "flip_all", "first_price_involved"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := strategy.FourLightEvidence{AllRed: true, PreviousState: tc.previous, Transitions: tc.flips, AllFlipped: tc.all}
			if got := classify(x); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
