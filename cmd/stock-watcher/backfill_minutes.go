package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

const minuteBackfillDefaultTradingDays = 200

type minuteFetchResult struct {
	stock   config.Stock
	archive market.MinuteArchive
	err     error
}

type minuteBackfillStockResult struct {
	Code            string   `json:"code"`
	Name            string   `json:"name,omitempty"`
	Status          string   `json:"status"`
	ExpectedDays    int      `json:"expected_days"`
	CompleteDays    int      `json:"complete_days"`
	MissingSessions int      `json:"missing_sessions"`
	PartialSessions int      `json:"partial_sessions"`
	FetchedBars     int      `json:"fetched_bars"`
	InsertedBars    int      `json:"inserted_bars"`
	ExistingBars    int      `json:"existing_bars"`
	OverlapMismatch int      `json:"overlap_mismatch"`
	MissingDays     []string `json:"missing_days,omitempty"`
	PartialDays     []string `json:"partial_days,omitempty"`
	Skipped         bool     `json:"skipped,omitempty"`
	Error           string   `json:"error,omitempty"`
}

type minuteBackfillReport struct {
	Command          string                      `json:"command"`
	Mode             string                      `json:"mode"`
	Provider         string                      `json:"provider"`
	From             string                      `json:"from"`
	AsOf             string                      `json:"as_of"`
	TradingDays      int                         `json:"trading_days"`
	Stocks           int                         `json:"stocks"`
	CompleteStocks   int                         `json:"complete_stocks"`
	IncompleteStocks int                         `json:"incomplete_stocks"`
	FailedStocks     int                         `json:"failed_stocks"`
	FetchedBars      int                         `json:"fetched_bars"`
	InsertedBars     int                         `json:"inserted_bars"`
	ExistingBars     int                         `json:"existing_bars"`
	OverlapMismatch  int                         `json:"overlap_mismatch"`
	MissingSessions  int                         `json:"missing_sessions"`
	PartialSessions  int                         `json:"partial_sessions"`
	SkippedStocks    int                         `json:"skipped_stocks"`
	StagingDataDir   string                      `json:"staging_data_dir,omitempty"`
	Results          []minuteBackfillStockResult `json:"results"`
}

func runMinuteBackfill(ctx context.Context, c config.Config, db *storage.MarketDB, log *logging.Logger, asof, from string, workers int, provider, codesCSV, importFrom, referenceDir string) error {
	if workers < 1 || workers > 8 {
		return errors.New("backfill workers must be between 1 and 8")
	}
	stocks, err := minuteBackfillStocks(c, codesCSV)
	if err != nil {
		return err
	}
	cal := calendar.New(c)
	completed, err := cal.Completed(time.Now())
	if err != nil {
		return err
	}
	if asof == "" {
		asof = completed
	}
	if asof > completed {
		return fmt.Errorf("backfill as-of %s is not a completed session (latest %s)", asof, completed)
	}
	trading, err := cal.Trading(asof)
	if err != nil || !trading {
		return fmt.Errorf("backfill as-of %s is not a trading day", asof)
	}
	allDays, err := cal.TradingDays(cal.ValidFrom(), asof)
	if err != nil || len(allDays) == 0 {
		return errors.New("cannot determine minute backfill trading range")
	}
	if from == "" {
		first := 0
		if len(allDays) > minuteBackfillDefaultTradingDays {
			first = len(allDays) - minuteBackfillDefaultTradingDays
		}
		from = allDays[first]
	}
	days, err := cal.TradingDays(from, asof)
	if err != nil || len(days) == 0 {
		return fmt.Errorf("invalid minute backfill range %s..%s", from, asof)
	}
	from = days[0]
	provider, err = minuteProviderForRange(provider, len(days))
	if err != nil {
		return err
	}
	tradingSet := make(map[string]bool, len(days))
	for _, day := range days {
		tradingSet[day] = true
	}
	mode := "fetch"
	if importFrom != "" {
		mode = "stage_import"
	}
	report := minuteBackfillReport{Command: "backfill-minutes", Mode: mode, Provider: provider, From: from, AsOf: asof, TradingDays: len(days), Stocks: len(stocks), StagingDataDir: importFrom}
	byCode := make(map[string]minuteBackfillStockResult, len(stocks))
	coverageDB := db
	if importFrom != "" && referenceDir != "" {
		return errors.New("reference-dir cannot be combined with import-from")
	}
	if mode == "fetch" && referenceDir != "" {
		reference, err := storage.OpenMarketView(referenceDir, false)
		if err != nil {
			return fmt.Errorf("open minute reference data: %w", err)
		}
		defer reference.Close()
		coverageDB = reference
	}
	if mode == "fetch" {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		log.Info("Minute backfill started", "mode", mode, "provider", provider, "from", from, "as_of", asof, "stocks", len(stocks), "workers", workers)
		pending := make([]config.Stock, 0, len(stocks))
		for _, stock := range stocks {
			expected, err := expectedMinuteDays(ctx, coverageDB, stock.Code, from, asof, c.Daily.Adjust, tradingSet)
			if err != nil {
				return err
			}
			counts, err := db.LoadMinuteDayCounts(ctx, stock.Code, from, asof)
			if err != nil {
				return err
			}
			if len(expected) > 0 && minuteCoverageComplete(expected, counts) {
				imported := storage.MinuteImportResult{DayBars: counts, Existing: minuteBarCount(counts)}
				result, err := summarizeMinuteBackfill(ctx, coverageDB, stock, market.MinuteArchive{From: from, Through: asof}, imported, c.Daily.Adjust, tradingSet)
				if err != nil {
					return err
				}
				result.Skipped = true
				byCode[stock.Code] = result
				report.SkippedStocks++
				continue
			}
			pending = append(pending, stock)
		}
		if len(pending) > 0 {
			log.Info("Minute backfill pending stocks", "pending", len(pending), "skipped", report.SkippedStocks)
		}
		for fetched := range fetchMinuteArchives(ctx, c, pending, from, asof, workers, provider) {
			if fetched.err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				byCode[fetched.stock.Code] = minuteBackfillStockResult{Code: fetched.stock.Code, Name: fetched.stock.Name, Status: "failed", Error: fetched.err.Error()}
				log.Info("Minute backfill stock failed", "code", fetched.stock.Code, "error", fetched.err)
				continue
			}
			imported, err := db.ImportMinuteArchive(ctx, fetched.stock, fetched.archive)
			if err != nil {
				cancel()
				return fmt.Errorf("import minute archive %s: %w", fetched.stock.Code, err)
			}
			result, err := summarizeMinuteBackfill(ctx, coverageDB, fetched.stock, fetched.archive, imported, c.Daily.Adjust, tradingSet)
			if err != nil {
				cancel()
				return err
			}
			byCode[fetched.stock.Code] = result
			log.Info("Minute backfill stock completed", "code", fetched.stock.Code, "fetched", result.FetchedBars, "inserted", result.InsertedBars, "existing", result.ExistingBars, "status", result.Status)
		}
	} else {
		stage, err := storage.OpenMarketView(importFrom, false)
		if err != nil {
			return fmt.Errorf("open minute staging data: %w", err)
		}
		defer stage.Close()
		log.Info("Minute backfill import started", "from", from, "as_of", asof, "stocks", len(stocks), "staging", importFrom)
		start, _ := time.ParseInLocation("2006-01-02", from, market.Shanghai)
		end, _ := time.ParseInLocation("2006-01-02", asof, market.Shanghai)
		for _, stock := range stocks {
			barsByCode, err := stage.LoadMinuteTrainingBars(ctx, []string{stock.Code}, start, end.AddDate(0, 0, 1))
			if err != nil {
				return fmt.Errorf("read staged minutes %s: %w", stock.Code, err)
			}
			bars := barsByCode[stock.Code]
			if len(bars) == 0 {
				byCode[stock.Code] = minuteBackfillStockResult{Code: stock.Code, Name: stock.Name, Status: "failed", Error: "no staged minute bars"}
				continue
			}
			archive := market.MinuteArchive{
				Version: 1, Code: stock.Code, Name: stock.Name, Source: "baostock", Period: 5, BasePeriod: 5,
				Adjust: "none", VolumeUnit: "provider_raw", From: from, Through: asof, FetchedAt: time.Now().In(market.Shanghai), Bars: bars,
			}
			imported, err := db.ImportMinuteArchive(ctx, stock, archive)
			if err != nil {
				return fmt.Errorf("import staged minutes %s: %w", stock.Code, err)
			}
			result, err := summarizeMinuteBackfill(ctx, db, stock, archive, imported, c.Daily.Adjust, tradingSet)
			if err != nil {
				return err
			}
			byCode[stock.Code] = result
			log.Info("Minute backfill stock imported", "code", stock.Code, "fetched", result.FetchedBars, "inserted", result.InsertedBars, "existing", result.ExistingBars, "status", result.Status)
		}
	}
	for _, stock := range stocks {
		result, ok := byCode[stock.Code]
		if !ok {
			result = minuteBackfillStockResult{Code: stock.Code, Name: stock.Name, Status: "failed", Error: "no backfill result"}
		}
		report.Results = append(report.Results, result)
		report.FetchedBars += result.FetchedBars
		report.InsertedBars += result.InsertedBars
		report.ExistingBars += result.ExistingBars
		report.OverlapMismatch += result.OverlapMismatch
		report.MissingSessions += result.MissingSessions
		report.PartialSessions += result.PartialSessions
		switch result.Status {
		case "complete":
			report.CompleteStocks++
		case "incomplete":
			report.IncompleteStocks++
		default:
			report.FailedStocks++
		}
	}
	log.Info("Minute backfill completed", "mode", mode, "stocks", report.Stocks, "complete", report.CompleteStocks, "incomplete", report.IncompleteStocks, "failed", report.FailedStocks, "inserted", report.InsertedBars, "missing", report.MissingSessions, "partial_sessions", report.PartialSessions)
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		return err
	}
	if report.FailedStocks > 0 {
		return errors.New("minute backfill has failed stocks")
	}
	if report.IncompleteStocks > 0 {
		return errors.New("minute backfill has incomplete stocks")
	}
	return nil
}

func minuteBackfillStocks(c config.Config, codesCSV string) ([]config.Stock, error) {
	if strings.TrimSpace(codesCSV) == "" {
		return append([]config.Stock(nil), c.Stocks...), nil
	}
	byCode := make(map[string]config.Stock, len(c.Stocks))
	for _, stock := range c.Stocks {
		byCode[stock.Code] = stock
	}
	seen := map[string]bool{}
	stocks := []config.Stock{}
	for _, part := range strings.Split(codesCSV, ",") {
		code := strings.TrimSpace(part)
		if code == "" || seen[code] {
			continue
		}
		stock, ok := byCode[code]
		if !ok {
			return nil, fmt.Errorf("minute backfill code %s is not in the configured pool", code)
		}
		seen[code] = true
		stocks = append(stocks, stock)
	}
	if len(stocks) == 0 {
		return nil, errors.New("minute backfill code list is empty")
	}
	return stocks, nil
}

type minuteRangeFetcher interface {
	FetchMinuteRange(context.Context, config.Stock, string, string) (market.MinuteArchive, error)
	Close()
}

func minuteProviderForRange(provider string, tradingDays int) (string, error) {
	switch provider {
	case "baostock":
		return provider, nil
	case "sina":
		if tradingDays > 104 {
			return "", errors.New("Sina minute history is limited to 104 complete sessions; narrow -from or use -provider baostock")
		}
		return provider, nil
	case "auto":
		if tradingDays <= 104 {
			return "sina", nil
		}
		return "baostock", nil
	default:
		return "", errors.New("minute provider must be auto, baostock or sina")
	}
}

func newMinuteRangeFetcher(c config.Config, provider string) minuteRangeFetcher {
	if provider == "sina" {
		client := market.NewSinaKLineClient(config.Duration(c.Timeout), config.Duration(c.Market.RequestInterval))
		client.Endpoint = c.Market.SinaDailyEndpoint
		return client
	}
	return market.NewBaoStockClient(config.Duration(c.Timeout), config.Duration(c.Market.RequestInterval))
}

func fetchMinuteArchives(ctx context.Context, c config.Config, stocks []config.Stock, from, through string, workers int, provider string) <-chan minuteFetchResult {
	out := make(chan minuteFetchResult)
	jobs := make(chan config.Stock)
	go func() {
		defer close(jobs)
		for _, stock := range stocks {
			select {
			case jobs <- stock:
			case <-ctx.Done():
				return
			}
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := newMinuteRangeFetcher(c, provider)
			defer client.Close()
			for stock := range jobs {
				archive, err := client.FetchMinuteRange(ctx, stock, from, through)
				result := minuteFetchResult{stock: stock, archive: archive, err: err}
				select {
				case out <- result:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

func summarizeMinuteBackfill(ctx context.Context, db *storage.MarketDB, stock config.Stock, archive market.MinuteArchive, imported storage.MinuteImportResult, adjust string, trading map[string]bool) (minuteBackfillStockResult, error) {
	out := minuteBackfillStockResult{
		Code: stock.Code, Name: stock.Name, Status: "complete", FetchedBars: len(archive.Bars),
		InsertedBars: imported.Inserted, ExistingBars: imported.Existing, OverlapMismatch: imported.Mismatch,
	}
	expected, err := expectedMinuteDays(ctx, db, stock.Code, archive.From, archive.Through, adjust, trading)
	if err != nil {
		return out, err
	}
	for day, count := range imported.DayBars {
		if trading[day] && count > 0 {
			expected[day] = true
		}
	}
	days := make([]string, 0, len(expected))
	for day := range expected {
		days = append(days, day)
	}
	sort.Strings(days)
	for _, day := range days {
		count := imported.DayBars[day]
		switch {
		case count == 0:
			out.MissingSessions++
			if len(out.MissingDays) < 20 {
				out.MissingDays = append(out.MissingDays, day)
			}
		case count < 48:
			out.PartialSessions++
			if len(out.PartialDays) < 20 {
				out.PartialDays = append(out.PartialDays, fmt.Sprintf("%s(%d)", day, count))
			}
		default:
			out.CompleteDays++
		}
	}
	out.ExpectedDays = len(days)
	if out.MissingSessions > 0 {
		out.Status = "incomplete"
	}
	return out, nil
}

func expectedMinuteDays(ctx context.Context, db *storage.MarketDB, code, from, through, adjust string, trading map[string]bool) (map[string]bool, error) {
	daily, err := db.LoadDailyDates(ctx, []string{code}, from, through, adjust)
	if err != nil {
		return nil, err
	}
	expected := map[string]bool{}
	for day := range daily[code] {
		if trading[day] {
			expected[day] = true
		}
	}
	return expected, nil
}

func minuteCoverageComplete(expected map[string]bool, counts map[string]int) bool {
	for day := range expected {
		if counts[day] != 48 {
			return false
		}
	}
	return true
}

func minuteBarCount(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}
