package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/trappedscan"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dataDir        = flag.String("data-dir", "", "data directory; empty uses STOCK_WATCHER_DATA_DIR or built-in default")
		requireCIFS    = flag.Bool("require-cifs", false, "require the data directory to be CIFS")
		asOf           = flag.String("as-of", "", "all-market snapshot date; empty uses latest unadjusted sync")
		topN           = flag.Int("top", trappedscan.DefaultTopN, "number of lowest-trapped stocks to emit")
		minHistory     = flag.Int("min-history-sessions", trappedscan.DefaultMinHistorySessions, "minimum raw sessions before eligibility")
		minAmount      = flag.Float64("min-amount", trappedscan.DefaultMinAmountCNY, "minimum 20-day estimated average traded amount in CNY; 0 disables")
		workers        = flag.Int("workers", trappedscan.DefaultWorkers, "parallel metric workers, 1..8")
		sourceName     = flag.String("source", "sohu", "turnover source: sohu or baostock")
		interval       = flag.Duration("request-interval", 350*time.Millisecond, "per-source request interval")
		cachePath      = flag.String("cache", ".scratch/allmarket-trapped/turnover-metrics.json", "turnover metric cache path; empty disables caching")
		outputPath     = flag.String("output", ".scratch/allmarket-trapped/top50.json", "JSON result path; empty disables the file")
		refresh        = flag.Bool("refresh", false, "ignore and replace the turnover metric cache")
		offline        = flag.Bool("offline", false, "fail instead of fetching missing turnover metrics")
		limit          = flag.Int("limit", 0, "scan only the first N eligible stocks; 0 scans all, intended for smoke tests")
		saveEvery      = flag.Int("save-every", trappedscan.DefaultCacheSaveEvery, "write the metric cache after this many new stocks")
		overallTimeout = flag.Duration("timeout", 2*time.Hour, "overall scan timeout")
		jsonOutput     = flag.Bool("json", false, "print the full result JSON to stdout")
	)
	flag.Parse()
	if *workers < 1 || *workers > 8 {
		return errors.New("workers must be between 1 and 8")
	}
	if *interval < 100*time.Millisecond {
		return errors.New("request interval must be at least 100ms")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	cfg.RequireCIFS = *requireCIFS
	db, err := storage.OpenAllMarketView(cfg.DataDir, cfg.RequireCIFS)
	if err != nil {
		return err
	}
	defer db.Close()
	logOptions, err := logging.EnvOptions()
	if err != nil {
		return err
	}
	logOptions.Stdout = os.Stderr
	logger, err := logging.New(logOptions)
	if err != nil {
		return err
	}
	defer logger.Close()
	ctx, cancel := context.WithTimeout(context.Background(), *overallTimeout)
	defer cancel()

	var metrics trappedscan.MetricsSource
	closeMetrics := func() {}
	if !*offline {
		metrics, closeMetrics, err = buildMetricsPool(*sourceName, *workers, *interval)
		if err != nil {
			return err
		}
	}
	defer closeMetrics()
	scanner := trappedscan.Scanner{Store: db, Metrics: metrics, Log: logger}
	result, err := scanner.Scan(ctx, trappedscan.Options{AsOf: *asOf, TopN: *topN, MinHistorySessions: *minHistory,
		MinAmountCNY: *minAmount, Workers: *workers, CachePath: *cachePath, Refresh: *refresh, Offline: *offline,
		Limit: *limit, CacheSaveEvery: *saveEvery})
	if err != nil {
		return err
	}
	if *outputPath != "" {
		if err := writeJSONAtomic(*outputPath, result); err != nil {
			return err
		}
	}
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
	}
	printResult(result)
	return nil
}

type metricsPool struct {
	sources []trappedscan.MetricsSource
	next    atomic.Uint64
}

func (p *metricsPool) FetchDailyMetrics(ctx context.Context, stock config.Stock, start, end string) (market.DailyMetricHistory, error) {
	index := int(p.next.Add(1)-1) % len(p.sources)
	return p.sources[index].FetchDailyMetrics(ctx, stock, start, end)
}

func buildMetricsPool(name string, workers int, interval time.Duration) (trappedscan.MetricsSource, func(), error) {
	pool := &metricsPool{}
	closeAll := func() {
		for _, source := range pool.sources {
			if client, ok := source.(*market.SohuMetricsClient); ok {
				client.Close()
			}
		}
	}
	for index := 0; index < workers; index++ {
		switch name {
		case "sohu":
			pool.sources = append(pool.sources, market.NewSohuMetricsClient(20*time.Second, interval))
		case "baostock":
			pool.sources = append(pool.sources, market.NewBaoStockClient(20*time.Second, interval))
		default:
			return nil, func() {}, errors.New("source must be sohu or baostock")
		}
	}
	return pool, closeAll, nil
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("cannot create result directory")
	}
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return errors.New("cannot encode scan result")
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(payload, '\n'), 0o600); err != nil {
		return errors.New("cannot write scan result")
	}
	if err := os.Rename(temporary, path); err != nil {
		return errors.New("cannot replace scan result")
	}
	return nil
}

func printResult(result trappedscan.Result) {
	fmt.Printf("全市场低套牢扫描  as_of=%s  universe=%d  raw=%d  eligible=%d  scanned=%d\n",
		result.AsOf, result.UniverseStocks, result.RawHistoryStocks, result.EligibleStocks, result.ScannedStocks)
	fmt.Printf("signals=%d  valid=%d  unknown=%d  fetched=%d  cache_hits=%d  fetch_failures=%d\n",
		result.Signals, result.ValidSignals, result.UnknownSignals, result.FetchedMetrics, result.CacheHits, result.FetchFailures)
	fmt.Printf("status: unknown=%d none=%d watch=%d triggered=%d\n",
		result.StatusCounts["unknown"], result.StatusCounts["none"], result.StatusCounts["watch"], result.StatusCounts["triggered"])
	printCounts("excluded", result.Excluded)
	printUnknownReasons(result.UnknownReasons)
	fmt.Print("distribution:")
	for _, bucket := range result.Distribution {
		fmt.Printf(" %s=%d", bucket.Label, bucket.Count)
	}
	fmt.Println()
	fmt.Println("rank | code | name | close | status | trapped | cost_distance | drawdown | zone | zones | avg_amount_cny")
	for _, candidate := range result.Top {
		fmt.Printf("%d | %s | %s | %.2f | %s | %.2f%% | %+.2f%% | %+.2f%% | %s | %s | %.0f\n",
			candidate.Rank, candidate.Code, candidate.Name, candidate.Close,
			candidate.Trapped.Status, candidate.Trapped.TrappedVolumeRatio*100, candidate.Trapped.OverheadDistancePct,
			candidate.Trapped.DrawdownFromHighPct,
			zoneName(candidate.Trapped.CurrentZone), zoneSummary(candidate.Trapped.Zones),
			candidate.AvgAmount20CNY)
	}
}

func printCounts(label string, counts map[string]int) {
	if len(counts) == 0 {
		fmt.Printf("%s: none\n", label)
		return
	}
	fmt.Printf("%s:", label)
	for _, key := range sortedKeys(counts) {
		fmt.Printf(" %s=%d", key, counts[key])
	}
	fmt.Println()
}

func printUnknownReasons(counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	fmt.Print("unknown_reasons:")
	for _, key := range sortedKeys(counts) {
		fmt.Printf(" %s=%d", key, counts[key])
	}
	fmt.Println()
}

func sortedKeys(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func zoneName(value string) string {
	switch value {
	case "shallow":
		return "浅度"
	case "moderate":
		return "中度"
	case "deep":
		return "深度"
	default:
		return "无上方套牢"
	}
}

func zoneSummary(zones []strategy.TrappedZone) string {
	parts := make([]string, 0, len(zones))
	for _, zone := range zones {
		if zone.SharePct == 0 || zone.Low <= 0 || zone.High <= 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s%d%%/%.2f-%.2f", zone.Label, zone.SharePct, zone.Low, zone.High))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}
