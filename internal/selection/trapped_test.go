package selection

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func TestAttachTrappedSignalsUsesRawHistoryAndTurnover(t *testing.T) {
	report := Report{
		Adjust:  "qfq",
		Ranking: []strategy.Candidate{{Code: "sz000001"}, {Code: "sz000002"}, {Code: "sz000003"}},
		Selected: []Candidate{
			{Code: "sz000001", Ranking: &strategy.Candidate{Code: "sz000001"}},
		},
	}
	raw := trappedTestHistory("sz000001", 120, 80, 10.3, 10)
	adjusted := trappedTestHistory("sz000001", 120, 80, 20.3, 20)
	raw.Adjust = "none"
	adjusted.Adjust = "qfq"
	adjusted.DataField = "qfqday"
	otherAdjusted := trappedTestHistory("sz000002", 120, 80, 20.3, 20)
	otherAdjusted.Adjust = "qfq"
	otherAdjusted.DataField = "qfqday"
	histories := []market.History{adjusted, otherAdjusted}
	metrics := map[string]market.DailyMetricHistory{
		"sz000001": trappedTestMetrics(raw, 0.001),
	}

	attachTrappedSignals(&report, histories, []market.History{raw}, nil, metrics)

	if report.Ranking[0].Trapped == nil || report.Ranking[0].Trapped.Status != strategy.TrappedStatusTriggered {
		t.Fatalf("triggered signal not attached: %+v", report.Ranking[0].Trapped)
	}
	if report.Ranking[0].Trapped.CurrentPrice != 10 {
		t.Fatalf("adjusted price was used: %+v", report.Ranking[0].Trapped)
	}
	if report.Selected[0].Ranking == nil || report.Selected[0].Ranking.Trapped == nil {
		t.Fatal("selected candidate was not aligned with ranking")
	}
	if report.Ranking[1].Trapped == nil || report.Ranking[1].Trapped.Reasons[0] != "unadjusted_history_unavailable" {
		t.Fatalf("adjusted history must not be used as cost input: %+v", report.Ranking[1].Trapped)
	}
	if report.Ranking[2].Trapped == nil || report.Ranking[2].Trapped.Reasons[0] != "unadjusted_history_unavailable" {
		t.Fatalf("missing raw history should be explicit: %+v", report.Ranking[2].Trapped)
	}
	encoded, err := json.Marshal(report.Ranking[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"trapped_signal"`) || !strings.Contains(string(encoded), `"turnover_adjusted_v3"`) {
		t.Fatalf("v3 trapped signal is not persisted in candidate JSON: %s", encoded)
	}
}

func TestLeaderDailyExecuteUsesIndependentTrappedRaw(t *testing.T) {
	c, histories, _ := executionFixture()
	c.Strategy.Enabled = false
	c.Selection.MinScore = 0
	c.Selection.MinVolumeRatio = 0
	c.Selection.RSIMin = 0
	c.Selection.RSIMax = 100
	c.Selection.MinDayChange = -100
	c.Selection.MaxDayChange = 100
	bars := append([]market.Bar(nil), histories[0].Bars...)
	for i := range bars {
		price := 10 + float64(i)*0.01
		if i >= len(bars)-40 {
			price += float64(i-(len(bars)-40)) * 0.03
		}
		bars[i].Open = price
		bars[i].Close = price + 0.03
		bars[i].High = price + 0.06
		bars[i].Low = price - 0.01
		bars[i].Volume = 1000
	}
	histories[0].Bars = bars

	asof := histories[0].AsOf
	trappedRaw := trappedTestHistoryAsOf("sz000001", asof, 120, 80, 10.3, 10)
	in := Input{
		Config: c, Histories: histories, TrappedRaw: []market.History{trappedRaw},
		TurnoverMetrics: map[string]market.DailyMetricHistory{"sz000001": trappedTestMetrics(trappedRaw, 0.001)},
		AsOf:            asof, Target: "2026-10-20", Now: time.Now(),
	}
	engine, err := DefaultEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Execute(context.Background(), engine, in, "test-build")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Selected) != 1 || report.Selected[0].Ranking == nil {
		t.Fatalf("leader daily ranking missing: %+v", report)
	}
	got := report.Selected[0].Ranking.Trapped
	if got == nil || got.Method != strategy.TrappedSignalMethod || got.Status == strategy.TrappedStatusUnknown {
		t.Fatalf("leader daily did not receive trapped raw history: %+v", got)
	}
	if got.Sessions != 120 || got.CurrentPrice != 10 {
		t.Fatalf("leader daily did not use the independent trapped window: %+v", got)
	}
}

func TestShortHorizonExecutePrefersTrappedRaw(t *testing.T) {
	c, histories, raw := executionFixture()
	asof := histories[0].AsOf
	trappedRaw := trappedTestHistoryAsOf("sz000001", asof, 120, 80, 10.3, 10)
	in := Input{
		Config: c, Histories: histories, Raw: raw, TrappedRaw: []market.History{trappedRaw},
		TurnoverMetrics: map[string]market.DailyMetricHistory{"sz000001": trappedTestMetrics(trappedRaw, 0.001)},
		AsOf:            asof, Target: "2026-10-20", Now: time.Now(),
	}
	engine, err := DefaultEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Execute(context.Background(), engine, in, "test-build")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Ranking) != 1 {
		t.Fatalf("short-horizon ranking missing: %+v", report)
	}
	got := report.Ranking[0].Trapped
	if got == nil || got.Method != strategy.TrappedSignalMethod || got.Status == strategy.TrappedStatusUnknown {
		t.Fatalf("short-horizon did not receive trapped raw history: %+v", got)
	}
	if got.CurrentPrice != 10 {
		t.Fatalf("short-horizon did not prefer TrappedRaw over Raw: %+v", got)
	}
}

func TestExecuteRejectsInvalidTrappedRaw(t *testing.T) {
	c := config.Defaults()
	c.Daily.Adjust = "none"
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "test"}}
	h := trappedTestHistory("sz000001", 120, 80, 10.3, 10)
	base := Input{Config: c, Histories: []market.History{h}, AsOf: h.AsOf, Target: "2026-10-20", Now: time.Now()}
	metric := trappedTestMetrics(h, 0.001)

	tests := []struct {
		name string
		raw  []market.History
	}{
		{name: "outside pool", raw: []market.History{func() market.History {
			bad := h
			bad.Code = "sh600519"
			return bad
		}()}},
		{name: "duplicate code", raw: []market.History{h, h}},
		{name: "wrong adjustment", raw: []market.History{func() market.History {
			bad := h
			bad.Adjust = "qfq"
			bad.DataField = "qfqday"
			return bad
		}()}},
		{name: "wrong as-of", raw: []market.History{func() market.History {
			bad := h
			bad.AsOf = "2026-12-31"
			return bad
		}()}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := base
			in.TrappedRaw = test.raw
			if _, err := Execute(context.Background(), testEngine{}, in, "test"); err == nil {
				t.Fatal("invalid TrappedRaw accepted")
			}
		})
	}

	valid := base
	valid.TrappedRaw = []market.History{h}
	valid.TurnoverMetrics = map[string]market.DailyMetricHistory{"sz000001": metric}
	if _, err := Execute(context.Background(), testEngine{}, valid, "test"); err != nil {
		t.Fatalf("turnover with only TrappedRaw was rejected: %v", err)
	}
}

func trappedTestHistory(code string, total, trapped int, trappedPrice, currentPrice float64) market.History {
	return trappedTestHistoryAsOf(code, "", total, trapped, trappedPrice, currentPrice)
}

func trappedTestHistoryAsOf(code, asof string, total, trapped int, trappedPrice, currentPrice float64) market.History {
	dates := make([]string, total)
	if asof == "" {
		date := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		for i := 0; i < total; {
			if date.Weekday() != time.Saturday && date.Weekday() != time.Sunday {
				dates[i] = date.Format("2006-01-02")
				i++
			}
			date = date.AddDate(0, 0, 1)
		}
	} else {
		date, _ := time.Parse("2006-01-02", asof)
		for i := total - 1; i >= 0; {
			if date.Weekday() != time.Saturday && date.Weekday() != time.Sunday {
				dates[i] = date.Format("2006-01-02")
				i--
			}
			date = date.AddDate(0, 0, -1)
		}
	}
	bars := make([]market.Bar, total)
	for i := range bars {
		price := currentPrice
		if i < trapped {
			price = trappedPrice
		}
		bars[i] = market.Bar{
			Date:   dates[i],
			Open:   price,
			High:   price,
			Low:    price,
			Close:  price,
			Volume: 100,
		}
	}
	return market.History{
		Version: 1, Code: code, Name: "测试", Source: "sina", Adjust: "none",
		DataField: "day", VolumeUnit: "provider_raw", AsOf: bars[len(bars)-1].Date,
		FetchedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Bars: bars,
	}
}

func trappedTestMetrics(history market.History, rate float64) market.DailyMetricHistory {
	metrics := market.DailyMetricHistory{
		Version: 1, Code: history.Code, Source: "sohu", FetchedAt: history.FetchedAt,
	}
	for _, bar := range history.Bars {
		if bar.Volume <= 0 {
			continue
		}
		metrics.Metrics = append(metrics.Metrics, market.DailyMetric{Date: bar.Date, TurnoverRatePct: rate, AmountCNY: 1e8})
	}
	return metrics
}
