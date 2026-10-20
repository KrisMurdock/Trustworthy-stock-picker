package storage

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const contextDBName = "context.db"
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
