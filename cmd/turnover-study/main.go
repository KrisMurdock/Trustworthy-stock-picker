package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/turnoverstudy"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dataDir      = flag.String("data-dir", "data", "stock-watcher data directory")
		from         = flag.String("from", "", "first signal date (default: after daily feature warmup)")
		through      = flag.String("through", "", "last signal date (default: latest saved quote)")
		topN         = flag.Int("top", 5, "top candidates per day")
		horizon      = flag.Int("horizon", 5, "holding horizon in trading days")
		minTrainDays = flag.Int("min-train-days", 40, "minimum prior signal dates before the first evaluation")
		retrainEvery = flag.Int("retrain-every", 5, "retrain frequency in evaluation days")
		ridge        = flag.Float64("ridge", 1.0, "L2 regularization for the research logistic model")
		bootstrap    = flag.Int("bootstrap", 2000, "date-cluster bootstrap resamples")
		seed         = flag.Int64("seed", 20261026, "bootstrap seed")
		placeboSeed  = flag.Int64("placebo-seed", 0, "permute turnover features across stocks within each date (0 disables the placebo control)")
		workers      = flag.Int("workers", 4, "parallel BaoStock metric fetches")
		cachePath    = flag.String("cache", ".scratch/turnover-study/metrics.json", "daily metric cache")
		reportPath   = flag.String("report", ".scratch/turnover-study/report.json", "JSON report output")
		refresh      = flag.Bool("refresh", false, "refetch cached daily metrics")
		skipFetch    = flag.Bool("offline", false, "fail rather than fetch missing daily metrics")
		requireCIFS  = flag.Bool("require-cifs", false, "require the market data directory to be CIFS")
	)
	flag.Parse()
	if *through == "" {
		*through = time.Now().In(market.Shanghai).Format(time.DateOnly)
	}
	fromTime, err := time.Parse(time.DateOnly, *through)
	if err != nil {
		return errors.New("through must be YYYY-MM-DD")
	}
	loadFrom := fromTime.AddDate(-2, 0, 0).Format(time.DateOnly)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.DataDir = *dataDir
	cfg.RequireCIFS = *requireCIFS
	db, err := storage.OpenMarketView(cfg.DataDir, cfg.RequireCIFS)
	if err != nil {
		return err
	}
	defer db.Close()
	codes := make([]string, 0, len(cfg.Stocks))
	for _, stock := range cfg.Stocks {
		codes = append(codes, stock.Code)
	}
	adjusted, err := db.LoadDailyTrainingBars(context.Background(), codes, loadFrom, *through, "qfq")
	if err != nil {
		return err
	}
	raw, err := db.LoadDailyTrainingBars(context.Background(), codes, loadFrom, *through, "none")
	if err != nil {
		return err
	}
	tradingDays, err := calendarDays(context.Background(), db, adjusted, *through)
	if err != nil {
		return err
	}
	if len(tradingDays) == 0 {
		return errors.New("no trading days in the selected window")
	}
	qfqStart := earliestAdjustDate(adjusted)
	if qfqStart == "" {
		return errors.New("no adjusted daily bars found")
	}
	if *from == "" {
		index := min(65, len(tradingDays)-1)
		*from = tradingDays[index]
	}
	loggingOptions, err := logging.EnvOptions()
	if err != nil {
		return err
	}
	loggingOptions.Stdout = os.Stderr
	logger, err := logging.New(loggingOptions)
	if err != nil {
		return err
	}
	defer logger.Close()

	metrics, err := loadMetrics(context.Background(), cfg, logger, *cachePath, qfqStart, *through, *refresh, *skipFetch, *workers)
	if err != nil {
		return err
	}
	calCfg := cfg
	calCfg.Calendar.ValidFrom = tradingDays[0]
	calCfg.Calendar.ValidThrough = tradingDays[len(tradingDays)-1]
	calCfg.Calendar.TradingDays = tradingDays
	cal := calendar.New(calCfg)
	report, err := turnoverstudy.Run(context.Background(), turnoverstudy.Data{
		Stocks: cfg.Stocks, Adjusted: adjusted, Raw: raw, Metrics: metrics, Calendar: cal, TradingDays: tradingDays,
	}, turnoverstudy.Options{From: *from, Through: *through, TopN: *topN, Horizon: *horizon,
		MinTrainDays: *minTrainDays, RetrainEvery: *retrainEvery, Ridge: *ridge, Bootstrap: *bootstrap, BootstrapSeed: *seed, PlaceboSeed: *placeboSeed})
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*reportPath), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(*reportPath, append(payload, '\n'), 0600); err != nil {
		return err
	}
	printSummary(report, *reportPath)
	return nil
}

func calendarDays(ctx context.Context, db *storage.MarketDB, adjusted map[string][]market.Bar, through string) ([]string, error) {
	start := earliestAdjustDate(adjusted)
	if start == "" {
		return nil, errors.New("no adjusted bars for calendar range")
	}
	return db.CalendarDays(ctx, start, through)
}

func earliestAdjustDate(bars map[string][]market.Bar) string {
	start := ""
	for _, rows := range bars {
		if len(rows) == 0 {
			continue
		}
		if start == "" || rows[0].Date < start {
			start = rows[0].Date
		}
	}
	return start
}

func loadMetrics(ctx context.Context, cfg config.Config, logger *logging.Logger, path, from, through string, refresh, offline bool, workers int) (map[string][]market.DailyMetric, error) {
	cache := map[string]market.DailyMetricHistory{}
	if !refresh {
		if payload, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(payload, &cache); err != nil {
				return nil, fmt.Errorf("read metric cache: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	need := []config.Stock{}
	for _, stock := range cfg.Stocks {
		history, ok := cache[stock.Code]
		// A recent listing can legitimately start after the requested global
		// from-date. The feature extractor skips stocks without a full
		// 21-observation warmup, so only the cache tail must cover the study end.
		if !ok || history.Validate() != nil || history.FetchedAt.IsZero() || len(history.Metrics) == 0 || history.Metrics[len(history.Metrics)-1].Date < through {
			need = append(need, stock)
		}
	}
	if len(need) > 0 && offline {
		return nil, fmt.Errorf("metric cache is incomplete: %d stocks need fetching", len(need))
	}
	if len(need) > 0 {
		client := market.NewBaoStockClient(20*time.Second, 350*time.Millisecond)
		defer client.Close()
		if workers < 1 {
			workers = 1
		}
		jobs := make(chan config.Stock)
		var mu sync.Mutex
		var firstErr error
		var wg sync.WaitGroup
		for worker := 0; worker < workers; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for stock := range jobs {
					var history market.DailyMetricHistory
					var err error
					for attempt := 0; attempt < 3; attempt++ {
						history, err = client.FetchDailyMetrics(ctx, stock, from, through)
						if err == nil {
							break
						}
						time.Sleep(time.Duration(attempt+1) * time.Second)
					}
					if err != nil {
						logger.Warn("daily metric fetch failed", "code", stock.Code, "error", err)
						mu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("fetch metrics for %s: %w", stock.Code, err)
						}
						mu.Unlock()
						continue
					}
					mu.Lock()
					cache[stock.Code] = history
					mu.Unlock()
					logger.Info("daily metric fetched", "code", stock.Code, "rows", len(history.Metrics))
				}
			}()
		}
		for _, stock := range need {
			jobs <- stock
		}
		close(jobs)
		wg.Wait()
		if firstErr != nil {
			return nil, firstErr
		}
		if err := writeJSONAtomic(path, cache); err != nil {
			return nil, err
		}
	}
	out := make(map[string][]market.DailyMetric, len(cache))
	for code, history := range cache {
		out[code] = history.Metrics
	}
	return out, nil
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, payload, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func printSummary(report turnoverstudy.Report, path string) {
	fmt.Printf("report=%s\n", path)
	fmt.Printf("data stocks=%d metric_stocks=%d feature_stocks=%d metric_rows=%d metric_range=%s..%s\n", report.Data.Stocks, report.Data.MetricStocks, report.Data.FeatureStocks, report.Data.MetricRows, report.Data.MetricFirstDate, report.Data.MetricLastDate)
	fmt.Printf("data feature_rows=%d resolved_labels=%d positive_labels=%d label_coverage=%.2f%% positive_rate=%.2f%%\n", report.Data.FeatureRows, report.Data.ResolvedLabels, report.Data.PositiveLabels, report.Data.LabelCoveragePct, report.Data.PositiveRatePct)
	fmt.Printf("dates signal=%d matured=%d eval=%d training=%d from=%s through=%s\n", report.DateRange.SignalDates, report.DateRange.MaturedSignalDates, report.DateRange.EvalDates, report.DateRange.TrainingDates, report.DateRange.FirstEvalDate, report.DateRange.LastEvalDate)
	fmt.Printf("baseline slots=%d executed=%d mean_executed=%.3f%% mean_slot=%.3f%% win=%.2f%% target=%.2f%% stop=%.2f%% cohort=%.2f%%\n", report.Baseline.Slots, report.Baseline.Executed, report.Baseline.MeanExecutedNetPct, report.Baseline.MeanSlotNetPct, report.Baseline.WinRatePct, report.Baseline.TargetRatePct, report.Baseline.StopRatePct, report.Baseline.CohortCompoundedPct)
	fmt.Printf("candidate slots=%d executed=%d mean_executed=%.3f%% mean_slot=%.3f%% win=%.2f%% target=%.2f%% stop=%.2f%% cohort=%.2f%%\n", report.Candidate.Slots, report.Candidate.Executed, report.Candidate.MeanExecutedNetPct, report.Candidate.MeanSlotNetPct, report.Candidate.WinRatePct, report.Candidate.TargetRatePct, report.Candidate.StopRatePct, report.Candidate.CohortCompoundedPct)
	fmt.Printf("increment mean_executed=%+.3f%% mean_slot=%+.3f%% win=%+.2fpp target=%+.2fpp stop=%+.2fpp cohort=%+.2f%%\n", report.Increment.MeanExecutedNetPct, report.Increment.MeanSlotNetPct, report.Increment.WinRatePct, report.Increment.TargetRatePct, report.Increment.StopRatePct, report.Increment.CohortCompoundedPct)
	fmt.Printf("bootstrap mean_slot_diff=%+.3f%% ci95=[%+.3f%%, %+.3f%%] positive=%.1f%%\n", report.Bootstrap.MeanNetDiff, report.Bootstrap.Lower95Pct, report.Bootstrap.Upper95Pct, report.Bootstrap.PositiveRate)
}
