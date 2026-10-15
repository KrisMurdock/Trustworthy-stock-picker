package storage

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"time"
)

// Only native five-minute bars are persisted. The other periods are derived
// from this same series on read, so corrections cannot leave stale aggregates.
func (m *MarketDB) initMinuteSchema() error {
	tx, err := m.db.Begin()
	if err != nil {
		return errors.New("cannot start minute schema migration")
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
		CREATE TABLE IF NOT EXISTS intraday_bar (
			code TEXT NOT NULL REFERENCES stock(code), end_unix INTEGER NOT NULL,
			open_micros INTEGER NOT NULL CHECK(open_micros>0), high_micros INTEGER NOT NULL,
			low_micros INTEGER NOT NULL CHECK(low_micros>0), close_micros INTEGER NOT NULL CHECK(close_micros>0),
			volume_raw REAL NOT NULL CHECK(volume_raw>=0), final INTEGER NOT NULL CHECK(final IN (0,1)),
			CHECK(high_micros>=open_micros AND high_micros>=close_micros),
			CHECK(low_micros<=open_micros AND low_micros<=close_micros),
			PRIMARY KEY(code,end_unix));
		CREATE TABLE IF NOT EXISTS intraday_sync (
			code TEXT PRIMARY KEY REFERENCES stock(code), target_unix INTEGER NOT NULL,
			attempt_at TEXT NOT NULL, fetched_at TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', calibrated_date TEXT NOT NULL DEFAULT '');
		PRAGMA user_version=3;
	`)
	if err != nil {
		return errors.New("cannot migrate minute schema")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit minute schema migration")
	}
	return nil
}

type MinuteState struct {
	Target         time.Time  `json:"target"`
	AttemptAt      time.Time  `json:"attempt_at"`
	FetchedAt      *time.Time `json:"fetched_at"`
	Status         string     `json:"status"`
	Reason         string     `json:"reason"`
	CalibratedDate string     `json:"calibrated_date"`
}

type MinuteSnapshot struct {
	market.MinuteHistory
	State MinuteState `json:"sync"`
}

func minuteState(ctx context.Context, r sqlReader, code string) (MinuteState, error) {
	var s MinuteState
	var target int64
	var attempt, fetched string
	err := r.QueryRowContext(ctx, `SELECT target_unix,attempt_at,fetched_at,status,reason,calibrated_date FROM intraday_sync WHERE code=?`, code).
		Scan(&target, &attempt, &fetched, &s.Status, &s.Reason, &s.CalibratedDate)
	if errors.Is(err, sql.ErrNoRows) {
		return s, fs.ErrNotExist
	}
	if err != nil {
		return s, errors.New("cannot read minute status")
	}
	s.Target = time.Unix(target, 0).In(market.Shanghai)
	s.AttemptAt, err = time.Parse(time.RFC3339Nano, attempt)
	if err != nil {
		return s, errors.New("invalid minute status time")
	}
	if fetched != "" {
		t, err := time.Parse(time.RFC3339Nano, fetched)
		if err != nil {
			return s, errors.New("invalid minute fetched time")
		}
		s.FetchedAt = &t
	}
	return s, nil
}

func (m *MarketDB) MinuteState(ctx context.Context, code string) (MinuteState, error) {
	if m == nil || m.db == nil {
		return MinuteState{}, fs.ErrNotExist
	}
	return minuteState(ctx, m.db, code)
}

// SaveMinutes atomically replaces the overlap reported by the provider while
// retaining older history. A failed fetch only changes status, never candles.
// calibration is true only after the caller verifies all 48 closed base bars.
func (m *MarketDB) SaveMinutes(ctx context.Context, stock config.Stock, target, attempt time.Time, h *market.MinuteHistory, status, reason string, calibration bool) error {
	if m == nil || !m.writable {
		return errors.New("market database is read-only")
	}
	if !config.ValidCode(stock.Code) || attempt.IsZero() || target.IsZero() {
		return errors.New("invalid minute persistence request")
	}
	if status != "ok" && status != "stale" && status != "partial" && status != "failed" {
		return errors.New("invalid minute status")
	}
	if h != nil {
		if err := h.Validate(); err != nil {
			return err
		}
		if h.Code != stock.Code || h.FetchedAt.After(attempt) {
			return errors.New("minute identity or observation mismatch")
		}
		if h.Bars[len(h.Bars)-1].End.After(target) {
			return errors.New("minute response exceeds requested target")
		}
	}
	if calibration && (h == nil || status != "ok") {
		return errors.New("invalid minute calibration")
	}
	if calibration {
		count := 0
		for _, b := range h.Bars {
			if b.End.In(market.Shanghai).Format("2006-01-02") == target.In(market.Shanghai).Format("2006-01-02") && b.Final {
				count++
			}
		}
		if target.In(market.Shanghai).Format("15:04") != "15:00" || count != 48 {
			return errors.New("minute calibration requires a complete closed session")
		}
	}
	if err := (&Store{root: m.root, requireCIFS: m.requireCIFS}).Check(); err != nil {
		return err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot start minute transaction")
	}
	defer tx.Rollback()
	previous, err := minuteState(ctx, tx, stock.Code)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if previous.AttemptAt.After(attempt) {
		return errors.New("out-of-order minute observation")
	}
	if h != nil && previous.FetchedAt != nil && h.FetchedAt.Before(*previous.FetchedAt) {
		return errors.New("out-of-order minute response")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO stock(code,name,sector,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(code) DO UPDATE SET name=excluded.name,sector=excluded.sector,updated_at=excluded.updated_at`, stock.Code, stock.Name, stock.Sector, attempt.Format(time.RFC3339Nano))
	if err != nil {
		return errors.New("cannot persist minute stock")
	}
	fetched := ""
	if previous.FetchedAt != nil {
		fetched = previous.FetchedAt.Format(time.RFC3339Nano)
	}
	if h != nil {
		fetched = h.FetchedAt.Format(time.RFC3339Nano)
		// Full response replaces overlapping timestamps, including bars removed
		// by an upstream correction. Keep data outside the observed window:
		// a lagging response cannot delete newer observations already saved.
		_, err = tx.ExecContext(ctx, `DELETE FROM intraday_bar WHERE code=? AND end_unix>=? AND end_unix<=?`, stock.Code, h.Bars[0].End.Unix(), h.Bars[len(h.Bars)-1].End.Unix())
		if err != nil {
			return errors.New("cannot replace minute overlap")
		}
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO intraday_bar(code,end_unix,open_micros,high_micros,low_micros,close_micros,volume_raw,final)
			VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(code,end_unix) DO UPDATE SET open_micros=excluded.open_micros,high_micros=excluded.high_micros,
			low_micros=excluded.low_micros,close_micros=excluded.close_micros,volume_raw=excluded.volume_raw,final=excluded.final`)
		if err != nil {
			return errors.New("cannot prepare minute write")
		}
		defer stmt.Close()
		for _, b := range h.Bars {
			_, err = stmt.ExecContext(ctx, stock.Code, b.End.Unix(), micros(b.Open), micros(b.High), micros(b.Low), micros(b.Close), b.Volume, boolInt(b.Final))
			if err != nil {
				return errors.New("cannot persist minute bar")
			}
		}
	}
	calibrated := previous.CalibratedDate
	if calibration {
		calibrated = target.In(market.Shanghai).Format("2006-01-02")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO intraday_sync(code,target_unix,attempt_at,fetched_at,status,reason,calibrated_date) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(code) DO UPDATE SET target_unix=excluded.target_unix,attempt_at=excluded.attempt_at,fetched_at=excluded.fetched_at,
		status=excluded.status,reason=excluded.reason,calibrated_date=excluded.calibrated_date`, stock.Code, target.Unix(), attempt.Format(time.RFC3339Nano), fetched, status, reason, calibrated)
	if err != nil {
		return errors.New("cannot persist minute status")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit minute history")
	}
	return nil
}

func (m *MarketDB) LoadMinutes(ctx context.Context, stock config.Stock, period int) (MinuteSnapshot, error) {
	out := MinuteSnapshot{}
	if !market.MinutePeriod(period) {
		return out, errors.New("unsupported minute period")
	}
	if m == nil || m.db == nil {
		return out, fs.ErrNotExist
	}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, errors.New("cannot open minute view")
	}
	defer tx.Rollback()
	out.State, err = minuteState(ctx, tx, stock.Code)
	if err != nil {
		return out, err
	}
	out.MinuteHistory = market.MinuteHistory{Version: 1, Code: stock.Code, Name: stock.Name, Source: "tencent", Period: period, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", Bars: []market.MinuteBar{}}
	if out.State.FetchedAt != nil {
		out.FetchedAt = *out.State.FetchedAt
	}
	rows, err := tx.QueryContext(ctx, `SELECT end_unix,open_micros,high_micros,low_micros,close_micros,volume_raw,final FROM intraday_bar WHERE code=? ORDER BY end_unix DESC LIMIT ?`, stock.Code, (market.MinuteLimit+1)*period/5)
	if err != nil {
		return out, errors.New("cannot read minute history")
	}
	bars := []market.MinuteBar{}
	for rows.Next() {
		var b market.MinuteBar
		var end, o, h, l, c int64
		if err := rows.Scan(&end, &o, &h, &l, &c, &b.Volume, &b.Final); err != nil {
			rows.Close()
			return out, errors.New("cannot decode minute history")
		}
		b.End = time.Unix(end, 0).In(market.Shanghai)
		b.Open, b.High, b.Low, b.Close = float64(o)/1e6, float64(h)/1e6, float64(l)/1e6, float64(c)/1e6
		bars = append(bars, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, errors.New("cannot read minute history")
	}
	for a, b := 0, len(bars)-1; a < b; a, b = a+1, b-1 {
		bars[a], bars[b] = bars[b], bars[a]
	}
	out.Bars = market.AggregateMinutes(bars, period, out.FetchedAt)
	if len(out.Bars) > market.MinuteLimit {
		out.Bars = out.Bars[len(out.Bars)-market.MinuteLimit:]
	}
	return out, tx.Commit()
}
