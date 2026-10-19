package strategy

import (
	"context"
	"math"
	"stock-watcher/internal/config"
	"stock-watcher/internal/indicators"
	"stock-watcher/internal/market"
	"testing"
	"time"
)

func TestSignalIndicatorValuesAndFlatBand(t *testing.T) {
	bars := make([]market.Bar, 60)
	for i := range bars {
		p := float64(i + 1)
		bars[i] = market.Bar{Open: p, Close: p, High: p + .2, Low: p - .2, Volume: 1000}
	}
	f, err := extractSignalFeatures(bars)
	if err != nil {
		t.Fatal(err)
	}
	x := f.SignalIndicators
	// Last 20 integers are 41..60: mean=50.5, population variance=33.25.
	if math.Abs(x.Bollinger.Middle-50.5) > 1e-10 || math.Abs(x.Bollinger.Upper-(50.5+2*math.Sqrt(33.25))) > 1e-10 || math.Abs(*x.Bollinger.PercentB-(.5+9.5/(4*math.Sqrt(33.25)))) > 1e-10 {
		t.Fatalf("wrong Bollinger convention: %+v", x.Bollinger)
	}
	previous, _ := indicators.Score(bars[:59])
	if x.PreviousRSI14 != previous.RSI14 || x.PreviousMACDHistogram != 2*(previous.DIF-previous.DEA) || x.MACDHistogram != 2*(f.Metrics.DIF-f.Metrics.DEA) {
		t.Fatal("previous-day indicator was shifted or uses a different convention")
	}
	for i := range bars {
		bars[i].Open *= 2
		bars[i].High *= 2
		bars[i].Low *= 2
		bars[i].Close *= 2
	}
	scaled, err := extractSignalFeatures(bars)
	if err != nil || math.Abs(*scaled.SignalIndicators.Bollinger.PercentB-*x.Bollinger.PercentB) > 1e-10 || math.Abs(scaled.SignalIndicators.Bollinger.WidthPct-x.Bollinger.WidthPct) > 1e-10 {
		t.Fatal("dimensionless band features changed with price scale")
	}
	for i := range bars {
		bars[i].Open, bars[i].High, bars[i].Low, bars[i].Close = 9.9, 9.9, 9.9, 9.9
	}
	flat, err := extractSignalFeatures(bars)
	if err != nil || flat.SignalIndicators.Bollinger.PercentB != nil || matchesSignalRule("bollinger", flat) {
		t.Fatal("zero band width created a buy signal")
	}
	if _, err := extractSignalFeatures(bars[:59]); err == nil {
		t.Fatal("missing warmup accepted")
	}
}

func TestFixedIndicatorRulesAndMissingEvidence(t *testing.T) {
	b, previousB := .4, .3
	f := Features{ATR14Pct: 3, MA20Distance: 5, VolumeRatio: .8,
		Metrics:          indicators.Metrics{RSI14: 65, DIF: .2, DEA: .1},
		SignalIndicators: &SignalIndicators{Version: 1, PreviousRSI14: 60, MACDHistogram: .2, PreviousMACDHistogram: .1, Bollinger: Bollinger{PercentB: &b}, PreviousPercentB: &previousB}}
	for _, rule := range []string{"rsi", "macd", "bollinger", "confluence", "all-three", "guard", "confluence-guard"} {
		if !validSignalRule(rule) || !matchesSignalRule(rule, f) || matchesSignalRule(rule, Features{}) {
			t.Fatalf("bad rule %s", rule)
		}
	}
	f.Metrics.RSI14 = 65.01
	if matchesSignalRule("rsi", f) || matchesSignalRule("all-three", f) || !matchesSignalRule("confluence", f) {
		t.Fatal("RSI bound or voting incorrect")
	}
	f.ATR14Pct = 3.001
	if matchesSignalRule("guard", f) || matchesSignalRule("confluence-guard", f) || !matchesSignalRule("confluence", f) {
		t.Fatal("guard not independent of confluence")
	}
	f.SignalIndicators.PreviousMACDHistogram = .2
	if matchesSignalRule("macd", f) || matchesSignalRule("confluence", f) {
		t.Fatal("unchanged histogram counted as strengthening")
	}
	if validSignalRule("typo") || matchesSignalRule("typo", f) {
		t.Fatal("unknown rule accepted")
	}
}

func indicatorResearchFixture() (config.Config, []market.History, []market.History, market.MinuteHistory) {
	c, h, raw := researchFixture()
	m := market.MinuteHistory{Version: 1, Code: h[0].Code, Name: h[0].Name, Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: time.Now()}
	for i := len(h[0].Bars) - 13; i < len(h[0].Bars); i++ {
		p := h[0].Bars[i-1].Close * 1.001
		bars := minuteDay(h[0].Bars[i].Date, p)
		bars[0].Open, bars[0].High, bars[0].Low, bars[0].Close = p*.988, p*.992, p*.984, p*.99
		bars[1].Open, bars[1].Low = p*.991, p*.988
		h[0].Bars[i], raw[0].Bars[i] = rawDay(bars), rawDay(bars)
		m.Bars = append(m.Bars, bars...)
	}
	return c, h, raw, m
}

func TestIndicatorSelectionPrecedesFuturePathAndRetainsFilteredEvidence(t *testing.T) {
	c, h, raw, m := indicatorResearchFixture()
	h2, r2 := h[0], raw[0]
	h2.Code, r2.Code = "sz000002", "sz000002"
	h2.Bars = append([]market.Bar(nil), h2.Bars...)
	r2.Bars = append([]market.Bar(nil), r2.Bars...)
	c.Stocks = append(c.Stocks, config.Stock{Code: h2.Code, Name: h2.Name})
	index := len(h2.Bars) - 10
	interval := &BacktestRange{From: h2.Bars[index].Date, Through: h2.Bars[index].Date}
	h2.Bars[index].Volume, r2.Bars[index].Volume = 1, 1
	c.Selection.TopN = 2
	x, err := AnalyzeConditional(context.Background(), c, append(h, h2), append(raw, r2), []market.MinuteHistory{m}, h[0].AsOf, interval, "guard")
	if err != nil || x.Selection.Candidates != 2 || x.Selection.Eligible != 1 || x.Selection.Selected != 1 || x.Selection.FilteredOriginalTop != 1 {
		t.Fatalf("filter coverage lost: %+v %v", x.Selection, err)
	}
	for _, trade := range x.Trades {
		if trade.Code == h2.Code && (trade.Selected || trade.Outcome.Status != "signal_filtered" || trade.Outcome.EntryDate != "" || trade.Rank < 1 || trade.Rank > 2 || trade.Features.Return5 == 0) {
			t.Fatalf("rejected stock lost audit evidence or became a hypothetical entry: %+v", trade)
		}
	}
	// Reset to identical features, leave only the second stock's minute path.
	h2.Bars[index].Volume, r2.Bars[index].Volume = h[0].Bars[index].Volume, raw[0].Bars[index].Volume
	m.Code = h2.Code
	c.Selection.TopN = 1
	x, err = AnalyzeConditional(context.Background(), c, append(h, h2), append(raw, r2), []market.MinuteHistory{m}, h[0].AsOf, interval, "guard")
	if err != nil || len(x.Trades) != 1 || x.Trades[0].Code != h[0].Code || x.Trades[0].Outcome.Status != "entry_path_unknown" || x.Research.Development.TopRanked.UnknownEntries != 1 {
		t.Fatalf("future path replaced top pick: %+v %v", x, err)
	}
	if _, err = AnalyzeConditional(context.Background(), c, h, raw, []market.MinuteHistory{m}, h[0].AsOf, interval, "typo"); err == nil {
		t.Fatal("invalid rule accepted")
	}
}
