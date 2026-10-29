package storage

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	_ "modernc.org/sqlite"
)

const contextDBName = "context.db"

// contextSchemaVersion stays 1 because the minute, thirty-minute and Korean
// caches are additive: readers tolerate missing tables and daily context
// remains usable.
const contextSchemaVersion = 1

// ContextDB stores index and sector-board snapshots. It is intentionally
// independent from MarketDB so a board-sync failure cannot block stock reports.
type ContextDB struct {
	db          *sql.DB
	root        string
	writable    bool
	requireCIFS bool
}

type ContextInstrument struct {
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Category    string    `json:"category"`
	Source      string    `json:"source"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastBarDate string    `json:"last_bar_date,omitempty"`
}

type ContextSnapshot struct {
	Instrument ContextInstrument
	Bars       []ContextBar
}

type ContextBar struct {
	Code   string  `json:"code"`
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	Close  float64 `json:"close"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Volume float64 `json:"volume_raw"`
	Amount float64 `json:"amount"`
}

type ContextSyncStatus struct {
	AsOf       string    `json:"as_of"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Status     string    `json:"status"`
	Total      int       `json:"total"`
	Succeeded  int       `json:"succeeded"`
	Failed     int       `json:"failed"`
	ErrorKind  string    `json:"error_kind,omitempty"`
}

func OpenContext(root string, requireCIFS bool) (*ContextDB, error) {
	return openContext(root, requireCIFS, true, false)
}

func OpenContextView(root string, requireCIFS bool) (*ContextDB, error) {
	return openContext(root, requireCIFS, false, false)
}

func InitContext(root string, requireCIFS bool) (*ContextDB, error) {
	return openContext(root, requireCIFS, true, true)
}

func openContext(root string, requireCIFS bool, writable, create bool) (*ContextDB, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	check := &Store{root: root, requireCIFS: requireCIFS}
	if err := check.Check(); err != nil {
		return nil, err
	}
	path := filepath.Join(root, contextDBName)
	if err := validateContextPath(path); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, errors.New("cannot inspect context database")
		}
		if !create {
			return nil, fs.ErrNotExist
		}
	}
	mode := "ro"
	if writable {
		mode = "rw"
		if create {
			mode = "rwc"
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode="+mode)
	if err != nil {
		return nil, errors.New("cannot open context database")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	c := &ContextDB{db: db, root: root, writable: writable, requireCIFS: requireCIFS}
	if err := c.configure(create); err != nil {
		db.Close()
		return nil, err
	}
	return c, nil
}

func validateContextPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot inspect context database")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("context database must be a regular file")
	}
	return nil
}

func (c *ContextDB) configure(create bool) error {
	if c.writable {
		for _, statement := range []string{
			"PRAGMA journal_mode=DELETE",
			"PRAGMA synchronous=FULL",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=5000",
		} {
			if _, err := c.db.Exec(statement); err != nil {
				return errors.New("cannot configure context database")
			}
		}
		if err := c.initSchema(create); err != nil {
			return err
		}
	} else {
		for _, statement := range []string{
			"PRAGMA query_only=ON",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=5000",
		} {
			if _, err := c.db.Exec(statement); err != nil {
				return errors.New("cannot configure context database view")
			}
		}
	}
	var version int
	if err := c.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != contextSchemaVersion {
		return errors.New("unsupported context database version")
	}
	return nil
}

func (c *ContextDB) initSchema(create bool) error {
	var version int
	if err := c.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 0 && version != contextSchemaVersion {
		return errors.New("unsupported context database version")
	}
	if version == 0 && !create {
		return errors.New("context database is not initialized; run stock-watcher init")
	}
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
		CREATE TABLE IF NOT EXISTS context_instrument (
			code TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			kind TEXT NOT NULL CHECK (kind IN ('index', 'board')),
			category TEXT NOT NULL,
			source TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS context_bar (
			code TEXT NOT NULL REFERENCES context_instrument(code) ON DELETE CASCADE,
			trade_date TEXT NOT NULL,
			open REAL NOT NULL CHECK (open > 0),
			close REAL NOT NULL CHECK (close > 0),
			high REAL NOT NULL CHECK (high >= open AND high >= close),
			low REAL NOT NULL CHECK (low > 0 AND low <= open AND low <= close),
			volume_raw REAL NOT NULL CHECK (volume_raw >= 0),
			amount REAL NOT NULL DEFAULT 0 CHECK (amount >= 0),
			PRIMARY KEY (code, trade_date)
		);
		CREATE INDEX IF NOT EXISTS context_bar_date_idx ON context_bar(trade_date);
		CREATE TABLE IF NOT EXISTS context_minute (
			code TEXT NOT NULL REFERENCES context_instrument(code) ON DELETE CASCADE,
			end_unix INTEGER NOT NULL,
			open REAL NOT NULL CHECK (open > 0),
			high REAL NOT NULL CHECK (high >= open),
			low REAL NOT NULL CHECK (low > 0),
			close REAL NOT NULL CHECK (close > 0),
			volume_raw REAL NOT NULL CHECK (volume_raw >= 0),
			PRIMARY KEY (code, end_unix)
		);
		CREATE INDEX IF NOT EXISTS context_minute_date_idx ON context_minute(end_unix);
		CREATE TABLE IF NOT EXISTS context_minute_30 (
			code TEXT NOT NULL REFERENCES context_instrument(code) ON DELETE CASCADE,
			end_unix INTEGER NOT NULL,
			open REAL NOT NULL CHECK (open > 0),
			high REAL NOT NULL CHECK (high >= open),
			low REAL NOT NULL CHECK (low > 0),
			close REAL NOT NULL CHECK (close > 0),
			volume_raw REAL NOT NULL CHECK (volume_raw >= 0),
			PRIMARY KEY (code, end_unix)
		);
		CREATE INDEX IF NOT EXISTS context_minute_30_date_idx ON context_minute_30(end_unix);
		CREATE TABLE IF NOT EXISTS context_korea (
			code TEXT NOT NULL CHECK (code IN ('KOSPI', 'KOSDAQ')),
			trade_date TEXT NOT NULL,
			open REAL NOT NULL CHECK (open > 0),
			high REAL NOT NULL CHECK (high >= open),
			low REAL NOT NULL CHECK (low > 0),
			close REAL NOT NULL CHECK (close > 0),
			volume_raw REAL NOT NULL CHECK (volume_raw >= 0),
			PRIMARY KEY (code, trade_date)
		);
		CREATE INDEX IF NOT EXISTS context_korea_date_idx ON context_korea(trade_date);
		CREATE TABLE IF NOT EXISTS context_sync (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			as_of TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('running', 'complete', 'partial', 'failed')),
			total INTEGER NOT NULL CHECK (total >= 0),
			succeeded INTEGER NOT NULL CHECK (succeeded >= 0),
			failed INTEGER NOT NULL CHECK (failed >= 0),
			error_kind TEXT NOT NULL DEFAULT ''
		);
		PRAGMA user_version=1;`)
	if err != nil {
		return errors.New("cannot initialize context database")
	}
	return tx.Commit()
}

func (c *ContextDB) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

func (c *ContextDB) Path() string {
	if c == nil {
		return ""
	}
	return filepath.Join(c.root, contextDBName)
}

func (c *ContextDB) HasDatabase() bool {
	return c != nil && c.db != nil
}

func (c *ContextDB) SaveInstrument(ctx context.Context, instrument ContextInstrument, bars []ContextBar) error {
	return c.SaveInstruments(ctx, []ContextSnapshot{{Instrument: instrument, Bars: bars}})
}

// SaveInstruments writes a batch in one SQLite transaction. A full board sync
// creates hundreds of small instruments; batching avoids one fsync per board on
// the CIFS-backed production database.
func (c *ContextDB) SaveInstruments(ctx context.Context, snapshots []ContextSnapshot) error {
	if c == nil || !c.writable {
		return errors.New("context database is read-only")
	}
	if len(snapshots) == 0 {
		return nil
	}
	if err := (&Store{root: c.root, requireCIFS: c.requireCIFS}).Check(); err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin context snapshot transaction")
	}
	defer tx.Rollback()
	barStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO context_bar(code,trade_date,open,close,high,low,volume_raw,amount) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(code,trade_date) DO UPDATE SET open=excluded.open,close=excluded.close,high=excluded.high,low=excluded.low,volume_raw=excluded.volume_raw,amount=excluded.amount`)
	if err != nil {
		return errors.New("cannot prepare context bars")
	}
	defer barStmt.Close()
	for _, snapshot := range snapshots {
		instrument := snapshot.Instrument
		if instrument.Code == "" || instrument.Name == "" || instrument.Kind == "" || instrument.Source == "" {
			return errors.New("invalid context instrument")
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO context_instrument(code,name,kind,category,source,updated_at) VALUES(?,?,?,?,?,?)
			ON CONFLICT(code) DO UPDATE SET name=excluded.name,kind=excluded.kind,category=excluded.category,source=excluded.source,updated_at=excluded.updated_at`,
			instrument.Code, instrument.Name, instrument.Kind, instrument.Category, instrument.Source, instrument.UpdatedAt.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return errors.New("cannot save context instrument")
		}
		for _, bar := range snapshot.Bars {
			if bar.Code != instrument.Code {
				return errors.New("context bar code mismatch")
			}
			if _, err := barStmt.ExecContext(ctx, bar.Code, bar.Date, bar.Open, bar.Close, bar.High, bar.Low, bar.Volume, bar.Amount); err != nil {
				return errors.New("cannot save context bars")
			}
		}
	}
	return tx.Commit()
}

func (c *ContextDB) LastBarDates(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	if c == nil || c.db == nil {
		return out, nil
	}
	rows, err := c.db.QueryContext(ctx, `SELECT code, MAX(trade_date) FROM context_bar GROUP BY code`)
	if err != nil {
		return nil, errors.New("cannot read context bar dates")
	}
	defer rows.Close()
	for rows.Next() {
		var code, date string
		if err := rows.Scan(&code, &date); err != nil {
			return nil, errors.New("cannot read context bar date")
		}
		out[code] = date
	}
	return out, rows.Err()
}

func (c *ContextDB) LoadInstruments(ctx context.Context) ([]ContextInstrument, error) {
	if c == nil || c.db == nil {
		return nil, fs.ErrNotExist
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT i.code,i.name,i.kind,i.category,i.source,i.updated_at,COALESCE(MAX(b.trade_date),'')
		FROM context_instrument i LEFT JOIN context_bar b ON b.code=i.code
		GROUP BY i.code,i.name,i.kind,i.category,i.source,i.updated_at
		ORDER BY i.kind,i.category,i.name,i.code`)
	if err != nil {
		return nil, errors.New("cannot read context instruments")
	}
	defer rows.Close()
	var out []ContextInstrument
	for rows.Next() {
		var item ContextInstrument
		var updated string
		if err := rows.Scan(&item.Code, &item.Name, &item.Kind, &item.Category, &item.Source, &updated, &item.LastBarDate); err != nil {
			return nil, errors.New("cannot read context instrument")
		}
		item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *ContextDB) LoadRecentBars(ctx context.Context, asOf string, limit int) (map[string][]ContextBar, error) {
	if c == nil || c.db == nil {
		return nil, fs.ErrNotExist
	}
	if limit < 1 || limit > 120 {
		return nil, errors.New("invalid context bar limit")
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT code,trade_date,open,close,high,low,volume_raw,amount FROM (
			SELECT code,trade_date,open,close,high,low,volume_raw,amount,
				ROW_NUMBER() OVER (PARTITION BY code ORDER BY trade_date DESC) AS row_number
			FROM context_bar WHERE trade_date <= ?
		) WHERE row_number <= ? ORDER BY code,trade_date`, asOf, limit)
	if err != nil {
		return nil, errors.New("cannot read context bars")
	}
	defer rows.Close()
	out := map[string][]ContextBar{}
	for rows.Next() {
		var bar ContextBar
		if err := rows.Scan(&bar.Code, &bar.Date, &bar.Open, &bar.Close, &bar.High, &bar.Low, &bar.Volume, &bar.Amount); err != nil {
			return nil, errors.New("cannot read context bar")
		}
		out[bar.Code] = append(out[bar.Code], bar)
	}
	return out, rows.Err()
}

// LoadBarsThrough returns the complete stored history for the requested
// instruments through a trading date. It is used by read-only research models
// that need more than the dashboard's recent-window limit.
func (c *ContextDB) LoadBarsThrough(ctx context.Context, codes []string, through string) (map[string][]ContextBar, error) {
	if c == nil || c.db == nil {
		return nil, fs.ErrNotExist
	}
	if len(codes) == 0 || len(codes) > 64 {
		return nil, errors.New("invalid context bar code count")
	}
	if parsed, err := time.Parse("2006-01-02", through); err != nil || parsed.Format("2006-01-02") != through {
		return nil, errors.New("invalid context bar through date")
	}
	seen := make(map[string]bool, len(codes))
	placeholders := make([]string, 0, len(codes))
	args := make([]any, 0, len(codes)+1)
	for _, code := range codes {
		if code == "" || seen[code] {
			return nil, errors.New("duplicate or empty context bar code")
		}
		seen[code] = true
		placeholders = append(placeholders, "?")
		args = append(args, code)
	}
	args = append(args, through)
	rows, err := c.db.QueryContext(ctx, `
		SELECT code,trade_date,open,close,high,low,volume_raw,amount
		FROM context_bar
		WHERE code IN (`+strings.Join(placeholders, ",")+`) AND trade_date <= ?
		ORDER BY code,trade_date`, args...)
	if err != nil {
		return nil, errors.New("cannot read context bar history")
	}
	defer rows.Close()
	out := map[string][]ContextBar{}
	for rows.Next() {
		var bar ContextBar
		if err := rows.Scan(&bar.Code, &bar.Date, &bar.Open, &bar.Close, &bar.High, &bar.Low, &bar.Volume, &bar.Amount); err != nil {
			return nil, errors.New("cannot read context bar history row")
		}
		out[bar.Code] = append(out[bar.Code], bar)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot read context bar history")
	}
	return out, nil
}

// SaveMinutes upserts one index's native five-minute series. Existing bars
// outside the supplied window are retained; the daily context tables remain
// independent so a minute backfill cannot change the dashboard's daily
// snapshot or recommendation inputs.
func (c *ContextDB) SaveMinutes(ctx context.Context, code string, bars []market.MinuteBar) error {
	if c == nil || !c.writable {
		return errors.New("context database is read-only")
	}
	if !config.ValidIndexCode(code) || len(bars) == 0 {
		return errors.New("invalid context minute request")
	}
	previous := time.Time{}
	for _, bar := range bars {
		if !bar.Final || !bar.End.After(previous) || bar.Open <= 0 || bar.Close <= 0 || bar.Low <= 0 || bar.High < bar.Open || bar.High < bar.Close || bar.Low > bar.Open || bar.Low > bar.Close {
			return errors.New("invalid context minute bar")
		}
		if _, ok := market.MinuteSession(bar.End, 5); !ok {
			return errors.New("invalid context minute session")
		}
		previous = bar.End
	}
	if err := (&Store{root: c.root, requireCIFS: c.requireCIFS}).Check(); err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin context minute transaction")
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO context_minute(code,end_unix,open,high,low,close,volume_raw) VALUES(?,?,?,?,?,?,?) ON CONFLICT(code,end_unix) DO UPDATE SET open=excluded.open,high=excluded.high,low=excluded.low,close=excluded.close,volume_raw=excluded.volume_raw`)
	if err != nil {
		return errors.New("cannot prepare context minutes")
	}
	defer stmt.Close()
	for _, bar := range bars {
		if _, err := stmt.ExecContext(ctx, code, bar.End.Unix(), bar.Open, bar.High, bar.Low, bar.Close, bar.Volume); err != nil {
			return errors.New("cannot save context minute")
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit context minutes")
	}
	return nil
}

// LoadMinutesThrough returns native five-minute index history through a trading
// date. Missing minute tables are treated as an empty cache so older context
// databases keep serving the daily signal.
func (c *ContextDB) LoadMinutesThrough(ctx context.Context, codes []string, through string) (map[string][]market.MinuteBar, error) {
	if c == nil || c.db == nil {
		return nil, fs.ErrNotExist
	}
	if len(codes) == 0 || len(codes) > 64 {
		return nil, errors.New("invalid context minute code count")
	}
	parsed, err := time.ParseInLocation("2006-01-02", through, market.Shanghai)
	if err != nil || parsed.Format("2006-01-02") != through {
		return nil, errors.New("invalid context minute through date")
	}
	var exists int
	if err := c.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='context_minute'`).Scan(&exists); err != nil {
		return nil, errors.New("cannot inspect context minute table")
	}
	if exists == 0 {
		return map[string][]market.MinuteBar{}, nil
	}
	seen := make(map[string]bool, len(codes))
	placeholders := make([]string, 0, len(codes))
	args := make([]any, 0, len(codes)+1)
	for _, code := range codes {
		if !config.ValidIndexCode(code) || seen[code] {
			return nil, errors.New("duplicate or invalid context minute code")
		}
		seen[code] = true
		placeholders = append(placeholders, "?")
		args = append(args, code)
	}
	args = append(args, parsed.AddDate(0, 0, 1).Unix())
	rows, err := c.db.QueryContext(ctx, `
		SELECT code,end_unix,open,high,low,close,volume_raw
		FROM context_minute
		WHERE code IN (`+strings.Join(placeholders, ",")+`) AND end_unix < ?
		ORDER BY code,end_unix`, args...)
	if err != nil {
		return nil, errors.New("cannot read context minute history")
	}
	defer rows.Close()
	out := map[string][]market.MinuteBar{}
	for rows.Next() {
		var bar market.MinuteBar
		var code string
		var end int64
		if err := rows.Scan(&code, &end, &bar.Open, &bar.High, &bar.Low, &bar.Close, &bar.Volume); err != nil {
			return nil, errors.New("cannot read context minute history row")
		}
		bar.End = time.Unix(end, 0).In(market.Shanghai)
		bar.Final = true
		out[code] = append(out[code], bar)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot read context minute history")
	}
	return out, nil
}

// SaveSessionMinutes upserts one index's native thirty-minute series. It is a
// separate table from the five-minute cache because the same end timestamp can
// belong to both granularities.
func (c *ContextDB) SaveSessionMinutes(ctx context.Context, code string, bars []market.MinuteBar) error {
	if c == nil || !c.writable {
		return errors.New("context database is read-only")
	}
	if !config.ValidIndexCode(code) || len(bars) == 0 {
		return errors.New("invalid context session minute request")
	}
	previous := time.Time{}
	for _, bar := range bars {
		if !bar.Final || !bar.End.After(previous) || bar.Open <= 0 || bar.Close <= 0 || bar.Low <= 0 || bar.High < bar.Open || bar.High < bar.Close || bar.Low > bar.Open || bar.Low > bar.Close {
			return errors.New("invalid context session minute bar")
		}
		if _, ok := market.MinuteSession(bar.End, 30); !ok {
			return errors.New("invalid context session minute timestamp")
		}
		previous = bar.End
	}
	if err := (&Store{root: c.root, requireCIFS: c.requireCIFS}).Check(); err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin context session minute transaction")
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO context_minute_30(code,end_unix,open,high,low,close,volume_raw) VALUES(?,?,?,?,?,?,?) ON CONFLICT(code,end_unix) DO UPDATE SET open=excluded.open,high=excluded.high,low=excluded.low,close=excluded.close,volume_raw=excluded.volume_raw`)
	if err != nil {
		return errors.New("cannot prepare context session minutes")
	}
	defer stmt.Close()
	for _, bar := range bars {
		if _, err := stmt.ExecContext(ctx, code, bar.End.Unix(), bar.Open, bar.High, bar.Low, bar.Close, bar.Volume); err != nil {
			return errors.New("cannot save context session minute")
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit context session minutes")
	}
	return nil
}

// LoadSessionMinutesThrough returns native thirty-minute index history through
// a trading date. Missing tables are treated as an empty optional cache.
func (c *ContextDB) LoadSessionMinutesThrough(ctx context.Context, codes []string, through string) (map[string][]market.MinuteBar, error) {
	return c.loadMinuteTable(ctx, "context_minute_30", codes, through)
}

// SaveKoreaBars upserts daily KOSPI or KOSDAQ bars. The series is independent
// from the Chinese context instruments so a Korean provider outage cannot
// corrupt or block the domestic index snapshot.
func (c *ContextDB) SaveKoreaBars(ctx context.Context, code string, bars []market.KoreaBar) error {
	if c == nil || !c.writable {
		return errors.New("context database is read-only")
	}
	if !market.ValidKoreaCode(code) || len(bars) == 0 || len(bars) > market.KoreaHistoryLimit {
		return errors.New("invalid context korea request")
	}
	previous := ""
	for _, bar := range bars {
		if _, err := time.Parse("2006-01-02", bar.Date); err != nil || bar.Date <= previous || bar.Open <= 0 || bar.Close <= 0 || bar.Low <= 0 || bar.High < bar.Open || bar.High < bar.Close || bar.Low > bar.Open || bar.Low > bar.Close || bar.Volume < 0 {
			return errors.New("invalid context korea bar")
		}
		previous = bar.Date
	}
	if err := (&Store{root: c.root, requireCIFS: c.requireCIFS}).Check(); err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin context korea transaction")
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO context_korea(code,trade_date,open,high,low,close,volume_raw) VALUES(?,?,?,?,?,?,?) ON CONFLICT(code,trade_date) DO UPDATE SET open=excluded.open,high=excluded.high,low=excluded.low,close=excluded.close,volume_raw=excluded.volume_raw`)
	if err != nil {
		return errors.New("cannot prepare context korea bars")
	}
	defer stmt.Close()
	for _, bar := range bars {
		if _, err := stmt.ExecContext(ctx, code, bar.Date, bar.Open, bar.High, bar.Low, bar.Close, bar.Volume); err != nil {
			return errors.New("cannot save context korea bar")
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit context korea bars")
	}
	return nil
}

// LoadKoreaBarsThrough returns stored KOSPI/KOSDAQ daily history through a
// date. Missing tables or codes are treated as an empty optional cache.
func (c *ContextDB) LoadKoreaBarsThrough(ctx context.Context, codes []string, through string) (map[string][]market.KoreaBar, error) {
	if c == nil || c.db == nil {
		return nil, fs.ErrNotExist
	}
	if len(codes) == 0 || len(codes) > 8 {
		return nil, errors.New("invalid korea code count")
	}
	if parsed, err := time.Parse("2006-01-02", through); err != nil || parsed.Format("2006-01-02") != through {
		return nil, errors.New("invalid korea through date")
	}
	var exists int
	if err := c.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='context_korea'`).Scan(&exists); err != nil {
		return nil, errors.New("cannot inspect korea table")
	}
	if exists == 0 {
		return map[string][]market.KoreaBar{}, nil
	}
	seen := make(map[string]bool, len(codes))
	placeholders := make([]string, 0, len(codes))
	args := make([]any, 0, len(codes)+1)
	for _, code := range codes {
		if !market.ValidKoreaCode(code) || seen[code] {
			return nil, errors.New("duplicate or invalid korea code")
		}
		seen[code] = true
		placeholders = append(placeholders, "?")
		args = append(args, code)
	}
	args = append(args, through)
	rows, err := c.db.QueryContext(ctx, `
		SELECT code,trade_date,open,high,low,close,volume_raw
		FROM context_korea
		WHERE code IN (`+strings.Join(placeholders, ",")+`) AND trade_date <= ?
		ORDER BY code,trade_date`, args...)
	if err != nil {
		return nil, errors.New("cannot read korea history")
	}
	defer rows.Close()
	out := map[string][]market.KoreaBar{}
	for rows.Next() {
		var bar market.KoreaBar
		var code string
		if err := rows.Scan(&code, &bar.Date, &bar.Open, &bar.High, &bar.Low, &bar.Close, &bar.Volume); err != nil {
			return nil, errors.New("cannot read korea history row")
		}
		out[code] = append(out[code], bar)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot read korea history")
	}
	return out, nil
}

func (c *ContextDB) loadMinuteTable(ctx context.Context, table string, codes []string, through string) (map[string][]market.MinuteBar, error) {
	if c == nil || c.db == nil {
		return nil, fs.ErrNotExist
	}
	if table != "context_minute" && table != "context_minute_30" {
		return nil, errors.New("invalid context minute table")
	}
	if len(codes) == 0 || len(codes) > 64 {
		return nil, errors.New("invalid context minute code count")
	}
	parsed, err := time.ParseInLocation("2006-01-02", through, market.Shanghai)
	if err != nil || parsed.Format("2006-01-02") != through {
		return nil, errors.New("invalid context minute through date")
	}
	var exists int
	if err := c.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
		return nil, errors.New("cannot inspect context minute table")
	}
	if exists == 0 {
		return map[string][]market.MinuteBar{}, nil
	}
	seen := make(map[string]bool, len(codes))
	placeholders := make([]string, 0, len(codes))
	args := make([]any, 0, len(codes)+1)
	for _, code := range codes {
		if !config.ValidIndexCode(code) || seen[code] {
			return nil, errors.New("duplicate or invalid context minute code")
		}
		seen[code] = true
		placeholders = append(placeholders, "?")
		args = append(args, code)
	}
	args = append(args, parsed.AddDate(0, 0, 1).Unix())
	rows, err := c.db.QueryContext(ctx, `
		SELECT code,end_unix,open,high,low,close,volume_raw
		FROM `+table+`
		WHERE code IN (`+strings.Join(placeholders, ",")+`) AND end_unix < ?
		ORDER BY code,end_unix`, args...)
	if err != nil {
		return nil, errors.New("cannot read context minute history")
	}
	defer rows.Close()
	out := map[string][]market.MinuteBar{}
	for rows.Next() {
		var bar market.MinuteBar
		var code string
		var end int64
		if err := rows.Scan(&code, &end, &bar.Open, &bar.High, &bar.Low, &bar.Close, &bar.Volume); err != nil {
			return nil, errors.New("cannot read context minute history row")
		}
		bar.End = time.Unix(end, 0).In(market.Shanghai)
		bar.Final = true
		out[code] = append(out[code], bar)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot read context minute history")
	}
	return out, nil
}

func (c *ContextDB) SaveSyncStatus(ctx context.Context, status ContextSyncStatus) error {
	if c == nil || !c.writable {
		return errors.New("context database is read-only")
	}
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO context_sync(id,as_of,started_at,finished_at,status,total,succeeded,failed,error_kind)
		VALUES(1,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET as_of=excluded.as_of,started_at=excluded.started_at,finished_at=excluded.finished_at,status=excluded.status,total=excluded.total,succeeded=excluded.succeeded,failed=excluded.failed,error_kind=excluded.error_kind`,
		status.AsOf, status.StartedAt.UTC().Format(time.RFC3339Nano), status.FinishedAt.UTC().Format(time.RFC3339Nano), status.Status, status.Total, status.Succeeded, status.Failed, status.ErrorKind)
	if err != nil {
		return errors.New("cannot save context sync status")
	}
	return nil
}

func (c *ContextDB) LoadSyncStatus(ctx context.Context) (ContextSyncStatus, error) {
	var status ContextSyncStatus
	if c == nil || c.db == nil {
		return status, fs.ErrNotExist
	}
	var started, finished string
	err := c.db.QueryRowContext(ctx, `SELECT as_of,started_at,finished_at,status,total,succeeded,failed,error_kind FROM context_sync WHERE id=1`).Scan(
		&status.AsOf, &started, &finished, &status.Status, &status.Total, &status.Succeeded, &status.Failed, &status.ErrorKind)
	if errors.Is(err, sql.ErrNoRows) {
		return status, fs.ErrNotExist
	}
	if err != nil {
		return status, errors.New("cannot read context sync status")
	}
	status.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	status.FinishedAt, _ = time.Parse(time.RFC3339Nano, finished)
	return status, nil
}
