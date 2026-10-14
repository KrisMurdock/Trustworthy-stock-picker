package monitor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"stock-watcher/internal/calendar"
	"stock-watcher/internal/config"
	"stock-watcher/internal/logging"
	"stock-watcher/internal/market"
	"stock-watcher/internal/notify"
	reportmessage "stock-watcher/internal/notify/report"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/storage"
	"time"
)

type Source interface {
	Fetch(context.Context, config.Stock, string, string, string) (market.History, error)
}

type State = notify.Ledger

type legacyPending struct {
	Report      selection.Report `json:"report"`
	Attempts    int              `json:"attempts"`
	NextAttempt time.Time        `json:"next_attempt"`
	Blocked     bool             `json:"blocked"`
}

type persistedState struct {
	Version       int                     `json:"schema_version"`
	Events        map[string]notify.Event `json:"events,omitempty"`
	LastDelivered string                  `json:"last_delivered_date,omitempty"`
	LastExpired   string                  `json:"last_expired_date,omitempty"`
	Pending       *legacyPending          `json:"pending,omitempty"`
}

type Monitor struct {
	Config   config.Config
	Store    *storage.Store
	Source   Source
	Channels []notify.Channel
	Log      *logging.Logger
	Calendar *calendar.Calendar
	Now      func() time.Time
}

func New(c config.Config, s *storage.Store, source Source, channels []notify.Channel, log *logging.Logger) *Monitor {
	if log == nil {
		log = logging.Nop()
	}
	return &Monitor{
		Config:   c,
		Store:    s,
		Source:   source,
		Channels: channels,
		Log:      log,
		Calendar: calendar.New(c),
		Now:      time.Now,
	}
}

func (m *Monitor) channelNames() []string {
	names := make([]string, 0, len(m.Channels))
	for _, channel := range m.Channels {
		if channel != nil && channel.Name() != "" {
			names = append(names, channel.Name())
		}
	}
	sort.Strings(names)
	return names
}

func (m *Monitor) readState() (State, error) {
	var raw persistedState
	err := m.Store.Read("state/delivery.json", &raw)
	if errors.Is(err, fs.ErrNotExist) {
		return notify.NewLedger(), nil
	}
	if err != nil {
		return State{}, err
	}
	switch raw.Version {
	case 1:
		return m.migrateLegacy(raw)
	case 2:
		state := notify.Ledger{Version: raw.Version, Events: raw.Events}
		if err := state.Validate(); err != nil {
			return state, err
		}
		if state.Normalize(m.Now()) {
			if err := m.writeState(state); err != nil {
				return state, err
			}
		}
		return state, nil
	default:
		return State{}, errors.New("unsupported delivery state version")
	}
}

func (m *Monitor) migrateLegacy(raw persistedState) (State, error) {
	state := notify.NewLedger()
	if raw.Pending != nil {
		pending := raw.Pending
		if err := validateLegacyReport(pending.Report, pending.Attempts); err != nil {
			return state, errors.New("invalid persisted delivery state")
		}
		target, targetErr := m.Calendar.Next(pending.Report.AsOf)
		if targetErr != nil || target != pending.Report.Target {
			return state, errors.New("pending report has an invalid T+1 date")
		}
		message, err := reportmessage.FromSelection(pending.Report)
		if err != nil {
			return state, errors.New("pending report has an invalid T+1 date")
		}
		// v1 only had the Feishu sender. Reuse the configured Feishu channel name
		// when present, otherwise preserve the legacy default for later re-enable.
		channels := []string{"feishu-main"}
		for _, configured := range m.Config.NotificationChannels() {
			if configured.Enabled && configured.Type == "feishu" {
				channels = []string{configured.Name}
				break
			}
		}
		if err := state.Enqueue(pending.Report.AsOf, message, channels); err != nil {
			return state, err
		}
		event := state.Events[message.EventID]
		for _, channel := range channels {
			channelState := event.Channels[channel]
			channelState.Attempts = pending.Attempts
			channelState.NextAttempt = pending.NextAttempt
			if pending.Blocked {
				channelState.Status = notify.Blocked
				channelState.Kind = notify.Permanent
			}
			event.Channels[channel] = channelState
		}
		state.Events[message.EventID] = event
	}
	if err := state.Validate(); err != nil {
		return state, err
	}
	if err := m.writeState(state); err != nil {
		return state, err
	}
	return state, nil
}

func validateLegacyReport(r selection.Report, attempts int) error {
	if (r.Version != 1 && r.Version != 2) || r.ID == "" || r.AsOf == "" || r.Target == "" || !r.SelectionEnabled || attempts < 0 {
		return errors.New("invalid persisted delivery state")
	}
	if r.Version == 2 {
		if r.Strategy != "short_horizon_v1" || r.Research == nil || r.Research.Version != 1 {
			return errors.New("invalid persisted strategy report")
		}
		if r.Research.AsOf != r.AsOf || r.Research.Strategy != r.Strategy || !r.Research.Parameters.Enabled ||
			r.Research.Parameters.Validate() != nil || len(r.Ranking) == 0 || r.Eligible != len(r.Ranking) {
			return errors.New("invalid persisted strategy metadata")
		}
		for _, candidate := range r.Ranking {
			if candidate.Plan != nil && candidate.Plan.Version != 1 {
				return errors.New("unsupported persisted trade plan version")
			}
		}
	}
	return nil
}

func (m *Monitor) writeState(state State) error {
	return m.Store.Write("state/delivery.json", state)
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
		history, err := m.Source.Fetch(ctx, stock, start, asof, m.Config.Daily.Adjust)
		if err != nil {
			return report, fmt.Errorf("download failed for %s: %w", stock.Code, err)
		}
		if err := m.Store.Write("daily/"+asof+"/"+stock.Code+".json", history); err != nil {
			return report, fmt.Errorf("daily storage failed for %s", stock.Code)
		}
		histories = append(histories, history)
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
			m.Log.Info("Daily download progress", "completed", i+1, "total", len(m.Config.Stocks), "as_of", asof)
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
	m.Log.Info("Daily report saved", "as_of", asof, "target", target, "selected", len(report.Selected), "eligible", report.Eligible, "event_id", report.ID)
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
	state, err := m.readState()
	if err != nil {
		return err
	}
	deliveryErr := m.flushState(ctx, &state)
	now := m.Now()
	asof, target, due, dueErr := m.Calendar.Due(now, m.Config.Daily.RunAt)
	if dueErr != nil {
		return errors.Join(deliveryErr, dueErr)
	}
	if !due {
		return deliveryErr
	}
	var report selection.Report
	err = m.Store.Read("reports/"+asof+".json", &report)
	if errors.Is(err, fs.ErrNotExist) {
		report, err = m.Sync(ctx, asof)
	} else if err == nil && report.Target != target {
		err = errors.New("stored report T+1 changed during synchronization")
	}
	if err != nil {
		return errors.Join(deliveryErr, err)
	}
	if len(m.Channels) == 0 {
		return deliveryErr
	}
	message, err := reportmessage.FromSelection(report)
	if err != nil {
		return errors.Join(deliveryErr, err)
	}
	if err := state.Enqueue(report.AsOf, message, m.channelNames()); err != nil {
		return errors.Join(deliveryErr, err)
	}
	if err := m.writeState(state); err != nil {
		return errors.Join(deliveryErr, err)
	}
	return errors.Join(deliveryErr, m.flushState(ctx, &state))
}

func (m *Monitor) Flush(ctx context.Context) error {
	state, err := m.readState()
	if err != nil {
		return err
	}
	return m.flushState(ctx, &state)
}

func (m *Monitor) flushState(ctx context.Context, state *State) error {
	changed := state.Normalize(m.Now())
	if state.Expire(m.Now()) {
		changed = true
	}
	if state.Prune(m.Now(), 30*24*time.Hour) {
		changed = true
	}
	if changed {
		if err := m.writeState(*state); err != nil {
			return err
		}
	}
	channels := map[string]notify.Channel{}
	for _, channel := range m.Channels {
		if channel != nil {
			channels[channel.Name()] = channel
		}
	}
	var firstErr error
	for _, delivery := range state.Due(m.Now()) {
		channel := channels[delivery.Channel]
		if channel == nil {
			continue
		}
		if err := m.Store.Check(); err != nil {
			return err
		}
		if err := state.MarkInFlight(delivery.EventID, delivery.Channel, m.Now()); err != nil {
			return err
		}
		if err := m.writeState(*state); err != nil {
			return err
		}
		err := channel.Send(ctx, delivery.Message)
		if err == nil {
			if err := state.MarkDelivered(delivery.EventID, delivery.Channel); err != nil {
				return err
			}
			if err := m.writeState(*state); err != nil {
				return err
			}
			m.Log.Info("Recommendation delivered", "channel", delivery.Channel, "as_of", state.Events[delivery.EventID].AsOf, "event_id", delivery.EventID)
			continue
		}
		if ctx.Err() != nil {
			if markErr := state.MarkUnknown(delivery.EventID, delivery.Channel); markErr != nil {
				return markErr
			}
			if saveErr := m.writeState(*state); saveErr != nil {
				return saveErr
			}
			return ctx.Err()
		}
		failure := deliveryFailure(delivery.Channel, err)
		if markErr := state.MarkFailure(delivery.EventID, delivery.Channel, failure, m.Now()); markErr != nil {
			return markErr
		}
		if saveErr := m.writeState(*state); saveErr != nil {
			return saveErr
		}
		if failure.Kind == notify.Permanent {
			m.Log.Error("["+delivery.Channel+"] Recommendation delivery blocked", "channel", delivery.Channel, "as_of", state.Events[delivery.EventID].AsOf, "event_id", delivery.EventID, "attempts", state.Events[delivery.EventID].Channels[delivery.Channel].Attempts, "kind", failure.Kind)
		} else {
			m.Log.Warn("["+delivery.Channel+"] Recommendation delivery failed", "channel", delivery.Channel, "as_of", state.Events[delivery.EventID].AsOf, "event_id", delivery.EventID, "attempts", state.Events[delivery.EventID].Channels[delivery.Channel].Attempts, "kind", failure.Kind)
		}
		if firstErr == nil {
			firstErr = failure
		}
	}
	return firstErr
}

func deliveryFailure(channel string, err error) *notify.DeliveryError {
	var failure *notify.DeliveryError
	if errors.As(err, &failure) {
		if failure.Channel == "" {
			copy := *failure
			copy.Channel = channel
			failure = &copy
		}
		if failure.Kind == "" {
			failure.Kind = notify.Unknown
		}
		return failure
	}
	return &notify.DeliveryError{Channel: channel, Kind: notify.Unknown, Reason: "unknown_error"}
}

func (m *Monitor) Retry(channel string) error {
	state, err := m.readState()
	if err != nil {
		return err
	}
	if _, err := state.Retry(channel, m.Now()); err != nil {
		return err
	}
	return m.writeState(state)
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
			m.Log.Warn("Monitor cycle failed", "error", err)
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

func max(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
