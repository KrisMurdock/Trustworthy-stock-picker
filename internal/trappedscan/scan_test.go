package trappedscan

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

type fakeStore struct {
	asOf      string
	stocks    []config.Stock
	histories []market.History
}

func (s fakeStore) LatestUnadjustedAsOf(context.Context) (string, error) { return s.asOf, nil }

func (s fakeStore) LoadUnadjustedHistories(context.Context, string, string) ([]config.Stock, []market.History, error) {
	return s.stocks, s.histories, nil
}

type fakeMetrics struct {
	mu      sync.Mutex
	calls   int
	byCode  map[string]market.DailyMetricHistory
	dropOne bool
	fail    bool
}

func (m *fakeMetrics) FetchDailyMetrics(_ context.Context, stock config.Stock, _, _ string) (market.DailyMetricHistory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.fail {
		return market.DailyMetricHistory{}, errors.New("forced metric failure")
	}
	history, ok := m.byCode[stock.Code]
	if !ok {
		return market.DailyMetricHistory{}, errors.New("missing fake metric")
	}
	if m.dropOne && len(history.Metrics) > 1 {
		history.Metrics = append([]market.DailyMetric{}, history.Metrics[:len(history.Metrics)-1]...)
	}
	return history, nil
}

func (m *fakeMetrics) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func testHistory(code, name, asOf string, count int, closeAt func(int) float64) market.History {
	start, _ := time.Parse("2006-01-02", asOf)
	history := market.History{Version: 1, Code: code, Name: name, Source: "tencent", Adjust: "none",
		DataField: "day", VolumeUnit: "provider_raw", AsOf: asOf, FetchedAt: time.Now().In(market.Shanghai)}
	for index := 0; index < count; index++ {
		close := closeAt(index)
		history.Bars = append(history.Bars, market.Bar{
			Date:   start.AddDate(0, 0, index-count+1).Format("2006-01-02"),
			Open:   close,
			Close:  close,
			High:   close * 1.01,
			Low:    close * 0.99,
			Volume: 300000,
		})
	}
	return history
}

func testMetrics(history market.History, rate float64) market.DailyMetricHistory {
	metrics := market.DailyMetricHistory{Version: 1, Code: history.Code, Source: "sohu", FetchedAt: time.Now().In(market.Shanghai)}
	for _, bar := range history.Bars {
		metrics.Metrics = append(metrics.Metrics, market.DailyMetric{Date: bar.Date, TurnoverRatePct: rate, AmountCNY: 100_000_000})
	}
	return metrics
}

func TestScanRanksLowestTrappedAndReusesCache(t *testing.T) {
	const asOf = "2026-11-02"
	rising := testHistory("sh600001", "上升股", asOf, 130, func(i int) float64 { return 5 + 5*float64(i)/129 })
	flat := testHistory("sh600002", "横盘股", asOf, 130, func(int) float64 { return 10 })
	falling := testHistory("sh600003", "下跌股", asOf, 130, func(i int) float64 { return 10 - 5*float64(i)/129 })
	store := fakeStore{asOf: asOf, stocks: []config.Stock{{Code: rising.Code, Name: rising.Name}, {Code: flat.Code, Name: flat.Name}, {Code: falling.Code, Name: falling.Name}},
		histories: []market.History{rising, flat, falling}}
	source := &fakeMetrics{byCode: map[string]market.DailyMetricHistory{
		rising.Code: testMetrics(rising, 2), flat.Code: testMetrics(flat, 2), falling.Code: testMetrics(falling, 2),
	}}
	cachePath := filepath.Join(t.TempDir(), "metrics.json")
	scanner := Scanner{Store: store, Metrics: source}
	options := Options{AsOf: asOf, TopN: 2, MinHistorySessions: 120, CachePath: cachePath}
	result, err := scanner.Scan(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.ValidSignals != 3 || len(result.Top) != 2 || result.FetchedMetrics != 3 || source.callCount() != 3 {
		t.Fatalf("unexpected scan result: %+v calls=%d", result, source.callCount())
	}
	if result.Top[0].Code != rising.Code {
		t.Fatalf("lowest trapped stock = %s, want %s", result.Top[0].Code, rising.Code)
	}
	if result.Top[0].Trapped.TrappedVolumeRatio > result.Top[1].Trapped.TrappedVolumeRatio {
		t.Fatalf("top is not sorted ascending: %+v", result.Top)
	}
	second := Scanner{Store: store, Metrics: &fakeMetrics{fail: true}}
	result, err = second.Scan(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheHits != 3 || result.FetchedMetrics != 0 || result.ValidSignals != 3 {
		t.Fatalf("cache was not reused: %+v", result)
	}
}

func TestScanAppliesEligibilityFilters(t *testing.T) {
	const asOf = "2026-11-02"
	normal := testHistory("sh600010", "正常股", asOf, 130, func(int) float64 { return 10 })
	st := testHistory("sh600011", "ST测试", asOf, 130, func(int) float64 { return 10 })
	short := testHistory("sh600012", "次新股", asOf, 100, func(int) float64 { return 10 })
	stale := testHistory("sh600013", "停牌股", asOf, 130, func(int) float64 { return 10 })
	stale.Bars[len(stale.Bars)-1].Date = "2026-11-01"
	low := testHistory("sh600014", "低流动股", asOf, 130, func(int) float64 { return 10 })
	for index := range low.Bars {
		low.Bars[index].Volume = 1
	}
	store := fakeStore{asOf: asOf, stocks: []config.Stock{
		{Code: normal.Code, Name: normal.Name}, {Code: st.Code, Name: st.Name}, {Code: short.Code, Name: short.Name},
		{Code: stale.Code, Name: stale.Name}, {Code: low.Code, Name: low.Name},
	}, histories: []market.History{normal, st, short, stale, low}}
	source := &fakeMetrics{byCode: map[string]market.DailyMetricHistory{normal.Code: testMetrics(normal, 2)}}
	result, err := (&Scanner{Store: store, Metrics: source}).Scan(context.Background(), Options{
		AsOf: asOf, TopN: 10, MinHistorySessions: 120, MinAmountCNY: 20_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.EligibleStocks != 1 || result.Signals != 1 || len(result.Top) != 1 {
		t.Fatalf("unexpected eligible result: %+v", result)
	}
	wantExcluded := map[string]int{"st_stock": 1, "insufficient_history": 1, "stale_last_bar": 1, "low_liquidity": 1}
	for reason, want := range wantExcluded {
		if result.Excluded[reason] != want {
			t.Fatalf("excluded[%s] = %d, want %d (%+v)", reason, result.Excluded[reason], want, result.Excluded)
		}
	}
}

func TestScanMarksIncompleteMetricsAsUnknown(t *testing.T) {
	const asOf = "2026-11-02"
	history := testHistory("sh600020", "测试股", asOf, 130, func(int) float64 { return 10 })
	store := fakeStore{asOf: asOf, stocks: []config.Stock{{Code: history.Code, Name: history.Name}}, histories: []market.History{history}}
	source := &fakeMetrics{dropOne: true, byCode: map[string]market.DailyMetricHistory{history.Code: testMetrics(history, 2)}}
	result, err := (&Scanner{Store: store, Metrics: source}).Scan(context.Background(), Options{
		AsOf: asOf, TopN: 10, MinHistorySessions: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FetchFailures != 1 || result.UnknownSignals != 1 || result.UnknownReasons["turnover_history_unavailable"] != 1 {
		t.Fatalf("unexpected unknown result: %+v", result)
	}
	if source.callCount() != 1 {
		t.Fatalf("incomplete metrics should not retry: calls=%d", source.callCount())
	}
}

func TestScanOfflineRejectsIncompleteCache(t *testing.T) {
	const asOf = "2026-11-02"
	history := testHistory("sh600030", "测试股", asOf, 130, func(int) float64 { return 10 })
	store := fakeStore{asOf: asOf, stocks: []config.Stock{{Code: history.Code, Name: history.Name}}, histories: []market.History{history}}
	_, err := (&Scanner{Store: store}).Scan(context.Background(), Options{
		AsOf: asOf, TopN: 10, MinHistorySessions: 120, Offline: true,
		CachePath: filepath.Join(t.TempDir(), "missing.json"),
	})
	if err == nil || !strings.Contains(err.Error(), "metric cache is incomplete") {
		t.Fatalf("offline scan error = %v", err)
	}
}
