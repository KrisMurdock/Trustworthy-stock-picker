package monitor

import (
	"context"
	"errors"
	"io/fs"
	"sort"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const turnoverCacheVersion = 1
const turnoverCachePath = "state/turnover-metrics.json"

// TurnoverSource fetches daily turnover observations used only by the trapped
// supply diagnostic. It is deliberately separate from the OHLCV source chain.
type TurnoverSource interface {
	FetchDailyMetrics(context.Context, config.Stock, string, string) (market.DailyMetricHistory, error)
}

type turnoverCache struct {
	Version   int                                  `json:"schema_version"`
	UpdatedAt time.Time                            `json:"updated_at"`
	Histories map[string]market.DailyMetricHistory `json:"histories"`
}

func (m *Monitor) loadTurnoverCache() turnoverCache {
	cache := turnoverCache{Version: turnoverCacheVersion, Histories: map[string]market.DailyMetricHistory{}}
	if m == nil || m.Store == nil {
		return cache
	}
	var stored turnoverCache
	if err := m.Store.Read(turnoverCachePath, &stored); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			m.Log.Warn("Turnover metric cache is unreadable", "error", err.Error())
		}
		return cache
	}
	if stored.Version != turnoverCacheVersion || stored.Histories == nil {
		m.Log.Warn("Turnover metric cache has an unsupported schema", "schema_version", stored.Version)
		return cache
	}
	return stored
}

// refreshTurnoverMetrics keeps the diagnostic cache current. Fetches are
// bounded per stock and never fail the daily report; a missing entry simply
// becomes an explicit unknown trapped signal.
func (m *Monitor) refreshTurnoverMetrics(ctx context.Context, items []storage.SyncItem) map[string]market.DailyMetricHistory {
	cache := m.loadTurnoverCache()
	changed := false
	for _, item := range items {
		if item.Raw == nil || len(item.Raw.Bars) == 0 {
			continue
		}
		start, lastPositive := turnoverWindow(item.Raw)
		if start == "" || lastPositive == "" {
			continue
		}
		if cached, ok := cache.Histories[item.Stock.Code]; ok && turnoverCacheUsable(cached, item.Stock.Code, start, lastPositive) {
			continue
		}
		if m.TurnoverSource == nil {
			m.Log.Debug("Turnover source unavailable; trapped signal will be unknown", "code", item.Stock.Code)
			continue
		}
		fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		history, err := m.TurnoverSource.FetchDailyMetrics(fetchCtx, item.Stock, start, lastPositive)
		cancel()
		if err != nil {
			m.Log.Warn("Turnover metric fetch failed", "code", item.Stock.Code, "start", start, "end", lastPositive, "error", err.Error())
			continue
		}
		if err := history.Validate(); err != nil {
			m.Log.Warn("Turnover metric history is invalid", "code", item.Stock.Code, "error", err.Error())
			continue
		}
		cache.Histories[item.Stock.Code] = history
		changed = true
	}
	if changed {
		cache.Version = turnoverCacheVersion
		cache.UpdatedAt = m.Now()
		if err := m.Store.Write(turnoverCachePath, cache); err != nil {
			m.Log.Warn("Turnover metric cache write failed", "error", err.Error())
		}
	}
	codes := make(map[string]bool, len(items))
	for _, item := range items {
		if item.Raw != nil {
			codes[item.Stock.Code] = true
		}
	}
	return currentTurnoverMetrics(cache, codes)
}

func currentTurnoverMetrics(cache turnoverCache, codes map[string]bool) map[string]market.DailyMetricHistory {
	out := make(map[string]market.DailyMetricHistory, len(cache.Histories))
	for code, history := range cache.Histories {
		if codes[code] {
			out[code] = history
		}
	}
	return out
}

func turnoverWindow(history *market.History) (string, string) {
	if history == nil || len(history.Bars) == 0 {
		return "", ""
	}
	start := len(history.Bars) - strategy.TrappedLookbackSessions
	if start < 0 {
		start = 0
	}
	first := history.Bars[start].Date
	lastPositive := ""
	for _, bar := range history.Bars[start:] {
		if bar.Volume > 0 && bar.Date > lastPositive {
			lastPositive = bar.Date
		}
	}
	return first, lastPositive
}

func turnoverCacheUsable(history market.DailyMetricHistory, code, start, end string) bool {
	if history.Validate() != nil || history.Code != code || len(history.Metrics) == 0 {
		return false
	}
	first := history.Metrics[0].Date
	last := history.Metrics[len(history.Metrics)-1].Date
	return first <= start && last >= end
}

// sortedTurnoverDates is used by tests and keeps metric provenance stable.
func sortedTurnoverDates(history market.DailyMetricHistory) []string {
	dates := make([]string, 0, len(history.Metrics))
	for _, metric := range history.Metrics {
		dates = append(dates, metric.Date)
	}
	sort.Strings(dates)
	return dates
}
