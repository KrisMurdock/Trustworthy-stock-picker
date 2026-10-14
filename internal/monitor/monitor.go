package monitor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"stock-watcher/internal/calendar"
	"stock-watcher/internal/config"
	"stock-watcher/internal/logging"
	"stock-watcher/internal/market"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/storage"
	"time"
)

type Source interface {
	Fetch(context.Context, config.Stock, string, string, string) (market.History, error)
}
type Sender interface {
	Send(context.Context, selection.Report) error
}
type Pending struct {
	Report      selection.Report `json:"report"`
	Attempts    int              `json:"attempts"`
	NextAttempt time.Time        `json:"next_attempt"`
	Blocked     bool             `json:"blocked"`
}
type State struct {
	Version       int      `json:"schema_version"`
	LastDelivered string   `json:"last_delivered_date"`
	LastExpired   string   `json:"last_expired_date"`
	Pending       *Pending `json:"pending,omitempty"`
}
type Monitor struct {
	Config   config.Config
	Store    *storage.Store
	Source   Source
	Sender   Sender
	Log      *logging.Logger
	Calendar *calendar.Calendar
	Now      func() time.Time
}

func New(c config.Config, s *storage.Store, source Source, sender Sender, log *logging.Logger) *Monitor {
	return &Monitor{Config: c, Store: s, Source: source, Sender: sender, Log: log, Calendar: calendar.New(c), Now: time.Now}
}
func (m *Monitor) info(msg string, a ...any) {
	if m.Log != nil {
		m.Log.Info(msg, a...)
	}
}
func (m *Monitor) warn(msg string, a ...any) {
	if m.Log != nil {
		m.Log.Warn(msg, a...)
	}
}

func (m *Monitor) readState() (State, error) {
	s := State{}
	err := m.Store.Read("state/delivery.json", &s)
	if errors.Is(err, fs.ErrNotExist) {
		return State{Version: 1}, nil
	}
	if err != nil {
		return s, err
	}
	if s.Version != 1 {
		return s, errors.New("unsupported delivery state version")
	}
	if s.Pending != nil {
		r := s.Pending.Report
		if (r.Version != 1 && r.Version != 2) || r.ID == "" || r.AsOf == "" || r.Target == "" || !r.SelectionEnabled || s.Pending.Attempts < 0 {
			return s, errors.New("invalid persisted delivery state")
		}
		if r.Version == 2 && (r.Strategy != "short_horizon_v1" || r.Research == nil || r.Research.Version != 1) {
			return s, errors.New("invalid persisted strategy report")
		}
		if r.Version == 2 {
			if r.Research.AsOf != r.AsOf || r.Research.Strategy != r.Strategy || !r.Research.Parameters.Enabled || r.Research.Parameters.Validate() != nil || len(r.Ranking) == 0 || r.Eligible != len(r.Ranking) {
				return s, errors.New("invalid persisted strategy metadata")
			}
			for _, candidate := range r.Ranking {
				if candidate.Plan != nil && candidate.Plan.Version != 1 {
					return s, errors.New("unsupported persisted trade plan version")
				}
			}
		}
		next, err := m.Calendar.Next(r.AsOf)
		if err != nil || next != r.Target {
			return s, errors.New("pending report has an invalid T+1 date")
		}
	}
	for _, day := range []string{s.LastDelivered, s.LastExpired} {
		if day != "" {
			if _, err := time.Parse("2006-01-02", day); err != nil {
				return s, errors.New("invalid state date")
			}
		}
	}
	return s, nil
}

// Sync downloads every pool entry before publishing any report; failed pools never send a partial recommendation.
func (m *Monitor) Sync(ctx context.Context, asof string) (selection.Report, error) {
	var report selection.Report
	ok, err := m.Calendar.Trading(asof)
	if err != nil || !ok {
		return report, errors.New("as-of must be a covered trading date")
	}
	latest, err := m.Calendar.Completed(m.Now())
	if err != nil || asof > latest {
		return report, errors.New("as-of session has not completed")
	}
	target, err := m.Calendar.Next(asof)
	if err != nil {
		return report, err
	}
	end, _ := time.Parse("2006-01-02", asof)
	start := end.AddDate(0, 0, -m.Config.Daily.HistoryDays).Format("2006-01-02")
	histories := make([]market.History, 0, len(m.Config.Stocks))
	raw := make([]market.History, 0, len(m.Config.Stocks))
	for i, stock := range m.Config.Stocks {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := m.Store.Check(); err != nil {
			return report, err
		}
		h, err := m.Source.Fetch(ctx, stock, start, asof, m.Config.Daily.Adjust)
		if err != nil {
			return report, fmt.Errorf("download failed for %s: %w", stock.Code, err)
		}
		if err := m.Store.Write("daily/"+asof+"/"+stock.Code+".json", h); err != nil {
			return report, fmt.Errorf("daily storage failed for %s", stock.Code)
		}
		histories = append(histories, h)
		if m.Config.Strategy.Enabled {
			select {
			case <-ctx.Done():
				return report, ctx.Err()
			case <-time.After(350 * time.Millisecond):
			}
			r, err := m.Source.Fetch(ctx, stock, start, asof, "none")
			if err != nil {
				return report, fmt.Errorf("raw download failed for %s: %w", stock.Code, err)
			}
			if err := m.Store.Write("execution/"+asof+"/"+stock.Code+".json", r); err != nil {
				return report, fmt.Errorf("raw storage failed for %s", stock.Code)
			}
			raw = append(raw, r)
		}
		if (i+1)%10 == 0 {
			m.info("Daily download progress", "completed", i+1, "total", len(m.Config.Stocks), "as_of", asof)
		}
		if i < len(m.Config.Stocks)-1 {
			timer := time.NewTimer(350 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return report, ctx.Err()
			case <-timer.C:
			}
		}
	}
	report, err = selection.BuildWithExecution(ctx, m.Config, histories, raw, asof, target, m.Now())
	if err != nil {
		return report, err
	}
	if err := m.Store.Write("reports/"+asof+".json", report); err != nil {
		return report, err
	}
	m.info("Daily report saved", "as_of", asof, "target", target, "selected", len(report.Selected), "eligible", report.Eligible, "event_id", report.ID)
	return report, nil
}
func (m *Monitor) Preview(asof string) (selection.Report, error) {
	return m.PreviewContext(context.Background(), asof)
}
func (m *Monitor) PreviewContext(ctx context.Context, asof string) (selection.Report, error) {
	histories := make([]market.History, 0, len(m.Config.Stocks))
	raw := make([]market.History, 0, len(m.Config.Stocks))
	for _, s := range m.Config.Stocks {
		if err := ctx.Err(); err != nil {
			return selection.Report{}, err
		}
		var h market.History
		if err := m.Store.Read("daily/"+asof+"/"+s.Code+".json", &h); err != nil {
			return selection.Report{}, err
		}
		histories = append(histories, h)
		if m.Config.Strategy.Enabled {
			var r market.History
			if err := m.Store.Read("execution/"+asof+"/"+s.Code+".json", &r); err != nil {
				return selection.Report{}, fmt.Errorf("raw execution history unavailable for %s; sync with strategy enabled", s.Code)
			}
			raw = append(raw, r)
		}
	}
	target, err := m.Calendar.Next(asof)
	if err != nil {
		return selection.Report{}, err
	}
	return selection.BuildWithExecution(ctx, m.Config, histories, raw, asof, target, m.Now())
}

func (m *Monitor) Cycle(ctx context.Context) error {
	now := m.Now()
	asof, target, due, err := m.Calendar.Due(now, m.Config.Daily.RunAt)
	if err != nil {
		return err
	}
	state, err := m.readState()
	if err != nil {
		return err
	}
	if state.Pending != nil && calendar.Expired(state.Pending.Report.Target, now) {
		state.LastExpired = state.Pending.Report.AsOf
		state.Pending = nil
		if err := m.Store.Write("state/delivery.json", state); err != nil {
			return err
		}
		m.warn("Expired recommendation discarded", "as_of", state.LastExpired)
	}
	if state.Pending != nil {
		return m.deliver(ctx, &state)
	}
	if !due || state.LastDelivered >= asof || state.LastExpired >= asof {
		return nil
	}
	report, err := m.Sync(ctx, asof)
	if err != nil {
		return err
	}
	if report.Target != target {
		return errors.New("report T+1 changed during synchronization")
	}
	if !m.Config.Feishu.Enabled {
		// Report-only mode still records completion to avoid repeated full downloads.
		state.LastDelivered = asof
		return m.Store.Write("state/delivery.json", state)
	}
	state.Pending = &Pending{Report: report}
	if err := m.Store.Write("state/delivery.json", state); err != nil {
		return err
	}
	return m.deliver(ctx, &state)
}
func (m *Monitor) deliver(ctx context.Context, state *State) error {
	p := state.Pending
	if p == nil {
		return nil
	}
	if p.Blocked {
		return errors.New("delivery blocked by permanent error; inspect logs and use retry after correction")
	}
	if m.Now().Before(p.NextAttempt) {
		return nil
	}
	if !m.Config.Feishu.Enabled || m.Sender == nil {
		return errors.New("pending delivery requires enabled Feishu configuration")
	}
	if calendar.Expired(p.Report.Target, m.Now()) {
		return errors.New("recommendation delivery window expired")
	}
	if err := m.Store.Check(); err != nil {
		return err
	}
	err := m.Sender.Send(ctx, p.Report)
	if err == nil {
		asof := p.Report.AsOf
		id := p.Report.ID
		state.LastDelivered = asof
		state.Pending = nil
		if err := m.Store.Write("state/delivery.json", state); err != nil {
			return err
		}
		m.info("Recommendation delivered", "as_of", asof, "event_id", id)
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	p.Attempts++
	retryable := true
	var failure interface{ Retryable() bool }
	if errors.As(err, &failure) {
		retryable = failure.Retryable()
	}
	p.Blocked = !retryable
	delay := time.Duration(1<<min(p.Attempts, 8)) * time.Minute
	p.NextAttempt = m.Now().Add(delay)
	if saveErr := m.Store.Write("state/delivery.json", state); saveErr != nil {
		return saveErr
	}
	m.warn("Recommendation delivery failed", "attempts", p.Attempts, "retryable", retryable, "error", err)
	return err
}
func (m *Monitor) Retry() error {
	s, err := m.readState()
	if err != nil {
		return err
	}
	if s.Pending == nil {
		return errors.New("no pending delivery")
	}
	if calendar.Expired(s.Pending.Report.Target, m.Now()) {
		return errors.New("pending recommendation has expired")
	}
	s.Pending.Blocked = false
	s.Pending.NextAttempt = time.Time{}
	return m.Store.Write("state/delivery.json", s)
}
func (m *Monitor) Status() (State, error) { return m.readState() }
func (m *Monitor) Run(ctx context.Context) error {
	for {
		err := m.Cycle(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		delay := config.Duration(m.Config.PollInterval)
		if err != nil {
			m.warn("Monitor cycle failed", "error", err)
			var failure interface{ Retryable() bool }
			if errors.As(err, &failure) && !failure.Retryable() {
				return err
			}
			delay = max(delay, time.Minute)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
