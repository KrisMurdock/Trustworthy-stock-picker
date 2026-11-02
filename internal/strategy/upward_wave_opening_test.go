package strategy

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func openingWaveFixture(t *testing.T) (config.Config, ExperimentTrade, Dataset) {
	t.Helper()
	c, tr, d := upwardFixture(t)
	c.Calendar.ValidFrom = "2025-01-01"
	cal := calendar.New(c)
	prefix := []market.Bar{}
	for date := "2025-11-03"; date < tr.Date; {
		prefix = append(prefix, market.Bar{Date: date, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10000})
		next, err := cal.Next(date)
		if err != nil {
			t.Fatal(err)
		}
		date = next
	}
	d.Raw.Bars = append(prefix, d.Raw.Bars...)
	d.Adjusted.Bars = append([]market.Bar{}, d.Raw.Bars...)
	return c, tr, d
}

func TestOpeningWaveMatchesRankedEntryAndATR(t *testing.T) {
	c, tr, d := openingWaveFixture(t)
	end := len(d.Raw.Bars) - 11
	f, err := Extract(d.Adjusted.Bars[:end+1])
	if err != nil {
		t.Fatal(err)
	}
	tr.Features = f
	tr.Outcome, err = Simulate(c.Strategy, calendar.New(c), tr.Code, d.Raw.Bars[end], d.Raw.Bars[end+1:], 5)
	if err != nil {
		t.Fatal(err)
	}
	asof := d.Raw.Bars[len(d.Raw.Bars)-1].Date
	want := labelUpwardWave(c.Strategy, calendar.New(c), tr, d, "", asof)
	got, err := EvaluateOpeningUpwardWave(context.Background(), c, tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(got)
	if string(a) != string(b) || got.Windows[0].Status != "success" {
		t.Fatalf("opening observer diverged from ranked entry: want %s, got %s", a, b)
	}
	// A shorter raw prefix must not move the signal or future observations.
	got, err = EvaluateOpeningUpwardWave(context.Background(), c, tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars[end:], "", asof)
	b, _ = json.Marshal(got)
	if err != nil || string(a) != string(b) {
		t.Fatalf("independent raw/adjusted date alignment: %s, %v", b, err)
	}
}

func TestOpeningWaveUnknownEntryAndSealedPrices(t *testing.T) {
	c, tr, d := openingWaveFixture(t)
	e, err := NewOpeningWaveEvaluator(c)
	if err != nil {
		t.Fatal(err)
	}
	i := len(d.Raw.Bars) - 11
	asof := d.Raw.Bars[len(d.Raw.Bars)-1].Date
	for _, tc := range []struct {
		name, entry, reason string
		change              func(*Dataset)
	}{
		{"raw_missing", "unknown", "signal_data_missing", func(d *Dataset) { d.Raw.Bars = nil }},
		{"entry_missing", "unknown", "session_missing", func(d *Dataset) { d.Raw.Bars = append(d.Raw.Bars[:i+1], d.Raw.Bars[i+2:]...) }},
		{"no_volume", "not_entered", "entry_skipped", func(d *Dataset) { d.Raw.Bars[i+1].Volume = 0 }},
		{"one_price", "not_entered", "entry_skipped", func(d *Dataset) { d.Raw.Bars[i+1].High = 100; d.Raw.Bars[i+1].Low = 100; d.Raw.Bars[i+1].Close = 100 }},
		{"bad_bar", "unknown", "bar_invalid_or_suspended", func(d *Dataset) { d.Raw.Bars[i+1].High = math.NaN() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dt := Dataset{Raw: market.History{Bars: append([]market.Bar{}, d.Raw.Bars...)}, Adjusted: d.Adjusted}
			tc.change(&dt)
			w, err := e.Evaluate(context.Background(), tr.Code, tr.Date, dt.Adjusted.Bars, dt.Raw.Bars, "", asof)
			if err != nil || w.EntryStatus != tc.entry || w.EntryReason != tc.reason {
				t.Fatalf("got %+v, %v", w, err)
			}
		})
	}
	boundary := d.Raw.Bars[i+6].Date
	before, err := e.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, boundary, asof)
	if err != nil || before.Windows[0].Status != "success" || before.Windows[1].Reason != "reserved_boundary" {
		t.Fatalf("boundary windows: %+v, %v", before, err)
	}
	for j := i + 6; j < len(d.Raw.Bars); j++ {
		d.Raw.Bars[j].Open = math.NaN()
		d.Adjusted.Bars[j].Close *= 5
	}
	after, err := e.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, boundary, asof)
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if err != nil || string(a) != string(b) {
		t.Fatal("reserved prices affected evaluation", err)
	}
	entryBoundary := d.Raw.Bars[i+1].Date
	d.Raw.Bars[i+1].Open = math.NaN()
	w, err := e.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, entryBoundary, asof)
	if err != nil || w.EntryReason != "reserved_boundary" {
		t.Fatal("entry beyond boundary was inspected", w, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Evaluate(ctx, tr.Code, tr.Date, nil, nil, "", asof); err != context.Canceled {
		t.Fatal("cancellation ignored", err)
	}
	if _, err := e.Evaluate(context.Background(), "bad", tr.Date, nil, nil, "", asof); err == nil {
		t.Fatal("invalid code accepted")
	}
	c.Daily.Adjust = "none"
	if _, err := NewOpeningWaveEvaluator(c); err == nil {
		t.Fatal("non-qfq features accepted")
	}
}

func TestUpwardWaveAccumulatorDetachedSummary(t *testing.T) {
	c, tr, d := upwardFixture(t)
	w := labelUpwardWave(c.Strategy, calendar.New(c), tr, d, "", d.Raw.Bars[10].Date)
	var a UpwardWaveAccumulator
	a.Add(tr.Date, w)
	first := a.Summary()
	a.Add(tr.Date, unknownOpeningWave("session_missing"))
	second := a.Summary()
	if first.Windows[0].Selected != 1 || first.Windows[0].UnknownReasons["session_missing"] != 0 || second.Windows[0].Selected != 2 || second.Windows[0].SignalDates != 1 || second.Windows[0].EntryUnknown != 1 || second.Windows[0].UnknownReasons["session_missing"] != 1 {
		t.Fatal("summary is mutable or unknown entry omitted", first, second)
	}
	second.Windows[0].UnknownReasons["session_missing"] = 99
	if a.Summary().Windows[0].UnknownReasons["session_missing"] != 1 {
		t.Fatal("summary leaked mutable accumulator map")
	}
}
