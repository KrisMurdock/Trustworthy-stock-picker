package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const adjustedBarCacheVersion = 2

type adjustedBarCache struct {
	Version   int                       `json:"schema_version"`
	From      string                    `json:"from"`
	Through   string                    `json:"through"`
	Adjust    string                    `json:"adjust"`
	Source    string                    `json:"source"`
	FetchedAt time.Time                 `json:"fetched_at"`
	Histories map[string]market.History `json:"histories"`
}

func loadAdjusted(ctx context.Context, cfg config.Config, logger *logging.Logger, path, from, through string, raw map[string][]market.Bar, refresh, offline bool, workers int) (map[string][]market.Bar, error) {
	cache := adjustedBarCache{Version: adjustedBarCacheVersion, From: from, Through: through, Adjust: "qfq", Source: "sina", Histories: map[string]market.History{}}
	if !refresh {
		payload, err := os.ReadFile(path)
		switch {
		case err == nil:
			var loaded adjustedBarCache
			if err := json.Unmarshal(payload, &loaded); err != nil {
				return nil, fmt.Errorf("read adjusted bar cache: %w", err)
			}
			if adjustedCacheCovers(loaded, from, through) {
				cache = loaded
			} else {
				logger.Info("adjusted bar cache window changed; refetching", "cache_from", loaded.From, "cache_through", loaded.Through, "from", from, "through", through)
			}
		case errors.Is(err, os.ErrNotExist):
		default:
			return nil, err
		}
	}
	expected := expectedStarts(cfg.Stocks, from, raw)
	need := make([]config.Stock, 0, len(cfg.Stocks))
	for _, stock := range cfg.Stocks {
		if expected[stock.Code] == "" {
			continue
		}
		history, ok := cache.Histories[stock.Code]
		if !ok || !adjustedHistoryCovers(history, expected[stock.Code], through) {
			need = append(need, stock)
		}
	}
	if len(need) > 0 && offline {
		return nil, fmt.Errorf("adjusted bar cache is incomplete: %d stocks need fetching", len(need))
	}
	if len(need) > 0 {
		client := market.NewSinaClient(20*time.Second, 350*time.Millisecond)
		defer client.Close()
		if workers < 1 {
			workers = 1
		}
		jobs := make(chan config.Stock)
		var mu sync.Mutex
		var firstErr error
		var cacheErr error
		var wg sync.WaitGroup
		for worker := 0; worker < workers; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for stock := range jobs {
					var history market.History
					var err error
					for attempt := 0; attempt < 3; attempt++ {
						history, err = client.Fetch(ctx, stock, from, through, "qfq")
						if err == nil {
							break
						}
						if ctx.Err() != nil {
							break
						}
						time.Sleep(time.Duration(attempt+1) * time.Second)
					}
					if err != nil {
						logger.Warn("adjusted bar fetch failed", "code", stock.Code, "error", err)
						mu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("fetch adjusted bars for %s: %w", stock.Code, err)
						}
						mu.Unlock()
						continue
					}
					mu.Lock()
					cache.Histories[stock.Code] = history
					cache.FetchedAt = time.Now().In(market.Shanghai)
					if err := writeJSONAtomic(path, cache); err != nil && cacheErr == nil {
						cacheErr = err
					}
					mu.Unlock()
					logger.Info("adjusted bar fetched", "code", stock.Code, "rows", len(history.Bars))
				}
			}()
		}
		for _, stock := range need {
			jobs <- stock
		}
		close(jobs)
		wg.Wait()
		cache.Version = adjustedBarCacheVersion
		cache.From = from
		cache.Through = through
		cache.Adjust = "qfq"
		cache.Source = "sina"
		if cache.FetchedAt.IsZero() {
			cache.FetchedAt = time.Now().In(market.Shanghai)
		}
		if err := writeJSONAtomic(path, cache); err != nil {
			return nil, err
		}
		if cacheErr != nil {
			return nil, cacheErr
		}
		if firstErr != nil {
			return nil, firstErr
		}
	}
	out := make(map[string][]market.Bar, len(cache.Histories))
	for code, history := range cache.Histories {
		out[code] = history.Bars
	}
	return out, nil
}

func adjustedCacheCovers(cache adjustedBarCache, from, through string) bool {
	return cache.Version == adjustedBarCacheVersion && cache.Adjust == "qfq" && cache.Source == "sina" &&
		cache.From != "" && cache.Through != "" && cache.From <= from && cache.Through >= through && len(cache.Histories) > 0
}

func adjustedHistoryCovers(history market.History, expectedStart, through string) bool {
	if history.Validate() != nil || history.Adjust != "qfq" || history.Source != "sina" || history.AsOf < through || len(history.Bars) == 0 {
		return false
	}
	limit, err := addCalendarDays(expectedStart, 120)
	if err != nil {
		return false
	}
	return history.Bars[0].Date <= limit
}

func expectedStarts(stocks []config.Stock, from string, raw map[string][]market.Bar) map[string]string {
	out := make(map[string]string, len(stocks))
	for _, stock := range stocks {
		if len(raw[stock.Code]) == 0 {
			out[stock.Code] = ""
			continue
		}
		expected := from
		if rows := raw[stock.Code]; rows[0].Date > expected {
			expected = rows[0].Date
		}
		out[stock.Code] = expected
	}
	return out
}

func addCalendarDays(date string, days int) (string, error) {
	parsed, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return "", err
	}
	return parsed.AddDate(0, 0, days).Format(time.DateOnly), nil
}
