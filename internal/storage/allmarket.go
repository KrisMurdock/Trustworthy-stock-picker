package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	allMarketDBName        = "all-market.db"
	allMarketLockName      = ".all-market.lock"
	hkMarketDBName         = "hk-market.db"
	hkMarketLockName       = ".hk-market.lock"
	allMarketSchemaVersion = 2
	hkMarketSchemaVersion  = 3
	maxRecommendationBytes = 16 << 20
)

// AllMarketDB is a deliberately separate store. The A-share instance holds the
// current Shanghai/Shenzhen universe and never participates in selection,
// reports or notification delivery. The same implementation also backs the
// independent HK Connect database, including its recommendation snapshots.
type AllMarketDB struct {
	db          *sql.DB
	root        string
	dbName      string
	lockName    string
	kind        string
	writable    bool
	requireCIFS bool
	lock        *os.File
}

type AllMarketSync struct {
	Code        string    `json:"code"`
	Adjust      string    `json:"adjust"`
	AsOf        string    `json:"as_of"`
	WindowStart string    `json:"window_start,omitempty"`
	Bars        int       `json:"bars"`
	Source      string    `json:"source,omitempty"`
	DataField   string    `json:"data_field,omitempty"`
	FetchedAt   time.Time `json:"fetched_at,omitempty"`
	Status      string    `json:"status"`
	Reason      string    `json:"reason,omitempty"`
}

// AllMarketQuote is the latest persisted unadjusted close for one full-market
// stock. The financial valuation model requires an unadjusted price anchor.
// PriceDate can be older than the requested as-of for a suspended stock; the
// valuation scanner must reject those rows instead of treating them as fresh.
type AllMarketQuote struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	PriceDate string  `json:"price_date"`
	Close     float64 `json:"close"`
	Volume    float64 `json:"volume_shares"`
}

type AllMarketRun struct {
	ID         string    `json:"id"`
	AsOf       string    `json:"as_of"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Status     string    `json:"status"`
	Total      int       `json:"total"`
	Succeeded  int       `json:"succeeded"`
	Partial    int       `json:"partial"`
	Failed     int       `json:"failed"`
	Skipped    int       `json:"skipped"`
	ErrorKind  string    `json:"error_kind,omitempty"`
}

type RecommendationArtifact struct {
	Market  string    `json:"market"`
	AsOf    string    `json:"as_of"`
	ID      string    `json:"id"`
	Target  string    `json:"target"`
	BuiltAt time.Time `json:"built_at"`
	Payload []byte    `json:"-"`
}

func OpenAllMarket(root string, requireCIFS bool) (*AllMarketDB, error) {
	return openAllMarket(root, requireCIFS, true, allMarketDBName, allMarketLockName, "all-market")
}

func OpenAllMarketView(root string, requireCIFS bool) (*AllMarketDB, error) {
	return openAllMarket(root, requireCIFS, false, allMarketDBName, allMarketLockName, "all-market")
}

// OpenHKMarket opens the Stock Connect daily store. It intentionally shares the
// full-market schema but uses a separate database and writer lock so a Hong
// Kong sync can run beside the A-share full-market sync.
func OpenHKMarket(root string, requireCIFS bool) (*AllMarketDB, error) {
	return openAllMarket(root, requireCIFS, true, hkMarketDBName, hkMarketLockName, "HK market")
}

func OpenHKMarketView(root string, requireCIFS bool) (*AllMarketDB, error) {
	return openAllMarket(root, requireCIFS, false, hkMarketDBName, hkMarketLockName, "HK market")
}

func openAllMarket(root string, requireCIFS, writable bool, dbName, lockName, kind string) (*AllMarketDB, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	check := &Store{root: root, requireCIFS: requireCIFS}
	if err := check.Check(); err != nil {
		return nil, err
	}
	path := filepath.Join(root, dbName)
	if !writable {
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New(kind + " database is not initialized")
		}
	}
	m := &AllMarketDB{root: root, dbName: dbName, lockName: lockName, kind: kind, writable: writable, requireCIFS: requireCIFS}
	if writable {
		f, err := os.OpenFile(filepath.Join(root, lockName), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, errors.New(kind + " lock is not writable")
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			f.Close()
			return nil, errors.New("another " + kind + " synchronization owns the data directory")
		}
		m.lock = f
	}
	mode := "ro"
	if writable {
		mode = "rwc"
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode="+mode)
	if err != nil {
		m.unlock()
		return nil, errors.New("cannot open " + kind + " database")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	m.db = db
	if err := m.configure(); err != nil {
		_ = m.Close()
		return nil, err
	}
	return m, nil
}

func (m *AllMarketDB) configure() error {
	if m == nil || m.db == nil {
		return errors.New("full-market database is unavailable")
	}
	if !m.writable {
		for _, statement := range []string{"PRAGMA query_only=ON", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=30000"} {
			if _, err := m.db.Exec(statement); err != nil {
				return errors.New("cannot configure " + m.kind + " database view")
			}
		}
		return nil
	}
	for _, statement := range []string{
		"PRAGMA journal_mode=DELETE",
		"PRAGMA synchronous=FULL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=30000",
	} {
		if _, err := m.db.Exec(statement); err != nil {
			return errors.New("cannot configure " + m.kind + " database")
		}
	}
	if err := m.initSchema(); err != nil {
		return err
	}
	return nil
}

func (m *AllMarketDB) initSchema() error {
	var version int
	if err := m.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return errors.New("cannot read " + m.kind + " database version")
	}
	schemaVersion := allMarketSchemaVersion
	if m.dbName == hkMarketDBName {
		schemaVersion = hkMarketSchemaVersion
	}
	if version < 0 || version > schemaVersion {
		return errors.New("unsupported " + m.kind + " database version")
	}
	tx, err := m.db.Begin()
	if err != nil {
		return errors.New("cannot start " + m.kind + " database migration")
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
		CREATE TABLE IF NOT EXISTS stock (
			code TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			listed_from TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS daily_bar (
			code TEXT NOT NULL REFERENCES stock(code),
			adjust TEXT NOT NULL CHECK (adjust IN ('none', 'qfq')),
			trade_date TEXT NOT NULL,
			open_micros INTEGER NOT NULL CHECK (open_micros > 0),
			close_micros INTEGER NOT NULL CHECK (close_micros > 0),
			high_micros INTEGER NOT NULL CHECK (high_micros >= open_micros AND high_micros >= close_micros),
			low_micros INTEGER NOT NULL CHECK (low_micros > 0 AND low_micros <= open_micros AND low_micros <= close_micros),
			volume_raw REAL NOT NULL CHECK (volume_raw >= 0),
			volume_shares INTEGER NOT NULL CHECK (volume_shares >= 0),
			data_field TEXT NOT NULL CHECK (data_field IN ('qfqday', 'day')),
			source TEXT NOT NULL,
			PRIMARY KEY (code, adjust, trade_date)
		);
		CREATE TABLE IF NOT EXISTS stock_sync (
			code TEXT NOT NULL REFERENCES stock(code),
			adjust TEXT NOT NULL CHECK (adjust IN ('none', 'qfq')),
			as_of TEXT NOT NULL,
			window_start TEXT NOT NULL DEFAULT '',
			bars INTEGER NOT NULL DEFAULT 0 CHECK (bars >= 0),
			source TEXT NOT NULL DEFAULT '',
			data_field TEXT NOT NULL DEFAULT '',
			fetched_at TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK (status IN ('ok', 'failed', 'stale')),
			reason TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (code, adjust, as_of)
		);
		CREATE TABLE IF NOT EXISTS sync_run (
			id TEXT PRIMARY KEY,
			as_of TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('running', 'complete', 'partial', 'failed')),
			total INTEGER NOT NULL DEFAULT 0,
			succeeded INTEGER NOT NULL DEFAULT 0,
			partial INTEGER NOT NULL DEFAULT 0,
			failed INTEGER NOT NULL DEFAULT 0,
			skipped INTEGER NOT NULL DEFAULT 0,
			error_kind TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS recommendation (
			market TEXT NOT NULL,
			as_of TEXT NOT NULL,
			id TEXT NOT NULL,
			target TEXT NOT NULL,
			built_at TEXT NOT NULL,
			payload BLOB NOT NULL CHECK (length(payload) > 0),
			PRIMARY KEY (market, as_of)
		);
	`)
	if err != nil {
		return errors.New("cannot initialize " + m.kind + " database schema")
	}
	if m.dbName == hkMarketDBName {
		if _, err := tx.Exec(hkMinuteSchema); err != nil {
			return errors.New("cannot initialize HK minute schema")
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)); err != nil {
		return errors.New("cannot update " + m.kind + " database version")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit " + m.kind + " database migration")
	}
	return nil
}

func (m *AllMarketDB) Close() error {
	if m == nil {
		return nil
	}
	var err error
	if m.db != nil {
		err = m.db.Close()
		m.db = nil
	}
	if unlockErr := m.unlock(); unlockErr != nil {
		err = errors.Join(err, unlockErr)
	}
	return err
}

func (m *AllMarketDB) unlock() error {
	if m.lock == nil {
		return nil
	}
	err := syscall.Flock(int(m.lock.Fd()), syscall.LOCK_UN)
	closeErr := m.lock.Close()
	m.lock = nil
	return errors.Join(err, closeErr)
}

func (m *AllMarketDB) checkWriter(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m == nil || !m.writable || m.db == nil {
		return errors.New("full-market database is read-only")
	}
	return (&Store{root: m.root, requireCIFS: m.requireCIFS}).Check()
}

func (m *AllMarketDB) SaveHistory(ctx context.Context, stock config.Stock, h market.History, replace bool) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	if stock.Code != h.Code || !config.ValidMarketStockCode(stock.Code) || (h.Adjust != "none" && h.Adjust != "qfq") {
		return errors.New("invalid " + m.kind + " history")
	}
	if err := h.Validate(); err != nil {
		return fmt.Errorf("invalid %s history: %w", m.kind, err)
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin " + m.kind + " transaction")
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	if err := upsertAllMarketStock(ctx, tx, stock); err != nil {
		return rollback(err)
	}
	if replace {
		if _, err := tx.ExecContext(ctx, "DELETE FROM daily_bar WHERE code=? AND adjust=?", stock.Code, h.Adjust); err != nil {
			return rollback(errors.New("cannot replace " + m.kind + " history"))
		}
	}
	for _, bar := range h.Bars {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO daily_bar(code, adjust, trade_date, open_micros, close_micros, high_micros, low_micros,
				volume_raw, volume_shares, data_field, source)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(code, adjust, trade_date) DO UPDATE SET open_micros=excluded.open_micros,
				close_micros=excluded.close_micros, high_micros=excluded.high_micros, low_micros=excluded.low_micros,
				volume_raw=excluded.volume_raw, volume_shares=excluded.volume_shares,
				data_field=excluded.data_field, source=excluded.source
		`, h.Code, h.Adjust, bar.Date, micros(bar.Open), micros(bar.Close), micros(bar.High), micros(bar.Low), bar.Volume,
			int64(providerVolumeShares(h.Source, h.Code, bar.Volume)), h.DataField, h.Source); err != nil {
			return rollback(errors.New("cannot persist " + m.kind + " daily history"))
		}
	}
	if err := upsertAllMarketStatus(ctx, tx, stock, h.Adjust, h.AsOf, h.Bars[0].Date, len(h.Bars), h.Source, h.DataField, h.FetchedAt, "ok", ""); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit " + m.kind + " history")
	}
	return nil
}

func (m *AllMarketDB) RecordStatus(ctx context.Context, stock config.Stock, adjust, asOf, status, reason string) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	if !config.ValidMarketStockCode(stock.Code) || (adjust != "none" && adjust != "qfq") || (status != "failed" && status != "stale") {
		return errors.New("invalid " + m.kind + " stock status")
	}
	if _, err := time.Parse("2006-01-02", asOf); err != nil {
		return errors.New("invalid " + m.kind + " status date")
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin " + m.kind + " status transaction")
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	if err := upsertAllMarketStock(ctx, tx, stock); err != nil {
		return rollback(err)
	}
	if err := upsertAllMarketStatus(ctx, tx, stock, adjust, asOf, "", 0, "", "", time.Time{}, status, reason); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit " + m.kind + " stock status")
	}
	return nil
}

func upsertAllMarketStock(ctx context.Context, tx *sql.Tx, stock config.Stock) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO stock(code, name, listed_from, active, updated_at) VALUES(?, ?, ?, 1, ?)
		ON CONFLICT(code) DO UPDATE SET name=excluded.name,
			listed_from=CASE WHEN excluded.listed_from<>'' THEN excluded.listed_from ELSE stock.listed_from END,
			active=1, updated_at=excluded.updated_at
	`, stock.Code, stock.Name, stock.ListedFrom, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return errors.New("cannot persist all-market stock metadata")
	}
	return nil
}

func upsertAllMarketStatus(ctx context.Context, tx *sql.Tx, stock config.Stock, adjust, asOf, windowStart string, bars int, source, dataField string, fetchedAt time.Time, status, reason string) error {
	fetched := ""
	if !fetchedAt.IsZero() {
		fetched = fetchedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO stock_sync(code, adjust, as_of, window_start, bars, source, data_field, fetched_at, status, reason)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(code, adjust, as_of) DO UPDATE SET
			status=CASE WHEN stock_sync.status='ok' THEN 'ok' ELSE excluded.status END,
			reason=CASE WHEN stock_sync.status='ok' THEN stock_sync.reason ELSE excluded.reason END,
			window_start=CASE WHEN excluded.status='ok' THEN excluded.window_start ELSE stock_sync.window_start END,
			bars=CASE WHEN excluded.status='ok' THEN excluded.bars ELSE stock_sync.bars END,
			source=CASE WHEN excluded.status='ok' THEN excluded.source ELSE stock_sync.source END,
			data_field=CASE WHEN excluded.status='ok' THEN excluded.data_field ELSE stock_sync.data_field END,
			fetched_at=CASE WHEN excluded.status='ok' THEN excluded.fetched_at ELSE stock_sync.fetched_at END
	`, stock.Code, adjust, asOf, windowStart, bars, source, dataField, fetched, status, reason)
	if err != nil {
		return errors.New("cannot persist all-market sync status")
	}
	return nil
}

func (m *AllMarketDB) SyncStatus(ctx context.Context, code, adjust, asOf string) (AllMarketSync, error) {
	var out AllMarketSync
	if m == nil || m.db == nil {
		return out, fs.ErrNotExist
	}
	var fetched string
	err := m.db.QueryRowContext(ctx, `
		SELECT code, adjust, as_of, window_start, bars, source, data_field, fetched_at, status, reason
		FROM stock_sync WHERE code=? AND adjust=? AND as_of=?
	`, code, adjust, asOf).Scan(&out.Code, &out.Adjust, &out.AsOf, &out.WindowStart, &out.Bars, &out.Source, &out.DataField, &fetched, &out.Status, &out.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return out, fs.ErrNotExist
	}
	if err != nil {
		return out, errors.New("cannot read all-market sync status")
	}
	if fetched != "" {
		out.FetchedAt, _ = time.Parse(time.RFC3339Nano, fetched)
	}
	return out, nil
}

func (m *AllMarketDB) LastSuccessful(ctx context.Context, code, adjust string) (AllMarketSync, error) {
	var out AllMarketSync
	if m == nil || m.db == nil {
		return out, fs.ErrNotExist
	}
	var fetched string
	err := m.db.QueryRowContext(ctx, `
		SELECT code, adjust, as_of, window_start, bars, source, data_field, fetched_at, status, reason
		FROM stock_sync WHERE code=? AND adjust=? AND status='ok'
		ORDER BY as_of DESC LIMIT 1
	`, code, adjust).Scan(&out.Code, &out.Adjust, &out.AsOf, &out.WindowStart, &out.Bars, &out.Source, &out.DataField, &fetched, &out.Status, &out.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return out, fs.ErrNotExist
	}
	if err != nil {
		return out, errors.New("cannot read latest all-market sync")
	}
	if fetched != "" {
		out.FetchedAt, _ = time.Parse(time.RFC3339Nano, fetched)
	}
	return out, nil
}

func (m *AllMarketDB) LastBarDate(ctx context.Context, code, adjust string) (string, error) {
	if m == nil || m.db == nil {
		return "", fs.ErrNotExist
	}
	var date sql.NullString
	if err := m.db.QueryRowContext(ctx, "SELECT MAX(trade_date) FROM daily_bar WHERE code=? AND adjust=?", code, adjust).Scan(&date); err != nil {
		return "", errors.New("cannot read latest all-market bar")
	}
	if !date.Valid || date.String == "" {
		return "", fs.ErrNotExist
	}
	return date.String, nil
}

// LatestQuotes returns one latest unadjusted close per active stock whose raw
// synchronization completed for asOf. It does not silently carry a prior
// as-of snapshot: callers receive the actual bar date and decide whether the
// stock was suspended or stale.
func (m *AllMarketDB) LatestQuotes(ctx context.Context, asOf string) ([]AllMarketQuote, error) {
	if m == nil || m.db == nil {
		return nil, fs.ErrNotExist
	}
	if _, err := time.Parse("2006-01-02", asOf); err != nil {
		return nil, errors.New("invalid quote as-of date")
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT st.code, st.name, b.trade_date, b.close_micros, b.volume_shares
		FROM stock st
		JOIN stock_sync ss ON ss.code=st.code AND ss.adjust='none' AND ss.as_of=? AND ss.status='ok'
		JOIN daily_bar b ON b.code=st.code AND b.adjust='none'
		  AND b.trade_date=(
			SELECT MAX(b2.trade_date) FROM daily_bar b2
			WHERE b2.code=st.code AND b2.adjust='none' AND b2.trade_date<=?
		  )
		WHERE st.active=1
		ORDER BY st.code
	`, asOf, asOf)
	if err != nil {
		return nil, errors.New("cannot load all-market quotes")
	}
	defer rows.Close()
	out := []AllMarketQuote{}
	for rows.Next() {
		var quote AllMarketQuote
		var closeMicros int64
		if err := rows.Scan(&quote.Code, &quote.Name, &quote.PriceDate, &closeMicros, &quote.Volume); err != nil {
			return nil, errors.New("cannot decode all-market quotes")
		}
		if closeMicros <= 0 {
			return nil, errors.New("all-market quote contains a non-positive close")
		}
		quote.Close = float64(closeMicros) / 1_000_000
		out = append(out, quote)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot read all-market quotes")
	}
	return out, nil
}

// LatestUnadjustedAsOf returns the newest date covering at least 90% of the
// stored universe with successful unadjusted syncs. This keeps a small
// -codes-only refresh from becoming the default cross-sectional snapshot.
func (m *AllMarketDB) LatestUnadjustedAsOf(ctx context.Context) (string, error) {
	if m == nil || m.db == nil {
		return "", fs.ErrNotExist
	}
	var date sql.NullString
	if err := m.db.QueryRowContext(ctx, `
		SELECT as_of FROM stock_sync
		WHERE adjust='none' AND status='ok'
		GROUP BY as_of
		HAVING COUNT(*) * 100 >= (
			SELECT MAX(daily_count) * 90 FROM (
				SELECT COUNT(*) AS daily_count FROM stock_sync
				WHERE adjust='none' AND status='ok' GROUP BY as_of
			)
		)
		ORDER BY as_of DESC LIMIT 1
	`).Scan(&date); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fs.ErrNotExist
		}
		return "", errors.New("cannot read latest unadjusted all-market date")
	}
	if !date.Valid || date.String == "" {
		return "", fs.ErrNotExist
	}
	return date.String, nil
}

func (m *AllMarketDB) LoadBars(ctx context.Context, code, adjust, from, through string) ([]market.Bar, error) {
	if m == nil || m.db == nil {
		return nil, fs.ErrNotExist
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT trade_date, open_micros, close_micros, high_micros, low_micros, volume_raw
		FROM daily_bar WHERE code=? AND adjust=? AND trade_date>=? AND trade_date<=? ORDER BY trade_date
	`, code, adjust, from, through)
	if err != nil {
		return nil, errors.New("cannot read all-market daily history")
	}
	defer rows.Close()
	out := []market.Bar{}
	for rows.Next() {
		var date string
		var open, close, high, low int64
		var volume float64
		if err := rows.Scan(&date, &open, &close, &high, &low, &volume); err != nil {
			return nil, errors.New("cannot decode all-market daily history")
		}
		out = append(out, market.Bar{Date: date, Open: float64(open) / 1_000_000, Close: float64(close) / 1_000_000,
			High: float64(high) / 1_000_000, Low: float64(low) / 1_000_000, Volume: volume})
	}
	return out, rows.Err()
}

// LoadAdjustedHistories reconstructs the persisted daily histories used by
// independent rule-based reports. It only includes stocks whose QFQ snapshot
// for the requested as-of date completed successfully.
func (m *AllMarketDB) LoadAdjustedHistories(ctx context.Context, asOf, from string) ([]config.Stock, []market.History, error) {
	return m.loadHistories(ctx, asOf, from, "qfq")
}

// LoadUnadjustedHistories returns raw-price histories from the same frozen
// all-market snapshot. The trapped diagnostic uses these prices so historical
// cost levels are not distorted by the adjusted analysis series.
func (m *AllMarketDB) LoadUnadjustedHistories(ctx context.Context, asOf, from string) ([]config.Stock, []market.History, error) {
	return m.loadHistories(ctx, asOf, from, "none")
}

func (m *AllMarketDB) loadHistories(ctx context.Context, asOf, from, adjust string) ([]config.Stock, []market.History, error) {
	if m == nil || m.db == nil {
		return nil, nil, fs.ErrNotExist
	}
	if _, err := time.Parse("2006-01-02", asOf); err != nil {
		return nil, nil, errors.New("invalid all-market as-of date")
	}
	if _, err := time.Parse("2006-01-02", from); err != nil || from > asOf {
		return nil, nil, errors.New("invalid all-market history start")
	}
	if adjust != "none" && adjust != "qfq" {
		return nil, nil, errors.New("invalid all-market history adjustment")
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT st.code, st.name, st.listed_from, ss.source, ss.data_field, ss.fetched_at,
			d.trade_date, d.open_micros, d.close_micros, d.high_micros, d.low_micros, d.volume_raw
		FROM stock st
		JOIN stock_sync ss ON ss.code=st.code AND ss.adjust=? AND ss.as_of=? AND ss.status='ok'
		JOIN daily_bar d ON d.code=st.code AND d.adjust=? AND d.trade_date>=? AND d.trade_date<=?
		ORDER BY st.code, d.trade_date
	`, adjust, asOf, adjust, from, asOf)
	if err != nil {
		return nil, nil, errors.New("cannot read all-market histories")
	}
	defer rows.Close()
	type stockState struct {
		stock     config.Stock
		source    string
		dataField string
		fetchedAt time.Time
		bars      []market.Bar
	}
	states := map[string]*stockState{}
	order := []string{}
	for rows.Next() {
		var code, name, listedFrom, source, dataField, fetched, date string
		var open, close, high, low int64
		var volume float64
		if err := rows.Scan(&code, &name, &listedFrom, &source, &dataField, &fetched,
			&date, &open, &close, &high, &low, &volume); err != nil {
			return nil, nil, errors.New("cannot decode all-market histories")
		}
		state := states[code]
		if state == nil {
			fetchedAt, err := time.Parse(time.RFC3339Nano, fetched)
			if err != nil {
				return nil, nil, errors.New("invalid all-market history timestamp")
			}
			state = &stockState{stock: config.Stock{Code: code, Name: name, ListedFrom: listedFrom}, source: source, dataField: dataField, fetchedAt: fetchedAt}
			states[code] = state
			order = append(order, code)
		}
		state.bars = append(state.bars, market.Bar{Date: date, Open: float64(open) / 1_000_000, Close: float64(close) / 1_000_000,
			High: float64(high) / 1_000_000, Low: float64(low) / 1_000_000, Volume: volume})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, errors.New("cannot read all-market histories")
	}
	stocks := make([]config.Stock, 0, len(order))
	histories := make([]market.History, 0, len(order))
	for _, code := range order {
		state := states[code]
		stocks = append(stocks, state.stock)
		histories = append(histories, market.History{Version: 1, Code: code, Name: state.stock.Name,
			Source: state.source, DataField: state.dataField, Adjust: adjust, VolumeUnit: "provider_raw",
			AsOf: asOf, FetchedAt: state.fetchedAt, Bars: state.bars})
	}
	return stocks, histories, nil
}

func (m *AllMarketDB) SaveRecommendation(ctx context.Context, artifact RecommendationArtifact) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(artifact.Market) == "" || strings.TrimSpace(artifact.ID) == "" || len(artifact.Payload) == 0 || len(artifact.Payload) > maxRecommendationBytes {
		return errors.New("invalid recommendation artifact")
	}
	for _, date := range []string{artifact.AsOf, artifact.Target} {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return errors.New("invalid recommendation artifact date")
		}
	}
	if artifact.BuiltAt.IsZero() {
		return errors.New("recommendation artifact requires built_at")
	}
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO recommendation(market, as_of, id, target, built_at, payload)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(market, as_of) DO UPDATE SET id=excluded.id, target=excluded.target,
			built_at=excluded.built_at, payload=excluded.payload
	`, artifact.Market, artifact.AsOf, artifact.ID, artifact.Target, artifact.BuiltAt.UTC().Format(time.RFC3339Nano), artifact.Payload)
	if err != nil {
		return errors.New("cannot save recommendation artifact")
	}
	return nil
}

func (m *AllMarketDB) LatestRecommendation(ctx context.Context, marketName string) (RecommendationArtifact, error) {
	var out RecommendationArtifact
	if m == nil || m.db == nil {
		return out, fs.ErrNotExist
	}
	var built string
	err := m.db.QueryRowContext(ctx, `
		SELECT market, as_of, id, target, built_at, payload
		FROM recommendation WHERE market=? ORDER BY as_of DESC LIMIT 1
	`, marketName).Scan(&out.Market, &out.AsOf, &out.ID, &out.Target, &built, &out.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return out, fs.ErrNotExist
	}
	if err != nil {
		return out, errors.New("cannot read recommendation artifact")
	}
	out.BuiltAt, _ = time.Parse(time.RFC3339Nano, built)
	return out, nil
}

func (m *AllMarketDB) StartRun(ctx context.Context, run AllMarketRun) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	if _, err := m.db.ExecContext(ctx, `
		UPDATE sync_run SET status='failed', finished_at=?, error_kind='stale' WHERE status='running'
	`, run.StartedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return errors.New("cannot clean stale all-market sync runs")
	}
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO sync_run(id, as_of, started_at, finished_at, status, total, succeeded, partial, failed, skipped, error_kind)
		VALUES(?, ?, ?, ?, 'running', ?, 0, 0, 0, 0, '')
	`, run.ID, run.AsOf, run.StartedAt.UTC().Format(time.RFC3339Nano), run.FinishedAt.UTC().Format(time.RFC3339Nano), run.Total)
	if err != nil {
		return errors.New("cannot start all-market sync run")
	}
	return nil
}

func (m *AllMarketDB) FinishRun(ctx context.Context, run AllMarketRun) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	_, err := m.db.ExecContext(ctx, `
		UPDATE sync_run SET finished_at=?, status=?, total=?, succeeded=?, partial=?, failed=?, skipped=?, error_kind=? WHERE id=?
	`, run.FinishedAt.UTC().Format(time.RFC3339Nano), run.Status, run.Total, run.Succeeded, run.Partial, run.Failed, run.Skipped, run.ErrorKind, run.ID)
	if err != nil {
		return errors.New("cannot finish all-market sync run")
	}
	return nil
}

// providerVolumeShares keeps the full-market store's share counts aligned with
// the production daily_bar contract while preserving provider_raw volume.
func providerVolumeShares(source, code string, raw float64) float64 {
	return market.ProviderVolumeShares(source, code, raw)
}
