package strategy

import (
	"context"
	"math"
	"reflect"
	"stock-watcher/internal/chanlun"
	"stock-watcher/internal/market"
	"testing"
	"time"
)

func TestChanLevelStatesUseOnlyConfirmedStructure(t *testing.T) {
	zone := chanlun.Zone{Upper: 12, Lower: 10, Mid: 11}
	context := chanlun.Analysis{
		FinishedStrokes: []chanlun.Stroke{{Direction: chanlun.Up}},
		Zones:           []chanlun.Zone{zone},
		LastClose:       10.5,
	}
	if status, _ := chanContextState(context); status != chanPass {
		t.Fatal("context above zone lower edge rejected", status)
	}
	context.LastClose = 9.9
	if status, _ := chanContextState(context); status != chanFail {
		t.Fatal("context below zone lower edge passed", status)
	}
	if status, _ := chanContextState(chanlun.Analysis{}); status != chanUnknown {
		t.Fatal("missing context structure passed")
	}

	setup := chanlun.Analysis{
		Fractals: []chanlun.Fractal{
			{Mark: chanlun.Bottom, Price: 9, High: 10},
		},
		LastClose: 10.1,
	}
	if status, _ := chanSetupState(setup); status != chanPass {
		t.Fatal("confirmed bottom not accepted", status)
	}
	setup.Fractals[0].Mark = chanlun.Top
	if status, _ := chanSetupState(setup); status != chanFail {
		t.Fatal("top fractal passed setup", status)
	}
	if status, _ := chanSetupState(chanlun.Analysis{}); status != chanUnknown {
		t.Fatal("missing setup structure passed")
	}

	trigger := chanlun.Analysis{
		Fractals:  []chanlun.Fractal{{Mark: chanlun.Bottom, Price: 8, High: 9}},
		LastClose: 9.1,
	}
	if status, _ := chanTriggerState(trigger); status != chanPass {
		t.Fatal("confirmed bottom not accepted", status)
	}
	trigger.Fractals[0].Mark = chanlun.Top
	if status, _ := chanTriggerState(trigger); status != chanFail {
		t.Fatal("top fractal passed trigger", status)
	}
}

func chanMinuteFixture(days int) (market.MinuteHistory, map[string]market.Bar) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, market.Shanghai)
	history := market.MinuteHistory{Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: start.AddDate(0, 0, days+1)}
	daily := map[string]market.Bar{}
	for day := 0; day < days; day++ {
		date := start.AddDate(0, 0, day)
		bars := []market.MinuteBar{}
		for i := 0; i < 48; i++ {
			minute := 575 + i*5
			if i >= 24 {
				minute = 785 + (i-24)*5
			}
			price := 10 + math.Sin(float64(day*48+i)/9)*0.8
			bars = append(bars, market.MinuteBar{End: date.Add(time.Duration(minute) * time.Minute), Open: price, High: price + .05, Low: price - .05, Close: price + .01, Volume: 1000, Final: true})
		}
		history.Bars = append(history.Bars, bars...)
		daily[date.Format("2006-01-02")] = rawDay(bars)
	}
	return history, daily
}

func TestChanEvidenceIsCausalAndRequiresCompleteSignalSession(t *testing.T) {
	history, daily := chanMinuteFixture(12)
	date := "2026-09-09"
	before, err := chanSignalEvidence(history, date, daily[date])
	if err != nil || before.Status == "" || len(before.Levels) != 3 {
		t.Fatalf("bad evidence: %+v %v", before, err)
	}
	for i, bar := range history.Bars {
		if bar.End.In(market.Shanghai).Format("2006-01-02") > date {
			history.Bars[i].High += 1
			history.Bars[i].Low -= 1
		}
	}
	after, err := chanSignalEvidence(history, date, daily[date])
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("future bars changed chan evidence: %v", err)
	}
	partial, _ := chanMinuteFixture(12)
	signalDate := "2026-09-09"
	for i, bar := range partial.Bars {
		if bar.End.In(market.Shanghai).Format("2006-01-02") == signalDate && bar.End.In(market.Shanghai).Hour() >= 14 {
			partial.Bars[i].Final = false
		}
	}
	unknown, err := chanSignalEvidence(partial, signalDate, daily[signalDate])
	if err != nil || unknown.Status != chanUnknown || len(unknown.Levels) != 3 {
		t.Fatalf("partial session not unknown: %+v %v", unknown, err)
	}
	one, oneDaily := chanMinuteFixture(1)
	short, err := chanSignalEvidence(one, "2026-09-01", oneDaily["2026-09-01"])
	if err != nil || short.Levels[0].Status != chanUnknown || short.Levels[0].Reason == "" {
		t.Fatalf("insufficient higher-level bars not unknown: %+v %v", short, err)
	}
}

func TestConditionalChanUnknownIsNotSafeAndKeepsReason(t *testing.T) {
	c, h, raw := researchFixture()
	c.Selection.TopN = 1
	m := market.MinuteHistory{Version: 1, Code: h[0].Code, Name: h[0].Name, Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: time.Now()}
	for i := len(h[0].Bars) - 13; i < len(h[0].Bars); i++ {
		bars := minuteDay(h[0].Bars[i].Date, 10)
		h[0].Bars[i], raw[0].Bars[i] = rawDay(bars), rawDay(bars)
		m.Bars = append(m.Bars, bars...)
	}
	x, err := AnalyzeConditional(context.Background(), c, h, raw, []market.MinuteHistory{m}, h[0].AsOf, nil, ChanSignalRule)
	if err != nil || len(x.Trades) == 0 || x.Selection.Eligible != 0 || x.Selection.Selected != 0 || x.Selection.FilteredOriginalTop == 0 {
		t.Fatalf("unknown chan structure was selected: %+v %v", x.Selection, err)
	}
	for _, trade := range x.Trades {
		if trade.Selected || trade.Rank != 1 || trade.Chan == nil || trade.Chan.Status != chanUnknown || trade.Chan.AsOf != trade.Date || trade.Outcome.Status != "chan_unknown" {
			t.Fatalf("unknown evidence not retained: %+v", trade)
		}
	}
}
