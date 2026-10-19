package strategy

import (
	"stock-watcher/internal/market"
	"testing"
	"time"
)

func reboundGuard() StructureGuardEvidence {
	return StructureGuardEvidence{Version: 1, Rule: StructureGuardRule, GuardStatus: guardPass, DataStatus: guardDataOK, DailyZone: &ChanZoneEvidence{Lower: 9, Upper: 11, Mid: 10}}
}

// reboundDaily returns a qfq/raw pair whose signal-day scale is 2:1. The
// minute fixtures below stay in raw prices, so a missing conversion cannot
// accidentally pass the daily zone checks.
func reboundDaily() (market.History, market.History) {
	asof, _ := time.ParseInLocation("2006-01-02", guardTestAsOf, market.Shanghai)
	bar := func(close float64) []market.Bar {
		return []market.Bar{{Date: guardTestAsOf, Open: close, High: close, Low: close, Close: close, Volume: 100}}
	}
	adjusted := market.History{Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Adjust: "qfq", DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: guardTestAsOf, FetchedAt: asof.Add(15*time.Hour + 10*time.Minute), Bars: bar(20)}
	raw := market.History{Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Adjust: "none", DataField: "day", VolumeUnit: "provider_raw", AsOf: guardTestAsOf, FetchedAt: asof.Add(15*time.Hour + 10*time.Minute), Bars: bar(10)}
	return adjusted, raw
}

func reboundThirty(armedAtClose bool) market.MinuteHistory {
	asof, _ := time.ParseInLocation("2006-01-02", guardTestAsOf, market.Shanghai)
	h := market.MinuteHistory{Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Period: 30, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: asof.Add(15*time.Hour + 10*time.Minute)}
	for _, minute := range []int{600, 630, 660, 690, 810, 840, 870, 900} {
		price, low, close := 5.7, 5.6, 5.7
		if !armedAtClose && minute == 600 {
			low, close = 4.7, 5.0
		}
		if armedAtClose && minute == 900 {
			low, close = 4.7, 5.0
		}
		h.Bars = append(h.Bars, market.MinuteBar{End: asof.Add(time.Duration(minute) * time.Minute), Open: price, High: price + 0.1, Low: low, Close: close, Volume: 100, Final: true})
	}
	return h
}

func reboundFive(after map[int]float64) market.MinuteHistory {
	asof, _ := time.ParseInLocation("2006-01-02", guardTestAsOf, market.Shanghai)
	h := market.MinuteHistory{Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: asof.Add(15*time.Hour + 10*time.Minute)}
	for i := 0; i < 48; i++ {
		minute := 575 + i*5
		if i >= 24 {
			minute = 785 + (i-24)*5
		}
		close := 5.25
		if price, ok := after[minute]; ok {
			close = price
		}
		h.Bars = append(h.Bars, market.MinuteBar{End: asof.Add(time.Duration(minute) * time.Minute), Open: close, High: close + 0.1, Low: close - 0.1, Close: close, Volume: 100, Final: true})
	}
	return h
}

func TestReboundSignalStates(t *testing.T) {
	tests := []struct {
		name   string
		thirty market.MinuteHistory
		five   market.MinuteHistory
		want   string
	}{
		{name: "armed", thirty: reboundThirty(true), five: reboundFive(nil), want: ReboundArmed},
		{name: "confirmed", thirty: reboundThirty(false), five: reboundFive(map[int]float64{605: 5.0, 610: 4.85, 615: 4.75, 620: 4.85, 625: 5.0, 630: 5.1}), want: ReboundConfirmed},
		{name: "failed", thirty: reboundThirty(false), five: reboundFive(map[int]float64{605: 4.65}), want: ReboundFailed},
		{name: "expired", thirty: reboundThirty(false), five: reboundFive(map[int]float64{605: 5.0, 610: 5.0, 615: 5.0, 620: 5.0, 625: 5.0, 630: 5.0}), want: ReboundExpired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adjusted, raw := reboundDaily()
			signal := EvaluateReboundSignal(reboundGuard(), adjusted, raw, test.thirty, test.five, guardTestAsOf)
			if signal.Status != test.want {
				t.Fatalf("status = %s (%s), want %s", signal.Status, signal.Reason, test.want)
			}
		})
	}
}

func TestReboundSignalRequiresPassingDailyGuard(t *testing.T) {
	guard := reboundGuard()
	guard.GuardStatus = guardFail
	adjusted, raw := reboundDaily()
	signal := EvaluateReboundSignal(guard, adjusted, raw, reboundThirty(false), reboundFive(nil), guardTestAsOf)
	if signal.Status != ReboundWatch || signal.ReasonCode != "daily_guard_not_pass" {
		t.Fatalf("non-passing guard produced %+v", signal)
	}
}

func TestReboundSignalWatchBeforePullback(t *testing.T) {
	thirty := reboundThirty(false)
	for i := range thirty.Bars {
		thirty.Bars[i].Low = 11.2
		thirty.Bars[i].Open = 11.4
		thirty.Bars[i].High = 11.6
		thirty.Bars[i].Close = 11.4
	}
	adjusted, raw := reboundDaily()
	signal := EvaluateReboundSignal(reboundGuard(), adjusted, raw, thirty, reboundFive(nil), guardTestAsOf)
	if signal.Status != ReboundWatch || signal.ReasonCode != "awaiting_pullback" {
		t.Fatalf("watch state missing: %+v", signal)
	}
}

func TestReboundSignalRejectsUnknownPriceScale(t *testing.T) {
	adjusted, raw := reboundDaily()
	raw.Bars[len(raw.Bars)-1].Date = "2026-10-15"
	signal := EvaluateReboundSignal(reboundGuard(), adjusted, raw, reboundThirty(false), reboundFive(nil), guardTestAsOf)
	if signal.Status != ReboundFailed || signal.ReasonCode != "price_scale_unknown" {
		t.Fatalf("missing scale was accepted: %+v", signal)
	}
}
