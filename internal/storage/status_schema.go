package storage

import (
	"database/sql"
	"errors"
	"strings"
)

// initStatusSchema adds the per-session provenance tables and the cached
// exchange calendar. The migration is monotonic: existing rows keep their
// data and new columns get conservative defaults until a backfill runs.
func (m *MarketDB) initStatusSchema() error {
	tx, err := m.db.Begin()
	if err != nil {
		return errors.New("cannot start status schema migration")
	}
	defer tx.Rollback()

	for _, column := range []struct {
		table      string
		name       string
		definition string
	}{
		{"stock_sync", "analyzable", "INTEGER NOT NULL DEFAULT 0 CHECK (analyzable IN (0, 1))"},
		{"stock_sync", "sessions", "INTEGER NOT NULL DEFAULT 0 CHECK (sessions >= 0)"},
		{"stock_sync", "suspended", "INTEGER NOT NULL DEFAULT 0 CHECK (suspended >= 0)"},
		{"stock_sync", "gaps", "INTEGER NOT NULL DEFAULT 0 CHECK (gaps >= 0)"},
		{"sync_run", "analyzable_pct", "REAL NOT NULL DEFAULT 0 CHECK (analyzable_pct >= 0 AND analyzable_pct <= 100)"},
	} {
		if err := ensureColumn(tx, column.table, column.name, column.definition); err != nil {
			return err
		}
	}

	_, err = tx.Exec(`
		CREATE TABLE IF NOT EXISTS session_status (
			code TEXT NOT NULL REFERENCES stock(code),
			trade_date TEXT NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('traded', 'suspended', 'unknown')),
			source TEXT NOT NULL,
			fetched_at TEXT NOT NULL,
			detail TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (code, trade_date)
		);
		CREATE TABLE IF NOT EXISTS status_sync (
			code TEXT NOT NULL REFERENCES stock(code),
			range_start TEXT NOT NULL,
			range_end TEXT NOT NULL,
			source TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('complete', 'partial', 'failed')),
			fetched_at TEXT NOT NULL,
			error_kind TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (code, range_start, range_end)
		);
		CREATE TABLE IF NOT EXISTS market_calendar (
			trade_date TEXT PRIMARY KEY,
			source TEXT NOT NULL,
			fetched_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS calendar_sync (
			range_start TEXT NOT NULL,
			range_end TEXT NOT NULL,
			source TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('complete', 'partial', 'failed')),
			fetched_at TEXT NOT NULL,
			error_kind TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (range_start, range_end)
		);
		PRAGMA user_version=5;
	`)
	if err != nil {
		return errors.New("cannot create status schema")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit status schema")
	}
	return nil
}

func ensureColumn(tx *sql.Tx, table, name, definition string) error {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return errors.New("cannot inspect table columns")
	}
	found := false
	for rows.Next() {
		var cid int
		var column, kind string
		var notNull, pk int
		var fallback sql.NullString
		if err := rows.Scan(&cid, &column, &kind, &notNull, &fallback, &pk); err != nil {
			rows.Close()
			return errors.New("cannot inspect table columns")
		}
		if strings.EqualFold(column, name) {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return errors.New("cannot inspect table columns")
	}
	rows.Close()
	if found {
		return nil
	}
	if _, err := tx.Exec("ALTER TABLE " + table + " ADD COLUMN " + name + " " + definition); err != nil {
		return errors.New("cannot add status schema column")
	}
	return nil
}
