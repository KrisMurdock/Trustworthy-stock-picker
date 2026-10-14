package selection

import (
	"math"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"testing"
	"time"
)

func TestScoreMinimumAndFlatSeries(t *testing.T) {
	bars := make([]market.Bar, 35)
	for i := range bars {
		bars[i] = market.Bar{Close: 10, Volume: 100}
	}
	m, err := Score(bars)
	if err != nil {
		t.Fatal(err)
	}
	if m.Score != 17 || m.RSI14 != 50 || m.VolumeRatio != 1 || m.MACDScore != 0 {
		t.Fatalf("unexpected flat score %+v", m)
	}
	if _, err := Score(bars[:34]); err == nil {
		t.Fatal("accepted insufficient history")
	}
}

func TestGoldenMetricsAndCandidateFilters(t *testing.T) {
	bars := make([]market.Bar, 61)
	for i := range bars {
		bars[i] = market.Bar{Date: "2026-10-12", Close: 50 + 0.1*float64(i) + 0.4*math.Sin(float64(i)*1.5), Volume: 100}
	}
	bars[len(bars)-1].Date = "2026-10-13"
	bars[len(bars)-1].Volume = 150
	m, err := Score(bars)
	if err != nil {
		t.Fatal(err)
	}
	// Independent reference computation using SMA-seeded EMA and Wilder RSI.
	for _, pair := range [][2]float64{{m.MA5, 55.85512823671642}, {m.MA10, 55.60504368300839}, {m.MA20, 55.056574362233356}, {m.DIF, 0.7243723402201709}, {m.DEA, 0.7008433148807519}, {m.RSI14, 68.26690998527425}} {
		if math.Abs(pair[0]-pair[1]) > 1e-9 {
			t.Fatalf("metric %v != %v", pair[0], pair[1])
		}
	}
	c := config.Defaults()
	c.Selection.Enabled = true
	c.Stocks = []config.Stock{{Code: "sz000001"}}
	h := market.History{Version: 1, Code: "sz000001", Name: "测试股票", AsOf: "2026-10-13", Adjust: "qfq", Bars: bars}
	r, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now())
	if err != nil || len(r.Selected) != 1 || r.Selected[0].Metrics.Score < 70 {
		t.Fatalf("expected candidate %+v %v", r, err)
	}
	same, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now().Add(time.Hour))
	if err != nil || same.ID != r.ID {
		t.Fatal("unstable event id")
	}
	h.Name = "*ST测试"
	r, err = Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now())
	if err != nil || len(r.Selected) != 0 || r.Filtered[0].Reason != "special_treatment" {
		t.Fatal("ST stock recommended")
	}
	h.Name = "正常股票"
	h.Bars[len(h.Bars)-1].Date = "2026-10-12"
	if _, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now()); err == nil {
		t.Fatal("stale pool should fail")
	}
}
