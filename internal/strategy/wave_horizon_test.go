package strategy

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func horizonFixture(t *testing.T) (config.Config, *OpeningWaveEvaluator, ExperimentTrade, Dataset, int) {
	t.Helper()
	c, trade, d := openingWaveFixture(t)
	cal := calendar.New(c)
	i := sort.Search(len(d.Raw.Bars), func(i int) bool { return d.Raw.Bars[i].Date >= trade.Date })
	for day := 11; day <= 60; day++ {
		date, err := cal.Next(d.Raw.Bars[len(d.Raw.Bars)-1].Date)
		if err != nil {
			t.Fatal(err)
		}
		price := 100 + float64(day)
		d.Raw.Bars = append(d.Raw.Bars, market.Bar{Date: date, Open: price - 1, High: price + .1, Low: price - 1, Close: price, Volume: 10000})
	}
	d.Adjusted.Bars = append([]market.Bar(nil), d.Raw.Bars...)
	e, err := NewOpeningWaveEvaluatorForModel(c, UpwardWaveNoDrawdownModel)
	if err != nil {
		t.Fatal(err)
	}
	return c, e, trade, d, i
}

func TestWaveHorizonsPreserveShortResultAndActualFillCosts(t *testing.T) {
	c, e, tr, d, i := horizonFixture(t)
	asof := d.Raw.Bars[len(d.Raw.Bars)-1].Date
	before, err := e.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
	if err != nil {
		t.Fatal(err)
	}
	short, long, err := e.EvaluateWithHorizons(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
	if err != nil || !reflect.DeepEqual(short, before) || !reflect.DeepEqual(long.StartupFive, short.Windows[0]) {
		t.Fatal("long observation changed the original result or startup label", err)
	}
	if long.Model != WaveHorizonsModel || long.EntryStatus != "entered" || len(short.Days) != 10 || len(long.Days) != 60 || long.EntryPrice != short.EntryPrice || long.EntryDate != short.EntryDate {
		t.Fatalf("entry or independent path lengths differ: short=%+v long=%+v", short, long)
	}
	outcome, err := Simulate(c.Strategy, calendar.New(c), tr.Code, d.Raw.Bars[i], d.Raw.Bars[i+1:i+2], 1)
	if err != nil || outcome.EntryDay == nil {
		t.Fatal("fixture did not establish actual opening fill", err)
	}
	entry := outcome.EntryDay
	plan := Plan{EntryPrice: entry.Fill, BuyCost: entry.BuyCost, Shares: entry.Shares}
	if long.EntryPrice != entry.Fill || long.EntryPrice <= d.Raw.Bars[i+1].Open {
		t.Fatal("entry slippage lost")
	}
	for _, w := range long.Windows {
		b := d.Raw.Bars[i+w.Days]
		want := netPct(c.Strategy, plan, tickDown(b.Close*(1-c.Strategy.SlippageRate)))
		if w.Status != "known" || w.ObservedDays != w.Days || w.EndDate != b.Date || w.Full == nil || w.Full.EndNetPct != want || *w.AfterDay5UpRatio != 1 || *w.AfterDay5LongestUp != w.Days-5 || *w.NewClosingHighDays != w.Days || *w.LastNewClosingHighDay != w.Days {
			t.Fatalf("wrong complete horizon or costs: %+v want_net=%v", w, want)
		}
	}
	// The two outputs are independent evidence artifacts. A consumer editing
	// its short copy must not silently rewrite the archived long observations.
	firstClose, fullEnd := long.Days[0].Close, long.StartupFive.Full.EndNetPct
	short.Days[0].Close = -1
	short.Windows[0].Full.EndNetPct = -1
	if long.Days[0].Close != firstClose || long.StartupFive.Full.EndNetPct != fullEnd {
		t.Fatal("short result aliases long path or startup evidence")
	}
}

func TestWaveHorizonsShortCompatibilityIncludesIncompleteAndUnknownCases(t *testing.T) {
	for _, mode := range []string{"d1_pair", "d6_pair", "d6_boundary", "entry_boundary", "missing_signal", "not_entered"} {
		t.Run(mode, func(t *testing.T) {
			_, e, tr, d, i := horizonFixture(t)
			asof, boundary := d.Raw.Bars[len(d.Raw.Bars)-1].Date, ""
			switch mode {
			case "d1_pair":
				d.Adjusted.Bars[i+1].Volume *= 2
			case "d6_pair":
				d.Adjusted.Bars[i+6].Volume *= 2
			case "d6_boundary":
				boundary = d.Raw.Bars[i+6].Date
			case "entry_boundary":
				boundary = d.Raw.Bars[i+1].Date
				d.Raw.Bars[i+1].Open = math.NaN()
			case "missing_signal":
				d.Raw.Bars = nil
			case "not_entered":
				d.Raw.Bars[i+1].Volume = 0
			}
			before, err := e.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, boundary, asof)
			if err != nil {
				t.Fatal(err)
			}
			after, long, err := e.EvaluateWithHorizons(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, boundary, asof)
			if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(before.Windows[0], long.StartupFive) {
				t.Fatalf("short compatibility failed for %s: err=%v", mode, err)
			}
			oldJSON, _ := json.Marshal(before)
			newJSON, _ := json.Marshal(after)
			if string(oldJSON) != string(newJSON) {
				t.Fatal("short JSON encoding changed")
			}
		})
	}
}

func TestWaveHorizonsDescribeEarlyRiseThenFlatOrDeclineWithoutNewGate(t *testing.T) {
	for _, shape := range []string{"flat", "decline"} {
		t.Run(shape, func(t *testing.T) {
			_, e, tr, d, i := horizonFixture(t)
			for day := 6; day <= 60; day++ {
				price := 105.0
				if shape == "decline" {
					price -= float64(day-5) * .5
				}
				bar := &d.Raw.Bars[i+day]
				bar.Open, bar.High, bar.Low, bar.Close = price, price+.1, price-.1, price
				d.Adjusted.Bars[i+day] = *bar
			}
			short, long, err := e.EvaluateWithHorizons(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", d.Raw.Bars[len(d.Raw.Bars)-1].Date)
			if err != nil || short.Windows[0].Status != "success" || long.StartupFive.Status != "success" {
				t.Fatal("later shape changed early startup", err)
			}
			for _, w := range long.Windows {
				wantReturn := (d.Raw.Bars[i+w.Days].Close/105 - 1) * 100
				if w.Status != "known" || w.Full == nil || w.AfterDay5CloseReturnPct == nil || *w.AfterDay5CloseReturnPct != wantReturn || *w.AfterDay5UpRatio != 0 || *w.AfterDay5LongestUp != 0 || *w.NewClosingHighDays != 5 || *w.LastNewClosingHighDay != 5 {
					t.Fatalf("long descriptor invented sustained rise or success gate: %+v", w)
				}
				if shape == "decline" && (*w.GivebackPct <= 0 || w.Full.CloseDrawdownPct <= 0) {
					t.Fatal("decline risk description disappeared")
				}
			}
		})
	}
}

func TestWaveHorizonsIndependentCoverageAndReservedIsolation(t *testing.T) {
	_, e, tr, d, i := horizonFixture(t)
	asof := d.Raw.Bars[len(d.Raw.Bars)-1].Date
	boundary := d.Raw.Bars[i+21].Date
	short, long, err := e.EvaluateWithHorizons(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, boundary, asof)
	if err != nil || len(long.Days) != 20 || long.Windows[0].Status != "known" || long.Windows[1].Status != "unknown" || long.Windows[1].Reason != "reserved_boundary" || long.Windows[1].ObservedDays != 20 || long.Windows[1].Full != nil || long.Windows[1].EndDate != "" {
		t.Fatal("completed D20 or incomplete D40 misreported", long, err)
	}
	_, boundary40, err := e.EvaluateWithHorizons(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, d.Raw.Bars[i+41].Date, asof)
	if err != nil || len(boundary40.Days) != 40 || boundary40.Windows[0].Status != "known" || boundary40.Windows[1].Status != "known" || boundary40.Windows[1].ObservedDays != 40 || boundary40.Windows[2].Status != "unknown" || boundary40.Windows[2].ObservedDays != 40 {
		t.Fatal("D41 boundary did not preserve complete D40", err, boundary40)
	}
	for j := i + 21; j < len(d.Raw.Bars); j++ {
		d.Raw.Bars[j].Open = math.NaN()
		d.Adjusted.Bars[j].Volume = math.NaN()
	}
	afterShort, afterLong, err := e.EvaluateWithHorizons(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, boundary, asof)
	if err != nil || !reflect.DeepEqual(short, afterShort) || !reflect.DeepEqual(long, afterLong) {
		t.Fatal("reserved data leaked into either result", err)
	}
	_, asofLong, err := e.EvaluateWithHorizons(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", d.Raw.Bars[i+20].Date)
	if err != nil || asofLong.Windows[0].Status != "known" || asofLong.Windows[1].Reason != "future_window_incomplete" {
		t.Fatal("asof boundary inspected future bad data", err)
	}
	_, _, _, full, si := horizonFixture(t)
	full.Raw.Bars, full.Adjusted.Bars = full.Raw.Bars[:si+47], full.Adjusted.Bars[:si+47]
	_, partial, err := e.EvaluateWithHorizons(context.Background(), tr.Code, tr.Date, full.Adjusted.Bars, full.Raw.Bars, "", asof)
	if err != nil || len(partial.Days) != 46 || partial.Windows[1].Status != "known" || partial.Windows[2].Status != "unknown" || partial.Windows[2].ObservedDays != 46 || partial.Windows[2].Full != nil || partial.Windows[2].EndDate != "" {
		t.Fatal("46 sessions masqueraded as a complete 60-session window", partial, err)
	}
}

func TestWaveHorizonsLaterFailureDoesNotInvalidateEarlierWindows(t *testing.T) {
	for _, fault := range []string{"price_transform", "volume", "invalid", "missing"} {
		t.Run(fault, func(t *testing.T) {
			_, e, tr, d, i := horizonFixture(t)
			asof := d.Raw.Bars[len(d.Raw.Bars)-1].Date
			beforeShort, before, err := e.EvaluateWithHorizons(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
			if err != nil {
				t.Fatal(err)
			}
			wantReason := ""
			switch fault {
			case "price_transform":
				b := &d.Adjusted.Bars[i+21]
				b.Open, b.High, b.Low, b.Close = b.Open+1, b.High+1, b.Low+1, b.Close+1
				wantReason = "price_transform_changed"
			case "volume":
				d.Raw.Bars[i+21].Volume = 0
				wantReason = "bar_invalid_or_suspended"
			case "invalid":
				d.Adjusted.Bars[i+21].Volume = math.NaN()
				wantReason = "bar_invalid_or_suspended"
			case "missing":
				d.Adjusted.Bars = append(d.Adjusted.Bars[:i+21], d.Adjusted.Bars[i+22:]...)
				wantReason = "session_missing"
			}
			afterShort, after, err := e.EvaluateWithHorizons(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
			if err != nil || !reflect.DeepEqual(beforeShort, afterShort) || !reflect.DeepEqual(before.Windows[0], after.Windows[0]) || after.Windows[1].Reason != wantReason || after.Windows[1].ObservedDays != 20 || len(after.Days) != 20 {
				t.Fatalf("future fault contaminated completed prefix: after=%+v err=%v", after, err)
			}
		})
	}
}

func TestWaveHorizonsNotEnteredUnknownAndExplicitNullMetrics(t *testing.T) {
	for _, skipped := range []bool{false, true} {
		_, e, tr, d, i := horizonFixture(t)
		asof := d.Raw.Bars[len(d.Raw.Bars)-1].Date
		wantStatus := "unknown"
		if skipped {
			d.Raw.Bars[i+1].Volume = 0
			wantStatus = "not_entered"
		} else {
			d.Raw.Bars = nil
		}
		short, long, err := e.EvaluateWithHorizons(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
		if err != nil || long.EntryStatus != wantStatus || len(long.Days) != 0 || long.Days == nil || !reflect.DeepEqual(long.StartupFive, short.Windows[0]) {
			t.Fatal("empty entry contract changed", long, err)
		}
		for _, w := range long.Windows {
			if w.Status != wantStatus || w.ObservedDays != 0 || w.Full != nil || w.AfterDay5CloseReturnPct != nil || w.AfterDay5UpRatio != nil || w.AfterDay5LongestUp != nil || w.NewClosingHighDays != nil {
				t.Fatal("unavailable metrics fabricated", w)
			}
		}
		encoded, err := json.Marshal(long)
		if err != nil || !strings.Contains(string(encoded), `"days":[]`) || !strings.Contains(string(encoded), `"full":null`) || !strings.Contains(string(encoded), `"after_day5_close_return_pct":null`) {
			t.Fatal("empty and unknown JSON semantics changed", string(encoded), err)
		}
	}
}

type horizonCancelContext struct {
	context.Context
	calls, cancelAt int
}

func (c *horizonCancelContext) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestWaveHorizonsCancellationAndLegacyRejection(t *testing.T) {
	c, e, tr, d, _ := horizonFixture(t)
	asof := d.Raw.Bars[len(d.Raw.Bars)-1].Date
	ctx := &horizonCancelContext{Context: context.Background(), cancelAt: 30}
	short, long, err := e.EvaluateWithHorizons(ctx, tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
	if !errors.Is(err, context.Canceled) || short != nil || long != nil {
		t.Fatal("scan cancellation returned partial success", err)
	}
	legacy, err := NewOpeningWaveEvaluator(c)
	if err != nil {
		t.Fatal(err)
	}
	if short, long, err := legacy.EvaluateWithHorizons(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof); err == nil || short != nil || long != nil {
		t.Fatal("legacy evaluator silently accepted new diagnostics")
	}
}
