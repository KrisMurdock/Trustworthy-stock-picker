package monitor

import (
	"context"
	"errors"
	"io/fs"
	"math"
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
	omitDate  string
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
		date := day.Format("2006-01-02")
		if date == f.omitDate {
			continue
		}
		history.Metrics = append(history.Metrics, market.DailyMetric{Date: date, TurnoverRatePct: 1, AmountCNY: 1e8})
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
	start, end, required := turnoverWindow(&raw)
	if start != raw.Bars[0].Date || end != raw.Bars[len(raw.Bars)-4].Date {
		t.Fatalf("window = %s~%s", start, end)
	}
	if len(required) != len(raw.Bars)-3 || required[len(required)-1] != end {
		t.Fatalf("required dates = %d, last = %v", len(required), required[len(required)-1:])
	}
}

func TestTurnoverWindowStartsAtFirstPositiveSession(t *testing.T) {
	raw := turnoverTestHistory()
	raw.Bars[0].Volume = 0
	start, end, required := turnoverWindow(&raw)
	if start != raw.Bars[1].Date || end != raw.Bars[len(raw.Bars)-1].Date {
		t.Fatalf("window = %s~%s", start, end)
	}
	if len(required) != len(raw.Bars)-1 || required[0] != start {
		t.Fatalf("required window = %v", required[:min(3, len(required))])
	}
}

func TestRefreshTurnoverMetricsUsesFirstPositiveSessionAsStart(t *testing.T) {
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	raw := turnoverTestHistory()
	raw.Bars[0].Volume = 0
	source := &fakeTurnoverSource{}
	m := &Monitor{Config: config.Config{Stocks: []config.Stock{{Code: raw.Code}}}, Store: store, Log: logging.Nop(), Now: time.Now, TurnoverSource: source}
	m.refreshTurnoverMetrics(context.Background(), []storage.SyncItem{{Stock: config.Stock{Code: raw.Code}, Raw: &raw}})
	if source.lastStart != raw.Bars[1].Date || source.lastEnd != raw.Bars[len(raw.Bars)-1].Date {
		t.Fatalf("fetch window = %s~%s, want %s~%s", source.lastStart, source.lastEnd, raw.Bars[1].Date, raw.Bars[len(raw.Bars)-1].Date)
	}
}

func TestTurnoverCacheRequiresEveryPositiveSession(t *testing.T) {
	raw := turnoverTestHistory()
	_, _, required := turnoverWindow(&raw)
	complete := turnoverMetricsFor(raw, "")
	if !turnoverCacheUsable(complete, raw.Code, required) {
		t.Fatal("complete cache rejected")
	}

	missing := turnoverMetricsFor(raw, required[len(required)/2])
	if turnoverCacheUsable(missing, raw.Code, required) {
		t.Fatal("cache with a missing middle session was accepted")
	}
	expired := turnoverMetricsFor(raw, required[len(required)-1])
	if turnoverCacheUsable(expired, raw.Code, required) {
		t.Fatal("cache missing the latest positive session was accepted")
	}

	withEarlier := turnoverMetricsFor(raw, "")
	withEarlier.Metrics = append([]market.DailyMetric{{Date: "2025-12-31", TurnoverRatePct: 1, AmountCNY: 1e8}}, withEarlier.Metrics...)
	if !turnoverCacheUsable(withEarlier, raw.Code, required) {
		t.Fatal("extra history before the window should not invalidate cache")
	}
}

func TestRefreshTurnoverMetricsRefetchesCacheWithMissingSession(t *testing.T) {
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	raw := turnoverTestHistory()
	_, _, required := turnoverWindow(&raw)
	missing := turnoverMetricsFor(raw, required[len(required)/2])
	if err := store.Write(turnoverCachePath, turnoverCache{Version: turnoverCacheVersion, UpdatedAt: time.Now(), Histories: map[string]market.DailyMetricHistory{raw.Code: missing}}); err != nil {
		t.Fatal(err)
	}
	source := &fakeTurnoverSource{}
	m := &Monitor{Config: config.Config{Stocks: []config.Stock{{Code: raw.Code}}}, Store: store, Log: logging.Nop(), Now: time.Now, TurnoverSource: source}
	got := m.refreshTurnoverMetrics(context.Background(), []storage.SyncItem{{Stock: config.Stock{Code: raw.Code}, Raw: &raw}})
	if source.calls != 1 {
		t.Fatalf("incomplete cache fetch calls = %d, want 1", source.calls)
	}
	if !turnoverCacheUsable(got[raw.Code], raw.Code, required) {
		t.Fatalf("replacement cache is incomplete: %+v", got[raw.Code])
	}
}

func TestRefreshTurnoverMetricsRefetchesInvalidOrExpiredCache(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*market.DailyMetricHistory)
	}{
		{name: "invalid", mutate: func(h *market.DailyMetricHistory) { h.Code = "sz000002" }},
		{name: "expired", mutate: func(h *market.DailyMetricHistory) { h.Metrics = h.Metrics[:len(h.Metrics)-1] }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := storage.Open(t.TempDir(), false)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			raw := turnoverTestHistory()
			cached := turnoverMetricsFor(raw, "")
			test.mutate(&cached)
			if err := store.Write(turnoverCachePath, turnoverCache{Version: turnoverCacheVersion, UpdatedAt: time.Now(), Histories: map[string]market.DailyMetricHistory{raw.Code: cached}}); err != nil {
				t.Fatal(err)
			}
			source := &fakeTurnoverSource{}
			m := &Monitor{Config: config.Config{Stocks: []config.Stock{{Code: raw.Code}}}, Store: store, Log: logging.Nop(), Now: time.Now, TurnoverSource: source}
			m.refreshTurnoverMetrics(context.Background(), []storage.SyncItem{{Stock: config.Stock{Code: raw.Code}, Raw: &raw}})
			if source.calls != 1 {
				t.Fatalf("fetch calls = %d, want 1", source.calls)
			}
		})
	}
}

func TestRefreshTurnoverMetricsDoesNotCachePartialSource(t *testing.T) {
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	raw := turnoverTestHistory()
	_, _, required := turnoverWindow(&raw)
	source := &fakeTurnoverSource{omitDate: required[len(required)/2]}
	m := &Monitor{Config: config.Config{Stocks: []config.Stock{{Code: raw.Code}}}, Store: store, Log: logging.Nop(), Now: time.Now, TurnoverSource: source}
	items := []storage.SyncItem{{Stock: config.Stock{Code: raw.Code}, Raw: &raw}}
	if got := m.refreshTurnoverMetrics(context.Background(), items); len(got) != 0 {
		t.Fatalf("partial source produced metrics: %+v", got)
	}
	var persisted turnoverCache
	if err := store.Read(turnoverCachePath, &persisted); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("partial source was cached: %v %+v", err, persisted)
	}
	m.refreshTurnoverMetrics(context.Background(), items)
	if source.calls != 2 {
		t.Fatalf("partial source fetch calls = %d, want 2", source.calls)
	}
}

func TestEffectiveRawHistoryFallsBackOnlyForUnadjustedAnalysis(t *testing.T) {
	raw := turnoverTestHistory()
	adjusted := raw
	adjusted.Adjust = "qfq"
	adjusted.DataField = "qfqday"
	if got := effectiveRawHistory(storage.SyncItem{Raw: &adjusted, Adjusted: &raw}); got != &adjusted {
		t.Fatal("explicit raw history was not preferred")
	}
	if got := effectiveRawHistory(storage.SyncItem{Adjusted: &raw}); got != &raw {
		t.Fatal("unadjusted analysis history was not used as raw")
	}
	if got := effectiveRawHistory(storage.SyncItem{Adjusted: &adjusted}); got != nil {
		t.Fatal("adjusted analysis history was used as raw")
	}
}

func TestRefreshTurnoverMetricsUsesAdjustedRawForLeaderDaily(t *testing.T) {
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	raw := turnoverTestHistory()
	source := &fakeTurnoverSource{}
	m := &Monitor{Config: config.Config{Stocks: []config.Stock{{Code: raw.Code}}}, Store: store, Log: logging.Nop(), Now: time.Now, TurnoverSource: source}
	got := m.refreshTurnoverMetrics(context.Background(), []storage.SyncItem{{Stock: config.Stock{Code: raw.Code}, Adjusted: &raw}})
	if source.calls != 1 || got[raw.Code].Validate() != nil {
		t.Fatalf("adjusted-only leader daily turnover: calls=%d history=%+v", source.calls, got[raw.Code])
	}
}

func TestTrappedRawWindowKeepsLookbackBeyondHistoryDays(t *testing.T) {
	raw := trappedPreviewHistory(150, "2026-10-13")
	histories := raw
	first := len(histories.Bars) - 90
	histories.Bars = append([]market.Bar(nil), histories.Bars[first:]...)
	got := trappedRawWindow([]market.History{histories}, []market.History{raw})
	if len(got) != 1 || len(got[0].Bars) != 120 {
		t.Fatalf("trapped raw bars = %d, want 120", len(got[0].Bars))
	}
	if got[0].Bars[0].Date != raw.Bars[len(raw.Bars)-120].Date || got[0].Bars[len(got[0].Bars)-1].Date != raw.AsOf {
		t.Fatalf("trapped raw window = %s~%s", got[0].Bars[0].Date, got[0].Bars[len(got[0].Bars)-1].Date)
	}
}

func TestPreviewLeaderDailyUsesAdjustedRawForTurnover(t *testing.T) {
	root := t.TempDir()
	store, err := storage.Open(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db, err := storage.InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	history := trappedPreviewHistory(150, "2026-10-13")
	c := config.Defaults()
	c.RequireCIFS = false
	c.Stocks = []config.Stock{{Code: history.Code, Name: history.Name}}
	c.Selection.Enabled = true
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Daily.Adjust = "none"
	c.Daily.HistoryDays = 90
	c.Strategy.Enabled = false
	m := New(c, store, nil, nil, logging.Nop())
	m.Market = db
	source := &fakeTurnoverSource{}
	m.TurnoverSource = source
	item := storage.SyncItem{Stock: c.Stocks[0], Adjusted: &history, Status: "ok", Analyzable: true, Complete: true}
	run := storage.SyncRun{ID: "preview-turnover", AsOf: history.AsOf, StartedAt: m.Now(), FinishedAt: m.Now(), CoveragePct: 100}
	if err := db.CommitCollectedData(context.Background(), run, []storage.SyncItem{item}, "none"); err != nil {
		t.Fatal(err)
	}
	if cached := m.refreshTurnoverMetrics(context.Background(), []storage.SyncItem{item}); cached[history.Code].Validate() != nil {
		t.Fatalf("adjusted-only turnover cache = %+v", cached[history.Code])
	}

	preview, err := m.Preview(history.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Selected) == 0 || preview.Selected[0].Ranking == nil {
		t.Fatalf("preview has no selected ranking: %+v", preview)
	}
	trapped := preview.Selected[0].Ranking.Trapped
	if trapped == nil || trapped.Version != 3 || trapped.Status == "unknown" {
		t.Fatalf("preview trapped signal = %+v", trapped)
	}
	if trapped.Sessions != 120 || trapped.TurnoverSessions != 120 || trapped.TurnoverCoveragePct != 100 {
		t.Fatalf("preview turnover window = %+v", trapped)
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

func turnoverMetricsFor(raw market.History, omit string) market.DailyMetricHistory {
	metrics := market.DailyMetricHistory{Version: 1, Code: raw.Code, Source: "sohu", FetchedAt: raw.FetchedAt}
	for _, bar := range raw.Bars {
		if bar.Volume <= 0 || bar.Date == omit {
			continue
		}
		metrics.Metrics = append(metrics.Metrics, market.DailyMetric{Date: bar.Date, TurnoverRatePct: 1, AmountCNY: 1e8})
	}
	return metrics
}

func trappedPreviewHistory(total int, asof string) market.History {
	bars := make([]market.Bar, total)
	day, _ := time.Parse("2006-01-02", asof)
	for i := total - 1; i >= 0; i-- {
		for day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			day = day.AddDate(0, 0, -1)
		}
		close := 50 + 0.1*float64(i) + 0.4*math.Sin(float64(i)*1.5)
		bars[i] = market.Bar{Date: day.Format("2006-01-02"), Open: close + 0.2, High: 100, Low: 1, Close: close, Volume: 100}
		day = day.AddDate(0, 0, -1)
	}
	bars[len(bars)-1].Volume = 150
	return market.History{
		Version: 1, Code: "sz000001", Name: "正常股票", Source: "tencent", Adjust: "none",
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
