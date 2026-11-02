package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type fakeTurnoverSource struct {
	calls     int
	err       error
	lastStart string
	lastEnd   string
}

func (f *fakeTurnoverSource) FetchDailyMetrics(_ context.Context, stock config.Stock, start, end string) (market.DailyMetricHistory, error) {
	f.calls++
	f.lastStart, f.lastEnd = start, end
	if f.err != nil {
		return market.DailyMetricHistory{}, f.err
	}
	history := market.DailyMetricHistory{Version: 1, Code: stock.Code, Source: "sohu", FetchedAt: time.Now()}
	first, _ := time.Parse("2006-01-02", start)
	last, _ := time.Parse("2006-01-02", end)
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		history.Metrics = append(history.Metrics, market.DailyMetric{Date: day.Format("2006-01-02"), TurnoverRatePct: 1, AmountCNY: 1e8})
	}
	return history, nil
}

func TestRefreshTurnoverMetricsCachesCompleteWindow(t *testing.T) {
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, market.Shanghai)
	source := &fakeTurnoverSource{}
	raw := turnoverTestHistory()
	m := &Monitor{Config: config.Config{Stocks: []config.Stock{{Code: raw.Code}}}, Store: store, Log: logging.Nop(), Now: func() time.Time { return now }, TurnoverSource: source}
	items := []storage.SyncItem{{Stock: config.Stock{Code: raw.Code}, Raw: &raw}}
	first := m.refreshTurnoverMetrics(context.Background(), items)
	if source.calls != 1 || first[raw.Code].Validate() != nil {
		t.Fatalf("first refresh calls=%d histories=%d", source.calls, len(first))
	}
	if source.lastStart != raw.Bars[0].Date || source.lastEnd != raw.Bars[len(raw.Bars)-1].Date {
		t.Fatalf("fetch window = %s~%s, want %s~%s", source.lastStart, source.lastEnd, raw.Bars[0].Date, raw.Bars[len(raw.Bars)-1].Date)
	}
	second := m.refreshTurnoverMetrics(context.Background(), items)
	if source.calls != 1 {
		t.Fatalf("complete cache was refetched: calls=%d", source.calls)
	}
	if got := len(sortedTurnoverDates(second[raw.Code])); got != len(raw.Bars) {
		t.Fatalf("cached metric dates = %d, want %d", got, len(raw.Bars))
	}
}

func TestRefreshTurnoverMetricsKeepsReportIndependentOfFetchFailure(t *testing.T) {
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	raw := turnoverTestHistory()
	m := &Monitor{Config: config.Config{Stocks: []config.Stock{{Code: raw.Code}}}, Store: store, Log: logging.Nop(), Now: time.Now, TurnoverSource: &fakeTurnoverSource{err: errors.New("network down")}}
	items := []storage.SyncItem{{Stock: config.Stock{Code: raw.Code}, Raw: &raw}}
	if got := m.refreshTurnoverMetrics(context.Background(), items); len(got) != 0 {
		t.Fatalf("failed fetch should return no metrics: %+v", got)
	}
}

func TestTurnoverWindowKeepsZeroVolumeSessionsOutOfFetchEnd(t *testing.T) {
	raw := turnoverTestHistory()
	for i := len(raw.Bars) - 3; i < len(raw.Bars); i++ {
		raw.Bars[i].Volume = 0
	}
	start, end := turnoverWindow(&raw)
	if start != raw.Bars[0].Date || end != raw.Bars[len(raw.Bars)-4].Date {
		t.Fatalf("window = %s~%s", start, end)
	}
}

func turnoverTestHistory() market.History {
	bars := make([]market.Bar, 120)
	for i := range bars {
		bars[i] = market.Bar{
			Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02"),
			Open: 10, High: 10, Low: 10, Close: 10, Volume: 100,
		}
	}
	return market.History{
		Version: 1, Code: "sz000001", Name: "测试", Source: "sina", Adjust: "none",
		DataField: "day", VolumeUnit: "provider_raw", AsOf: bars[len(bars)-1].Date,
		FetchedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Bars: bars,
	}
}

func TestCurrentTurnoverMetricsFiltersCodesOutsideCurrentInput(t *testing.T) {
	cache := turnoverCache{Histories: map[string]market.DailyMetricHistory{
		"sz000001": {Version: 1, Code: "sz000001"},
		"sz000002": {Version: 1, Code: "sz000002"},
	}}
	got := currentTurnoverMetrics(cache, map[string]bool{"sz000002": true})
	if len(got) != 1 || got["sz000002"].Code != "sz000002" {
		t.Fatalf("filtered metrics = %+v", got)
	}
}
