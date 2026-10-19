package experiment

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"testing"
	"time"
)

func TestFrozenMinutesCannotBeTamperedAndLegacyDigestSurvives(t *testing.T) {
	c := config.Defaults()
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试"}}
	at, _ := time.ParseInLocation("2006-01-02 15:00", "2026-10-15 15:00", market.Shanghai)
	h := market.History{Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Adjust: "qfq", DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: "2026-10-15", FetchedAt: at, Bars: []market.Bar{{Date: "2026-10-15", Open: 10, High: 11, Low: 9, Close: 10, Volume: 100}}}
	raw := h
	raw.Adjust = "none"
	raw.DataField = "day"
	snapshot, err := NewSnapshot(c, h.AsOf, []market.History{h}, []market.History{raw})
	if err != nil {
		t.Fatal(err)
	}
	if ValidateSnapshot(snapshot) != nil {
		t.Fatal("no-minute snapshot hash changed")
	}
	minute := market.MinuteHistory{Version: 1, Code: h.Code, Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: at, Bars: []market.MinuteBar{{End: at, Open: 10, High: 11, Low: 9, Close: 10, Volume: 100, Final: true}}}
	frozen, err := WithMinutes(snapshot, []market.MinuteHistory{minute})
	if err != nil {
		t.Fatal(err)
	}
	if frozen.ID == snapshot.ID || frozen.MinuteStocks != 1 || frozen.MinuteThrough != "2026-10-15" || ValidateSnapshot(frozen) != nil {
		t.Fatal("minute paths not frozen")
	}
	frozen.Minutes[0].Bars[0].Close = 10.1
	if ValidateSnapshot(frozen) == nil {
		t.Fatal("tampered minute path accepted")
	}
	spec, _ := Specification("conditional-entry")
	if _, err := NewRecord(snapshot, spec, nil, "test"); err == nil {
		t.Fatal("conditional entry accepted daily-only snapshot")
	}
}
