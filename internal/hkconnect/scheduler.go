package hkconnect

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	dailyPollInterval = time.Minute
	dailyRetryInitial = 5 * time.Minute
	dailyRetryMaximum = time.Hour
	dailySyncTimeout  = 24 * time.Hour
	dailyScheduleTime = 18*60 + 50
)

type DailySyncer interface {
	SyncThrough(context.Context, string) (Result, error)
}

type DailyMonitor struct {
	syncer     DailySyncer
	log        *logging.Logger
	Now        func() time.Time
	through    string
	completed  bool
	nextRetry  time.Time
	retryDelay time.Duration
}

func NewDailyMonitor(syncer DailySyncer, log *logging.Logger) *DailyMonitor {
	if log == nil {
		log = logging.Nop()
	}
	return &DailyMonitor{syncer: syncer, log: log, Now: time.Now}
}

func scheduledThrough(now time.Time) string {
	now = now.In(market.Shanghai)
	if now.Hour()*60+now.Minute() < dailyScheduleTime {
		now = now.AddDate(0, 0, -1)
	}
	for now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		now = now.AddDate(0, 0, -1)
	}
	return now.Format("2006-01-02")
}

func (m *DailyMonitor) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	now := m.Now()
	through := scheduledThrough(now)
	if m.through != through {
		m.through, m.completed = through, false
		m.nextRetry, m.retryDelay = time.Time{}, 0
	}
	if m.completed || now.Before(m.nextRetry) {
		return
	}
	cycle, cancel := context.WithTimeout(ctx, dailySyncTimeout)
	result, err := m.syncer.SyncThrough(cycle, through)
	cancel()
	if ctx.Err() != nil {
		return
	}
	if err == nil && result.Status != "complete" {
		err = fmt.Errorf("HK Connect synchronization returned status %q", result.Status)
	}
	if err != nil {
		if m.retryDelay == 0 {
			m.retryDelay = dailyRetryInitial
		} else {
			m.retryDelay *= 2
			if m.retryDelay > dailyRetryMaximum {
				m.retryDelay = dailyRetryMaximum
			}
		}
		m.nextRetry = m.Now().Add(m.retryDelay)
		m.log.Warn("HK Connect scheduled synchronization failed", "through", through, "status", result.Status, "partial", result.Partial, "failed", result.Failed, "reason", err.Error(), "retry_in", m.retryDelay.String())
		return
	}
	m.completed = true
	m.log.Info("HK Connect scheduled synchronization completed", "through", through, "as_of", result.AsOf, "succeeded", result.Succeeded, "skipped", result.Skipped)
}

func (m *DailyMonitor) Run(ctx context.Context) error {
	if m == nil || m.syncer == nil || m.Now == nil {
		return errors.New("HK Connect daily monitor is not configured")
	}
	m.log.Info("HK Connect daily monitor started", "schedule", "18:50 Asia/Shanghai", "workers", defaultWorkers, "timeout", dailySyncTimeout.String())
	for {
		m.tick(ctx)
		timer := time.NewTimer(dailyPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
