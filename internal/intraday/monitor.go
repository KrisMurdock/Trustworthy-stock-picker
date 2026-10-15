// Package intraday owns minute polling and post-close reconciliation separately
// from daily selection and notification delivery.
package intraday

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"stock-watcher/internal/calendar"
	"stock-watcher/internal/config"
	"stock-watcher/internal/logging"
	"stock-watcher/internal/market"
	"stock-watcher/internal/storage"
)

type Source interface {
	FetchMinutes(context.Context, config.Stock, int) (market.MinuteHistory, error)
}

type Monitor struct {
	Config   config.Config
	Calendar *calendar.Calendar
	Store    *storage.Store
	DB       *storage.MarketDB
	Source   Source
	Log      *logging.Logger
	Now      func() time.Time
}

func New(c config.Config, s *storage.Store, db *storage.MarketDB, source Source, log *logging.Logger) *Monitor {
	if log == nil {
		log = logging.Nop()
	}
	return &Monitor{Config: c, Calendar: calendar.New(c), Store: s, DB: db, Source: source, Log: log, Now: time.Now}
}

type Plan struct {
	Target              time.Time
	Active, Calibration bool
}

func Schedule(cal *calendar.Calendar, now time.Time, calibrationAt string) (Plan, error) {
	now = now.In(market.Shanghai)
	day := calendar.Date(now)
	trading, err := cal.Trading(day)
	if err != nil {
		return Plan{}, err
	}
	n := now.Hour()*60 + now.Minute()
	date := now
	end := 900
	active := false
	if trading && n >= 570 {
		switch {
		case n < 690:
			end = 570 + ((n-570)/5+1)*5
			active = true
		case n < 780:
			end = 690
		case n < 900:
			end = 780 + ((n-780)/5+1)*5
			active = true
		}
	} else {
		previous, err := cal.Completed(now)
		if err != nil {
			return Plan{}, err
		}
		date, _ = time.ParseInLocation("2006-01-02", previous, market.Shanghai)
	}
	target := time.Date(date.Year(), date.Month(), date.Day(), end/60, end%60, 0, 0, market.Shanghai)
	calibrate := calendar.Date(target) < day || end == 900 && now.Format("15:04") >= calibrationAt
	return Plan{Target: target, Active: active, Calibration: calibrate}, nil
}

type Result struct {
	Processed  int `json:"processed"`
	Succeeded  int `json:"succeeded"`
	Failed     int `json:"failed"`
	Calibrated int `json:"calibrated"`
}

type job struct {
	stock config.Stock
	count int
}
type outcome struct {
	status     string
	calibrated bool
	err        error
}

func (m *Monitor) Sync(ctx context.Context, now time.Time) (Result, error) {
	result := Result{}
	plan, err := Schedule(m.Calendar, now, m.Config.Intraday.CalibrationAt)
	if err != nil {
		return result, err
	}
	if err := m.Store.Check(); err != nil {
		return result, err
	}
	jobs := []job{}
	for _, stock := range m.Config.Stocks {
		state, err := m.DB.MinuteState(ctx, stock.Code)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return result, err
		}
		interval := config.Duration(m.Config.Intraday.PollInterval)
		// Failed/incomplete reconciliation still retries, at a lower rate off
		// session. Newly due calibration bypasses this cooldown once.
		if !plan.Active && interval < 5*time.Minute {
			interval = 5 * time.Minute
		}
		previousPlan, _ := Schedule(m.Calendar, state.AttemptAt, m.Config.Intraday.CalibrationAt)
		newCalibration := plan.Calibration && (!previousPlan.Calibration || calendar.Date(previousPlan.Target) != calendar.Date(plan.Target))
		if !newCalibration && !state.AttemptAt.IsZero() && now.Before(state.AttemptAt.Add(interval)) {
			continue
		}
		if !plan.Active && (state.CalibratedDate >= calendar.Date(plan.Target) || !plan.Calibration && state.Target.Equal(plan.Target) && state.Status == "ok") {
			continue
		}
		count := 64
		if state.FetchedAt == nil || plan.Calibration {
			count = market.MinuteLimit
		}
		jobs = append(jobs, job{stock: stock, count: count})
	}
	if len(jobs) == 0 {
		return result, ctx.Err()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	queue := make(chan job, len(jobs))
	results := make(chan outcome, len(jobs))
	for _, j := range jobs {
		queue <- j
	}
	close(queue)
	var wg sync.WaitGroup
	for worker := 0; worker < m.Config.Intraday.Workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				if ctx.Err() != nil {
					return
				}
				out := m.syncStock(ctx, j)
				results <- out
				if out.err != nil {
					cancel()
					return
				}
			}
		}()
	}
	wg.Wait()
	close(results)
	var fatal error
	for out := range results {
		result.Processed++
		if out.err != nil {
			fatal = errors.Join(fatal, out.err)
		} else if out.status == "ok" {
			result.Succeeded++
		} else {
			result.Failed++
		}
		if out.calibrated {
			result.Calibrated++
		}
	}
	m.Log.Info("Intraday synchronization completed", "processed", result.Processed, "succeeded", result.Succeeded, "failed", result.Failed, "calibrated", result.Calibrated)
	if fatal != nil {
		return result, fatal
	}
	return result, ctx.Err()
}

func (m *Monitor) syncStock(ctx context.Context, j job) outcome {
	if err := m.Store.Check(); err != nil {
		return outcome{err: err}
	}
	plan, err := Schedule(m.Calendar, m.Now(), m.Config.Intraday.CalibrationAt)
	if err != nil {
		return outcome{err: err}
	}
	h, fetchErr := m.Source.FetchMinutes(ctx, j.stock, j.count)
	if ctx.Err() != nil {
		return outcome{err: ctx.Err()}
	}
	status, reason := "ok", ""
	var data *market.MinuteHistory
	calibrated := false
	if fetchErr == nil {
		fetchErr = h.Validate()
		if fetchErr == nil && h.Code != j.stock.Code {
			fetchErr = errors.New("minute response stock mismatch")
		}
	}
	if fetchErr == nil {
		// Filter only dates outside the configured calendar; exchange holidays
		// inside its coverage must never appear as real trading observations.
		kept := h.Bars[:0]
		for _, b := range h.Bars {
			day := calendar.Date(b.End)
			if day < m.Calendar.ValidFrom() {
				continue
			}
			trading, e := m.Calendar.Trading(day)
			if e != nil || !trading {
				fetchErr = errors.New("minute response outside exchange sessions")
				break
			}
			kept = append(kept, b)
		}
		h.Bars = kept
		if len(h.Bars) == 0 {
			fetchErr = errors.New("empty minute response")
		}
	}
	if fetchErr != nil {
		status, reason = "failed", "source_unavailable"
		var dataErr *market.DataError
		var failure *market.Failure
		if errors.As(fetchErr, &dataErr) {
			reason = "invalid_source_data"
		}
		if errors.As(fetchErr, &failure) && failure.Status > 0 {
			reason = fmt.Sprintf("http_%d", failure.Status)
		}
		m.Log.Warn("Intraday stock synchronization failed", "code", j.stock.Code, "reason", reason)
	} else {
		plan, err = Schedule(m.Calendar, h.FetchedAt, m.Config.Intraday.CalibrationAt)
		if err != nil {
			return outcome{err: err}
		}
		data = &h
		last := h.Bars[len(h.Bars)-1]
		if last.End.Before(plan.Target) {
			status, reason = "stale", "latest_bar_missing"
		} else if !plan.Active && !last.Final {
			status, reason = "partial", "awaiting_closed_bar"
		} else if !completeDay(h, plan.Target) {
			status, reason = "partial", "intraday_gaps"
		}
		calibrated = plan.Calibration && status == "ok" && last.Final
	}
	if err := m.Store.Check(); err != nil {
		return outcome{err: err}
	}
	err = m.DB.SaveMinutes(ctx, j.stock, plan.Target, m.Now(), data, status, reason, calibrated)
	return outcome{status: status, calibrated: calibrated, err: err}
}

func completeDay(h market.MinuteHistory, target time.Time) bool {
	seen := map[int64]bool{}
	for _, b := range h.Bars {
		seen[b.End.Unix()] = true
	}
	t := target.In(market.Shanghai)
	base := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, market.Shanghai)
	for _, start := range []int{570, 780} {
		for minute := start + 5; minute <= start+120; minute += 5 {
			end := base.Add(time.Duration(minute) * time.Minute)
			if end.After(target) {
				return true
			}
			if !seen[end.Unix()] {
				return false
			}
		}
	}
	return true
}

func (m *Monitor) Run(ctx context.Context) error {
	m.Log.Info("Intraday synchronization started", "periods", []int{5, 15, 30, 60}, "base_period", 5, "source", "tencent", "poll_interval", m.Config.Intraday.PollInterval, "calibration_at", m.Config.Intraday.CalibrationAt)
	for {
		if _, err := m.Sync(ctx, m.Now()); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m.Log.Warn("Intraday synchronization cycle failed", "error", err.Error())
		}
		timer := time.NewTimer(15 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
