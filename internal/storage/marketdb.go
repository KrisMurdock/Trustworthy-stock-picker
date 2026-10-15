package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"stock-watcher/internal/selection"
	"time"

	_ "modernc.org/sqlite"
)

const marketDBName = "market.db"
const marketSchemaVersion = 3

type MarketDB struct {
	db          *sql.DB
	root        string
	writable    bool
	requireCIFS bool
}

type sqlReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type SyncItem struct {
	Stock    config.Stock
	Adjusted *market.History
	Raw      *market.History
	Status   string
	Reason   string
}

type SyncRun struct {
	ID          string
	AsOf        string
	StartedAt   time.Time
	FinishedAt  time.Time
	Status      string
	CoveragePct float64
	ErrorKind   string
}

func OpenMarket(root string, requireCIFS bool) (*MarketDB, error) {
	return openMarket(root, requireCIFS, true, false)
}

func OpenMarketView(root string, requireCIFS bool) (*MarketDB, error) {
	return openMarket(root, requireCIFS, false, false)
}

func InitMarket(root string, requireCIFS bool) (*MarketDB, error) {
	return openMarket(root, requireCIFS, true, true)
}

func openMarket(root string, requireCIFS bool, writable, create bool) (*MarketDB, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	check := &Store{root: root, requireCIFS: requireCIFS}
	if err := check.Check(); err != nil {
		return nil, err
	}
	path := filepath.Join(root, marketDBName)
	if err := validateMarketPath(path, create); err != nil {
		return nil, err
	}
	if writable && !create {
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, errors.New("market database is not initialized; run stock-watcher init")
			}
			return nil, errors.New("cannot inspect market database")
		}
	}
	if !writable {
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return &MarketDB{root: root}, nil
			}
			return nil, err
		}
	}
	mode := "ro"
	if writable {
		mode = "rw"
		if create {
			mode = "rwc"
		}
	}
	dsn := "file:" + filepath.ToSlash(path) + "?mode=" + mode
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, errors.New("cannot open market database")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	m := &MarketDB{db: db, root: root, writable: writable, requireCIFS: requireCIFS}
	if err := m.configure(); err != nil {
		db.Close()
		return nil, err
	}
	if create {
		if err := validateMarketPath(path, false); err != nil {
			db.Close()
			return nil, err
		}
	}
	return m, nil
}

func validateMarketPath(path string, create bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if create {
			return nil
		}
		return nil
	}
	if err != nil {
		return errors.New("cannot inspect market database")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("market database must be a regular file")
	}
	return nil
}

func (m *MarketDB) configure() error {
	if m.writable {
		for _, statement := range []string{
			"PRAGMA journal_mode=DELETE",
			"PRAGMA synchronous=FULL",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=5000",
		} {
			if _, err := m.db.Exec(statement); err != nil {
				return errors.New("cannot configure market database")
			}
		}
		if err := m.initSchema(); err != nil {
			return err
		}
	} else {
		for _, statement := range []string{
			"PRAGMA query_only=ON",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=5000",
		} {
			if _, err := m.db.Exec(statement); err != nil {
				return errors.New("cannot configure market database view")
			}
		}
	}
	var version int
	if err := m.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != marketSchemaVersion {
		return errors.New("unsupported market database version")
	}
	if !m.writable {
		if err := verifySchema(m.db); err != nil {
			return err
		}
	}
	var result string
	if err := m.db.QueryRow("PRAGMA quick_check").Scan(&result); err != nil || result != "ok" {
		return errors.New("market database quick_check failed")
	}
	return nil
}

func verifySchema(reader sqlReader) error {
	rows, err := reader.QueryContext(context.Background(), `
		SELECT name FROM sqlite_master
		WHERE type='table' AND name IN ('stock', 'daily_bar', 'stock_sync', 'sync_run', 'selection_run', 'selection_result', 'intraday_bar', 'intraday_sync')
	`)
	if err != nil {
		return errors.New("cannot inspect market database schema")
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return errors.New("cannot inspect market database schema")
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil || len(found) != 8 {
		return errors.New("market database schema is incomplete")
	}
	return nil
}

func (m *MarketDB) initSchema() error {
	var version int
	if err := m.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return errors.New("cannot read market database version")
	}
	if version > marketSchemaVersion {
		return errors.New("unsupported market database version")
	}
	_, err := m.db.Exec(`
		CREATE TABLE IF NOT EXISTS stock (
			code TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			sector TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS daily_bar (
			code TEXT NOT NULL REFERENCES stock(code),
			adjust TEXT NOT NULL CHECK (adjust IN ('qfq', 'none', 'hfq')),
			trade_date TEXT NOT NULL,
			open_micros INTEGER NOT NULL CHECK (open_micros > 0),
			close_micros INTEGER NOT NULL CHECK (close_micros > 0),
			high_micros INTEGER NOT NULL CHECK (high_micros >= open_micros AND high_micros >= close_micros),
			low_micros INTEGER NOT NULL CHECK (low_micros > 0 AND low_micros <= open_micros AND low_micros <= close_micros),
			volume_raw REAL NOT NULL CHECK (volume_raw >= 0),
			volume_shares INTEGER NOT NULL CHECK (volume_shares >= 0),
			data_field TEXT NOT NULL CHECK (data_field IN ('qfqday', 'hfqday', 'day')),
			source TEXT NOT NULL,
			PRIMARY KEY (code, adjust, trade_date)
		);
		CREATE TABLE IF NOT EXISTS stock_sync (
			code TEXT NOT NULL REFERENCES stock(code),
			adjust TEXT NOT NULL CHECK (adjust IN ('qfq', 'none', 'hfq')),
			as_of TEXT NOT NULL,
			window_start TEXT NOT NULL DEFAULT '',
			window_hash TEXT NOT NULL DEFAULT '',
			bars INTEGER NOT NULL DEFAULT 0 CHECK (bars >= 0),
			data_field TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			fetched_at TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK (status IN ('ok', 'isolated', 'failed')),
			reason TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (code, adjust, as_of)
		);
		CREATE TABLE IF NOT EXISTS sync_run (
			id TEXT PRIMARY KEY,
			as_of TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('running', 'published', 'failed')),
			coverage_pct REAL NOT NULL CHECK (coverage_pct >= 0 AND coverage_pct <= 100),
			error_kind TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS selection_run (
			as_of TEXT PRIMARY KEY,
			id TEXT NOT NULL UNIQUE,
			generated_at TEXT NOT NULL,
			report_json BLOB NOT NULL
		);
		CREATE TABLE IF NOT EXISTS selection_result (
			as_of TEXT NOT NULL REFERENCES selection_run(as_of),
			code TEXT NOT NULL,
			rank INTEGER NOT NULL DEFAULT 0,
			selected INTEGER NOT NULL DEFAULT 0 CHECK (selected IN (0, 1)),
			result_json BLOB NOT NULL,
			PRIMARY KEY (as_of, code)
		);
	`)
	if err != nil {
		return errors.New("cannot initialize market database")
	}
	if version == 0 || version == 2 {
		return m.initMinuteSchema()
	}
	return nil
}

func (m *MarketDB) Close() error {
	if m == nil || m.db == nil {
		return nil
	}
	return m.db.Close()
}

func (m *MarketDB) Path() string {
	if m == nil {
		return ""
	}
	return filepath.Join(m.root, marketDBName)
}

func (m *MarketDB) CommitSnapshot(ctx context.Context, run SyncRun, items []SyncItem, report selection.Report) error {
	return m.commitMarket(ctx, run, items, report.Adjust, &report)
}

// CommitMarketData saves validated windows and a failed coverage run atomically,
// without creating or replacing any selection report.
func (m *MarketDB) CommitMarketData(ctx context.Context, run SyncRun, items []SyncItem, adjust string) error {
	run.Status, run.ErrorKind = "failed", "coverage"
	return m.commitMarket(ctx, run, items, adjust, nil)
}

func (m *MarketDB) commitMarket(ctx context.Context, run SyncRun, items []SyncItem, adjust string, report *selection.Report) error {
	if m == nil || !m.writable {
		return errors.New("market database is read-only")
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin market snapshot transaction")
	}
	rollback := func(e error) error {
		_ = tx.Rollback()
		return e
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO stock(code, name, sector, active, updated_at) VALUES(?, ?, ?, 1, ?)
			ON CONFLICT(code) DO UPDATE SET name=excluded.name, sector=excluded.sector, active=1, updated_at=excluded.updated_at
		`, item.Stock.Code, item.Stock.Name, item.Stock.Sector, run.FinishedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return rollback(errors.New("cannot persist stock metadata"))
		}
		if item.Adjusted != nil {
			if err := insertHistory(ctx, tx, *item.Adjusted); err != nil {
				return rollback(err)
			}
			if item.Raw != nil {
				if err := insertHistory(ctx, tx, *item.Raw); err != nil {
					return rollback(err)
				}
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO stock_sync(code, adjust, as_of, window_start, window_hash, bars, data_field, source, fetched_at, status, reason)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(code, adjust, as_of) DO UPDATE SET window_start=excluded.window_start, window_hash=excluded.window_hash, bars=excluded.bars,
					data_field=excluded.data_field, source=excluded.source, fetched_at=excluded.fetched_at,
					status=excluded.status, reason=excluded.reason
			`, item.Stock.Code, item.Adjusted.Adjust, item.Adjusted.AsOf, item.Adjusted.Bars[0].Date, historyHash(*item.Adjusted), len(item.Adjusted.Bars),
				item.Adjusted.DataField, item.Adjusted.Source, item.Adjusted.FetchedAt.UTC().Format(time.RFC3339Nano), item.Status, item.Reason); err != nil {
				return rollback(errors.New("cannot persist adjusted sync status"))
			}
			if item.Raw != nil {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO stock_sync(code, adjust, as_of, window_start, window_hash, bars, data_field, source, fetched_at, status, reason)
					VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
					ON CONFLICT(code, adjust, as_of) DO UPDATE SET window_start=excluded.window_start, window_hash=excluded.window_hash, bars=excluded.bars,
						data_field=excluded.data_field, source=excluded.source, fetched_at=excluded.fetched_at,
						status=excluded.status, reason=excluded.reason
				`, item.Stock.Code, item.Raw.Adjust, item.Raw.AsOf, item.Raw.Bars[0].Date, historyHash(*item.Raw), len(item.Raw.Bars),
					item.Raw.DataField, item.Raw.Source, item.Raw.FetchedAt.UTC().Format(time.RFC3339Nano), item.Status, item.Reason); err != nil {
					return rollback(errors.New("cannot persist raw sync status"))
				}
			}
		} else {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO stock_sync(code, adjust, as_of, status, reason)
				VALUES(?, ?, ?, ?, ?)
				ON CONFLICT(code, adjust, as_of) DO UPDATE SET status=excluded.status, reason=excluded.reason
			`, item.Stock.Code, adjust, run.AsOf, item.Status, item.Reason); err != nil {
				return rollback(errors.New("cannot persist isolated sync status"))
			}
		}
	}
	if report == nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sync_run(id, as_of, started_at, finished_at, status, coverage_pct, error_kind)
			VALUES(?, ?, ?, ?, ?, ?, ?)`, run.ID, run.AsOf, run.StartedAt.UTC().Format(time.RFC3339Nano),
			run.FinishedAt.UTC().Format(time.RFC3339Nano), run.Status, run.CoveragePct, run.ErrorKind); err != nil {
			return rollback(errors.New("cannot persist market data run"))
		}
		if err := tx.Commit(); err != nil {
			return errors.New("cannot commit market data")
		}
		return nil
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return rollback(errors.New("cannot encode selection report"))
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sync_run(id, as_of, started_at, finished_at, status, coverage_pct, error_kind)
		VALUES(?, ?, ?, ?, 'published', ?, '')
	`, run.ID, run.AsOf, run.StartedAt.UTC().Format(time.RFC3339Nano), run.FinishedAt.UTC().Format(time.RFC3339Nano), run.CoveragePct); err != nil {
		return rollback(errors.New("cannot persist sync run"))
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO selection_run(as_of, id, generated_at, report_json)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(as_of) DO UPDATE SET id=excluded.id, generated_at=excluded.generated_at, report_json=excluded.report_json
	`, report.AsOf, report.ID, report.GeneratedAt.UTC().Format(time.RFC3339Nano), reportJSON); err != nil {
		return rollback(errors.New("cannot persist selection report"))
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM selection_result WHERE as_of=?", report.AsOf); err != nil {
		return rollback(errors.New("cannot replace selection results"))
	}
	if err := insertSelectionResults(ctx, tx, *report); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit market snapshot")
	}
	return nil
}

func (m *MarketDB) RecordFailedRun(ctx context.Context, run SyncRun) error {
	if m == nil || !m.writable {
		return errors.New("market database is read-only")
	}
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO sync_run(id, as_of, started_at, finished_at, status, coverage_pct, error_kind)
		VALUES(?, ?, ?, ?, 'failed', ?, ?)
	`, run.ID, run.AsOf, run.StartedAt.UTC().Format(time.RFC3339Nano), run.FinishedAt.UTC().Format(time.RFC3339Nano),
		run.CoveragePct, run.ErrorKind)
	if err != nil {
		return errors.New("cannot persist failed sync run")
	}
	return nil
}

func insertHistory(ctx context.Context, tx *sql.Tx, h market.History) error {
	if err := h.Validate(); err != nil {
		return fmt.Errorf("invalid history for %s: %w", h.Code, err)
	}
	for _, bar := range h.Bars {
		open, high, low, close := micros(bar.Open), micros(bar.High), micros(bar.Low), micros(bar.Close)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO daily_bar(code, adjust, trade_date, open_micros, close_micros, high_micros, low_micros,
				volume_raw, volume_shares, data_field, source)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(code, adjust, trade_date) DO UPDATE SET open_micros=excluded.open_micros,
				close_micros=excluded.close_micros, high_micros=excluded.high_micros, low_micros=excluded.low_micros,
				volume_raw=excluded.volume_raw, volume_shares=excluded.volume_shares,
				data_field=excluded.data_field, source=excluded.source
		`, h.Code, h.Adjust, bar.Date, open, close, high, low, bar.Volume,
			int64(math.Round(market.ProviderVolumeShares(h.Source, h.Code, bar.Volume))), h.DataField, h.Source); err != nil {
			return errors.New("cannot persist daily history")
		}
	}
	return nil
}

func insertSelectionResults(ctx context.Context, tx *sql.Tx, report selection.Report) error {
	selected := map[string]bool{}
	for _, candidate := range report.Selected {
		selected[candidate.Code] = true
	}
	for _, candidate := range report.Ranking {
		data, err := json.Marshal(candidate)
		if err != nil {
			return errors.New("cannot encode selection result")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO selection_result(as_of, code, rank, selected, result_json) VALUES(?, ?, ?, ?, ?)
		`, report.AsOf, candidate.Code, candidate.Rank, boolInt(selected[candidate.Code]), data); err != nil {
			return errors.New("cannot persist selection result")
		}
	}
	for _, candidate := range report.Selected {
		if _, exists := selected[candidate.Code]; !exists {
			continue
		}
		rank := 0
		if candidate.Ranking != nil {
			rank = candidate.Ranking.Rank
		}
		data, err := json.Marshal(candidate)
		if err != nil {
			return errors.New("cannot encode selected result")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO selection_result(as_of, code, rank, selected, result_json) VALUES(?, ?, ?, 1, ?)
			ON CONFLICT(as_of, code) DO UPDATE SET rank=excluded.rank, selected=1, result_json=excluded.result_json
		`, report.AsOf, candidate.Code, rank, data); err != nil {
			return errors.New("cannot persist selected result")
		}
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func micros(value float64) int64 {
	return int64(math.Round(value * 1_000_000))
}

func historyHash(h market.History) string {
	data, _ := json.Marshal(struct {
		Code   string       `json:"code"`
		Adjust string       `json:"adjust"`
		Bars   []market.Bar `json:"bars"`
	}{h.Code, h.Adjust, h.Bars})
	sum := sha256Sum(data)
	return sum
}

func sha256Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

func (m *MarketDB) LoadHistories(ctx context.Context, stocks []config.Stock, asof string, adjust string) ([]market.History, error) {
	if m == nil || m.db == nil {
		return nil, fs.ErrNotExist
	}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, errors.New("cannot begin market read transaction")
	}
	histories, err := loadHistories(ctx, tx, stocks, asof, adjust)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, errors.New("cannot commit market read transaction")
	}
	return histories, nil
}

func (m *MarketDB) LoadHistoriesPair(ctx context.Context, stocks []config.Stock, asof, adjusted string, includeRaw bool) ([]market.History, []market.History, error) {
	if m == nil || m.db == nil {
		return nil, nil, fs.ErrNotExist
	}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, errors.New("cannot begin market read transaction")
	}
	histories, err := loadHistories(ctx, tx, stocks, asof, adjusted)
	if err != nil {
		_ = tx.Rollback()
		return nil, nil, err
	}
	raw := []market.History{}
	if includeRaw {
		raw, err = loadHistories(ctx, tx, stocks, asof, "none")
		if err != nil {
			_ = tx.Rollback()
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, errors.New("cannot commit market read transaction")
	}
	return histories, raw, nil
}

func loadHistories(ctx context.Context, reader sqlReader, stocks []config.Stock, asof string, adjust string) ([]market.History, error) {
	out := make([]market.History, 0, len(stocks))
	for _, stock := range stocks {
		h, err := loadHistory(ctx, reader, stock, asof, adjust)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if len(out) == 0 {
		return nil, fs.ErrNotExist
	}
	return out, nil
}

func (m *MarketDB) LoadLatestHistory(ctx context.Context, code, adjust, limitAsOf string) (market.History, error) {
	if m == nil || m.db == nil {
		return market.History{}, fs.ErrNotExist
	}
	var asof, name string
	err := m.db.QueryRowContext(ctx, `
		SELECT sync.as_of, stock.name
		FROM stock_sync AS sync JOIN stock ON stock.code=sync.code
		WHERE sync.code=? AND sync.adjust=? AND sync.status='ok' AND sync.as_of<=?
		ORDER BY sync.as_of DESC LIMIT 1
	`, code, adjust, limitAsOf).Scan(&asof, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return market.History{}, fs.ErrNotExist
	}
	if err != nil {
		return market.History{}, errors.New("cannot read latest stock sync")
	}
	return loadHistory(ctx, m.db, config.Stock{Code: code, Name: name}, asof, adjust)
}

func loadHistory(ctx context.Context, reader sqlReader, stock config.Stock, asof, adjust string) (market.History, error) {
	var h market.History
	var dataField, source, fetchedAt, status, reason, windowStart string
	var bars int
	err := reader.QueryRowContext(ctx, `
		SELECT data_field, source, fetched_at, status, reason, bars, window_start
		FROM stock_sync WHERE code=? AND adjust=? AND as_of=?
	`, stock.Code, adjust, asof).Scan(&dataField, &source, &fetchedAt, &status, &reason, &bars, &windowStart)
	if errors.Is(err, sql.ErrNoRows) {
		return h, fs.ErrNotExist
	}
	if err != nil {
		return h, errors.New("cannot read stock sync status")
	}
	if status != "ok" {
		if status == "isolated" {
			return h, fmt.Errorf("%w: stock %s is isolated: %s", fs.ErrNotExist, stock.Code, reason)
		}
		return h, fmt.Errorf("stock %s is %s: %s", stock.Code, status, reason)
	}
	parsed, err := time.Parse(time.RFC3339Nano, fetchedAt)
	if err != nil {
		return h, errors.New("invalid stored fetch time")
	}
	h = market.History{Version: 1, Code: stock.Code, Name: stock.Name, Source: source, Adjust: adjust,
		DataField: dataField, VolumeUnit: "provider_raw", AsOf: asof, FetchedAt: parsed}
	rows, err := reader.QueryContext(ctx, `
		SELECT trade_date, open_micros, close_micros, high_micros, low_micros, volume_raw, source, data_field
		FROM daily_bar WHERE code=? AND adjust=? AND trade_date>=? AND trade_date<=? ORDER BY trade_date
	`, stock.Code, adjust, windowStart, asof)
	if err != nil {
		return h, errors.New("cannot read daily history")
	}
	defer rows.Close()
	for rows.Next() {
		var date string
		var open, close, high, low int64
		var volume float64
		var barSource, barField string
		if err := rows.Scan(&date, &open, &close, &high, &low, &volume, &barSource, &barField); err != nil {
			return h, errors.New("cannot decode daily history")
		}
		if barSource != source || barField != dataField {
			return h, errors.New("stored daily history contains inconsistent sources or adjustment fields")
		}
		h.Bars = append(h.Bars, market.Bar{Date: date, Open: float64(open) / 1_000_000, Close: float64(close) / 1_000_000,
			High: float64(high) / 1_000_000, Low: float64(low) / 1_000_000, Volume: volume})
	}
	if err := rows.Err(); err != nil || len(h.Bars) != bars {
		return h, errors.New("stored daily history is incomplete")
	}
	if err := h.Validate(); err != nil {
		return h, err
	}
	return h, nil
}

func (m *MarketDB) LoadReport(ctx context.Context, asof string) (selection.Report, error) {
	if m == nil || m.db == nil {
		return selection.Report{}, fs.ErrNotExist
	}
	var data []byte
	err := m.db.QueryRowContext(ctx, "SELECT report_json FROM selection_run WHERE as_of=?", asof).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return selection.Report{}, fs.ErrNotExist
	}
	if err != nil {
		return selection.Report{}, errors.New("cannot read selection report")
	}
	var report selection.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return report, errors.New("invalid stored selection report")
	}
	return report, nil
}

func (m *MarketDB) LatestRun(ctx context.Context, asof string) (SyncRun, error) {
	if m == nil || m.db == nil {
		return SyncRun{}, fs.ErrNotExist
	}
	var run SyncRun
	var started, finished string
	err := m.db.QueryRowContext(ctx, `
		SELECT id, as_of, started_at, finished_at, status, coverage_pct, error_kind
		FROM sync_run WHERE as_of=? ORDER BY finished_at DESC LIMIT 1
	`, asof).Scan(&run.ID, &run.AsOf, &started, &finished, &run.Status, &run.CoveragePct, &run.ErrorKind)
	if errors.Is(err, sql.ErrNoRows) {
		return run, fs.ErrNotExist
	}
	if err != nil {
		return run, errors.New("cannot read sync run")
	}
	run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	run.FinishedAt, _ = time.Parse(time.RFC3339Nano, finished)
	return run, nil
}

func (m *MarketDB) HasDatabase() bool {
	return m != nil && m.db != nil
}
