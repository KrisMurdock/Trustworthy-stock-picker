package storage

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"time"
)

// StatusSync describes one audited status window. A complete interval means
// every non-trading question for that range was answered by at least one
// authoritative source; it never implies every day traded.
type StatusSync struct {
	Code       string    `json:"code"`
	RangeStart string    `json:"range_start"`
	RangeEnd   string    `json:"range_end"`
	Source     string    `json:"source"`
	Status     string    `json:"status"`
	FetchedAt  time.Time `json:"fetched_at"`
	ErrorKind  string    `json:"error_kind,omitempty"`
}

type CalendarSync struct {
	RangeStart string    `json:"range_start"`
	RangeEnd   string    `json:"range_end"`
	Source     string    `json:"source"`
	Status     string    `json:"status"`
	FetchedAt  time.Time `json:"fetched_at"`
	ErrorKind  string    `json:"error_kind,omitempty"`
}

func validMetadataRange(start, end string) bool {
	if start == "" || end == "" || start > end {
		return false
	}
	first, err := time.Parse("2006-01-02", start)
	if err != nil {
		return false
	}
	last, err := time.Parse("2006-01-02", end)
	return err == nil && !first.After(last)
}

// SaveStatusWindow stores one merged window. A full window replaces every answer
// in its range and establishes coverage. A sparse suspension feed only merges
// individual events and invalidates any overlapping completeness claim, because
// absent sparse rows do not prove that a session traded.
func (m *MarketDB) SaveStatusWindow(ctx context.Context, stock config.Stock, window market.StatusWindow) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	if !config.ValidCode(stock.Code) || window.Code != stock.Code || !validMetadataRange(window.Start, window.End) {
		return errors.New("invalid status window")
	}
	if err := window.Validate(); err != nil {
		return err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin status transaction")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO stock(code, name, sector, active, updated_at) VALUES(?, ?, ?, 1, ?)
		ON CONFLICT(code) DO UPDATE SET name=excluded.name, sector=excluded.sector, active=1, updated_at=excluded.updated_at
	`, stock.Code, stock.Name, stock.Sector, window.FetchedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return errors.New("cannot persist status stock metadata")
	}
	if window.Full {
		if _, err := tx.ExecContext(ctx, "DELETE FROM session_status WHERE code=? AND trade_date>=? AND trade_date<=?", stock.Code, window.Start, window.End); err != nil {
			return errors.New("cannot replace session statuses")
		}
	} else if err := invalidateStatusCoverage(ctx, tx, stock.Code, window.Start, window.End); err != nil {
		return err
	}
	insertSQL := `
		INSERT INTO session_status(code, trade_date, state, source, fetched_at, detail) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(code, trade_date) DO UPDATE SET state=excluded.state, source=excluded.source, fetched_at=excluded.fetched_at, detail=excluded.detail`
	if !window.Full {
		// Two explicit answers that disagree are a conflict, not evidence for the
		// suspension. An existing unknown may be resolved by the new event.
		insertSQL = `
			INSERT INTO session_status(code, trade_date, state, source, fetched_at, detail) VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(code, trade_date) DO UPDATE SET
				state=CASE
					WHEN session_status.state=excluded.state THEN excluded.state
					WHEN session_status.state='unknown' THEN excluded.state
					WHEN excluded.state='unknown' THEN 'unknown'
					ELSE 'unknown'
				END,
				source=CASE
					WHEN session_status.state=excluded.state OR session_status.state='unknown' THEN excluded.source
					ELSE session_status.source || '+' || excluded.source
				END,
				fetched_at=excluded.fetched_at,
				detail=CASE
					WHEN session_status.state=excluded.state OR session_status.state='unknown' THEN excluded.detail
					ELSE 'status_conflict'
				END`
	}
	insert, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		return errors.New("cannot prepare session status")
	}
	defer insert.Close()
	for _, status := range window.Statuses {
		if _, err := insert.ExecContext(ctx, stock.Code, status.Date, string(status.State), status.Source, status.FetchedAt.UTC().Format(time.RFC3339Nano), status.Detail); err != nil {
			return errors.New("cannot persist session status")
		}
	}
	coverageStatus, errorKind := "complete", ""
	if !window.Full {
		coverageStatus, errorKind = "partial", "sparse_status_source"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO status_sync(code, range_start, range_end, source, status, fetched_at, error_kind) VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(code, range_start, range_end) DO UPDATE SET source=excluded.source, status=excluded.status, fetched_at=excluded.fetched_at, error_kind=excluded.error_kind
	`, stock.Code, window.Start, window.End, window.Source, coverageStatus, window.FetchedAt.UTC().Format(time.RFC3339Nano), errorKind); err != nil {
		return errors.New("cannot persist status sync")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit session statuses")
	}
	return nil
}

// invalidateStatusCoverage removes completeness claims that overlap a sparse
// refresh while retaining valid coverage on either side of the refreshed range.
func invalidateStatusCoverage(ctx context.Context, tx *sql.Tx, code, start, end string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT range_start, range_end, source, fetched_at FROM status_sync
		WHERE code=? AND status='complete' AND range_start<=? AND range_end>=?
		ORDER BY range_start
	`, code, end, start)
	if err != nil {
		return errors.New("cannot inspect status coverage")
	}
	type interval struct{ start, end, source, fetched string }
	intervals := []interval{}
	for rows.Next() {
		var entry interval
		if err := rows.Scan(&entry.start, &entry.end, &entry.source, &entry.fetched); err != nil {
			rows.Close()
			return errors.New("cannot inspect status coverage")
		}
		intervals = append(intervals, entry)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return errors.New("cannot inspect status coverage")
	}
	rows.Close()
	for _, entry := range intervals {
		if _, err := tx.ExecContext(ctx, "DELETE FROM status_sync WHERE code=? AND range_start=? AND range_end=?", code, entry.start, entry.end); err != nil {
			return errors.New("cannot invalidate status coverage")
		}
		for _, part := range [][2]string{{entry.start, previousDay(start)}, {nextDay(end), entry.end}} {
			if part[0] > part[1] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO status_sync(code, range_start, range_end, source, status, fetched_at, error_kind) VALUES(?, ?, ?, ?, 'complete', ?, '')
				ON CONFLICT(code, range_start, range_end) DO NOTHING
			`, code, part[0], part[1], entry.source, entry.fetched); err != nil {
				return errors.New("cannot preserve status coverage")
			}
		}
	}
	return nil
}

func (m *MarketDB) RecordStatusFailure(ctx context.Context, stock config.Stock, start, end, source, kind string) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	if !config.ValidCode(stock.Code) || !validMetadataRange(start, end) {
		return errors.New("invalid status failure range")
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin status failure transaction")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO stock(code, name, sector, active, updated_at) VALUES(?, ?, ?, 1, ?)
		ON CONFLICT(code) DO UPDATE SET name=excluded.name, sector=excluded.sector, active=1, updated_at=excluded.updated_at
	`, stock.Code, stock.Name, stock.Sector, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return errors.New("cannot persist status failure stock metadata")
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT range_start, range_end, source, fetched_at FROM status_sync
		WHERE code=? AND status='complete' AND range_start<=? AND range_end>=?
		ORDER BY range_start
	`, stock.Code, end, start)
	if err != nil {
		return errors.New("cannot inspect status coverage")
	}
	type interval struct{ start, end, source, fetched string }
	intervals := []interval{}
	for rows.Next() {
		var entry interval
		if err := rows.Scan(&entry.start, &entry.end, &entry.source, &entry.fetched); err != nil {
			rows.Close()
			return errors.New("cannot inspect status coverage")
		}
		intervals = append(intervals, entry)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return errors.New("cannot inspect status coverage")
	}
	rows.Close()
	// A failed refresh invalidates its range without discarding successful
	// coverage on either side. Keeping the surrounding intervals avoids turning a
	// thirty-day retry into a decade-wide backfill.
	for _, entry := range intervals {
		if _, err := tx.ExecContext(ctx, "DELETE FROM status_sync WHERE code=? AND range_start=? AND range_end=?", stock.Code, entry.start, entry.end); err != nil {
			return errors.New("cannot invalidate status coverage")
		}
		for _, part := range [][2]string{{entry.start, previousDay(start)}, {nextDay(end), entry.end}} {
			if part[0] > part[1] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO status_sync(code, range_start, range_end, source, status, fetched_at, error_kind) VALUES(?, ?, ?, ?, 'complete', ?, '')
				ON CONFLICT(code, range_start, range_end) DO UPDATE SET source=excluded.source, status='complete', fetched_at=excluded.fetched_at, error_kind=''
			`, stock.Code, part[0], part[1], entry.source, entry.fetched); err != nil {
				return errors.New("cannot preserve status coverage")
			}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO status_sync(code, range_start, range_end, source, status, fetched_at, error_kind) VALUES(?, ?, ?, ?, 'failed', ?, ?)
		ON CONFLICT(code, range_start, range_end) DO UPDATE SET source=excluded.source, status='failed', fetched_at=excluded.fetched_at, error_kind=excluded.error_kind
	`, stock.Code, start, end, source, now, kind); err != nil {
		return errors.New("cannot persist status failure")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit status failure")
	}
	return nil
}

// LoadSessionStatuses returns the merged answers for a range. Missing dates are
// intentionally absent so callers treat them as unknown rather than traded.
func (m *MarketDB) LoadSessionStatuses(ctx context.Context, code, start, end string) (map[string]market.SessionStatus, error) {
	out := map[string]market.SessionStatus{}
	if m == nil || m.db == nil {
		return out, errors.New("market database unavailable")
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT trade_date, state, source, fetched_at, detail FROM session_status
		WHERE code=? AND trade_date>=? AND trade_date<=? ORDER BY trade_date
	`, code, start, end)
	if err != nil {
		return nil, errors.New("cannot read session statuses")
	}
	defer rows.Close()
	for rows.Next() {
		var status market.SessionStatus
		var state, fetched string
		if err := rows.Scan(&status.Date, &state, &status.Source, &fetched, &status.Detail); err != nil {
			return nil, errors.New("cannot decode session status")
		}
		status.Code = code
		status.State = market.SessionState(state)
		status.FetchedAt, _ = time.Parse(time.RFC3339Nano, fetched)
		if !market.ValidSessionState(status.State) || status.Source == "" {
			return nil, errors.New("invalid stored session status")
		}
		out[status.Date] = status
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot read session statuses")
	}
	return out, nil
}

func (m *MarketDB) StatusCoverage(ctx context.Context, code, start, end string) (bool, error) {
	if m == nil || m.db == nil {
		return false, errors.New("market database unavailable")
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT range_start, range_end FROM status_sync
		WHERE code=? AND status='complete' AND range_start<=? AND range_end>=?
	`, code, end, start)
	if err != nil {
		return false, errors.New("cannot read status coverage")
	}
	defer rows.Close()
	intervals := [][2]string{}
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return false, errors.New("cannot read status coverage")
		}
		intervals = append(intervals, [2]string{from, to})
	}
	if err := rows.Err(); err != nil {
		return false, errors.New("cannot read status coverage")
	}
	return covers(start, end, intervals), nil
}

func (m *MarketDB) SaveCalendar(ctx context.Context, start, end, source string, days []string, fetchedAt time.Time) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	if !validMetadataRange(start, end) || source == "" || fetchedAt.IsZero() {
		return errors.New("invalid calendar window")
	}
	previous := ""
	for _, day := range days {
		parsed, err := time.Parse("2006-01-02", day)
		if err != nil || day < start || day > end || day <= previous || parsed.Weekday() == time.Saturday || parsed.Weekday() == time.Sunday {
			return errors.New("invalid calendar day")
		}
		previous = day
	}
	if len(days) == 0 {
		first, _ := time.Parse("2006-01-02", start)
		last, _ := time.Parse("2006-01-02", end)
		for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
			if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
				return errors.New("calendar window has no trading days")
			}
		}
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin calendar transaction")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM market_calendar WHERE trade_date>=? AND trade_date<=?", start, end); err != nil {
		return errors.New("cannot replace exchange calendar")
	}
	insert, err := tx.PrepareContext(ctx, `
		INSERT INTO market_calendar(trade_date, source, fetched_at) VALUES(?, ?, ?)
		ON CONFLICT(trade_date) DO UPDATE SET source=excluded.source, fetched_at=excluded.fetched_at
	`)
	if err != nil {
		return errors.New("cannot prepare exchange calendar")
	}
	defer insert.Close()
	for _, day := range days {
		if _, err := insert.ExecContext(ctx, day, source, fetchedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return errors.New("cannot persist exchange calendar")
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO calendar_sync(range_start, range_end, source, status, fetched_at, error_kind) VALUES(?, ?, ?, 'complete', ?, '')
		ON CONFLICT(range_start, range_end) DO UPDATE SET source=excluded.source, status='complete', fetched_at=excluded.fetched_at, error_kind=''
	`, start, end, source, fetchedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return errors.New("cannot persist calendar sync")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit exchange calendar")
	}
	return nil
}

func (m *MarketDB) RecordCalendarFailure(ctx context.Context, start, end, source, kind string) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	if !validMetadataRange(start, end) {
		return errors.New("invalid calendar failure range")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO calendar_sync(range_start, range_end, source, status, fetched_at, error_kind) VALUES(?, ?, ?, 'failed', ?, ?)
		ON CONFLICT(range_start, range_end) DO UPDATE SET source=excluded.source, status='failed', fetched_at=excluded.fetched_at, error_kind=excluded.error_kind
	`, start, end, source, now, kind)
	if err != nil {
		return errors.New("cannot persist calendar failure")
	}
	return nil
}

func (m *MarketDB) CalendarCoverage(ctx context.Context, start, end string) (bool, error) {
	if m == nil || m.db == nil {
		return false, errors.New("market database unavailable")
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT range_start, range_end FROM calendar_sync
		WHERE status='complete' AND range_start<=? AND range_end>=?
	`, end, start)
	if err != nil {
		return false, errors.New("cannot read calendar coverage")
	}
	defer rows.Close()
	intervals := [][2]string{}
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return false, errors.New("cannot read calendar coverage")
		}
		intervals = append(intervals, [2]string{from, to})
	}
	if err := rows.Err(); err != nil {
		return false, errors.New("cannot read calendar coverage")
	}
	return covers(start, end, intervals), nil
}

func (m *MarketDB) CalendarGaps(ctx context.Context, start, end string) ([][2]string, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("market database unavailable")
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT range_start, range_end FROM calendar_sync
		WHERE status='complete' AND range_start<=? AND range_end>=? ORDER BY range_start
	`, end, start)
	if err != nil {
		return nil, errors.New("cannot read calendar coverage")
	}
	defer rows.Close()
	intervals := [][2]string{}
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return nil, errors.New("cannot read calendar coverage")
		}
		intervals = append(intervals, [2]string{from, to})
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot read calendar coverage")
	}
	return missingRanges(start, end, intervals), nil
}

// CalendarDays returns the authoritative trading sessions in a covered range.
// It refuses to silently fall back to the configured current-year calendar.
func (m *MarketDB) CalendarDays(ctx context.Context, start, end string) ([]string, error) {
	if !validMetadataRange(start, end) {
		return nil, errors.New("invalid calendar range")
	}
	covered, err := m.CalendarCoverage(ctx, start, end)
	if err != nil {
		return nil, err
	}
	if !covered {
		return nil, errors.New("exchange calendar coverage is incomplete; run status-backfill")
	}
	rows, err := m.db.QueryContext(ctx, "SELECT trade_date FROM market_calendar WHERE trade_date>=? AND trade_date<=? ORDER BY trade_date", start, end)
	if err != nil {
		return nil, errors.New("cannot read exchange calendar")
	}
	defer rows.Close()
	days := []string{}
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return nil, errors.New("cannot read exchange calendar")
		}
		days = append(days, day)
	}
	if err := rows.Err(); err != nil || len(days) == 0 {
		return nil, errors.New("cannot read exchange calendar")
	}
	return days, nil
}

func covers(start, end string, intervals [][2]string) bool {
	return len(missingRanges(start, end, intervals)) == 0
}

func missingRanges(start, end string, intervals [][2]string) [][2]string {
	if len(intervals) == 0 {
		return [][2]string{{start, end}}
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i][0] < intervals[j][0] })
	cursor := start
	gaps := [][2]string{}
	for _, interval := range intervals {
		if interval[1] < cursor || interval[0] > end {
			continue
		}
		if interval[0] > cursor {
			gaps = append(gaps, [2]string{cursor, previousDay(interval[0])})
		}
		if interval[1] >= end {
			return gaps
		}
		if interval[1] >= cursor {
			cursor = nextDay(interval[1])
		}
	}
	if cursor <= end {
		gaps = append(gaps, [2]string{cursor, end})
	}
	return gaps
}

func previousDay(day string) string {
	parsed, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return parsed.AddDate(0, 0, -1).Format("2006-01-02")
}

func nextDay(day string) string {
	parsed, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return parsed.AddDate(0, 0, 1).Format("2006-01-02")
}

func (m *MarketDB) StatusSyncEntries(ctx context.Context, code string) ([]StatusSync, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("market database unavailable")
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT code, range_start, range_end, source, status, fetched_at, error_kind
		FROM status_sync WHERE code=? ORDER BY range_start, range_end
	`, code)
	if err != nil {
		return nil, errors.New("cannot read status sync metadata")
	}
	defer rows.Close()
	out := []StatusSync{}
	for rows.Next() {
		var entry StatusSync
		var fetched string
		if err := rows.Scan(&entry.Code, &entry.RangeStart, &entry.RangeEnd, &entry.Source, &entry.Status, &fetched, &entry.ErrorKind); err != nil {
			return nil, errors.New("cannot read status sync metadata")
		}
		entry.FetchedAt, _ = time.Parse(time.RFC3339Nano, fetched)
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot read status sync metadata")
	}
	return out, nil
}

func (m *MarketDB) CalendarSyncEntries(ctx context.Context) ([]CalendarSync, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("market database unavailable")
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT range_start, range_end, source, status, fetched_at, error_kind
		FROM calendar_sync ORDER BY range_start, range_end
	`)
	if err != nil {
		return nil, errors.New("cannot read calendar sync metadata")
	}
	defer rows.Close()
	out := []CalendarSync{}
	for rows.Next() {
		var entry CalendarSync
		var fetched string
		if err := rows.Scan(&entry.RangeStart, &entry.RangeEnd, &entry.Source, &entry.Status, &fetched, &entry.ErrorKind); err != nil {
			return nil, errors.New("cannot read calendar sync metadata")
		}
		entry.FetchedAt, _ = time.Parse(time.RFC3339Nano, fetched)
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot read calendar sync metadata")
	}
	return out, nil
}
