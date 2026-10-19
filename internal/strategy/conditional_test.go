package strategy

import (
	"context"
	"encoding/json"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"testing"
	"time"
)

func minuteDay(date string, price float64) []market.MinuteBar {
	day, _ := time.ParseInLocation("2006-01-02", date, market.Shanghai)
	bars := []market.MinuteBar{}
	for i := 0; i < 48; i++ {
		minute := 575 + i*5
		if i >= 24 {
			minute = 785 + (i-24)*5
		}
		bars = append(bars, market.MinuteBar{End: day.Add(time.Duration(minute) * time.Minute), Open: price, High: price + .02, Low: price - .02, Close: price, Volume: 1000, Final: true})
	}
	return bars
}
func rawDay(bars []market.MinuteBar) market.Bar {
	b := market.Bar{Date: bars[0].End.In(market.Shanghai).Format("2006-01-02"), Open: bars[0].Open, Close: bars[len(bars)-1].Close, High: bars[0].High, Low: bars[0].Low}
	for _, x := range bars {
		b.High = max(b.High, x.High)
		b.Low = min(b.Low, x.Low)
		b.Volume += x.Volume
	}
	return b
}
func conditionalFixture() ([]market.MinuteBar, []market.MinuteBar) {
	buy := minuteDay("2026-10-13", 10)
	buy[0].Open, buy[0].High, buy[0].Low, buy[0].Close = 9.88, 9.92, 9.84, 9.90
	buy[1].Open, buy[1].High, buy[1].Low, buy[1].Close = 9.91, 10.02, 9.88, 10
	sell := minuteDay("2026-10-14", 10.30)
	return buy, sell
}
func TestConditionalNextCandleEntryAndEntryDayOutcomeAreCausal(t *testing.T) {
	s, cal, signal, _ := tradingFixture()
	buy, sell := conditionalFixture()
	out, err := SimulateConditional(s, cal, "sz000001", signal, []market.Bar{rawDay(buy), rawDay(sell)}, buy, sell)
	if err != nil || out.EntryDay == nil || out.EntryDay.Fill != 9.92 || out.EntryDay.EntryAt != "2026-10-13T09:35:00+08:00" || out.EntryDay.CloseNetPct <= 0 || out.HighTouch.Day30 != 1 {
		t.Fatalf("bad conditional fill %+v %v", out, err)
	}
	// A later low and negative close cannot remove an already triggered trade.
	buy[30].Low = 9.5
	buy[47].Open, buy[47].High, buy[47].Low, buy[47].Close = 9.8, 9.82, 9.78, 9.8
	later, err := SimulateConditional(s, cal, "sz000001", signal, []market.Bar{rawDay(buy), rawDay(sell)}, buy, sell)
	if err != nil || later.EntryDay == nil || later.EntryDay.Fill != out.EntryDay.Fill || later.EntryDay.EntryAt != out.EntryDay.EntryAt || later.EntryDay.CloseNetPct >= 0 {
		t.Fatalf("future close changed entry %+v %v", later, err)
	}
	stats := SummarizeExperimentTrades([]ExperimentTrade{{Selected: true, Date: signal.Date, Outcome: later}}, s)
	if stats.NextDay30 != 1 || stats.Joint30 != 0 || stats.EntryDayNonPositive != 1 || JointVerdict(stats) != "entry_day_rejected" {
		t.Fatalf("negative close omitted: %+v", stats)
	}
}
func TestConditionalNoTriggerInvalidationJumpAndMissingPaths(t *testing.T) {
	s, cal, signal, _ := tradingFixture()
	for _, tc := range []struct {
		name, want string
		mutate     func(*[]market.MinuteBar, *[]market.MinuteBar)
	}{
		{"not_in_zone", "entry_not_triggered", func(b, e *[]market.MinuteBar) { *b = minuteDay("2026-10-13", 10) }},
		{"breaks_lower_before_bounce", "entry_invalidated", func(b, e *[]market.MinuteBar) { (*b)[0].Low = 9.7; (*b)[0].Close = 9.75 }},
		{"gap_after_signal", "entry_price_outside_zone", func(b, e *[]market.MinuteBar) { (*b)[1].Open = 10 }},
		{"missing_entry", "entry_path_unknown", func(b, e *[]market.MinuteBar) { *b = (*b)[1:] }},
		{"missing_exit", "missing_exit_session", func(b, e *[]market.MinuteBar) { *e = (*e)[1:] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, e := conditionalFixture()
			daily := []market.Bar{rawDay(b), rawDay(e)}
			tc.mutate(&b, &e)
			if len(b) == 48 {
				daily[0] = rawDay(b)
			}
			if len(e) == 48 {
				daily[1] = rawDay(e)
			}
			out, err := SimulateConditional(s, cal, "sz000001", signal, daily, b, e)
			if err != nil || out.Status != tc.want {
				t.Fatalf("got %+v %v", out, err)
			}
			if tc.name == "missing_exit" && out.EntryDay == nil {
				t.Fatal("missing exit erased entry")
			}
		})
	}
}
func TestEntryDayPositiveIsAfterCostsAndJointNeedsBoth(t *testing.T) {
	s, cal, signal, future := tradingFixture()
	future[0].Close = 10.01 // Greater than raw open, insufficient after fees/slippage.
	out, err := Simulate(s, cal, "sz000001", signal, future, 1)
	if err != nil || out.EntryDay == nil || out.EntryDay.CloseNetPct >= 0 {
		t.Fatalf("fees omitted %+v", out)
	}
	stats := Stats{}
	for _, o := range []Outcome{
		{Status: "target", EntryDate: "2026-10-13", NetPct: 3, EntryDay: &EntryDay{Version: 1, CloseNetPct: 1}, HighTouch: &HighTouch{Version: 1, ObservedDays: 1, Day25: 1, Day30: 1}},
		{Status: "target", EntryDate: "2026-10-13", NetPct: 3, EntryDay: &EntryDay{Version: 1, CloseNetPct: 0}, HighTouch: &HighTouch{Version: 1, ObservedDays: 1, Day25: 1, Day30: 1}},
		{Status: "missing_exit_session", EntryDate: "2026-10-13", EntryDay: &EntryDay{Version: 1, CloseNetPct: 1}},
		{Status: "target", EntryDate: "2026-10-13", NetPct: 3, HighTouch: &HighTouch{Version: 1, ObservedDays: 1, Day25: 1, Day30: 1}},
	} {
		add(&stats, Sample{Date: "2026-10-12"}, o)
	}
	stats = finalize(stats)
	if stats.EntryDayPositive != 2 || stats.EntryDayNonPositive != 1 || stats.EntryDayUnknown != 1 || stats.Joint30 != 1 || stats.Joint30Rate != .25 {
		t.Fatalf("joint denominator incorrect %+v", stats)
	}
}
func TestExplicitRangeFiltersSignalsAndRetainsWarmup(t *testing.T) {
	c, h, r := researchFixture()
	original, err := AnalyzeDevelopment(context.Background(), c, h, r, h[0].AsOf, EntryPolicy{NextDayRule: "baseline"})
	if err != nil {
		t.Fatal(err)
	}
	interval := &BacktestRange{From: original.Trades[2].Date, Through: original.Trades[len(original.Trades)/2].Date}
	x, err := AnalyzeDevelopment(context.Background(), c, h, r, h[0].AsOf, EntryPolicy{NextDayRule: "baseline", Range: interval})
	if err != nil {
		t.Fatal(err)
	}
	if x.HoldoutFrom != "" || x.HoldoutThrough != "" || len(x.Trades) == 0 {
		t.Fatal("explicit range mislabeled as holdout or omitted warmup")
	}
	for _, trade := range x.Trades {
		if !interval.Includes(trade.Date) {
			t.Fatal("signal outside range")
		}
	}
	before, _ := json.Marshal(x)
	// The policy is fixed; returned evidence cannot depend on future test prices.
	for i, b := range h[0].Bars {
		if b.Date > x.Trades[len(x.Trades)-1].Outcome.ExitDate && b.Date > interval.Through {
			h[0].Bars[i].High *= 1.01
			r[0].Bars[i].High *= 1.01
		}
	}
	after, err := AnalyzeDevelopment(context.Background(), c, h, r, h[0].AsOf, EntryPolicy{NextDayRule: "baseline", Range: interval})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(after)
	if string(before) != string(encoded) {
		t.Fatal("later prices changed interval results")
	}
	for _, bad := range []*BacktestRange{{From: "bad", Through: interval.Through}, {From: interval.Through, Through: interval.From}, {From: h[0].AsOf, Through: h[0].AsOf}} {
		if bad.Validate(c, h[0].AsOf, 1) == nil {
			t.Fatal("bad date range accepted")
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AnalyzeConditional(canceled, config.Defaults(), nil, nil, nil, "2026-10-15", nil, ""); err != context.Canceled {
		t.Fatal("cancel ignored")
	}
}

func TestConditionalHoldoutIsPurgedAndUnknownTopPickIsNotReplaced(t *testing.T) {
	c, h, raw := researchFixture()
	c.Selection.TopN = 1
	m := market.MinuteHistory{Version: 1, Code: h[0].Code, Name: h[0].Name, Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: time.Now()}
	for i := len(h[0].Bars) - 13; i < len(h[0].Bars); i++ {
		bars := minuteDay(h[0].Bars[i].Date, 10)
		bars[0].Open, bars[0].High, bars[0].Low, bars[0].Close = 9.88, 9.92, 9.84, 9.90
		bars[1].Open, bars[1].Low = 9.91, 9.88
		h[0].Bars[i], raw[0].Bars[i] = rawDay(bars), rawDay(bars)
		m.Bars = append(m.Bars, bars...)
	}
	x, err := AnalyzeConditional(context.Background(), c, h, raw, []market.MinuteHistory{m}, h[0].AsOf, nil, "")
	if err != nil || len(x.Trades) == 0 || x.HoldoutFrom != h[0].Bars[len(h[0].Bars)-3].Date {
		t.Fatalf("missing reserved sessions: %+v %v", x, err)
	}
	if x.Research.Development.Cohort.IncrementBootstrap == nil || x.Research.Development.PoolBaseline.Cohort.Version == 0 {
		t.Fatal("conditional entry did not produce a paired full-pool baseline")
	}
	for _, trade := range x.Trades {
		if trade.Outcome.ExitDate >= x.HoldoutFrom {
			t.Fatal("trade crossed holdout boundary")
		}
	}
	before, _ := json.Marshal(x)
	for i := len(h[0].Bars) - 3; i < len(h[0].Bars); i++ {
		h[0].Bars[i].High++
		raw[0].Bars[i].High++
	}
	for i := len(m.Bars) - 144; i < len(m.Bars); i++ {
		m.Bars[i].High++
	}
	again, err := AnalyzeConditional(context.Background(), c, h, raw, []market.MinuteHistory{m}, h[0].AsOf, nil, "")
	after, _ := json.Marshal(again)
	if err != nil || string(before) != string(after) {
		t.Fatalf("holdout prices changed development results: %v", err)
	}
	// Identical features rank code 000001 first. Only 000002 has a minute path;
	// knowing that tomorrow must never replace the preselected stock.
	h2, r2 := h[0], raw[0]
	h2.Code, r2.Code, m.Code = "sz000002", "sz000002", "sz000002"
	c.Stocks = append(c.Stocks, config.Stock{Code: "sz000002", Name: h2.Name})
	x, err = AnalyzeConditional(context.Background(), c, append(h, h2), append(raw, r2), []market.MinuteHistory{m}, h[0].AsOf, nil, "")
	if err != nil || len(x.Trades) == 0 || x.Research.Development.TopRanked.UnknownEntries != len(x.Trades) {
		t.Fatalf("unknown minute paths were excluded: %+v %v", x.Research.Development, err)
	}
	for _, trade := range x.Trades {
		if trade.Code != "sz000001" || trade.Outcome.Status != "entry_path_unknown" {
			t.Fatal("tomorrow's path influenced candidate selection")
		}
	}
}
