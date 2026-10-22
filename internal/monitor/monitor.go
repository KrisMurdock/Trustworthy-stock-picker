package monitor

import (
	"context"
	"errors"
	"fmt"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/marketcontext"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	reportmessage "github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify/report"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"io/fs"
	"sort"
	"time"
)

type Source = market.Source

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
	Engine         selection.Engine
	BuildVersion   string
	Config         config.Config
	Store          *storage.Store
	Market         *storage.MarketDB
	Source         Source
	StatusSource   market.StatusSource
	CalendarSource market.TradingCalendarSource
	Listings       market.ListingSource
	// MarketContextSync synchronizes index and sector-board snapshots together
	// with the stock pool. A failure is logged but never blocks stock data or
	// report publication; the context database records its own partial status.
	MarketContextSync  func(context.Context, string) (marketcontext.SyncResult, error)
	Channels           []notify.Channel
	Log                *logging.Logger
	Calendar           *calendar.Calendar
	Now                func() time.Time
	secondStagePending bool
}

func New(c config.Config, s *storage.Store, source Source, channels []notify.Channel, log *logging.Logger) *Monitor {
	return NewWithMarketDB(c, s, nil, source, channels, log)
}

func NewWithMarketDB(c config.Config, s *storage.Store, marketDB *storage.MarketDB, source Source, channels []notify.Channel, log *logging.Logger) *Monitor {
	if log == nil {
		log = logging.Nop()
	}
	m := &Monitor{
		Config:   c,
		Store:    s,
		Market:   marketDB,
		Source:   source,
		Channels: channels,
		Log:      log,
		Calendar: calendar.New(c),
		Now:      time.Now,
	}
	if listings, ok := source.(market.ListingSource); ok {
		m.Listings = listings
	}
	if statuses, ok := source.(market.StatusSource); ok {
		m.StatusSource = statuses
	}
	if days, ok := source.(market.TradingCalendarSource); ok {
		m.CalendarSource = days
	}
	return m
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

func (m *Monitor) channelNamesForRoute(route string) []string {
	configured := m.Config.NotificationChannels()
	if len(configured) == 0 {
		return m.channelNames()
	}
	routes := make(map[string]string, len(configured))
	for _, channel := range configured {
		routes[channel.Name] = channel.Route
	}
	names := make([]string, 0, len(m.Channels))
	for _, channel := range m.Channels {
		if channel == nil || channel.Name() == "" {
			continue
		}
		channelRoute, known := routes[channel.Name()]
		if !known || channelRoute == "" || channelRoute == config.NotifyRouteAll || channelRoute == route {
			names = append(names, channel.Name())
		}
	}
	sort.Strings(names)
	return names
}

func (m *Monitor) readState() (State, error) { return m.loadState(true) }

// loadState only recovers interrupted sends when the caller owns the writer lock.
func (m *Monitor) loadState(recover bool) (State, error) {
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
		state, err := m.migrateLegacy(raw)
		if err == nil && recover {
			err = m.writeState(state)
		}
		return state, err
	case 2:
		state := notify.Ledger{Version: raw.Version, Events: raw.Events}
		if err := state.Validate(); err != nil {
			return state, err
		}
		if recover && state.Normalize(m.Now()) {
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
			cards, err := reportmessage.FromSelection(report)
			if err != nil {
				return state, err
			}
			channels := m.legacyChannels()
			if err := enqueueCards(&state, report.AsOf, cards, channels, channels, channels); err != nil {
				return state, err
			}
			for _, name := range channels {
				if err := markCardsDelivered(&state, cards, name); err != nil {
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
		cards, err := reportmessage.FromSelection(pending.Report)
		if err != nil {
			return state, errors.New("pending report has an invalid T+1 date")
		}
		channels := m.legacyChannels()
		if err := enqueueCards(&state, pending.Report.AsOf, cards, channels, channels, channels); err != nil {
			return state, err
		}
		event := state.Events[cards.Main.EventID]
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
		state.Events[cards.Main.EventID] = event
	}
	if err := state.Validate(); err != nil {
		return state, err
	}
	return state, nil
}

func enqueueCards(state *State, asof string, cards reportmessage.Cards, selectionChannels, firstStageChannels, secondStageChannels []string) error {
	if len(selectionChannels) > 0 {
		if err := state.EnqueuePart(asof, cards.Main, selectionChannels, "main", 0, ""); err != nil {
			return err
		}
		previous := cards.Main.EventID
		for i, detail := range cards.Details {
			if err := state.EnqueuePart(asof, detail, selectionChannels, "detail", i+1, previous); err != nil {
				return err
			}
			previous = detail.EventID
		}
	}
	if len(firstStageChannels) > 0 {
		previous := ""
		for i, firstStage := range cards.FirstStage {
			if err := state.EnqueuePart(asof, firstStage, firstStageChannels, "first_stage", len(cards.Details)+i+1, previous); err != nil {
				return err
			}
			previous = firstStage.EventID
		}
	}
	if len(secondStageChannels) > 0 {
		previous := ""
		for i, secondStage := range cards.SecondStage {
			if err := state.EnqueuePart(asof, secondStage, secondStageChannels, "second_stage", len(cards.Details)+len(cards.FirstStage)+i+1, previous); err != nil {
				return err
			}
			previous = secondStage.EventID
		}
	}
	return nil
}

func markCardsDelivered(state *State, cards reportmessage.Cards, channel string) error {
	eventIDs := []string{cards.Main.EventID}
	for _, detail := range cards.Details {
		eventIDs = append(eventIDs, detail.EventID)
	}
	for _, firstStage := range cards.FirstStage {
		eventIDs = append(eventIDs, firstStage.EventID)
	}
	for _, secondStage := range cards.SecondStage {
		eventIDs = append(eventIDs, secondStage.EventID)
	}
	for _, eventID := range eventIDs {
		event, ok := state.Events[eventID]
		if !ok {
			continue
		}
		if _, ok := event.Channels[channel]; !ok {
			continue
		}
		if err := state.MarkDelivered(eventID, channel); err != nil {
			return err
		}
	}
	return nil
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

// Sync downloads the entire pool and validates each stock before publication.
// Valid histories remain viewable if the recommendation coverage gate fails.
func (m *Monitor) Sync(ctx context.Context, asof string) (report selection.Report, resultErr error) {
	if err := m.validateAsOf(asof); err != nil {
		return report, err
	}
	target, err := m.Calendar.Next(asof)
	if err != nil {
		return report, err
	}
	timeout := config.Duration(m.Config.Analysis.SyncTimeout)
	if timeout <= 0 {
		timeout = 90 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	waitContext := m.startMarketContextSync(ctx, asof)
	defer waitContext()
	end, _ := time.Parse("2006-01-02", asof)
	analysisStart := end.AddDate(0, 0, -m.Config.Daily.HistoryDays).Format("2006-01-02")
	if analysisStart < historyStart(end) {
		analysisStart = historyStart(end)
	}
	run := storage.SyncRun{ID: fmt.Sprintf("%s-%d", asof, time.Now().UnixNano()), AsOf: asof, StartedAt: m.Now(), FinishedAt: m.Now()}
	durable := m.Market != nil && m.Market.HasDatabase()
	statusMode := m.statusEnabled()
	requiredStart := historyStart(end)
	if statusMode {
		if err := m.ensureCalendar(ctx, requiredStart, asof); err != nil {
			return report, err
		}
		if err := m.refreshRecentStatuses(ctx, asof); err != nil {
			return report, err
		}
	}
	fingerprint, _ := selection.ConfigDigest(m.Config)
	task := storage.SyncTask{ID: run.ID, AsOf: asof, Fingerprint: fingerprint, Phase: "collecting", Status: "running", Total: len(m.Config.Stocks), StartedAt: m.Now(), UpdatedAt: m.Now()}
	cached := map[string]storage.SyncItem{}
	if durable {
		task, cached, err = m.Market.BeginSyncTask(ctx, task)
		if err != nil {
			return report, err
		}
		run.ID, run.StartedAt = task.ID, task.StartedAt
		stopHeartbeat := m.heartbeat(ctx, task.ID)
		defer stopHeartbeat()
		if err := m.Market.RecordRunningSync(ctx, run); err != nil {
			return report, err
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			task.UpdatedAt = m.Now()
			if resultErr != nil {
				task.Status = "failed"
				if task.ErrorKind == "" {
					task.ErrorKind = task.Phase
				}
				run.FinishedAt = m.Now()
				run.ErrorKind = task.ErrorKind
				resultErr = errors.Join(resultErr, m.Market.RecordFailedRun(cleanup, run))
			} else {
				task.Phase = "completed"
				task.Status = "completed"
				task.ErrorKind = ""
			}
			resultErr = errors.Join(resultErr, m.Market.SaveSyncTask(cleanup, task))
		}()
	}
	m.Log.Info("Daily history synchronization started", "as_of", asof, "task_id", task.ID, "resume_stocks", len(cached), "stocks", len(m.Config.Stocks))
	items := make([]storage.SyncItem, 0, len(m.Config.Stocks))
	histories, raw := []market.History{}, []market.History{}
	for i, stock := range m.Config.Stocks {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := m.Store.Check(); err != nil {
			return report, err
		}
		targetStock := stock
		var evidence statusEvidence
		var listingErr error
		if statusMode {
			targetStock, listingErr = m.listingStock(ctx, stock)
			if listingErr == nil {
				stockStart := requiredStart
				if targetStock.ListedFrom > stockStart {
					stockStart = targetStock.ListedFrom
				}
				if stockStart <= asof {
					if err := m.ensureStatusCoverage(ctx, targetStock, stockStart, asof); err != nil {
						return report, err
					}
					evidence, err = m.loadEvidence(ctx, targetStock, stockStart, asof)
					if err != nil {
						return report, err
					}
				}
			}
		}
		item, found := cached[stock.Code]
		if statusMode && listingErr != nil {
			found = false
		}
		// A completed analysis input is frozen even when some stocks were isolated.
		// During collection retries only successful stocks are reused.
		if !found || (item.Status != "ok" && task.Phase == "collecting") {
			if statusMode && listingErr != nil {
				item, err = m.isolateStock(stock, listingErr, "listing_lookup: ")
			} else {
				item, err = m.collectStockWithEvidence(ctx, targetStock, end, analysisStart, asof, evidence)
			}
			if err != nil {
				return report, err
			}
			if durable {
				if err := m.Market.CheckpointStock(ctx, task, item); err != nil {
					return report, err
				}
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
		if statusMode && listingErr == nil {
			item = m.recalibrateItem(item, targetStock, end, analysisStart, asof, evidence)
		}
		items = append(items, item)
		// The execution engine consumes adjusted and raw histories as one
		// aligned snapshot. If either side is unavailable for a strategy run,
		// keep the collected side archived but exclude the stock from the
		// algorithm input so one partial stock cannot fail the whole report.
		requireRaw := m.Config.Strategy.Enabled && m.Config.Daily.Adjust != "none"
		if item.Adjusted != nil && (!requireRaw || item.Raw != nil) {
			histories = append(histories, *item.Adjusted)
			if item.Raw != nil {
				raw = append(raw, *item.Raw)
			}
		}
		task.Completed = i + 1
		task.UpdatedAt = m.Now()
		if durable {
			if err := m.Market.SaveSyncTask(ctx, task); err != nil {
				return report, err
			}
		}
		if (i+1)%10 == 0 || i+1 == len(m.Config.Stocks) {
			m.Log.Info("Daily download progress", "task_id", task.ID, "as_of", asof, "completed", i+1, "total", len(m.Config.Stocks), "adjusted_valid", len(histories), "raw_valid", len(raw))
		}
		m.Log.Debug("Daily collection checkpoint saved", "task_id", task.ID, "code", stock.Code, "status", item.Status, "completed", i+1, "total", len(m.Config.Stocks))
	}
	if len(m.Config.Stocks) == 0 {
		return report, errors.New("stock pool is empty")
	}
	completeCount, analyzableCount := 0, 0
	for _, item := range items {
		if item.Complete {
			completeCount++
		}
		if item.Analyzable {
			analyzableCount++
		}
	}
	run.CoveragePct = float64(completeCount) / float64(len(m.Config.Stocks)) * 100
	run.AnalyzablePct = float64(analyzableCount) / float64(len(m.Config.Stocks)) * 100
	run.FinishedAt = m.Now()
	if run.CoveragePct < m.Config.Sync.MinCoveragePct || run.AnalyzablePct < m.Config.Sync.MinAnalyzablePct {
		task.Phase = "collecting"
		task.ErrorKind = "coverage"
		if durable {
			if err := m.Market.CommitMarketData(ctx, run, items, m.Config.Daily.Adjust); err != nil {
				return report, err
			}
		}
		if run.CoveragePct < m.Config.Sync.MinCoveragePct {
			return report, fmt.Errorf("sync coverage %.2f%% is below %.2f%%", run.CoveragePct, m.Config.Sync.MinCoveragePct)
		}
		return report, fmt.Errorf("sync analyzable coverage %.2f%% is below %.2f%%", run.AnalyzablePct, m.Config.Sync.MinAnalyzablePct)
	}
	if durable {
		if err := m.Market.CommitCollectedData(ctx, run, items, m.Config.Daily.Adjust); err != nil {
			return report, err
		}
		task.Phase = "analyzing"
		task.UpdatedAt = m.Now()
		if err := m.Market.SaveSyncTask(ctx, task); err != nil {
			return report, err
		}
	}
	minutes, err := m.reportMinutes(ctx, histories, asof)
	if err != nil {
		return report, err
	}
	for i := range items {
		for j := range minutes {
			if items[i].Stock.Code == minutes[j].Code {
				items[i].Minutes = append(items[i].Minutes, minutes[j])
			}
		}
	}
	report, err = m.buildReportWithMinutes(ctx, histories, raw, minutes, asof, target)
	if err != nil {
		return report, err
	}
	run.FinishedAt = m.Now()
	if durable {
		if err := m.Market.CommitAnalysis(ctx, run, items, report); err != nil {
			return report, err
		}
		report, err = m.Market.LoadReport(ctx, asof)
		if err != nil {
			return report, err
		}
	} else {
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
	m.Log.Info("Daily report saved", "as_of", asof, "target", target, "selected", len(report.Selected), "eligible", report.Eligible, "event_id", report.ID, "coverage_pct", run.CoveragePct, "input_id", report.InputID)
	return report, nil
}

// startMarketContextSync runs the independent index/board feed alongside the
// stock collection. The caller waits for it before returning so a manual sync
// has persisted every feed, while stock publication remains independent of
// board discovery or board-history failures.
func (m *Monitor) startMarketContextSync(ctx context.Context, asof string) func() {
	if m.MarketContextSync == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err := m.MarketContextSync(ctx, asof)
		if err != nil {
			if ctx.Err() != nil {
				m.Log.Warn("Market context synchronization canceled", "as_of", asof, "reason", ctx.Err().Error())
				return
			}
			m.Log.Warn("Market context synchronization failed", "as_of", asof, "reason", err.Error())
			return
		}
		m.Log.Info("Market context synchronized", "as_of", asof, "status", result.Status, "indices", result.Indices, "boards", result.Boards, "succeeded", result.Succeeded, "failed", result.Failed)
	}()
	return func() { <-done }
}

func (m *Monitor) isolateStock(stock config.Stock, err error, prefix string) (storage.SyncItem, error) {
	if !market.IsProviderFailure(err) {
		return storage.SyncItem{}, err
	}
	reason := prefix + syncReason(err)
	m.Log.Warn("Daily stock isolated", "code", stock.Code, "reason", reason)
	return storage.SyncItem{Stock: stock, Status: "isolated", Reason: reason, RawStatus: "isolated", RawReason: reason}, nil
}

func (m *Monitor) collectStock(ctx context.Context, stock config.Stock, end time.Time, analysisStart, asof string) (storage.SyncItem, error) {
	return m.collectStockWithEvidence(ctx, stock, end, analysisStart, asof, statusEvidence{})
}

func (m *Monitor) collectStockWithEvidence(ctx context.Context, stock config.Stock, end time.Time, analysisStart, asof string, evidence statusEvidence) (storage.SyncItem, error) {
	resolved, err := m.listingStock(ctx, stock)
	if err != nil {
		return m.isolateStock(stock, err, "listing_lookup: ")
	}
	stock = resolved
	statusMode := m.statusEnabled()
	start := requiredHistoryStart(end, stock.ListedFrom)
	adjustedStart := analysisStart
	if m.Config.Daily.Adjust == "none" {
		adjustedStart = start
	}
	if stock.ListedFrom > adjustedStart {
		adjustedStart = stock.ListedFrom
	}
	item := storage.SyncItem{Stock: stock}
	var adjusted market.History
	adjustedErr := error(nil)
	if start <= asof {
		adjusted, adjustedErr = m.fetchHistoryUsing(ctx, stock, adjustedStart, asof, m.Config.Daily.Adjust, func(h market.History) error {
			if statusMode {
				return validateHistoryWithStatus(stock, h, adjustedStart, asof, selectDays(evidence.Days, adjustedStart, asof), evidence.Statuses)
			}
			return validateHistory(m.Calendar, stock, h, adjustedStart, asof)
		})
	} else {
		adjustedErr = &market.DataError{Reason: "listed_from is after requested session"}
	}
	if adjustedErr == nil {
		item.Adjusted = &adjusted
		item.Status = "ok"
		item.Analyzable = historyHasDate(adjusted, asof)
	} else {
		reason, ok := isolateReason(adjustedErr, "")
		if !ok {
			return storage.SyncItem{}, adjustedErr
		}
		item.Status, item.Reason = "isolated", reason
		m.Log.Warn("Adjusted history isolated", "code", stock.Code, "reason", reason)
	}
	var raw market.History
	rawErr := error(nil)
	if m.Config.Daily.Adjust != "none" {
		timer := time.NewTimer(350 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return storage.SyncItem{}, ctx.Err()
		case <-timer.C:
		}
		if start <= asof {
			raw, rawErr = m.fetchHistoryUsing(ctx, stock, start, asof, "none", func(h market.History) error {
				if statusMode {
					return validateHistoryWithStatus(stock, h, start, asof, evidence.Days, evidence.Statuses)
				}
				return validateHistory(m.Calendar, stock, h, start, asof)
			})
		} else {
			rawErr = &market.DataError{Reason: "listed_from is after requested session"}
		}
		if rawErr == nil {
			item.Raw = &raw
			item.RawStatus = "ok"
			item.RawAnalyzable = historyHasDate(raw, asof)
		} else {
			reason, ok := isolateReason(rawErr, "")
			if !ok {
				return storage.SyncItem{}, rawErr
			}
			item.RawStatus, item.RawReason = "isolated", reason
			m.Log.Warn("Raw history isolated", "code", stock.Code, "reason", reason)
			if item.Status == "isolated" && item.Reason == "" {
				item.Reason = reason
			}
		}
	}
	if statusMode {
		var history market.History
		var historyErr error
		if m.Config.Daily.Adjust == "none" {
			history, historyErr = adjusted, adjustedErr
		} else {
			history, historyErr = raw, rawErr
		}
		completeness := evaluateHistory(history, historyErr, evidence.Days, evidence.Statuses, asof)
		item.Sessions, item.Suspended, item.Gaps, item.Complete = completeness.sessions, completeness.suspended, completeness.gaps, completeness.complete
		if m.Config.Daily.Adjust == "none" {
			item.Analyzable = completeness.hasAsOf
		}
	} else {
		if m.Config.Daily.Adjust == "none" {
			item.Complete = item.Adjusted != nil
		} else {
			item.Complete = item.Raw != nil
		}
	}
	return item, nil
}

func isolateReason(err error, prefix string) (string, bool) {
	if !market.IsProviderFailure(err) {
		return "", false
	}
	return prefix + syncReason(err), true
}

func (m *Monitor) recalibrateItem(item storage.SyncItem, stock config.Stock, end time.Time, analysisStart, asof string, evidence statusEvidence) storage.SyncItem {
	if len(evidence.Days) == 0 {
		return item
	}
	item.Analyzable = item.Adjusted != nil && historyHasDate(*item.Adjusted, asof)
	if item.Raw != nil {
		item.RawAnalyzable = historyHasDate(*item.Raw, asof)
	}
	var history market.History
	var historyErr error
	if m.Config.Daily.Adjust == "none" {
		if item.Adjusted != nil {
			history = *item.Adjusted
		} else {
			historyErr = errors.New("missing adjusted history")
		}
	} else {
		if item.Raw != nil {
			history = *item.Raw
		} else {
			historyErr = errors.New("missing raw history")
		}
	}
	completeness := evaluateHistory(history, historyErr, evidence.Days, evidence.Statuses, asof)
	item.Sessions, item.Suspended, item.Gaps, item.Complete = completeness.sessions, completeness.suspended, completeness.gaps, completeness.complete
	if m.Config.Daily.Adjust == "none" {
		item.Analyzable = completeness.hasAsOf
	}
	return item
}

func (m *Monitor) loadPreviousHistory(ctx context.Context, code, adjust, asof string) (market.History, error) {
	if m.Market != nil && m.Market.HasDatabase() {
		return m.Market.LoadLatestHistory(ctx, code, adjust, asof)
	}
	return market.History{}, fs.ErrNotExist
}

func historyStart(end time.Time) string {
	start := end.AddDate(-10, 0, 0)
	if start.Month() != end.Month() {
		// Clamp Feb 29 to Feb 28 instead of normalizing it into March.
		start = start.AddDate(0, 0, -1)
	}
	return start.Format("2006-01-02")
}

func (m *Monitor) fetchHistory(ctx context.Context, stock config.Stock, start, asof, adjust string) (market.History, error) {
	return m.fetchHistoryUsing(ctx, stock, start, asof, adjust, func(h market.History) error {
		return validateHistory(m.Calendar, stock, h, start, asof)
	})
}

func (m *Monitor) fetchHistoryUsing(ctx context.Context, stock config.Stock, start, asof, adjust string, validate func(market.History) error) (market.History, error) {
	if stock.ListedFrom > start {
		start = stock.ListedFrom
	}
	if start > asof {
		return market.History{}, &market.DataError{Reason: "listed_from is after requested session"}
	}
	fetch := func(source market.Source) (market.History, error) {
		h, err := m.fetchHistoryFrom(ctx, source, stock, start, asof, adjust)
		if err == nil {
			if h.Code != stock.Code || h.Adjust != adjust {
				err = &market.DataError{Reason: "history_request_mismatch"}
			} else {
				err = validate(h)
			}
		}
		return h, err
	}
	if sources, ok := m.Source.(interface {
		Attempt(context.Context, config.Stock, string, func(market.Source) (market.History, error)) (market.History, error)
	}); ok {
		return sources.Attempt(ctx, stock, adjust, fetch)
	}
	return fetch(m.Source)
}

func (m *Monitor) fetchHistoryFrom(ctx context.Context, provider Source, stock config.Stock, start, asof, adjust string) (market.History, error) {
	if source, ok := provider.(WindowSource); ok && adjust == "none" && m.Market != nil && m.Market.HasDatabase() {
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
			if compatibleIncrement(previous, recent) {
				merged := mergeHistory(previous, recent, stock, start, asof)
				if validateHistory(m.Calendar, stock, merged, start, asof) == nil {
					return merged, nil
				}
			}
		}
	}
	return provider.Fetch(ctx, stock, start, asof, adjust)
}

func compatibleIncrement(previous, recent market.History) bool {
	if previous.Source != recent.Source || previous.Code != recent.Code || previous.Adjust != recent.Adjust || previous.VolumeUnit != recent.VolumeUnit || previous.DataField != recent.DataField || len(previous.Bars) == 0 || len(recent.Bars) == 0 {
		return false
	}
	old := make(map[string]market.Bar, len(previous.Bars))
	for _, bar := range previous.Bars {
		old[bar.Date] = bar
	}
	overlap := false
	for _, bar := range recent.Bars {
		if earlier, ok := old[bar.Date]; ok {
			if bar != earlier {
				return false
			}
			overlap = true
		}
	}
	return overlap
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

func syncReason(err error) string {
	if summary := market.ProviderFailureSummary(err); summary != "" {
		return summary
	}
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
	minutes, err := m.reportMinutes(ctx, histories, asof)
	if err != nil {
		return selection.Report{}, err
	}
	return m.buildReportWithMinutes(ctx, histories, raw, minutes, asof, target)
}

// reportMinutes preserves the existing bounded Chan input, excluding forming
// bars and observations from a later session. Replay never calls this loader.
func (m *Monitor) reportMinutes(ctx context.Context, histories []market.History, asof string) ([]market.MinuteHistory, error) {
	if !m.Config.Strategy.Enabled || m.Market == nil || !m.Market.HasDatabase() {
		return nil, nil
	}
	minutes := []market.MinuteHistory{}
	valid := make(map[string]bool, len(histories))
	for _, h := range histories {
		valid[h.Code] = true
	}
	for _, stock := range m.Config.Stocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !valid[stock.Code] {
			continue
		}
		set, err := m.Market.LoadMinuteSet(ctx, stock, 30, 5)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, source := range set {
			h := source
			observed := len(h.Bars)
			h.Bars = nil
			for _, bar := range source.Bars {
				if bar.Final && bar.End.In(market.Shanghai).Format("2006-01-02") <= asof {
					h.Bars = append(h.Bars, bar)
				}
			}
			if observed > 0 && len(h.Bars) == 0 {
				return nil, fmt.Errorf("no completed %dm history on or before %s for %s", h.Period, asof, stock.Code)
			}
			if len(h.Bars) > 0 {
				minutes = append(minutes, h)
			}
		}
	}
	m.Log.Debug("Minute histories loaded for Chan gate", "loaded", len(minutes), "eligible_daily", len(histories))
	return minutes, nil
}

func (m *Monitor) buildReportWithMinutes(ctx context.Context, histories, raw []market.History, minutes []market.MinuteHistory, asof, target string) (selection.Report, error) {
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
	engine := m.Engine
	if engine == nil {
		engine, err = selection.DefaultEngine(m.Config)
		if err != nil {
			return selection.Report{}, err
		}
	}
	timeout := config.Duration(m.Config.Analysis.Timeout)
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return selection.Execute(ctx, engine, selection.Input{Config: m.Config, Histories: window(histories), Raw: window(raw), Minutes: minutes, AsOf: asof, Target: target, Now: m.Now()}, m.BuildVersion)
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
	report, deferSecondStage, err := m.prepareSecondStage(ctx, &state, report)
	if err != nil {
		return errors.Join(deliveryErr, err)
	}
	if _, err := m.enqueueReportDeferred(&state, report, deferSecondStage); err != nil {
		return errors.Join(deliveryErr, err)
	}
	if _, reviewErr := m.enqueueFirstStageReview(ctx, &state, report.AsOf); reviewErr != nil {
		m.Log.Warn("First-stage review enqueue failed", "as_of", report.AsOf, "reason", reviewErr.Error())
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
		if ctx.Err() != nil {
			return ctx.Err()
		}
		channel := channels[delivery.Channel]
		if channel == nil {
			continue
		}
		if !state.CanDeliver(delivery.EventID, delivery.Channel) {
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
		// Persisting in-flight can itself take time on a network filesystem.
		// Recheck after that write and before entering the transport.
		remaining := delivery.Message.ExpiresAt.Sub(m.Now())
		if remaining <= 0 {
			state.Expire(m.Now())
			if err := m.writeState(*state); err != nil {
				return err
			}
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, remaining)
		err := channel.Send(sendCtx, delivery.Message)
		sendErr := sendCtx.Err()
		cancel()
		if err == nil {
			if err := state.MarkDelivered(delivery.EventID, delivery.Channel); err != nil {
				return err
			}
			if err := m.writeState(*state); err != nil {
				return err
			}
			m.Log.Info("Recommendation delivered", "channel", delivery.Channel, "as_of", state.Events[delivery.EventID].AsOf, "event_id", delivery.EventID)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if sendErr != nil {
			if markErr := state.MarkUnknown(delivery.EventID, delivery.Channel); markErr != nil {
				return markErr
			}
			if ctx.Err() == nil {
				state.Expire(delivery.Message.ExpiresAt)
			}
			if saveErr := m.writeState(*state); saveErr != nil {
				return saveErr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if firstErr == nil {
				firstErr = sendErr
			}
			continue
		}
		failure := deliveryFailure(delivery.Channel, err)
		if markErr := state.MarkFailure(delivery.EventID, delivery.Channel, failure, m.Now()); markErr != nil {
			return markErr
		}
		if saveErr := m.writeState(*state); saveErr != nil {
			return saveErr
		}
		if failure.Kind == notify.Permanent {
			m.Log.Error("["+delivery.Channel+"] Recommendation delivery blocked", "channel", delivery.Channel, "as_of", state.Events[delivery.EventID].AsOf, "event_id", delivery.EventID, "attempts", state.Events[delivery.EventID].Channels[delivery.Channel].Attempts, "kind", failure.Kind, "reason", failure.Reason)
		} else {
			m.Log.Warn("["+delivery.Channel+"] Recommendation delivery failed", "channel", delivery.Channel, "as_of", state.Events[delivery.EventID].AsOf, "event_id", delivery.EventID, "attempts", state.Events[delivery.EventID].Channels[delivery.Channel].Attempts, "kind", failure.Kind, "reason", failure.Reason)
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

func (m *Monitor) Status() (State, error) { return m.loadState(false) }

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
		if m.secondStagePending && delay > time.Minute {
			delay = time.Minute
		}
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
