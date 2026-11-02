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
		raw := effectiveRawHistory(item)
		if raw == nil || len(raw.Bars) == 0 {
			continue
		}
		start, lastPositive, required := turnoverWindow(raw)
		if start == "" || lastPositive == "" {
			continue
		}
		if cached, ok := cache.Histories[item.Stock.Code]; ok && turnoverCacheUsable(cached, item.Stock.Code, required) {
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
		if !turnoverCacheUsable(history, item.Stock.Code, required) {
			m.Log.Warn("Turnover metric history is incomplete", "code", item.Stock.Code, "start", start, "end", lastPositive)
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
		if effectiveRawHistory(item) != nil {
			codes[item.Stock.Code] = true
		}
	}
	return currentTurnoverMetrics(cache, codes)
}

// effectiveRawHistory returns the unadjusted prices used by the trapped
// diagnostic. When the analysis window itself is unadjusted, callers do not
// need to fetch the same history twice; the adjusted side is already raw.
func effectiveRawHistory(item storage.SyncItem) *market.History {
	if item.Raw != nil {
		return item.Raw
	}
	if item.Adjusted != nil && item.Adjusted.Adjust == "none" {
		return item.Adjusted
	}
	return nil
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

func turnoverWindow(history *market.History) (string, string, []string) {
	if history == nil || len(history.Bars) == 0 {
		return "", "", nil
	}
	start := len(history.Bars) - strategy.TrappedLookbackSessions
	if start < 0 {
		start = 0
	}
	required := make([]string, 0, strategy.TrappedLookbackSessions)
	for _, bar := range history.Bars[start:] {
		if bar.Volume > 0 {
			required = append(required, bar.Date)
		}
	}
	if len(required) == 0 {
		return "", "", nil
	}
	sort.Strings(required)
	deduped := required[:0]
	for _, date := range required {
		if len(deduped) == 0 || deduped[len(deduped)-1] != date {
			deduped = append(deduped, date)
		}
	}
	return deduped[0], deduped[len(deduped)-1], deduped
}

func turnoverCacheUsable(history market.DailyMetricHistory, code string, required []string) bool {
	if history.Validate() != nil || history.Code != code || len(required) == 0 {
		return false
	}
	for _, date := range required {
		index := sort.Search(len(history.Metrics), func(i int) bool { return history.Metrics[i].Date >= date })
		if index == len(history.Metrics) || history.Metrics[index].Date != date {
			return false
		}
	}
	return true
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
