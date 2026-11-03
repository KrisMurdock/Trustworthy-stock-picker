package hkintraday

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type MinuteSource interface {
	FetchMinutes(context.Context, config.Stock, string, string) (market.HKMinuteHistory, error)
}

type UniverseSource interface {
	Discover(context.Context) (market.Universe, error)
}

type CalendarSource interface {
	TradingDays(context.Context, string, string) ([]string, error)
}

type Monitor struct {
	Config   config.Config
	Market   MinuteSource
	Universe UniverseSource
	Calendar CalendarSource
	Log      *logging.Logger
	Now      func() time.Time

	cacheDay     string
	cachedStocks []config.Stock
	cacheSource  string
}

func New(c config.Config, source MinuteSource, universe UniverseSource, calendar CalendarSource, log *logging.Logger) *Monitor {
	if log == nil {
		log = logging.Nop()
	}
	return &Monitor{Config: c, Market: source, Universe: universe, Calendar: calendar, Log: log, Now: time.Now}
}

type Options struct {
	From      string
	Through   string
	Codes     []string
	Automatic bool
}

type StockError struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

type Result struct {
	Version    int          `json:"schema_version"`
	Market     string       `json:"market"`
	From       string       `json:"from"`
	Through    string       `json:"through"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt time.Time    `json:"finished_at"`
	Status     string       `json:"status"`
	Reason     string       `json:"reason,omitempty"`
	Source     string       `json:"universe_source,omitempty"`
	Total      int          `json:"total"`
	Succeeded  int          `json:"succeeded"`
	Partial    int          `json:"partial"`
	Failed     int          `json:"failed"`
	Errors     []StockError `json:"errors,omitempty"`
}

type job struct {
	stock config.Stock
	from  string
}

type outcome struct {
	code   string
	status string
	reason string
	err    error
}

func Active(c config.HKIntraday, now time.Time) (bool, error) {
	now = now.In(market.Shanghai)
	day := now.Format("2006-01-02")
	if day < c.ValidFrom || day > c.ValidThrough {
		return false, errors.New("HK minute calendar is outside its declared coverage")
	}
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return false, nil
	}
	minute := now.Hour()*60 + now.Minute()
	if c.EarlyCloseMap()[day] {
		return minute >= 570 && minute <= 740, nil
	}
	return minute >= 570 && minute <= 740 || minute >= 780 && minute <= 1020, nil
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now().In(market.Shanghai)
	}
	return time.Now().In(market.Shanghai)
}

func (m *Monitor) Sync(ctx context.Context, options Options) (Result, error) {
	if m == nil || m.Market == nil || m.Universe == nil || m.Calendar == nil {
		return Result{}, errors.New("HK minute monitor is not configured")
	}
	started := m.now()
	result := Result{Version: 1, Market: "hk_connect", StartedAt: started}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !m.Config.HKIntraday.Enabled {
		return result, errors.New("HK minute synchronization is disabled")
	}
	if err := m.Config.HKIntraday.Validate(); err != nil {
		return result, err
	}
	today := started.Format("2006-01-02")
	from, through := options.From, options.Through
	if (from == "") != (through == "") {
		return result, errors.New("HK minute from and through must be supplied together")
	}
	if from == "" {
		through = today
		from = started.AddDate(0, 0, -market.HKMinuteWindowDays).Format("2006-01-02")
		if from < m.Config.HKIntraday.ValidFrom {
			from = m.Config.HKIntraday.ValidFrom
		}
	}
	first, firstErr := time.ParseInLocation("2006-01-02", from, market.Shanghai)
	last, lastErr := time.ParseInLocation("2006-01-02", through, market.Shanghai)
	if firstErr != nil || lastErr != nil || first.Format("2006-01-02") != from || last.Format("2006-01-02") != through ||
		first.After(last) || last.Sub(first) > market.HKMinuteWindowDays*24*time.Hour || through > today ||
		from < m.Config.HKIntraday.ValidFrom || through > m.Config.HKIntraday.ValidThrough {
		return result, errors.New("HK minute range is outside calendar coverage or request bounds")
	}
	result.From, result.Through = from, through
	root, err := storage.OpenView(m.Config.DataDir, m.Config.RequireCIFS)
	if err != nil {
		return result, err
	}
	if err := root.Close(); err != nil {
		return result, err
	}
	days, err := m.Calendar.TradingDays(ctx, from, through)
	if err != nil {
		var dataError *market.DataError
		if options.Automatic && errors.As(err, &dataError) && dataError.Reason == "empty_hk_trading_days" {
			result.Status, result.Reason, result.FinishedAt = "skipped", "trading_day_unconfirmed", m.now()
			return result, nil
		}
		return result, err
	}
	tradingDays := make(map[string]bool, len(days))
	for _, day := range days {
		parsed, err := time.Parse("2006-01-02", day)
		if err != nil || parsed.Format("2006-01-02") != day || day < from || day > through || tradingDays[day] || parsed.Weekday() == time.Saturday || parsed.Weekday() == time.Sunday {
			return result, errors.New("invalid HK trading-day evidence")
		}
		tradingDays[day] = true
	}
	if options.Automatic && !tradingDays[today] {
		result.Status, result.Reason, result.FinishedAt = "skipped", "trading_day_unconfirmed", m.now()
		return result, nil
	}
	if len(tradingDays) == 0 {
		return result, errors.New("HK trading days are unavailable for the requested range")
	}
	stocks, source, err := m.stocks(ctx, today, options)
	if err != nil {
		return result, err
	}
	result.Total, result.Source = len(stocks), source
	db, err := storage.OpenHKMarket(m.Config.DataDir, m.Config.RequireCIFS)
	if err != nil {
		return result, err
	}
	defer db.Close()
	jobs := make([]job, 0, len(stocks))
	actualFrom := through
	for _, stock := range stocks {
		jobFrom := from
		if options.From == "" {
			jobFrom = started.AddDate(0, 0, -30).Format("2006-01-02")
			latest, err := db.LatestHKMinuteDate(ctx, stock.Code)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return result, err
			}
			if err == nil {
				latestTime, parseErr := time.ParseInLocation("2006-01-02", latest, market.Shanghai)
				if parseErr != nil {
					return result, errors.New("invalid latest HK minute date")
				}
				jobFrom = latestTime.AddDate(0, 0, -1).Format("2006-01-02")
				weekAgo := started.AddDate(0, 0, -7).Format("2006-01-02")
				if jobFrom > weekAgo {
					jobFrom = weekAgo
				}
			}
			state, err := db.HKMinuteState(ctx, stock.Code)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return result, err
			}
			if err == nil {
				if state.UnresolvedFrom != "" && state.UnresolvedFrom < jobFrom {
					jobFrom = state.UnresolvedFrom
				}
				if state.Status == "failed" && state.RequestedFrom < jobFrom {
					jobFrom = state.RequestedFrom
				}
			}
			if jobFrom < from {
				jobFrom = from
			}
		}
		if stock.ListedFrom != "" && stock.ListedFrom > jobFrom {
			jobFrom = stock.ListedFrom
		}
		if jobFrom > through {
			continue
		}
		if jobFrom < actualFrom {
			actualFrom = jobFrom
		}
		jobs = append(jobs, job{stock: stock, from: jobFrom})
	}
	if len(jobs) == 0 {
		return result, errors.New("HK minute universe has no eligible stocks")
	}
	result.From, result.Total = actualFrom, len(jobs)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	queue := make(chan job)
	results := make(chan outcome, m.Config.HKIntraday.Workers)
	var workers sync.WaitGroup
	for count := 0; count < m.Config.HKIntraday.Workers; count++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range queue {
				if workCtx.Err() != nil {
					return
				}
				out := m.syncStock(workCtx, db, item, through, tradingDays)
				results <- out
				if out.err != nil {
					cancel()
					return
				}
			}
		}()
	}
	go func() {
		defer close(queue)
		for _, item := range jobs {
			select {
			case queue <- item:
			case <-workCtx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	var fatal error
	for out := range results {
		if out.err != nil {
			if fatal == nil {
				fatal = out.err
			}
			continue
		}
		switch out.status {
		case "ok":
			result.Succeeded++
		case "partial":
			result.Partial++
		case "failed":
			result.Failed++
		}
		if out.reason != "" && len(result.Errors) < 40 {
			result.Errors = append(result.Errors, StockError{Code: out.code, Reason: out.reason})
		}
	}
	if fatal == nil {
		fatal = ctx.Err()
	}
	result.FinishedAt = m.now()
	result.Status = "complete"
	if fatal != nil || result.Succeeded+result.Partial+result.Failed != result.Total {
		result.Status = "failed"
	} else if result.Partial > 0 || result.Failed > 0 {
		result.Status = "partial"
	}
	m.Log.Info("HK minute synchronization finished", "from", result.From, "through", result.Through, "status", result.Status,
		"total", result.Total, "succeeded", result.Succeeded, "partial", result.Partial, "failed", result.Failed)
	return result, fatal
}

func (m *Monitor) stocks(ctx context.Context, day string, options Options) ([]config.Stock, string, error) {
	var stocks []config.Stock
	source := m.cacheSource
	if options.Automatic && m.cacheDay == day && len(m.cachedStocks) > 0 {
		stocks = append(stocks, m.cachedStocks...)
	} else {
		universe, err := m.Universe.Discover(ctx)
		if err != nil {
			return nil, "", err
		}
		if universe.Market != "hk_connect" || len(universe.Stocks) == 0 || len(universe.Stocks) > 2000 {
			return nil, "", errors.New("invalid HK Connect universe")
		}
		seen := map[string]bool{}
		for _, stock := range universe.Stocks {
			if !config.ValidHKStockCode(stock.Code) || seen[stock.Code] {
				return nil, "", errors.New("invalid or duplicate HK Connect stock")
			}
			seen[stock.Code] = true
			stocks = append(stocks, stock)
		}
		source = universe.Source
		if options.Automatic {
			m.cacheDay, m.cacheSource = day, source
			m.cachedStocks = append([]config.Stock(nil), stocks...)
		}
	}
	if len(options.Codes) > 0 {
		wanted := make(map[string]bool, len(options.Codes))
		for _, code := range options.Codes {
			if !config.ValidHKStockCode(code) || wanted[code] {
				return nil, "", errors.New("invalid or duplicate requested HK minute code")
			}
			wanted[code] = true
		}
		selected := make([]config.Stock, 0, len(wanted))
		for _, stock := range stocks {
			if wanted[stock.Code] {
				selected = append(selected, stock)
				delete(wanted, stock.Code)
			}
		}
		if len(wanted) != 0 {
			return nil, "", errors.New("requested code is not in the HK Connect universe")
		}
		stocks = selected
	}
	sort.Slice(stocks, func(left, right int) bool { return stocks[left].Code < stocks[right].Code })
	return stocks, source, nil
}

func (m *Monitor) syncStock(ctx context.Context, db *storage.AllMarketDB, item job, through string, days map[string]bool) outcome {
	out := outcome{code: item.stock.Code}
	history, err := m.Market.FetchMinutes(ctx, item.stock, item.from, through)
	if ctx.Err() != nil {
		out.err = ctx.Err()
		return out
	}
	if err != nil {
		out.status, out.reason = "failed", failureReason(err)
		out.err = db.SaveHKMinutes(ctx, item.stock, item.from, through, m.now(), nil, out.status, out.reason, 0)
		return out
	}
	if err := history.ValidateBase(); err != nil || history.Code != item.stock.Code {
		out.status, out.reason = "failed", "invalid_hk_minutes"
		out.err = db.SaveHKMinutes(ctx, item.stock, item.from, through, m.now(), nil, out.status, out.reason, 0)
		return out
	}
	missing, expected, err := coverage(history, item.from, through, days, m.Config.HKIntraday.EarlyCloseMap())
	if err != nil {
		out.status, out.reason = "failed", "invalid_hk_minute_calendar"
		out.err = db.SaveHKMinutes(ctx, item.stock, item.from, through, m.now(), nil, out.status, out.reason, 0)
		return out
	}
	out.status = "ok"
	if missing > 0 {
		out.status, out.reason = "partial", "incomplete_session"
	} else if expected == 0 {
		out.status, out.reason = "partial", "no_final_bars_due"
	}
	out.err = db.SaveHKMinutes(ctx, item.stock, item.from, through, m.now(), &history, out.status, out.reason, missing)
	if out.err == nil && out.status == "ok" {
		state, err := db.HKMinuteState(ctx, item.stock.Code)
		if err != nil {
			out.err = err
		} else if state.UnresolvedFrom != "" {
			out.status, out.reason = "partial", "history_gap_unresolved"
		}
	}
	return out
}

func coverage(history market.HKMinuteHistory, from, through string, days, early map[string]bool) (int, int, error) {
	present := make(map[int64]bool, len(history.Bars))
	for _, bar := range history.Bars {
		day := bar.End.In(market.Shanghai).Format("2006-01-02")
		slot, valid := market.HKMinuteSlot(bar.End)
		if !valid || day < from || day > through || !days[day] || early[day] && slot >= 30 {
			return 0, 0, errors.New("HK minute bar is outside confirmed trading sessions")
		}
		if bar.Final {
			present[bar.End.Unix()] = true
		}
	}
	missing, expected := 0, 0
	for day := range days {
		if day < from || day > through {
			continue
		}
		date, _ := time.ParseInLocation("2006-01-02", day, market.Shanghai)
		sessions := [][2]int{{570, 720}, {780, 960}}
		if early[day] {
			sessions = sessions[:1]
		}
		for _, session := range sessions {
			for minute := session[0] + 5; minute <= session[1]; minute += 5 {
				end := time.Date(date.Year(), date.Month(), date.Day(), minute/60, minute%60, 0, 0, market.Shanghai)
				watermark := end.Add(10 * time.Second)
				if minute == 720 || minute == 960 {
					watermark = end.Add(10 * time.Minute)
				}
				if watermark.After(history.FetchedAt) {
					continue
				}
				expected++
				if !present[end.Unix()] {
					missing++
				}
			}
		}
	}
	return missing, expected, nil
}

func failureReason(err error) string {
	var data *market.DataError
	if errors.As(err, &data) && data.Reason != "" {
		return data.Reason
	}
	var failure *market.Failure
	if errors.As(err, &failure) {
		if failure.Status > 0 {
			return fmt.Sprintf("http_%d", failure.Status)
		}
		return "network_or_timeout"
	}
	return "provider_failure"
}

func (m *Monitor) Run(ctx context.Context) error {
	m.Log.Info("HK minute synchronization started", "source", "eastmoney_hk", "poll_interval", m.Config.HKIntraday.PollInterval)
	for {
		started := time.Now()
		active, err := Active(m.Config.HKIntraday, m.now())
		if err != nil {
			m.Log.Warn("HK minute calendar is unavailable", "reason", err.Error())
		} else if active {
			cycle, cancel := context.WithTimeout(ctx, config.Duration(m.Config.HKIntraday.CycleTimeout))
			_, err := m.Sync(cycle, Options{Automatic: true})
			cancel()
			if err != nil && ctx.Err() == nil {
				m.Log.Warn("HK minute synchronization cycle failed", "reason", err.Error())
			}
		}
		wait := config.Duration(m.Config.HKIntraday.PollInterval) - time.Since(started)
		if wait < 15*time.Second {
			wait = 15 * time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
