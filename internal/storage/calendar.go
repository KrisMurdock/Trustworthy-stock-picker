package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"

	_ "modernc.org/sqlite"
)

const calendarDBName = "calendar.db"
const calendarSchemaVersion = 1

// CalendarDB owns the independent trading-calendar cache in
// <data_dir>/calendar.db. It never opens or extends market.db.
type CalendarDB struct {
	db          *sql.DB
	root        string
	writable    bool
	requireCIFS bool
}

// OpenCalendar opens the writable calendar cache and creates it when absent.
func OpenCalendar(root string, requireCIFS bool) (*CalendarDB, error) {
	return openCalendar(root, requireCIFS, true, true)
}

// OpenCalendarView opens a read-only view. A missing cache returns a usable
// empty view so callers can distinguish missing coverage from storage failure.
func OpenCalendarView(root string, requireCIFS bool) (*CalendarDB, error) {
	return openCalendar(root, requireCIFS, false, false)
}

func openCalendar(root string, requireCIFS bool, writable, create bool) (*CalendarDB, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	check := &Store{root: root, requireCIFS: requireCIFS}
	if err := check.Check(); err != nil {
		return nil, err
	}
	path := filepath.Join(root, calendarDBName)
	if err := validateMarketPath(path, create); err != nil {
		return nil, err
	}
	if !writable {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return &CalendarDB{root: root}, nil
		} else if err != nil {
			return nil, errors.New("cannot inspect calendar database")
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
		return nil, errors.New("cannot open calendar database")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	c := &CalendarDB{db: db, root: root, writable: writable, requireCIFS: requireCIFS}
	if err := c.configure(); err != nil {
		db.Close()
		return nil, err
	}
	return c, nil
}

func (c *CalendarDB) configure() error {
	if c.writable {
		for _, statement := range []string{
			"PRAGMA journal_mode=DELETE",
			"PRAGMA synchronous=FULL",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=5000",
		} {
			if _, err := c.db.Exec(statement); err != nil {
				return errors.New("cannot configure calendar database")
			}
		}
		if err := c.initSchema(); err != nil {
			return err
		}
	} else {
		for _, statement := range []string{
			"PRAGMA query_only=ON",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=5000",
		} {
			if _, err := c.db.Exec(statement); err != nil {
				return errors.New("cannot configure calendar database view")
			}
		}
	}
	var version int
	if err := c.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != calendarSchemaVersion {
		return errors.New("unsupported calendar database version")
	}
	if !c.writable {
		return c.verifySchema()
	}
	var result string
	if err := c.db.QueryRow("PRAGMA quick_check").Scan(&result); err != nil || result != "ok" {
		return errors.New("calendar database quick_check failed")
	}
	return nil
}

func (c *CalendarDB) initSchema() error {
	var version int
	if err := c.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return errors.New("cannot read calendar database version")
	}
	if version != 0 && version != calendarSchemaVersion {
		return errors.New("unsupported calendar database version")
	}
	tx, err := c.db.Begin()
	if err != nil {
		return errors.New("cannot initialize calendar database")
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
		CREATE TABLE IF NOT EXISTS calendar_history (
			digest TEXT PRIMARY KEY,
			source TEXT NOT NULL,
			valid_from TEXT NOT NULL,
			valid_through TEXT NOT NULL,
			fetched_at TEXT NOT NULL,
			payload BLOB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS calendar_history_window
			ON calendar_history(valid_from, valid_through);
		PRAGMA user_version=1;
	`)
	if err != nil {
		return errors.New("cannot initialize calendar database")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot initialize calendar database")
	}
	return nil
}

func (c *CalendarDB) verifySchema() error {
	var version, tables int
	if err := c.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != calendarSchemaVersion {
		return errors.New("unsupported calendar database version")
	}
	err := c.db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='calendar_history'
	`).Scan(&tables)
	if err != nil || tables != 1 {
		return errors.New("calendar database schema is incomplete")
	}
	return nil
}

func (c *CalendarDB) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

func (c *CalendarDB) Path() string {
	if c == nil {
		return ""
	}
	return filepath.Join(c.root, calendarDBName)
}

func (c *CalendarDB) HasDatabase() bool {
	return c != nil && c.db != nil
}

// Save appends an immutable calendar window in one transaction. Re-saving the
// same digest is idempotent.
func (c *CalendarDB) Save(ctx context.Context, history calendar.History) error {
	if c == nil || c.db == nil || !c.writable {
		return errors.New("calendar database is read-only")
	}
	if err := (&Store{root: c.root, requireCIFS: c.requireCIFS}).Check(); err != nil {
		return err
	}
	if err := history.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(history)
	if err != nil {
		return errors.New("cannot encode trading calendar")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin calendar transaction")
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO calendar_history(digest, source, valid_from, valid_through, fetched_at, payload)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(digest) DO NOTHING
	`, history.Digest, history.Source, history.From, history.Through,
		history.FetchedAt.Format(time.RFC3339Nano), payload)
	if err != nil {
		return errors.New("cannot save trading calendar")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit trading calendar")
	}
	return nil
}

// Load returns cached windows overlapping the requested range without merging
// their metadata.
func (c *CalendarDB) Load(ctx context.Context, from, through string) (calendar.Dataset, error) {
	if err := calendar.ValidateRange(from, through); err != nil {
		return nil, err
	}
	if c == nil || c.db == nil {
		return nil, fs.ErrNotExist
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT digest, source, valid_from, valid_through, fetched_at, payload
		FROM calendar_history
		WHERE valid_from<=? AND valid_through>=?
		ORDER BY valid_from, fetched_at
	`, through, from)
	if err != nil {
		return nil, errors.New("cannot read trading calendars")
	}
	defer rows.Close()
	dataset := calendar.Dataset{}
	for rows.Next() {
		var digest, source, validFrom, validThrough, fetchedAt string
		var payload []byte
		if err := rows.Scan(&digest, &source, &validFrom, &validThrough, &fetchedAt, &payload); err != nil {
			return nil, errors.New("cannot read trading calendars")
		}
		var history calendar.History
		if err := json.Unmarshal(payload, &history); err != nil {
			return nil, errors.New("invalid stored trading calendar")
		}
		if err := history.Validate(); err != nil {
			return nil, errors.New("invalid stored trading calendar")
		}
		storedAt, err := time.Parse(time.RFC3339Nano, fetchedAt)
		if err != nil || history.Digest != digest || history.Source != source ||
			history.From != validFrom || history.Through != validThrough || !history.FetchedAt.Equal(storedAt) {
			return nil, errors.New("stored trading calendar metadata mismatch")
		}
		dataset = append(dataset, history)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot read trading calendars")
	}
	if len(dataset) == 0 {
		return nil, fs.ErrNotExist
	}
	return dataset, nil
}

// Coverage reports missing intervals without treating partial coverage as a
// storage error, so production Analyze can explicitly emit partial_calendar.
func (c *CalendarDB) Coverage(ctx context.Context, from, through string) (calendar.Coverage, error) {
	if err := calendar.ValidateRange(from, through); err != nil {
		return calendar.Coverage{}, err
	}
	dataset, err := c.Load(ctx, from, through)
	if errors.Is(err, fs.ErrNotExist) {
		dataset = calendar.Dataset{}
	} else if err != nil {
		return calendar.Coverage{}, err
	}
	return dataset.Coverage(from, through)
}

// Freeze returns one canonical history or a *calendar.CoverageError when the
// cache cannot prove complete coverage.
func (c *CalendarDB) Freeze(ctx context.Context, from, through string) (calendar.History, error) {
	dataset, err := c.Load(ctx, from, through)
	if errors.Is(err, fs.ErrNotExist) {
		dataset = calendar.Dataset{}
	} else if err != nil {
		return calendar.History{}, err
	}
	return dataset.Freeze(from, through)
}
