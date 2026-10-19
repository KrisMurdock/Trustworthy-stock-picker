package monitor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"time"
)

// StatusAudit is the user-visible answer to "is the ten-year window complete?".
// It is derived from stored bars plus authoritative no-trade facts; it never
// fills or rewrites a daily bar.
type StatusAudit struct {
	Version       int                `json:"schema_version"`
	AsOf          string             `json:"as_of"`
	RangeStart    string             `json:"range_start"`
	RangeEnd      string             `json:"range_end"`
	PoolSize      int                `json:"pool_size"`
	Complete      int                `json:"complete"`
	CompletePct   float64            `json:"complete_pct"`
	Analyzable    int                `json:"analyzable"`
	AnalyzablePct float64            `json:"analyzable_pct"`
	Suspended     int                `json:"suspended"`
	Gaps          int                `json:"gaps"`
	Stocks        []StatusStockAudit `json:"stocks"`
}

type StatusStockAudit struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	RangeStart string `json:"range_start"`
	RangeEnd   string `json:"range_end"`
	Sessions   int    `json:"sessions"`
	Suspended  int    `json:"suspended"`
	Gaps       int    `json:"gaps"`
	Complete   bool   `json:"complete"`
	Analyzable bool   `json:"analyzable"`
	Reason     string `json:"reason"`
	LastDate   string `json:"last_date,omitempty"`
	AdjustLast string `json:"adjust_last_date,omitempty"`
}

type statusEvidence struct {
	Days     []string
	Statuses map[string]market.SessionStatus
}

func (m *Monitor) statusEnabled() bool {
	return m.StatusSource != nil && m.CalendarSource != nil && m.Market != nil && m.Market.HasDatabase()
}

func (m *Monitor) validateAsOf(asof string) error {
	ok, err := m.Calendar.Trading(asof)
	if err != nil || !ok {
		return errors.New("as-of must be a covered trading date")
	}
	latest, err := m.Calendar.Completed(m.Now())
	if err != nil || asof > latest {
		return errors.New("as-of session has not completed")
	}
	return nil
}

// StatusBackfill answers every trading day from max(listing, ten years ago) to
// as_of for the whole pool. It is deliberately explicit: Sync refuses to
// publish until this evidence exists.
func (m *Monitor) StatusBackfill(ctx context.Context, asof string) (StatusAudit, error) {
	if err := m.validateAsOf(asof); err != nil {
		return StatusAudit{}, err
	}
	if m.Market == nil || !m.Market.HasDatabase() {
		return StatusAudit{}, errors.New("market database is not initialized")
	}
	if m.StatusSource == nil || m.CalendarSource == nil {
		return StatusAudit{}, errors.New("status sources are not configured")
	}
	end, _ := time.Parse("2006-01-02", asof)
	requiredStart := historyStart(end)
	if err := m.ensureCalendar(ctx, requiredStart, asof); err != nil {
		return StatusAudit{}, err
	}
	for i, stock := range m.Config.Stocks {
		if err := ctx.Err(); err != nil {
			return StatusAudit{}, err
		}
		resolved, err := m.listingStock(ctx, stock)
		if err != nil {
			return StatusAudit{}, fmt.Errorf("status backfill %s: %w", stock.Code, err)
		}
		start := requiredStart
		if resolved.ListedFrom > start {
			start = resolved.ListedFrom
		}
		if start > asof {
			continue
		}
		window, err := m.fetchStatus(ctx, resolved, start, asof)
		if err != nil {
			if ctx.Err() != nil {
				return StatusAudit{}, ctx.Err()
			}
			if recordErr := m.Market.RecordStatusFailure(ctx, resolved, start, asof, "market", syncReason(err)); recordErr != nil {
				return StatusAudit{}, errors.Join(fmt.Errorf("status backfill %s: %w", stock.Code, err), recordErr)
			}
			return StatusAudit{}, fmt.Errorf("status backfill %s: %w", stock.Code, err)
		}
		if err := m.Market.SaveStatusWindow(ctx, resolved, window); err != nil {
			return StatusAudit{}, err
		}
		if (i+1)%10 == 0 || i+1 == len(m.Config.Stocks) {
			m.Log.Info("Status backfill progress", "as_of", asof, "completed", i+1, "total", len(m.Config.Stocks), "code", stock.Code)
		}
	}
	return m.StatusAudit(ctx, asof)
}

// StatusRefresh re-answers the most recent configured window. It is safe to
// repeat and keeps first-session suspensions visible without replaying a decade.
func (m *Monitor) StatusRefresh(ctx context.Context, asof string) (StatusAudit, error) {
	if err := m.validateAsOf(asof); err != nil {
		return StatusAudit{}, err
	}
	if m.Market == nil || !m.Market.HasDatabase() {
		return StatusAudit{}, errors.New("market database is not initialized")
	}
	if m.StatusSource == nil || m.CalendarSource == nil {
		return StatusAudit{}, errors.New("status sources are not configured")
	}
	end, _ := time.Parse("2006-01-02", asof)
	requiredStart := historyStart(end)
	if err := m.ensureCalendar(ctx, requiredStart, asof); err != nil {
		return StatusAudit{}, err
	}
	if err := m.refreshRecentStatuses(ctx, asof); err != nil {
		return StatusAudit{}, err
	}
	return m.StatusAudit(ctx, asof)
}

func (m *Monitor) refreshRecentStatuses(ctx context.Context, asof string) error {
	if !m.statusEnabled() {
		return nil
	}
	end, _ := time.Parse("2006-01-02", asof)
	start := end.AddDate(0, 0, -m.Config.Sync.StatusRefreshDays+1).Format("2006-01-02")
	for _, stock := range m.Config.Stocks {
		if err := ctx.Err(); err != nil {
			return err
		}
		resolved, err := m.listingStock(ctx, stock)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if recordErr := m.Market.RecordStatusFailure(ctx, stock, start, asof, "listing", syncReason(err)); recordErr != nil {
				return recordErr
			}
			m.Log.Warn("Recent status refresh skipped", "code", stock.Code, "reason", syncReason(err))
			continue
		}
		stockStart := start
		if resolved.ListedFrom > stockStart {
			stockStart = resolved.ListedFrom
		}
		if stockStart > asof {
			continue
		}
		window, err := m.fetchStatus(ctx, resolved, stockStart, asof)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if recordErr := m.Market.RecordStatusFailure(ctx, resolved, stockStart, asof, "market", syncReason(err)); recordErr != nil {
				return recordErr
			}
			m.Log.Warn("Recent status refresh failed", "code", stock.Code, "range_start", stockStart, "as_of", asof, "error", err)
			continue
		}
		if err := m.Market.SaveStatusWindow(ctx, resolved, window); err != nil {
			return err
		}
	}
	return nil
}

func (m *Monitor) fetchStatus(ctx context.Context, stock config.Stock, start, end string) (market.StatusWindow, error) {
	timeout := config.Duration(m.Config.Analysis.SyncTimeout)
	if timeout <= 0 {
		timeout = 90 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return m.StatusSource.FetchStatus(ctx, stock, start, end)
}

// StatusAudit is read-only and reports the exact gap days without contacting
// any upstream. It also verifies adjusted analyzability independently.
func (m *Monitor) StatusAudit(ctx context.Context, asof string) (StatusAudit, error) {
	out := StatusAudit{Version: 1, AsOf: asof, RangeEnd: asof, Stocks: []StatusStockAudit{}}
	if err := m.validateAsOf(asof); err != nil {
		return out, err
	}
	if m.Market == nil || !m.Market.HasDatabase() {
		return out, errors.New("market database is not initialized")
	}
	end, _ := time.Parse("2006-01-02", asof)
	out.RangeStart = historyStart(end)
	out.PoolSize = len(m.Config.Stocks)
	days, err := m.Market.CalendarDays(ctx, out.RangeStart, asof)
	if err != nil {
		return out, err
	}
	for _, stock := range m.Config.Stocks {
		resolved := m.auditStock(stock)
		start := out.RangeStart
		if resolved.ListedFrom > start {
			start = resolved.ListedFrom
		}
		stockDays := selectDays(days, start, asof)
		row := StatusStockAudit{Code: resolved.Code, Name: resolved.Name, RangeStart: start, RangeEnd: asof}
		statuses, err := m.Market.LoadSessionStatuses(ctx, resolved.Code, start, asof)
		if err != nil {
			return out, err
		}
		statusCovered := true
		if m.StatusSource != nil || m.CalendarSource != nil {
			statusCovered, err = m.Market.StatusCoverage(ctx, resolved.Code, start, asof)
			if err != nil {
				return out, err
			}
		}
		raw, rawErr := m.loadAuditHistory(ctx, resolved, asof, "none")
		adjustedAdjust := m.Config.Daily.Adjust
		adjusted, adjustedErr := m.loadAuditHistory(ctx, resolved, asof, adjustedAdjust)
		var evidence statusEvidence
		if m.StatusSource != nil || m.CalendarSource != nil {
			evidence = statusEvidence{Days: stockDays, Statuses: statuses}
		}
		evaluation := evaluateHistory(raw, rawErr, stockDays, statuses, asof)
		row.Sessions, row.Suspended, row.Gaps = evaluation.sessions, evaluation.suspended, evaluation.gaps
		row.Complete = evaluation.complete && statusCovered
		asOfSuspended := statuses[asof].State == market.SessionSuspended
		row.Analyzable = adjustedErr == nil && historyHasDate(adjusted, asof)
		if adjustedAdjust == "none" {
			row.Analyzable = evaluation.hasAsOf
		}
		if rawErr == nil {
			row.LastDate = lastBarDate(raw)
		}
		if adjustedErr == nil {
			row.AdjustLast = lastBarDate(adjusted)
		}
		switch {
		case rawErr != nil && !errors.Is(rawErr, fs.ErrNotExist):
			row.Reason = "invalid_history"
			row.Complete = false
		case rawErr != nil:
			row.Reason = "missing_raw_history"
			row.Gaps = len(stockDays)
			row.Complete = false
		case !statusCovered:
			row.Reason = "status_coverage_incomplete"
		case !row.Complete:
			row.Reason = "history_gap"
		case !row.Analyzable && asOfSuspended:
			row.Reason = "complete_not_analyzable"
		case !row.Analyzable:
			row.Reason = "adjusted_history_unavailable"
		default:
			row.Reason = "complete"
		}
		if evidence.Days != nil {
			if rawErr == nil {
				if err := validateHistoryWithStatus(resolved, raw, start, asof, stockDays, statuses); err != nil {
					row.Reason = "invalid_history"
					row.Complete = false
				}
			}
			if adjustedErr == nil {
				analysisStart := m.analysisStart(asof)
				adjustDays := selectDays(days, maxString(start, analysisStart), asof)
				if err := validateHistoryWithStatus(resolved, adjusted, maxString(start, analysisStart), asof, adjustDays, statuses); err != nil {
					row.Analyzable = false
				}
			}
		}
		if row.Complete {
			out.Complete++
		}
		if row.Analyzable {
			out.Analyzable++
		}
		if row.Complete && !row.Analyzable && asOfSuspended {
			out.Suspended++
		}
		if row.Gaps > 0 || !row.Complete {
			out.Gaps++
		}
		out.Stocks = append(out.Stocks, row)
	}
	if out.PoolSize > 0 {
		out.CompletePct = float64(out.Complete) * 100 / float64(out.PoolSize)
		out.AnalyzablePct = float64(out.Analyzable) * 100 / float64(out.PoolSize)
	}
	return out, nil
}

func (m *Monitor) auditStock(stock config.Stock) config.Stock {
	if stock.ListedFrom != "" {
		return stock
	}
	var listing market.Listing
	if err := m.Store.Read("metadata/listings/"+stock.Code+".json", &listing); err == nil && listing.Validate() == nil && listing.Code == stock.Code {
		stock.ListedFrom = listing.ListedFrom
	}
	return stock
}

func (m *Monitor) loadAuditHistory(ctx context.Context, stock config.Stock, asof, adjust string) (market.History, error) {
	histories, err := m.Market.LoadHistories(ctx, []config.Stock{stock}, asof, adjust)
	if errors.Is(err, fs.ErrNotExist) {
		return market.History{}, fs.ErrNotExist
	}
	if err != nil {
		return market.History{}, err
	}
	if len(histories) != 1 || histories[0].Code != stock.Code {
		return market.History{}, errors.New("invalid stored history")
	}
	return histories[0], nil
}

func (m *Monitor) analysisStart(asof string) string {
	end, _ := time.Parse("2006-01-02", asof)
	start := end.AddDate(0, 0, -m.Config.Daily.HistoryDays).Format("2006-01-02")
	if start < historyStart(end) {
		start = historyStart(end)
	}
	return start
}

func (m *Monitor) ensureCalendar(ctx context.Context, start, end string) error {
	covered, err := m.Market.CalendarCoverage(ctx, start, end)
	if err != nil {
		return err
	}
	if covered {
		return nil
	}
	gaps, err := m.Market.CalendarGaps(ctx, start, end)
	if err != nil {
		return err
	}
	for _, gap := range gaps {
		days, err := m.CalendarSource.FetchTradingDays(ctx, gap[0], gap[1])
		if err != nil {
			_ = m.Market.RecordCalendarFailure(ctx, gap[0], gap[1], "market", syncReason(err))
			return fmt.Errorf("exchange calendar %s..%s: %w", gap[0], gap[1], err)
		}
		if err := m.Market.SaveCalendar(ctx, gap[0], gap[1], "exchange", days, m.Now()); err != nil {
			return err
		}
	}
	covered, err = m.Market.CalendarCoverage(ctx, start, end)
	if err != nil {
		return err
	}
	if !covered {
		return errors.New("exchange calendar coverage is incomplete")
	}
	days, err := m.Market.CalendarDays(ctx, start, end)
	if err != nil {
		return err
	}
	for _, day := range days {
		if day == end {
			return nil
		}
	}
	if ok, err := m.Calendar.Trading(end); err == nil && ok {
		return errors.New("authoritative exchange calendar does not include as-of session")
	}
	return nil
}

func (m *Monitor) ensureStatusCoverage(ctx context.Context, stock config.Stock, start, end string) error {
	covered, err := m.Market.StatusCoverage(ctx, stock.Code, start, end)
	if err != nil {
		return err
	}
	if !covered {
		return fmt.Errorf("status backfill incomplete for %s (%s..%s); run stock-watcher status-backfill", stock.Code, start, end)
	}
	return nil
}

func (m *Monitor) loadEvidence(ctx context.Context, stock config.Stock, start, end string) (statusEvidence, error) {
	days, err := m.Market.CalendarDays(ctx, start, end)
	if err != nil {
		return statusEvidence{}, err
	}
	statuses, err := m.Market.LoadSessionStatuses(ctx, stock.Code, start, end)
	if err != nil {
		return statusEvidence{}, err
	}
	return statusEvidence{Days: days, Statuses: statuses}, nil
}

func selectDays(days []string, start, end string) []string {
	first := sort.SearchStrings(days, start)
	last := sort.SearchStrings(days, end)
	if last < len(days) && days[last] == end {
		last++
	}
	if first >= last {
		return []string{}
	}
	return days[first:last]
}

func maxString(a, b string) string {
	if a > b {
		return a
	}
	return b
}

type completeness struct {
	sessions  int
	suspended int
	gaps      int
	complete  bool
	hasAsOf   bool
}

func evaluateHistory(history market.History, historyErr error, days []string, statuses map[string]market.SessionStatus, asof string) completeness {
	out := completeness{sessions: len(days)}
	if historyErr != nil || len(days) == 0 {
		out.gaps = len(days)
		return out
	}
	bars := make(map[string]market.Bar, len(history.Bars))
	for _, bar := range history.Bars {
		bars[bar.Date] = bar
	}
	_, out.hasAsOf = bars[asof]
	for _, day := range days {
		if _, ok := bars[day]; ok {
			continue
		}
		if status := statuses[day]; status.State == market.SessionSuspended {
			out.suspended++
		} else {
			out.gaps++
		}
	}
	out.complete = out.gaps == 0
	return out
}

func historyHasDate(history market.History, date string) bool {
	for i := len(history.Bars) - 1; i >= 0; i-- {
		if history.Bars[i].Date == date {
			return true
		}
		if history.Bars[i].Date < date {
			return false
		}
	}
	return false
}

func lastBarDate(history market.History) string {
	if len(history.Bars) == 0 {
		return ""
	}
	return history.Bars[len(history.Bars)-1].Date
}

// validateHistoryWithStatus replaces the implicit "every weekday is a trading
// day" assumption with stored exchange-calendar and per-session evidence.
func validateHistoryWithStatus(stock config.Stock, history market.History, start, asof string, days []string, statuses map[string]market.SessionStatus) error {
	if err := history.Validate(); err != nil {
		return &market.DataError{Reason: err.Error()}
	}
	if history.AsOf != asof || len(history.Bars) == 0 {
		return &market.DataError{Reason: "stale_or_suspended"}
	}
	if history.Adjust == "qfq" && history.DataField != "qfqday" {
		return &market.DataError{Reason: "qfq_adjustment_missing"}
	}
	if len(days) == 0 {
		return &market.DataError{Reason: "window_gap"}
	}
	expected := make(map[string]bool, len(days))
	for _, day := range days {
		expected[day] = true
	}
	anchor := false
	for _, bar := range history.Bars {
		if bar.Date > asof {
			return &market.DataError{Reason: "bar_after_as_of"}
		}
		if expected[bar.Date] {
			continue
		}
		if bar.Date < days[0] && !anchor {
			anchor = true
			continue
		}
		return &market.DataError{Reason: "calendar_conflict"}
	}
	bars := make(map[string]bool, len(history.Bars))
	for _, bar := range history.Bars {
		bars[bar.Date] = true
	}
	for _, day := range days {
		if bars[day] {
			continue
		}
		if status := statuses[day]; status.State == market.SessionSuspended {
			continue
		}
		if status := statuses[day]; status.State == market.SessionTraded {
			return &market.DataError{Reason: "missing_bar"}
		}
		return &market.DataError{Reason: "window_gap"}
	}
	return nil
}
