package hkconnect

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

const (
	defaultWorkers        = 3
	maxWorkers            = 8
	defaultOverlapDays    = 30
	fullCoverageTolerance = 90
	hkCloseMinute         = 16 * 60
)

var runSequence atomic.Int64

type UniverseSource interface {
	Discover(context.Context) (market.Universe, error)
}

type FailoverUniverse struct {
	Sources []UniverseSource
}

func (f FailoverUniverse) Discover(ctx context.Context) (market.Universe, error) {
	var errs []error
	for _, source := range f.Sources {
		if source == nil {
			continue
		}
		universe, err := source.Discover(ctx)
		if err == nil {
			return universe, nil
		}
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return market.Universe{}, errors.New("no HK Connect universe source is configured")
	}
	return market.Universe{}, errors.Join(errs...)
}

type HistorySource interface {
	Fetch(context.Context, config.Stock, string, string, string) (market.History, error)
}

type TradingDaysSource interface {
	TradingDays(context.Context, string, string) ([]string, error)
}

type Options struct {
	AsOf        string
	Full        bool
	Workers     int
	Codes       []string
	OverlapDays int
}

type StockError struct {
	Code   string `json:"code"`
	Name   string `json:"name,omitempty"`
	Adjust string `json:"adjust,omitempty"`
	Reason string `json:"reason"`
}

type Result struct {
	Version                  int          `json:"schema_version"`
	Market                   string       `json:"market"`
	AsOf                     string       `json:"as_of"`
	StartedAt                time.Time    `json:"started_at"`
	FinishedAt               time.Time    `json:"finished_at"`
	Status                   string       `json:"status"`
	UniverseTotal            int          `json:"universe_total"`
	UniverseSource           string       `json:"universe_source,omitempty"`
	Eligible                 int          `json:"eligible"`
	ExcludedOther            int          `json:"excluded_other"`
	Succeeded                int          `json:"succeeded"`
	Partial                  int          `json:"partial"`
	Failed                   int          `json:"failed"`
	Skipped                  int          `json:"skipped"`
	Errors                   []StockError `json:"errors,omitempty"`
	RecommendationID         string       `json:"recommendation_id,omitempty"`
	RecommendationTarget     string       `json:"recommendation_target,omitempty"`
	RecommendationCandidates int          `json:"recommendation_candidates,omitempty"`
}

type Syncer struct {
	Config      config.Config
	DB          *storage.AllMarketDB
	Market      HistorySource
	Universe    UniverseSource
	TradingDays TradingDaysSource
	Log         *logging.Logger
	Now         func() time.Time
}

func New(c config.Config, db *storage.AllMarketDB, sources HistorySource, universe UniverseSource, tradingDays TradingDaysSource, log *logging.Logger) *Syncer {
	if log == nil {
		log = logging.Nop()
	}
	return &Syncer{Config: c, DB: db, Market: sources, Universe: universe, TradingDays: tradingDays, Log: log, Now: time.Now}
}

type stockResult struct {
	code    string
	name    string
	ok      bool
	partial bool
	failed  bool
	skipped bool
	errs    []StockError
	fatal   error
}

type outcome string

const (
	outcomeOK      outcome = "ok"
	outcomeStale   outcome = "stale"
	outcomeFailed  outcome = "failed"
	outcomeSkipped outcome = "skipped"
)

func (s *Syncer) Run(ctx context.Context, options Options) (Result, error) {
	result := Result{Version: 1, Market: "hk_connect", AsOf: options.AsOf, StartedAt: s.now(), Errors: []StockError{}}
	if s == nil || s.DB == nil || s.Market == nil || s.Universe == nil || s.TradingDays == nil {
		return result, errors.New("HK Connect syncer is not configured")
	}
	asOf, err := s.resolveAsOf(ctx, options.AsOf)
	if err != nil {
		return result, err
	}
	result.AsOf = asOf
	if options.Workers <= 0 {
		options.Workers = defaultWorkers
	}
	if options.Workers > maxWorkers {
		return result, fmt.Errorf("sync-hk-connect workers must be between 1 and %d", maxWorkers)
	}
	if options.OverlapDays <= 0 {
		options.OverlapDays = defaultOverlapDays
	}
	universe, err := s.Universe.Discover(ctx)
	if err != nil {
		return result, err
	}
	result.UniverseTotal = universe.Total
	result.UniverseSource = universe.Source
	result.ExcludedOther = universe.ExcludedOther
	stocks, selectedErrors := selectStocks(universe, options.Codes, result.AsOf)
	result.Eligible = len(stocks)
	result.Errors = append(result.Errors, selectedErrors...)
	if len(selectedErrors) > 0 {
		return result, errors.New("requested codes are not in the HK Connect universe")
	}
	if len(stocks) == 0 {
		return result, errors.New("HK Connect universe has no eligible stocks")
	}
	run := storage.AllMarketRun{ID: fmt.Sprintf("hk-%s-%d-%d", result.AsOf, s.now().UnixNano(), runSequence.Add(1)),
		AsOf: result.AsOf, StartedAt: result.StartedAt, Total: len(stocks)}
	if err := s.DB.StartRun(ctx, run); err != nil {
		return result, err
	}
	s.Log.Info("HK Connect synchronization started", "as_of", result.AsOf, "universe_total", result.UniverseTotal, "universe_source", result.UniverseSource, "eligible", result.Eligible, "excluded_other", result.ExcludedOther, "workers", options.Workers)

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan config.Stock)
	results := make(chan stockResult, options.Workers)
	var wg sync.WaitGroup
	for i := 0; i < options.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for stock := range jobs {
				if workCtx.Err() != nil {
					return
				}
				out, err := s.syncStock(workCtx, stock, result.AsOf, options.Full, options.OverlapDays)
				out.code, out.name = stock.Code, stock.Name
				if err != nil {
					out.fatal = err
					cancel()
				}
				results <- out
				if out.fatal != nil {
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, stock := range stocks {
			select {
			case jobs <- stock:
			case <-workCtx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	var fatal error
	completed := 0
	for out := range results {
		if out.fatal != nil {
			fatal = out.fatal
			continue
		}
		completed++
		switch {
		case out.skipped:
			result.Skipped++
		case out.failed:
			result.Failed++
		case out.partial:
			result.Partial++
		case out.ok:
			result.Succeeded++
		}
		for _, item := range out.errs {
			if len(result.Errors) < 40 {
				result.Errors = append(result.Errors, item)
			}
		}
		if completed%100 == 0 {
			s.Log.Info("HK Connect synchronization progress", "completed", completed, "total", len(stocks), "succeeded", result.Succeeded, "partial", result.Partial, "failed", result.Failed, "skipped", result.Skipped)
		}
	}
	if fatal == nil && ctx.Err() != nil {
		fatal = ctx.Err()
	}
	result.FinishedAt = s.now()
	result.Status = "complete"
	switch {
	case fatal != nil:
		result.Status = "failed"
	case result.Failed > 0 || result.Partial > 0:
		result.Status = "partial"
	}
	run = storage.AllMarketRun{ID: run.ID, AsOf: run.AsOf, StartedAt: run.StartedAt, FinishedAt: result.FinishedAt,
		Status: result.Status, Total: len(stocks), Succeeded: result.Succeeded, Partial: result.Partial, Failed: result.Failed, Skipped: result.Skipped}
	if fatal != nil {
		run.ErrorKind = "fatal"
	}
	cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := s.DB.FinishRun(cleanup, run); err != nil && fatal == nil {
		fatal = err
		result.Status = "failed"
	}
	s.Log.Info("HK Connect synchronization finished", "as_of", result.AsOf, "status", result.Status, "succeeded", result.Succeeded, "partial", result.Partial, "failed", result.Failed, "skipped", result.Skipped)
	return result, fatal
}

func (s *Syncer) resolveAsOf(ctx context.Context, requested string) (string, error) {
	now := s.now().In(market.Shanghai)
	through := now.Format("2006-01-02")
	if now.Hour()*60+now.Minute() < hkCloseMinute {
		through = now.AddDate(0, 0, -1).Format("2006-01-02")
	}
	latest, err := s.latestTradingDay(ctx, through)
	if err != nil {
		return "", err
	}
	if requested == "" {
		return latest, nil
	}
	if _, err := time.Parse("2006-01-02", requested); err != nil {
		return "", errors.New("as-of must be a valid date")
	}
	if requested > latest {
		return "", errors.New("as-of session has not completed")
	}
	days, err := s.TradingDays.TradingDays(ctx, addDays(requested, -10), requested)
	if err != nil {
		return "", err
	}
	if len(days) == 0 || days[len(days)-1] != requested {
		return "", errors.New("as-of must be a Hong Kong trading date")
	}
	return requested, nil
}

func (s *Syncer) latestTradingDay(ctx context.Context, through string) (string, error) {
	days, err := s.TradingDays.TradingDays(ctx, addDays(through, -21), through)
	if err != nil {
		return "", err
	}
	if len(days) == 0 {
		return "", errors.New("Hong Kong trading calendar returned no sessions")
	}
	return days[len(days)-1], nil
}

func selectStocks(universe market.Universe, requested []string, asOf string) ([]config.Stock, []StockError) {
	wanted := map[string]bool{}
	errs := []StockError{}
	for _, code := range requested {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if !config.ValidHKStockCode(code) {
			errs = append(errs, StockError{Code: code, Reason: "invalid_code"})
			continue
		}
		wanted[code] = true
	}
	out := make([]config.Stock, 0, len(universe.Stocks))
	found := map[string]bool{}
	for _, stock := range universe.Stocks {
		if len(wanted) > 0 && !wanted[stock.Code] {
			continue
		}
		if stock.ListedFrom > asOf {
			continue
		}
		out = append(out, stock)
		found[stock.Code] = true
	}
	for code := range wanted {
		if !found[code] {
			errs = append(errs, StockError{Code: code, Reason: "not_in_eligible_universe"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, errs
}

func (s *Syncer) syncStock(ctx context.Context, stock config.Stock, asOf string, full bool, overlapDays int) (stockResult, error) {
	out := stockResult{}
	rawStatus, err := s.syncStatus(ctx, stock.Code, "none", asOf)
	if err != nil {
		return out, err
	}
	adjStatus, err := s.syncStatus(ctx, stock.Code, "qfq", asOf)
	if err != nil {
		return out, err
	}
	refreshRaw := full || !terminal(rawStatus)
	refreshAdj := full || !terminal(adjStatus)
	if !refreshRaw {
		refreshRaw, err = s.needsRefresh(ctx, stock.Code, "none", asOf)
		if err != nil {
			return out, err
		}
	}
	if !refreshAdj {
		refreshAdj, err = s.needsRefresh(ctx, stock.Code, "qfq", asOf)
		if err != nil {
			return out, err
		}
	}
	if !refreshRaw && !refreshAdj {
		out.skipped = true
		return out, nil
	}

	rawOutcome := outcomeSkipped
	if refreshRaw {
		rawOutcome, err = s.syncOne(ctx, stock, "none", asOf, full, overlapDays)
		if err != nil {
			return out, err
		}
	}
	adjOutcome := outcomeSkipped
	if refreshAdj {
		adjOutcome, err = s.syncOne(ctx, stock, "qfq", asOf, full, overlapDays)
		if err != nil {
			return out, err
		}
	}
	if !(rawOutcome == outcomeStale && adjOutcome == outcomeStale) {
		for _, item := range []struct {
			adjust  string
			outcome outcome
		}{{"none", rawOutcome}, {"qfq", adjOutcome}} {
			if item.outcome != outcomeStale && item.outcome != outcomeFailed {
				continue
			}
			reason := "no_new_bars"
			if item.outcome == outcomeFailed {
				reason = "sync_failed"
			}
			out.errs = append(out.errs, StockError{Code: stock.Code, Name: stock.Name, Adjust: item.adjust, Reason: reason})
		}
	}
	settled := func(value outcome) bool { return value == outcomeOK || value == outcomeSkipped }
	switch {
	case rawOutcome == outcomeSkipped && adjOutcome == outcomeSkipped:
		out.skipped = true
	case settled(rawOutcome) && settled(adjOutcome):
		out.ok = true
	case rawOutcome == outcomeFailed && adjOutcome == outcomeFailed:
		out.failed = true
	case rawOutcome == outcomeStale && adjOutcome == outcomeStale:
		out.skipped = true
	default:
		out.partial = true
	}
	return out, nil
}

func (s *Syncer) syncStatus(ctx context.Context, code, adjust, asOf string) (string, error) {
	item, err := s.DB.SyncStatus(ctx, code, adjust, asOf)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return item.Status, nil
}

func terminal(status string) bool { return status == "ok" || status == "stale" }

func (s *Syncer) needsRefresh(ctx context.Context, code, adjust, asOf string) (bool, error) {
	lastDate, err := s.DB.LastBarDate(ctx, code, adjust)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return lastDate != asOf, nil
}

func (s *Syncer) syncOne(ctx context.Context, stock config.Stock, adjust, asOf string, full bool, overlapDays int) (outcome, error) {
	end, err := time.Parse("2006-01-02", asOf)
	if err != nil {
		return outcomeFailed, errors.New("invalid as-of date")
	}
	requiredStart := maxDate(historyStart(end), stock.ListedFrom)
	if requiredStart > asOf {
		return outcomeStale, nil
	}
	lastDate, err := s.DB.LastBarDate(ctx, stock.Code, adjust)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return outcomeFailed, err
	}
	lastMeta, metaErr := s.DB.LastSuccessful(ctx, stock.Code, adjust)
	if metaErr != nil && !errors.Is(metaErr, fs.ErrNotExist) {
		return outcomeFailed, metaErr
	}
	start := requiredStart
	replace := true
	incremental := false
	if adjust == "none" && !full && lastDate != "" && metaErr == nil {
		candidate := addDays(lastDate, -overlapDays)
		if candidate > start {
			start = candidate
		}
		replace = false
		incremental = true
	}
	if adjust == "qfq" {
		analysisStart := addDays(asOf, -s.historyDays())
		start = maxDate(requiredStart, analysisStart)
	}
	h, err := s.fetch(ctx, stock, start, asOf, adjust)
	if err != nil {
		if ctx.Err() != nil {
			return outcomeFailed, ctx.Err()
		}
		if incremental && isEmptyWindow(err) {
			h, err = s.fetch(ctx, stock, requiredStart, asOf, adjust)
			if err == nil {
				start, replace, incremental = requiredStart, true, false
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return outcomeFailed, ctx.Err()
			}
			status, reason := "failed", reasonString(err)
			if isEmptyWindow(err) && lastDate != "" {
				status, reason = "stale", "no_new_bars"
			}
			if recordErr := s.DB.RecordStatus(ctx, stock, adjust, asOf, status, reason); recordErr != nil {
				return outcomeFailed, recordErr
			}
			if status == "stale" {
				return outcomeStale, nil
			}
			return outcomeFailed, nil
		}
	}

	if incremental {
		if lastMeta.Source != "" && h.Source != lastMeta.Source {
			h, err = s.fetch(ctx, stock, requiredStart, asOf, adjust)
			if err != nil {
				if ctx.Err() != nil {
					return outcomeFailed, ctx.Err()
				}
				if recordErr := s.DB.RecordStatus(ctx, stock, adjust, asOf, "failed", reasonString(err)); recordErr != nil {
					return outcomeFailed, recordErr
				}
				return outcomeFailed, nil
			}
			start, replace, incremental = requiredStart, true, false
		} else {
			matches, compareErr := s.overlapMatches(ctx, stock.Code, adjust, h, lastDate)
			if compareErr != nil {
				return outcomeFailed, compareErr
			}
			if !matches {
				h, err = s.fetch(ctx, stock, requiredStart, asOf, adjust)
				if err != nil {
					if ctx.Err() != nil {
						return outcomeFailed, ctx.Err()
					}
					if recordErr := s.DB.RecordStatus(ctx, stock, adjust, asOf, "failed", reasonString(err)); recordErr != nil {
						return outcomeFailed, recordErr
					}
					return outcomeFailed, nil
				}
				start, replace, incremental = requiredStart, true, false
			}
		}
	}
	if err := validateCoverage(h, start, asOf, stock.ListedFrom, replace); err != nil {
		if recordErr := s.DB.RecordStatus(ctx, stock, adjust, asOf, "failed", err.Error()); recordErr != nil {
			return outcomeFailed, recordErr
		}
		return outcomeFailed, nil
	}
	if err := s.DB.SaveHistory(ctx, stock, h, replace); err != nil {
		return outcomeFailed, err
	}
	return outcomeOK, nil
}

func (s *Syncer) fetch(ctx context.Context, stock config.Stock, start, asOf, adjust string) (market.History, error) {
	h, err := s.Market.Fetch(ctx, stock, start, asOf, adjust)
	if err != nil {
		return market.History{}, err
	}
	if h.Code != stock.Code || h.Adjust != adjust || h.AsOf != asOf {
		return market.History{}, &market.DataError{Reason: "history_request_mismatch"}
	}
	if err := h.Validate(); err != nil {
		return market.History{}, &market.DataError{Reason: err.Error()}
	}
	return h, nil
}

func (s *Syncer) overlapMatches(ctx context.Context, code, adjust string, h market.History, lastDate string) (bool, error) {
	if lastDate == "" || len(h.Bars) == 0 || h.Bars[0].Date > lastDate {
		return false, nil
	}
	existing, err := s.DB.LoadBars(ctx, code, adjust, h.Bars[0].Date, lastDate)
	if err != nil {
		return false, err
	}
	if len(existing) == 0 {
		return false, nil
	}
	byDate := make(map[string]market.Bar, len(existing))
	for _, bar := range existing {
		byDate[bar.Date] = bar
	}
	seen := 0
	for _, bar := range h.Bars {
		if bar.Date > lastDate {
			break
		}
		old, ok := byDate[bar.Date]
		if !ok || !sameBar(old, bar) {
			return false, nil
		}
		seen++
	}
	return seen == len(existing), nil
}

func sameBar(a, b market.Bar) bool {
	return roundMicros(a.Open) == roundMicros(b.Open) && roundMicros(a.High) == roundMicros(b.High) &&
		roundMicros(a.Low) == roundMicros(b.Low) && roundMicros(a.Close) == roundMicros(b.Close) &&
		math.Abs(a.Volume-b.Volume) <= 0.5
}

func roundMicros(value float64) int64 { return int64(math.Round(value * 1_000_000)) }

func validateCoverage(h market.History, start, asOf, listedFrom string, enforceStart bool) error {
	if len(h.Bars) == 0 || h.Bars[len(h.Bars)-1].Date > asOf {
		return errors.New("history does not cover the requested end date")
	}
	if !enforceStart || listedFrom == "" {
		return nil
	}
	expected, err := time.Parse("2006-01-02", maxDate(start, listedFrom))
	if err != nil {
		return errors.New("invalid history start")
	}
	first, err := time.Parse("2006-01-02", h.Bars[0].Date)
	if err != nil {
		return errors.New("invalid first history date")
	}
	if first.After(expected.AddDate(0, 0, fullCoverageTolerance)) {
		return errors.New("history_start_incomplete")
	}
	return nil
}

func isEmptyWindow(err error) bool {
	var data *market.DataError
	if errors.As(err, &data) {
		reason := strings.ToLower(data.Reason)
		if strings.Contains(reason, "empty") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(market.ProviderFailureSummary(err)), "empty")
}

func reasonString(err error) string {
	var data *market.DataError
	if errors.As(err, &data) && data.Reason != "" {
		return data.Reason
	}
	if summary := market.ProviderFailureSummary(err); summary != "" {
		return summary
	}
	var failure *market.Failure
	if errors.As(err, &failure) {
		return fmt.Sprintf("request_failed_%s", failure.Kind)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "request_canceled"
	}
	return "request_failed"
}

func historyStart(end time.Time) string {
	start := end.AddDate(-10, 0, 0)
	if start.Month() != end.Month() {
		start = start.AddDate(0, 0, -1)
	}
	return start.Format("2006-01-02")
}

func addDays(date string, days int) string {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return parsed.AddDate(0, 0, days).Format("2006-01-02")
}

func maxDate(a, b string) string {
	if a > b {
		return a
	}
	return b
}

func (s *Syncer) historyDays() int {
	if s.Config.Daily.HistoryDays < 90 {
		return 365
	}
	return s.Config.Daily.HistoryDays
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
