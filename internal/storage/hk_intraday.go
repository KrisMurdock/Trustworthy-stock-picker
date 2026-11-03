package storage

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const hkMinuteSchema = `
	CREATE TABLE IF NOT EXISTS intraday_bar (
		code TEXT NOT NULL REFERENCES stock(code),
		end_unix INTEGER NOT NULL,
		open_micros INTEGER NOT NULL CHECK(open_micros>0),
		high_micros INTEGER NOT NULL,
		low_micros INTEGER NOT NULL CHECK(low_micros>0),
		close_micros INTEGER NOT NULL CHECK(close_micros>0),
		volume_raw REAL NOT NULL CHECK(volume_raw>=0),
		final INTEGER NOT NULL CHECK(final IN (0,1)),
		observed_ns INTEGER NOT NULL,
		CHECK(high_micros>=open_micros AND high_micros>=close_micros),
		CHECK(low_micros<=open_micros AND low_micros<=close_micros),
		PRIMARY KEY(code,end_unix)
	);
	CREATE TABLE IF NOT EXISTS intraday_sync (
		code TEXT PRIMARY KEY REFERENCES stock(code),
		requested_from TEXT NOT NULL,
		requested_through TEXT NOT NULL,
		attempt_at TEXT NOT NULL,
		fetched_at TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL CHECK(status IN ('ok','partial','failed')),
		reason TEXT NOT NULL DEFAULT '',
		missing_bars INTEGER NOT NULL DEFAULT 0 CHECK(missing_bars>=0),
		unresolved_from TEXT NOT NULL DEFAULT '',
		unresolved_through TEXT NOT NULL DEFAULT ''
	);
`

type HKMinuteState struct {
	RequestedFrom     string     `json:"requested_from"`
	RequestedThrough  string     `json:"requested_through"`
	AttemptAt         time.Time  `json:"attempt_at"`
	FetchedAt         *time.Time `json:"fetched_at"`
	Status            string     `json:"status"`
	Reason            string     `json:"reason"`
	MissingBars       int        `json:"missing_bars"`
	UnresolvedFrom    string     `json:"unresolved_from,omitempty"`
	UnresolvedThrough string     `json:"unresolved_through,omitempty"`
}

type HKMinuteSnapshot struct {
	Version    int                  `json:"schema_version"`
	Code       string               `json:"code"`
	Name       string               `json:"name"`
	Source     string               `json:"source"`
	Period     int                  `json:"period_minutes"`
	BasePeriod int                  `json:"base_period_minutes"`
	Adjust     string               `json:"adjust"`
	VolumeUnit string               `json:"volume_unit"`
	FetchedAt  time.Time            `json:"fetched_at"`
	Bars       []market.HKMinuteBar `json:"bars"`
	Sync       HKMinuteState        `json:"sync"`
}

func hkMinuteState(ctx context.Context, reader sqlReader, code string) (HKMinuteState, error) {
	var state HKMinuteState
	var attempt, fetched string
	err := reader.QueryRowContext(ctx, `SELECT requested_from,requested_through,attempt_at,fetched_at,status,reason,missing_bars,unresolved_from,unresolved_through
		FROM intraday_sync WHERE code=?`, code).Scan(&state.RequestedFrom, &state.RequestedThrough, &attempt, &fetched, &state.Status, &state.Reason, &state.MissingBars, &state.UnresolvedFrom, &state.UnresolvedThrough)
	if errors.Is(err, sql.ErrNoRows) {
		return state, fs.ErrNotExist
	}
	if err != nil {
		return state, errors.New("cannot read HK minute status")
	}
	state.AttemptAt, err = time.Parse(time.RFC3339Nano, attempt)
	if err != nil {
		return state, errors.New("invalid HK minute attempt time")
	}
	if fetched != "" {
		observed, err := time.Parse(time.RFC3339Nano, fetched)
		if err != nil {
			return state, errors.New("invalid HK minute observation time")
		}
		state.FetchedAt = &observed
	}
	return state, nil
}

func (m *AllMarketDB) HKMinuteState(ctx context.Context, code string) (HKMinuteState, error) {
	if m == nil || m.db == nil || m.dbName != hkMarketDBName {
		return HKMinuteState{}, fs.ErrNotExist
	}
	if !config.ValidHKStockCode(code) {
		return HKMinuteState{}, errors.New("invalid HK minute code")
	}
	return hkMinuteState(ctx, m.db, code)
}

func (m *AllMarketDB) LatestHKMinuteDate(ctx context.Context, code string) (string, error) {
	if m == nil || m.db == nil || m.dbName != hkMarketDBName {
		return "", fs.ErrNotExist
	}
	if !config.ValidHKStockCode(code) {
		return "", errors.New("invalid HK minute code")
	}
	var latest sql.NullInt64
	if err := m.db.QueryRowContext(ctx, "SELECT MAX(end_unix) FROM intraday_bar WHERE code=?", code).Scan(&latest); err != nil {
		return "", errors.New("cannot read latest HK minute")
	}
	if !latest.Valid {
		return "", fs.ErrNotExist
	}
	return time.Unix(latest.Int64, 0).In(market.Shanghai).Format("2006-01-02"), nil
}

func (m *AllMarketDB) SaveHKMinutes(ctx context.Context, stock config.Stock, from, through string, attempt time.Time, history *market.HKMinuteHistory, status, reason string, missing int) error {
	if m == nil || m.dbName != hkMarketDBName {
		return errors.New("HK minute database is unavailable")
	}
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	first, firstErr := time.Parse("2006-01-02", from)
	last, lastErr := time.Parse("2006-01-02", through)
	if !config.ValidHKStockCode(stock.Code) || attempt.IsZero() || firstErr != nil || lastErr != nil || first.Format("2006-01-02") != from ||
		last.Format("2006-01-02") != through || first.After(last) || missing < 0 {
		return errors.New("invalid HK minute persistence request")
	}
	if status != "ok" && status != "partial" && status != "failed" {
		return errors.New("invalid HK minute status")
	}
	if status == "ok" && (history == nil || reason != "" || missing != 0) || status != "ok" && reason == "" || status == "failed" && history != nil {
		return errors.New("inconsistent HK minute status")
	}
	if history != nil {
		if err := history.ValidateBase(); err != nil {
			return err
		}
		if history.Code != stock.Code || history.FetchedAt.After(attempt) {
			return errors.New("HK minute identity or observation mismatch")
		}
		for _, bar := range history.Bars {
			day := bar.End.In(market.Shanghai).Format("2006-01-02")
			if day < from || day > through {
				return errors.New("HK minute response exceeds requested range")
			}
		}
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot start HK minute transaction")
	}
	defer tx.Rollback()
	previous, err := hkMinuteState(ctx, tx, stock.Code)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if !previous.AttemptAt.IsZero() && attempt.Before(previous.AttemptAt) {
		return errors.New("HK minute attempt is older than persisted status")
	}
	if err := upsertAllMarketStock(ctx, tx, stock); err != nil {
		return err
	}
	fetched := ""
	if previous.FetchedAt != nil {
		fetched = previous.FetchedAt.Format(time.RFC3339Nano)
	}
	if history != nil {
		if previous.FetchedAt == nil || history.FetchedAt.After(*previous.FetchedAt) {
			fetched = history.FetchedAt.Format(time.RFC3339Nano)
		}
		statement, err := tx.PrepareContext(ctx, `INSERT INTO intraday_bar(code,end_unix,open_micros,high_micros,low_micros,close_micros,volume_raw,final,observed_ns)
			VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(code,end_unix) DO UPDATE SET open_micros=excluded.open_micros,
			high_micros=excluded.high_micros,low_micros=excluded.low_micros,close_micros=excluded.close_micros,
			volume_raw=excluded.volume_raw,final=excluded.final,observed_ns=excluded.observed_ns
			WHERE intraday_bar.observed_ns<excluded.observed_ns`)
		if err != nil {
			return errors.New("cannot prepare HK minute write")
		}
		defer statement.Close()
		for _, bar := range history.Bars {
			_, err := statement.ExecContext(ctx, stock.Code, bar.End.Unix(), micros(bar.Open), micros(bar.High), micros(bar.Low), micros(bar.Close), bar.Volume, boolInt(bar.Final), history.FetchedAt.UnixNano())
			if err != nil {
				return errors.New("cannot persist HK minute bar")
			}
		}
	}
	unresolvedFrom, unresolvedThrough := previous.UnresolvedFrom, previous.UnresolvedThrough
	if status == "failed" || status == "partial" && missing > 0 {
		if unresolvedFrom == "" || from < unresolvedFrom {
			unresolvedFrom = from
		}
		if unresolvedThrough == "" || through > unresolvedThrough {
			unresolvedThrough = through
		}
	} else if status == "ok" && unresolvedFrom != "" {
		switch {
		case from <= unresolvedFrom && through >= unresolvedThrough:
			unresolvedFrom, unresolvedThrough = "", ""
		case from <= unresolvedFrom && through >= unresolvedFrom:
			unresolvedFrom = last.AddDate(0, 0, 1).Format("2006-01-02")
		case from <= unresolvedThrough && through >= unresolvedThrough:
			unresolvedThrough = first.AddDate(0, 0, -1).Format("2006-01-02")
		}
		if unresolvedFrom != "" {
			status, reason = "partial", "history_gap_unresolved"
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO intraday_sync(code,requested_from,requested_through,attempt_at,fetched_at,status,reason,missing_bars,unresolved_from,unresolved_through)
		VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(code) DO UPDATE SET requested_from=excluded.requested_from,
		requested_through=excluded.requested_through,attempt_at=excluded.attempt_at,fetched_at=excluded.fetched_at,
		status=excluded.status,reason=excluded.reason,missing_bars=excluded.missing_bars,
		unresolved_from=excluded.unresolved_from,unresolved_through=excluded.unresolved_through`,
		stock.Code, from, through, attempt.Format(time.RFC3339Nano), fetched, status, reason, missing, unresolvedFrom, unresolvedThrough)
	if err != nil {
		return errors.New("cannot persist HK minute status")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit HK minute history")
	}
	return nil
}

func (m *AllMarketDB) LoadHKMinutes(ctx context.Context, code string, period int, calendar config.HKIntraday) (HKMinuteSnapshot, error) {
	result := HKMinuteSnapshot{}
	if !config.ValidHKStockCode(code) || !market.MinutePeriod(period) {
		return result, errors.New("invalid HK minute request")
	}
	if m == nil || m.db == nil || m.dbName != hkMarketDBName {
		return result, fs.ErrNotExist
	}
	if err := calendar.ValidateCalendar(); err != nil {
		return result, errors.New("HK minute calendar configuration is unavailable")
	}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, errors.New("cannot open HK minute view")
	}
	defer tx.Rollback()
	var name string
	if err := tx.QueryRowContext(ctx, "SELECT name FROM stock WHERE code=?", code).Scan(&name); errors.Is(err, sql.ErrNoRows) {
		return result, fs.ErrNotExist
	} else if err != nil {
		return result, errors.New("cannot read HK stock")
	}
	state, err := hkMinuteState(ctx, tx, code)
	if err != nil {
		return result, err
	}
	result = HKMinuteSnapshot{Version: 1, Code: code, Name: name, Source: "eastmoney_hk", Period: period, BasePeriod: 5,
		Adjust: "none", VolumeUnit: "provider_raw", Bars: []market.HKMinuteBar{}, Sync: state}
	if state.FetchedAt != nil {
		result.FetchedAt = *state.FetchedAt
	}
	bars, err := readMinuteBars(ctx, tx, code, (market.MinuteLimit+1)*period/5)
	if err != nil {
		return HKMinuteSnapshot{}, err
	}
	earlyCloses := calendar.EarlyCloseMap()
	for _, bar := range bars {
		day := bar.End.In(market.Shanghai).Format("2006-01-02")
		slot, valid := market.HKMinuteSlot(bar.End)
		if !valid || day < calendar.ValidFrom || day > calendar.ValidThrough || earlyCloses[day] && slot >= 30 {
			return HKMinuteSnapshot{}, errors.New("HK minute data is outside configured trading sessions")
		}
	}
	result.Bars = market.AggregateHKMinutes(bars, period, result.FetchedAt, earlyCloses)
	if len(result.Bars) > market.MinuteLimit {
		result.Bars = result.Bars[len(result.Bars)-market.MinuteLimit:]
	}
	return result, tx.Commit()
}
