// Package health combines read-only operational metadata. Liveness belongs to
// the HTTP server; readiness and business freshness have different meanings.
package health

import (
	"context"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/intraday"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/monitor"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"time"
)

type Status struct {
	Version       int               `json:"schema_version"`
	Ready         bool              `json:"ready"`
	Business      string            `json:"business"`
	ExpectedDate  string            `json:"expected_date"`
	Available     int               `json:"available"`
	PoolSize      int               `json:"pool_size"`
	AnalysisReady bool              `json:"analysis_ready"`
	Task          *storage.SyncTask `json:"task,omitempty"`
	Issues        []string          `json:"issues"`
	Notifications map[string]int    `json:"notifications"`
	NextAttempt   *time.Time        `json:"next_attempt,omitempty"`
	MinuteSource  string            `json:"minute_source"`
	MinuteCurrent int               `json:"minute_current"`
	MinuteEnabled bool              `json:"minute_enabled"`
	Predictor     *PredictorStatus  `json:"predictor,omitempty"`
}

func Read(ctx context.Context, c config.Config, db *storage.MarketDB, now time.Time) (Status, error) {
	out := Status{Version: 1, Business: "degraded", PoolSize: len(c.Stocks), Issues: []string{}, Notifications: map[string]int{}, MinuteSource: "tencent", MinuteEnabled: c.Intraday.Enabled}
	cal := calendar.New(c)
	day, err := cal.Completed(now)
	if err != nil {
		return out, err
	}
	// Until the scheduled daily refresh, the previous session is still expected.
	// This also keeps normal weekends and exchange holidays from looking stale.
	completed := day
	local := now.In(market.Shanghai)
	if day == calendar.Date(local) && local.Format("15:04") < c.Daily.RunAt {
		midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, market.Shanghai)
		day, err = cal.Completed(midnight)
		if err != nil {
			return out, err
		}
	}
	out.ExpectedDate = day
	data, err := db.RuntimeStatus(ctx, c.Stocks, day, c.Daily.Adjust)
	if err != nil {
		return out, err
	}
	// Startup may have already published the newer completed session.
	if completed != day {
		current, err := db.RuntimeStatus(ctx, c.Stocks, completed, c.Daily.Adjust)
		if err != nil {
			return out, err
		}
		if current.ReportReady && len(c.Stocks) > 0 && float64(current.Available)*100/float64(len(c.Stocks)) >= c.Sync.MinCoveragePct {
			data = current
			out.ExpectedDate = completed
		}
	}
	view, err := storage.OpenView(c.DataDir, c.RequireCIFS)
	if err != nil {
		return out, err
	}
	defer view.Close()
	m := monitor.New(c, view, nil, nil, nil)
	m.Now = func() time.Time { return now }
	ledger, err := m.Status()
	if err != nil {
		return out, err
	}
	out.Ready = true
	out.Available = data.Available
	if predictor, issues := predictorHealth(view, c, cal, now); predictor != nil {
		out.Predictor = predictor
		out.Issues = append(out.Issues, issues...)
	}
	out.AnalysisReady = data.ReportReady
	out.Task = data.Task
	if len(c.Stocks) == 0 {
		return out, errors.New("stock pool is empty")
	}
	if float64(data.Available)*100/float64(len(c.Stocks)) < c.Sync.MinCoveragePct {
		out.Issues = append(out.Issues, "daily_data_stale_or_incomplete")
	}
	if !data.ReportReady {
		out.Issues = append(out.Issues, "analysis_not_published")
	}
	if data.Task != nil {
		if data.Task.Status == "failed" {
			out.Issues = append(out.Issues, "task_failed_"+data.Task.ErrorKind)
		}
		if data.Task.Status == "running" && now.Sub(data.Task.UpdatedAt) > 2*time.Minute {
			out.Issues = append(out.Issues, "task_heartbeat_stale")
		}
	}
	for _, event := range ledger.Events {
		for _, state := range event.Channels {
			status := state.Status
			if !now.Before(event.ExpiresAt) && status != notify.Delivered {
				status = notify.Expired
			}
			out.Notifications[string(status)]++
			if status == notify.Blocked || status == notify.Unknowned {
				out.Issues = appendUnique(out.Issues, "notification_"+string(status))
			}
			if (status == notify.Pending || status == notify.Unknowned) && !state.NextAttempt.IsZero() {
				if out.NextAttempt == nil || state.NextAttempt.Before(*out.NextAttempt) {
					next := state.NextAttempt
					out.NextAttempt = &next
				}
			}
			if status == notify.Pending && !state.NextAttempt.IsZero() && now.After(state.NextAttempt.Add(2*config.Duration(c.PollInterval))) {
				out.Issues = appendUnique(out.Issues, "notification_overdue")
			}
		}
	}
	if c.Intraday.Enabled {
		plan, err := intraday.Schedule(cal, now, c.Intraday.CalibrationAt)
		if err != nil {
			return out, err
		}
		for _, s := range data.Minutes {
			current := s.Status == "ok" && s.Target.Equal(plan.Target)
			if plan.Active {
				current = (s.Status == "ok" || s.Status == "partial") && !s.Target.Before(plan.Target.Add(-5*time.Minute)) && now.Sub(s.AttemptAt) <= 2*config.Duration(c.Intraday.PollInterval)
			}
			if plan.Calibration {
				current = current && s.CalibratedDate == calendar.Date(plan.Target)
			}
			if current {
				out.MinuteCurrent++
			}
		}
		if out.MinuteCurrent < len(c.Stocks) {
			out.Issues = append(out.Issues, "minute_data_stale_or_incomplete")
		}
	}
	if len(out.Issues) == 0 {
		out.Business = "healthy"
	} else if data.Task != nil && data.Task.Status == "running" && now.Sub(data.Task.UpdatedAt) <= 2*time.Minute {
		out.Business = "syncing"
	}
	return out, nil
}
func appendUnique(values []string, value string) []string {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	return append(values, value)
}
