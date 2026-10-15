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

type WindowSource interface {
	FetchWindow(context.Context, config.Stock, string, string, string, int) (market.History, error)
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
	Market   *storage.MarketDB
	Source   Source
	Channels []notify.Channel
	Log      *logging.Logger
	Calendar *calendar.Calendar
	Now      func() time.Time
}

func New(c config.Config, s *storage.Store, source Source, channels []notify.Channel, log *logging.Logger) *Monitor {
	return NewWithMarketDB(c, s, nil, source, channels, log)
}

func NewWithMarketDB(c config.Config, s *storage.Store, marketDB *storage.MarketDB, source Source, channels []notify.Channel, log *logging.Logger) *Monitor {
	if log == nil {
		log = logging.Nop()
	}
	return &Monitor{
		Config:   c,
		Store:    s,
		Market:   marketDB,
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
	// v1 recorded successful delivery by date. Preserve it before a startup
	// refresh replaces the report, otherwise upgrading can resend that date.
	if raw.LastDelivered != "" {
		target, err := m.Calendar.Next(raw.LastDelivered)
		if err != nil {
			return state, err
		}
		if !calendar.Expired(target, m.Now()) {
			var report selection.Report
			if err := m.Store.Read("reports/"+raw.LastDelivered+".json", &report); err != nil {
				return state, fmt.Errorf("cannot recover legacy delivered report: %w", err)
			}
			if validateLegacyReport(report, 0) != nil || report.AsOf != raw.LastDelivered || report.Target != target {
				return state, errors.New("invalid legacy delivered report")
			}
			message, err := reportmessage.FromSelection(report)
			if err != nil {
				return state, err
			}
			channels := m.legacyChannels()
			if err := state.Enqueue(report.AsOf, message, channels); err != nil {
				return state, err
			}
			for _, name := range channels {
				if err := state.MarkDelivered(message.EventID, name); err != nil {
					return state, err
				}
			}
		}
	}
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
		channels := m.legacyChannels()
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

func (m *Monitor) legacyChannels() []string {
	// v1 only had the Feishu sender. Preserve its configured name, or the
	// legacy default if notifications are temporarily disabled.
	for _, configured := range m.Config.NotificationChannels() {
		if configured.Enabled && configured.Type == "feishu" {
			return []string{configured.Name}
		}
	}
	return []string{"feishu-main"}
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

// Sync downloads every pool entry before publishing any report. Any incomplete
// stock blocks publication without changing the previous published snapshot.
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
	start := historyStart(end, m.Config.Daily.HistoryDays)
	analysisStart := end.AddDate(0, 0, -m.Config.Daily.HistoryDays).Format("2006-01-02")
	m.Log.Info("Daily history synchronization started", "as_of", asof, "history_start", start, "stocks", len(m.Config.Stocks))
	histories := make([]market.History, 0, len(m.Config.Stocks))
	raw := make([]market.History, 0, len(m.Config.Stocks))
	items := make([]storage.SyncItem, 0, len(m.Config.Stocks))
	started := m.Now()
	run := storage.SyncRun{ID: fmt.Sprintf("%s-%d", asof, started.UnixNano()), AsOf: asof, StartedAt: started}
	for i, stock := range m.Config.Stocks {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := m.Store.Check(); err != nil {
			return report, err
		}
		adjustedStart := analysisStart
		if m.Config.Daily.Adjust == "none" {
			adjustedStart = start
		}
		history, err := m.fetchHistory(ctx, stock, adjustedStart, asof, m.Config.Daily.Adjust)
		if err != nil {
			if market.IsIsolatable(err) {
				items = append(items, storage.SyncItem{Stock: stock, Status: "isolated", Reason: syncReason(err)})
				m.Log.Warn("Daily stock isolated", "code", stock.Code, "adjust", m.Config.Daily.Adjust, "reason", syncReason(err))
				continue
			}
			return m.failSync(ctx, run, fmt.Errorf("download failed for %s: %w", stock.Code, err))
		}
		if err := validateHistory(m.Calendar, stock, history, adjustedStart, asof); err != nil {
			items = append(items, storage.SyncItem{Stock: stock, Status: "isolated", Reason: syncReason(err)})
			m.Log.Warn("Daily stock isolated", "code", stock.Code, "adjust", history.Adjust, "reason", syncReason(err))
			continue
		}
		histories = append(histories, history)
		item := storage.SyncItem{Stock: stock, Adjusted: &history, Status: "ok"}
		if m.Config.Daily.Adjust != "none" {
			select {
			case <-ctx.Done():
				return report, ctx.Err()
			case <-time.After(350 * time.Millisecond):
			}
			r, err := m.fetchHistory(ctx, stock, start, asof, "none")
			if err != nil {
				histories = histories[:len(histories)-1]
				if market.IsIsolatable(err) {
					items = append(items, storage.SyncItem{Stock: stock, Status: "isolated", Reason: syncReason(err)})
					m.Log.Warn("Daily stock isolated", "code", stock.Code, "adjust", "none", "reason", syncReason(err))
					continue
				}
				return m.failSync(ctx, run, fmt.Errorf("raw download failed for %s: %w", stock.Code, err))
			}
			if err := validateHistory(m.Calendar, stock, r, start, asof); err != nil {
				histories = histories[:len(histories)-1]
				if market.IsIsolatable(err) {
					items = append(items, storage.SyncItem{Stock: stock, Status: "isolated", Reason: syncReason(err)})
					m.Log.Warn("Daily stock isolated", "code", stock.Code, "adjust", "none", "reason", syncReason(err))
					continue
				}
				return m.failSync(ctx, run, fmt.Errorf("raw data failed for %s: %w", stock.Code, err))
			}
			raw = append(raw, r)
			item.Raw = &r
		} else {
			raw = append(raw, history)
		}
		items = append(items, item)
		if (i+1)%10 == 0 {
			m.Log.Info("Daily download progress", "completed", i+1, "total", len(m.Config.Stocks), "as_of", asof, "adjusted_valid", len(histories), "raw_valid", len(raw))
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
	coverage := float64(len(raw)) / float64(len(m.Config.Stocks)) * 100
	run.CoveragePct = coverage
	run.FinishedAt = m.Now()
	if len(histories) != len(m.Config.Stocks) || len(raw) != len(m.Config.Stocks) {
		return m.failSync(ctx, run, fmt.Errorf("sync coverage %.2f%% is below required 100%%; every stock must have complete history", coverage))
	}
	report, err = m.buildReport(ctx, histories, raw, asof, target)
	if err != nil {
		return m.failSync(ctx, run, err)
	}
	if m.Market != nil && m.Market.HasDatabase() {
		if err := m.Market.CommitSnapshot(ctx, run, items, report); err != nil {
			return report, err
		}
	} else {
		// Compatibility path for package-level tests and old callers. The CLI
		// always opens the SQLite market database before constructing Monitor.
		for _, item := range items {
			if item.Adjusted != nil {
				if err := m.Store.Write("daily/"+asof+"/"+item.Stock.Code+".json", *item.Adjusted); err != nil {
					return report, err
				}
			}
			if item.Raw != nil {
				if err := m.Store.Write("execution/"+asof+"/"+item.Stock.Code+".json", *item.Raw); err != nil {
					return report, err
				}
			}
		}
		if err := m.Store.Write("reports/"+asof+".json", report); err != nil {
			return report, err
		}
	}
	m.Log.Info("Daily report saved", "as_of", asof, "target", target, "selected", len(report.Selected), "eligible", report.Eligible, "event_id", report.ID, "coverage_pct", coverage, "adjusted_valid", len(histories), "raw_valid", len(raw))
	return report, nil
}

func (m *Monitor) loadPreviousHistory(ctx context.Context, code, adjust, asof string) (market.History, error) {
	if m.Market != nil && m.Market.HasDatabase() {
		return m.Market.LoadLatestHistory(ctx, code, adjust, asof)
	}
	return market.History{}, fs.ErrNotExist
}

func historyStart(end time.Time, days int) string {
	start := end.AddDate(-10, 0, 0)
	if start.Month() != end.Month() {
		// Clamp Feb 29 to Feb 28 instead of normalizing it into March.
		start = start.AddDate(0, 0, -1)
	}
	if configured := end.AddDate(0, 0, -days); configured.Before(start) {
		start = configured
	}
	return start.Format("2006-01-02")
}

func (m *Monitor) fetchHistory(ctx context.Context, stock config.Stock, start, asof, adjust string) (market.History, error) {
	if stock.ListedFrom > start {
		start = stock.ListedFrom
	}
	if start > asof {
		return market.History{}, &market.DataError{Reason: "listed_from is after requested session"}
	}
	if source, ok := m.Source.(WindowSource); ok && adjust == "none" && m.Market != nil && m.Market.HasDatabase() {
		previous, err := m.loadPreviousHistory(ctx, stock.Code, adjust, asof)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return market.History{}, err
		}
		// A pre-upgrade short window must be backfilled before incremental sync
		// can be used. A shared session also detects outages longer than count.
		if err == nil && len(previous.Bars) > 0 && previous.Bars[0].Date <= start {
			recent, err := source.FetchWindow(ctx, stock, start, asof, adjust, m.Config.Sync.IncrementalBars)
			if err != nil {
				return market.History{}, err
			}
			if len(recent.Bars) > 0 && recent.Bars[0].Date <= previous.Bars[len(previous.Bars)-1].Date {
				merged := mergeHistory(previous, recent, stock, start, asof)
				if validateHistory(m.Calendar, stock, merged, start, asof) == nil {
					return merged, nil
				}
			}
		}
	}
	return m.Source.Fetch(ctx, stock, start, asof, adjust)
}

func mergeHistory(previous, recent market.History, stock config.Stock, start, asof string) market.History {
	byDate := map[string]market.Bar{}
	for _, bar := range previous.Bars {
		byDate[bar.Date] = bar
	}
	for _, bar := range recent.Bars {
		byDate[bar.Date] = bar
	}
	dates := make([]string, 0, len(byDate))
	for date := range byDate {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	bars := make([]market.Bar, 0, len(dates))
	var anchor *market.Bar
	for _, date := range dates {
		if date < start {
			bar := byDate[date]
			anchor = &bar
		}
		if date >= start && date <= asof {
			bars = append(bars, byDate[date])
		}
	}
	if anchor != nil && (len(bars) == 0 || bars[0].Date != start) {
		bars = append([]market.Bar{*anchor}, bars...)
	}
	recent.Code = stock.Code
	if stock.Name != "" {
		recent.Name = stock.Name
	} else if recent.Name == "" {
		recent.Name = previous.Name
	}
	recent.Bars = bars
	return recent
}

func (m *Monitor) failSync(ctx context.Context, run storage.SyncRun, err error) (selection.Report, error) {
	run.FinishedAt = m.Now()
	if run.FinishedAt.IsZero() {
		run.FinishedAt = time.Now()
	}
	run.ErrorKind = "sync"
	if m.Market != nil && m.Market.HasDatabase() {
		_ = m.Market.RecordFailedRun(ctx, run)
	}
	return selection.Report{}, err
}

func syncReason(err error) string {
	var dataErr *market.DataError
	if errors.As(err, &dataErr) {
		return dataErr.Reason
	}
	var failure *market.Failure
	if errors.As(err, &failure) {
		if failure.Status > 0 {
			return fmt.Sprintf("http_%d", failure.Status)
		}
		return failure.Kind
	}
	return "invalid_data"
}

func validateHistory(cal *calendar.Calendar, stock config.Stock, history market.History, start, asof string) error {
	if err := history.Validate(); err != nil {
		return &market.DataError{Reason: err.Error()}
	}
	if history.AsOf != asof || len(history.Bars) == 0 || history.Bars[len(history.Bars)-1].Date != asof {
		return &market.DataError{Reason: "stale_or_suspended"}
	}
	if history.Adjust == "qfq" && history.DataField != "qfqday" {
		return &market.DataError{Reason: "qfq_adjustment_missing"}
	}
	windowStart := start
	if stock.ListedFrom != "" && stock.ListedFrom > windowStart {
		windowStart = stock.ListedFrom
	}
	// Do not let a calendar covering only the current year disguise a short
	// history as a complete decade. The preceding session is a boundary anchor.
	if history.Adjust == "none" && windowStart < cal.ValidFrom() && history.Bars[0].Date > windowStart {
		return &market.DataError{Reason: "historical_coverage_incomplete"}
	}
	if cal.ValidFrom() > windowStart {
		windowStart = cal.ValidFrom()
	}
	expected, err := cal.TradingDays(windowStart, asof)
	if err != nil {
		return &market.DataError{Reason: "calendar_window"}
	}
	if len(expected) == 0 {
		return &market.DataError{Reason: "window_gap"}
	}
	first := 0
	for first < len(history.Bars) && history.Bars[first].Date < expected[0] {
		first++
	}
	if first == len(history.Bars) || history.Bars[first].Date != expected[0] {
		return &market.DataError{Reason: "window_gap"}
	}
	if len(history.Bars)-first != len(expected) {
		return &market.DataError{Reason: "window_gap"}
	}
	for i, day := range expected {
		if history.Bars[first+i].Date != day {
			return &market.DataError{Reason: "window_gap"}
		}
	}
	return nil
}

func (m *Monitor) Preview(asof string) (selection.Report, error) {
	return m.PreviewContext(context.Background(), asof)
}
func (m *Monitor) PreviewContext(ctx context.Context, asof string) (selection.Report, error) {
	if m.Market != nil && m.Market.HasDatabase() {
		histories, raw, err := m.Market.LoadHistoriesPair(ctx, m.Config.Stocks, asof, m.Config.Daily.Adjust, m.Config.Strategy.Enabled)
		if err == nil {
			return m.buildReport(ctx, histories, raw, asof, mustNext(m.Calendar, asof))
		}
		return selection.Report{}, err
	}
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
	return m.buildReport(ctx, histories, raw, asof, target)
}

func (m *Monitor) buildReport(ctx context.Context, histories, raw []market.History, asof, target string) (selection.Report, error) {
	end, err := time.Parse("2006-01-02", asof)
	if err != nil {
		return selection.Report{}, err
	}
	start := end.AddDate(0, 0, -m.Config.Daily.HistoryDays).Format("2006-01-02")
	// Raw archival history is longer than the adjusted analysis window. Clip
	// copies for analysis so strategy alignment and persisted data stay intact.
	window := func(histories []market.History) []market.History {
		out := make([]market.History, len(histories))
		for i, h := range histories {
			first := sort.Search(len(h.Bars), func(i int) bool { return h.Bars[i].Date >= start })
			h.Bars = h.Bars[first:]
			out[i] = h
		}
		return out
	}
	return selection.BuildWithExecution(ctx, m.Config, window(histories), window(raw), asof, target, m.Now())
}

func mustNext(cal *calendar.Calendar, asof string) string {
	target, _ := cal.Next(asof)
	return target
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
	report, reportErr := m.readReport(ctx, asof)
	err = reportErr
	if errors.Is(err, fs.ErrNotExist) {
		report, err = m.Sync(ctx, asof)
	} else if err == nil && report.Target != target {
		err = errors.New("stored report T+1 changed during synchronization")
	}
	if err != nil {
		return errors.Join(deliveryErr, err)
	}
	if _, err := m.enqueueReport(&state, report); err != nil {
		return errors.Join(deliveryErr, err)
	}
	return errors.Join(deliveryErr, m.flushState(ctx, &state))
}

func (m *Monitor) readReport(ctx context.Context, asof string) (selection.Report, error) {
	if m.Market != nil && m.Market.HasDatabase() {
		return m.Market.LoadReport(ctx, asof)
	}
	var report selection.Report
	if err := m.Store.Read("reports/"+asof+".json", &report); err != nil {
		return report, err
	}
	return report, nil
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
	startupReady := false
	for {
		var err error
		if !startupReady {
			err = m.prepareStartup(ctx)
			startupReady = err == nil
		}
		if startupReady {
			err = m.Cycle(ctx)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		delay := config.Duration(m.Config.PollInterval)
		if err != nil {
			delay = max(delay, time.Minute)
			m.Log.Warn("Monitor cycle failed", "startup_ready", startupReady, "retry_after", delay.String(), "error", err)
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
