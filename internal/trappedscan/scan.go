// Package trappedscan builds an offline, cross-sectional view of the
// turnover-adjusted trapped-supply diagnostic across the all-market database.
// It deliberately does not participate in daily selection or notification.
package trappedscan

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const (
	SchemaVersion = 1

	DefaultTopN               = 50
	DefaultMinHistorySessions = 250
	DefaultMinAmountCNY       = 20_000_000
	DefaultWorkers            = 2
	DefaultCacheSaveEvery     = 200
)

// Store is the read-only all-market slice needed by the scanner.
type Store interface {
	LatestUnadjustedAsOf(context.Context) (string, error)
	LoadUnadjustedHistories(context.Context, string, string) ([]config.Stock, []market.History, error)
}

// MetricsSource supplies daily turnover and amount observations.
type MetricsSource interface {
	FetchDailyMetrics(context.Context, config.Stock, string, string) (market.DailyMetricHistory, error)
}

type Options struct {
	AsOf               string
	TopN               int
	MinHistorySessions int
	MinAmountCNY       float64
	Workers            int
	CachePath          string
	Refresh            bool
	Offline            bool
	Limit              int
	CacheSaveEvery     int
}

type Scanner struct {
	Store   Store
	Metrics MetricsSource
	Log     *logging.Logger
	Now     func() time.Time
}

type Candidate struct {
	Rank           int                     `json:"rank"`
	Code           string                  `json:"code"`
	Name           string                  `json:"name"`
	Close          float64                 `json:"close"`
	AvgAmount20CNY float64                 `json:"avg_amount_20d_cny"`
	Trapped        *strategy.TrappedSignal `json:"trapped_signal"`
}

type RatioBucket struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type Result struct {
	SchemaVersion      int            `json:"schema_version"`
	AsOf               string         `json:"as_of"`
	HistoryFrom        string         `json:"history_from"`
	GeneratedAt        time.Time      `json:"generated_at"`
	UniverseStocks     int            `json:"universe_stocks"`
	RawHistoryStocks   int            `json:"raw_history_stocks"`
	EligibleStocks     int            `json:"eligible_stocks"`
	Signals            int            `json:"signals"`
	ValidSignals       int            `json:"valid_signals"`
	UnknownSignals     int            `json:"unknown_signals"`
	FetchedMetrics     int            `json:"fetched_metrics"`
	CacheHits          int            `json:"cache_hits"`
	FetchFailures      int            `json:"fetch_failures"`
	Excluded           map[string]int `json:"excluded"`
	UnknownReasons     map[string]int `json:"unknown_reasons"`
	StatusCounts       map[string]int `json:"status_counts"`
	Distribution       []RatioBucket  `json:"distribution"`
	Top                []Candidate    `json:"top"`
	MinHistorySessions int            `json:"min_history_sessions"`
	MinAmountCNY       float64        `json:"min_amount_cny"`
	TopN               int            `json:"top_n"`
	ScannedStocks      int            `json:"scanned_stocks"`
}

type workItem struct {
	stock       config.Stock
	history     market.History
	avgAmount   float64
	required    []string
	windowStart string
	windowEnd   string
}

type metricCollector struct {
	mu          sync.Mutex
	metrics     map[string]market.DailyMetricHistory
	cachePath   string
	saveEvery   int
	dirty       int
	fetched     int
	failed      int
	cacheHits   int
	failureLogs int
	cacheErr    error
}

// Scan loads one frozen all-market snapshot, fills missing turnover metrics,
// and returns the lowest trapped-volume eligible stocks.
func (s *Scanner) Scan(ctx context.Context, options Options) (Result, error) {
	result := Result{SchemaVersion: SchemaVersion, Excluded: map[string]int{},
		UnknownReasons: map[string]int{}, StatusCounts: map[string]int{}}
	if s == nil || s.Store == nil {
		return result, errors.New("trapped scan store is not configured")
	}
	if options.TopN <= 0 {
		options.TopN = DefaultTopN
	}
	if options.MinHistorySessions <= 0 {
		options.MinHistorySessions = DefaultMinHistorySessions
	}
	if options.MinHistorySessions < strategy.TrappedLookbackSessions {
		return result, fmt.Errorf("min history sessions must be at least %d", strategy.TrappedLookbackSessions)
	}
	if options.MinAmountCNY < 0 {
		return result, errors.New("min amount must not be negative")
	}
	if options.Workers <= 0 {
		options.Workers = DefaultWorkers
	}
	if options.Workers > 8 {
		return result, errors.New("workers must be between 1 and 8")
	}
	if options.CacheSaveEvery <= 0 {
		options.CacheSaveEvery = DefaultCacheSaveEvery
	}
	if options.Limit < 0 {
		return result, errors.New("limit must not be negative")
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	log := s.Log
	if log == nil {
		log = logging.Nop()
	}
	asOf := strings.TrimSpace(options.AsOf)
	if asOf == "" {
		var err error
		asOf, err = s.Store.LatestUnadjustedAsOf(ctx)
		if err != nil {
			return result, err
		}
	}
	asOfTime, err := time.Parse("2006-01-02", asOf)
	if err != nil {
		return result, errors.New("as-of must be YYYY-MM-DD")
	}
	result.AsOf = asOf
	result.GeneratedAt = now().In(market.Shanghai)
	result.MinHistorySessions = options.MinHistorySessions
	result.MinAmountCNY = options.MinAmountCNY
	result.TopN = options.TopN
	from := asOfTime.AddDate(-2, 0, 0).Format("2006-01-02")
	result.HistoryFrom = from
	log.Info("All-market trapped scan started", "as_of", asOf, "from", from, "top_n", options.TopN, "workers", options.Workers)
	stocks, histories, err := s.Store.LoadUnadjustedHistories(ctx, asOf, from)
	if err != nil {
		return result, err
	}
	result.UniverseStocks = len(stocks)
	result.RawHistoryStocks = len(histories)
	stockByCode := make(map[string]config.Stock, len(stocks))
	for _, stock := range stocks {
		stockByCode[stock.Code] = stock
	}
	items := make([]workItem, 0, len(histories))
	for _, history := range histories {
		stock, ok := stockByCode[history.Code]
		if !ok || history.Adjust != "none" || len(history.Bars) == 0 {
			result.Excluded["invalid_history"]++
			continue
		}
		if market.IsSTStockName(stock.Name) {
			result.Excluded["st_stock"]++
			continue
		}
		if len(history.Bars) < options.MinHistorySessions {
			result.Excluded["insufficient_history"]++
			continue
		}
		if history.Bars[len(history.Bars)-1].Date != asOf {
			result.Excluded["stale_last_bar"]++
			continue
		}
		avgAmount := averageAmountCNY(history, 20)
		if options.MinAmountCNY > 0 && avgAmount < options.MinAmountCNY {
			result.Excluded["low_liquidity"]++
			continue
		}
		start, end, required := turnoverWindow(history)
		items = append(items, workItem{stock: stock, history: history, avgAmount: avgAmount,
			required: required, windowStart: start, windowEnd: end})
	}
	result.EligibleStocks = len(items)
	if options.Limit > 0 && len(items) > options.Limit {
		items = items[:options.Limit]
	}
	result.ScannedStocks = len(items)
	log.Info("All-market trapped scan eligible set", "eligible", result.EligibleStocks, "scanned", result.ScannedStocks, "excluded", result.Excluded)

	cache, err := loadMetricCache(options.CachePath)
	if err != nil {
		return result, err
	}
	if options.Refresh {
		cache = map[string]market.DailyMetricHistory{}
	}
	need := make([]workItem, 0, len(items))
	for _, item := range items {
		if len(item.required) == 0 {
			continue
		}
		if history, ok := cache[item.stock.Code]; ok && metricsCover(history, item.required) {
			result.CacheHits++
			continue
		}
		need = append(need, item)
	}
	if options.Offline && len(need) > 0 {
		return result, fmt.Errorf("metric cache is incomplete: %d stocks need fetching", len(need))
	}
	collector := &metricCollector{metrics: cache, cachePath: options.CachePath, saveEvery: options.CacheSaveEvery, cacheHits: result.CacheHits}
	if len(need) > 0 {
		if s.Metrics == nil {
			return result, errors.New("trapped scan metric source is not configured")
		}
		if err := s.fetchMetrics(ctx, need, options.Workers, collector, log); err != nil {
			return result, err
		}
	}
	if err := collector.flush(); err != nil {
		log.Warn("Turnover metric cache flush failed", "error", err.Error())
	}
	result.FetchedMetrics = collector.fetched
	result.FetchFailures = collector.failed
	if collector.cacheErr != nil {
		log.Warn("Turnover metric cache write failed", "error", collector.cacheErr.Error())
	}

	candidates := make([]Candidate, 0, len(items))
	distribution := newRatioDistribution()
	for _, item := range items {
		metrics, ok := collector.metric(item.stock.Code)
		var metricPtr *market.DailyMetricHistory
		if ok {
			copy := metrics
			metricPtr = &copy
		}
		signal := strategy.AnalyzeTrappedSignalV3(item.history, metricPtr)
		result.Signals++
		result.StatusCounts[signal.Status]++
		if signal.Status == strategy.TrappedStatusUnknown {
			result.UnknownSignals++
			reason := "unknown"
			if len(signal.Reasons) > 0 && signal.Reasons[0] != "" {
				reason = signal.Reasons[0]
			}
			result.UnknownReasons[reason]++
			continue
		}
		result.ValidSignals++
		distribution.add(signal.TrappedVolumeRatio)
		candidates = append(candidates, Candidate{Code: item.stock.Code, Name: item.stock.Name,
			Close: signal.CurrentPrice, AvgAmount20CNY: item.avgAmount, Trapped: signal})
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i].Trapped.TrappedVolumeRatio, candidates[j].Trapped.TrappedVolumeRatio
		if math.Abs(left-right) < 1e-12 {
			if math.Abs(candidates[i].AvgAmount20CNY-candidates[j].AvgAmount20CNY) > 1 {
				return candidates[i].AvgAmount20CNY > candidates[j].AvgAmount20CNY
			}
			return candidates[i].Code < candidates[j].Code
		}
		return left < right
	})
	if len(candidates) > options.TopN {
		candidates = candidates[:options.TopN]
	}
	for index := range candidates {
		candidates[index].Rank = index + 1
	}
	result.Top = candidates
	result.Distribution = distribution.rows()
	log.Info("All-market trapped scan completed", "signals", result.Signals, "valid", result.ValidSignals,
		"unknown", result.UnknownSignals, "fetched", result.FetchedMetrics, "cache_hits", result.CacheHits,
		"fetch_failures", result.FetchFailures)
	return result, nil
}

func (s *Scanner) fetchMetrics(ctx context.Context, items []workItem, workers int, collector *metricCollector, log *logging.Logger) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan workItem)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				if workCtx.Err() != nil {
					return
				}
				history, err := s.fetchOne(workCtx, item)
				if err != nil {
					collector.recordFailure(item.stock.Code, err, log)
					continue
				}
				if !metricsCover(history, item.required) {
					collector.recordFailure(item.stock.Code, errors.New("turnover history incomplete"), log)
					continue
				}
				collector.recordMetric(item.stock.Code, history, log)
			}
		}()
	}
	for _, item := range items {
		select {
		case jobs <- item:
		case <-workCtx.Done():
			errMu.Lock()
			firstErr = workCtx.Err()
			errMu.Unlock()
			break
		}
		if workCtx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil && !errors.Is(firstErr, context.Canceled) {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (s *Scanner) fetchOne(ctx context.Context, item workItem) (market.DailyMetricHistory, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		fetchCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		history, err := s.Metrics.FetchDailyMetrics(fetchCtx, item.stock, item.windowStart, item.windowEnd)
		cancel()
		if err == nil {
			return history, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return market.DailyMetricHistory{}, ctx.Err()
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return market.DailyMetricHistory{}, ctx.Err()
		case <-timer.C:
		}
	}
	return market.DailyMetricHistory{}, lastErr
}

func (c *metricCollector) recordMetric(code string, history market.DailyMetricHistory, log *logging.Logger) {
	c.mu.Lock()
	c.metrics[code] = history
	c.dirty++
	c.fetched++
	fetched := c.fetched
	shouldSave := c.cachePath != "" && c.dirty >= c.saveEvery
	if shouldSave {
		c.dirty = 0
		if err := writeMetricCacheLocked(c.cachePath, c.metrics); err != nil {
			c.cacheErr = err
		}
	}
	c.mu.Unlock()
	if fetched%200 == 0 {
		log.Info("Turnover metric fetch progress", "fetched", fetched, "cache", c.snapshotCounts())
	}
}

func (c *metricCollector) recordFailure(code string, err error, log *logging.Logger) {
	c.mu.Lock()
	c.failed++
	failed := c.failed
	show := c.failureLogs < 20
	if show {
		c.failureLogs++
	}
	c.mu.Unlock()
	if show {
		log.Warn("Turnover metric fetch failed", "code", code, "error", err.Error(), "failures", failed)
	}
}

func (c *metricCollector) metric(code string) (market.DailyMetricHistory, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	history, ok := c.metrics[code]
	return history, ok
}

func (c *metricCollector) snapshotCounts() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fmt.Sprintf("cache_hits=%d failures=%d", c.cacheHits, c.failed)
}

func (c *metricCollector) flush() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cachePath == "" {
		return nil
	}
	return writeMetricCacheLocked(c.cachePath, c.metrics)
}

func averageAmountCNY(history market.History, lookback int) float64 {
	start := max(0, len(history.Bars)-lookback)
	var total float64
	var count int
	for _, bar := range history.Bars[start:] {
		if !validBar(bar) || bar.Volume <= 0 || bar.Close <= 0 {
			continue
		}
		shares := market.ProviderVolumeShares(history.Source, history.Code, bar.Volume)
		if shares <= 0 {
			continue
		}
		total += shares * bar.Close
		count++
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func turnoverWindow(history market.History) (string, string, []string) {
	if len(history.Bars) == 0 {
		return "", "", nil
	}
	start := max(0, len(history.Bars)-strategy.TrappedLookbackSessions)
	window := history.Bars[start:]
	required := make([]string, 0, len(window))
	for _, bar := range window {
		if validBar(bar) && bar.Volume > 0 {
			required = append(required, bar.Date)
		}
	}
	if len(required) == 0 {
		return window[0].Date, window[len(window)-1].Date, nil
	}
	sort.Strings(required)
	deduped := required[:0]
	for _, date := range required {
		if len(deduped) == 0 || deduped[len(deduped)-1] != date {
			deduped = append(deduped, date)
		}
	}
	return window[0].Date, window[len(window)-1].Date, deduped
}

func validBar(bar market.Bar) bool {
	if bar.Close <= 0 || bar.High < math.Max(bar.Open, bar.Close) || bar.Low > math.Min(bar.Open, bar.Close) || bar.Low <= 0 {
		return false
	}
	for _, value := range []float64{bar.Open, bar.Close, bar.High, bar.Low, bar.Volume} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return false
		}
	}
	return true
}

type ratioDistribution struct {
	buckets []RatioBucket
}

func newRatioDistribution() *ratioDistribution {
	return &ratioDistribution{buckets: []RatioBucket{
		{Label: "0-5%"}, {Label: "5-10%"}, {Label: "10-20%"}, {Label: "20-30%"},
		{Label: "30-50%"}, {Label: "50-100%"},
	}}
}

func (d *ratioDistribution) add(value float64) {
	index := 0
	switch {
	case value >= 1:
		index = 5
	case value >= 0.5:
		index = 5
	case value >= 0.3:
		index = 4
	case value >= 0.2:
		index = 3
	case value >= 0.1:
		index = 2
	case value >= 0.05:
		index = 1
	}
	d.buckets[index].Count++
}

func (d *ratioDistribution) rows() []RatioBucket {
	return append([]RatioBucket(nil), d.buckets...)
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
