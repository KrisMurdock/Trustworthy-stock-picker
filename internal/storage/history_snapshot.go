package storage

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"io"
)

// Snapshots preserve exact upstream values and provenance. daily_bar remains a
// replaceable latest view. Revisions for the same date have separate content IDs.
func (m *MarketDB) initSnapshotSchema() error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
 CREATE TABLE IF NOT EXISTS history_snapshot(id TEXT PRIMARY KEY, payload BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS history_revision(code TEXT NOT NULL,adjust TEXT NOT NULL,as_of TEXT NOT NULL,snapshot_id TEXT NOT NULL REFERENCES history_snapshot(id),PRIMARY KEY(code,adjust,as_of));
 CREATE TABLE IF NOT EXISTS report_input(id TEXT PRIMARY KEY,payload BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS report_archive(id TEXT PRIMARY KEY,report_json BLOB NOT NULL,as_of TEXT NOT NULL DEFAULT '',generated_at TEXT NOT NULL DEFAULT '',schema_version INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS sync_task(id TEXT PRIMARY KEY,as_of TEXT NOT NULL,fingerprint TEXT NOT NULL,phase TEXT NOT NULL,status TEXT NOT NULL,completed INTEGER NOT NULL,total INTEGER NOT NULL,started_at TEXT NOT NULL,updated_at TEXT NOT NULL,error_kind TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS sync_checkpoint(task_id TEXT NOT NULL REFERENCES sync_task(id),code TEXT NOT NULL,digest TEXT NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(task_id,code));
 PRAGMA user_version=4;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func encodeSnapshot(v any) (string, []byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", nil, err
	}
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err = w.Write(raw); err != nil {
		return "", nil, err
	}
	if err = w.Close(); err != nil {
		return "", nil, err
	}
	return sha256Sum(raw), buf.Bytes(), nil
}
func decodeSnapshot(id string, payload []byte, out any, limit int64) error {
	r, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return errors.New("invalid compressed snapshot")
	}
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(raw)) > limit || sha256Sum(raw) != id {
		return errors.New("snapshot integrity check failed")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New("invalid snapshot payload")
	}
	return nil
}
func saveHistorySnapshot(ctx context.Context, tx *sql.Tx, h market.History) error {
	id, payload, err := encodeSnapshot(h)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO history_snapshot(id,payload) VALUES(?,?) ON CONFLICT(id) DO NOTHING`, id, payload); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO history_revision(code,adjust,as_of,snapshot_id) VALUES(?,?,?,?) ON CONFLICT(code,adjust,as_of) DO UPDATE SET snapshot_id=excluded.snapshot_id`, h.Code, h.Adjust, h.AsOf, id)
	return err
}
func loadHistorySnapshot(ctx context.Context, r sqlReader, code, adjust, asof string) (market.History, error) {
	var h market.History
	var id string
	var payload []byte
	err := r.QueryRowContext(ctx, `SELECT s.id,s.payload FROM history_revision r JOIN history_snapshot s ON r.snapshot_id=s.id WHERE r.code=? AND r.adjust=? AND r.as_of=?`, code, adjust, asof).Scan(&id, &payload)
	if err != nil {
		return h, err
	}
	if err = decodeSnapshot(id, payload, &h, 8<<20); err != nil {
		return h, err
	}
	if h.Code != code || h.Adjust != adjust || h.AsOf != asof {
		return h, errors.New("snapshot identity mismatch")
	}
	return h, h.Validate()
}

// LoadReportInputs restores the exact input windows referenced by a report,
// including superseded same-date revisions. Legacy reports have no input ID.
func (m *MarketDB) LoadReportInputs(ctx context.Context, id string) ([]SyncItem, error) {
	var items []SyncItem
	var payload []byte
	if m == nil || m.db == nil {
		return nil, errors.New("market database unavailable")
	}
	if err := m.db.QueryRowContext(ctx, `SELECT payload FROM report_input WHERE id=?`, id).Scan(&payload); err != nil {
		return nil, err
	}
	err := decodeSnapshot(id, payload, &items, 256<<20)
	return items, err
}

func (m *MarketDB) LoadReportRevision(ctx context.Context, id string) (selection.Report, error) {
	var report selection.Report
	var data []byte
	if m == nil || m.db == nil {
		return report, errors.New("market database unavailable")
	}
	if len(id) != 64 {
		return report, errors.New("invalid report revision")
	}
	if err := m.db.QueryRowContext(ctx, `SELECT report_json FROM report_archive WHERE id=?`, id).Scan(&data); err != nil {
		return report, err
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return report, err
	}
	report.Revision = ""
	raw, err := json.Marshal(report)
	if err != nil || sha256Sum(raw) != id {
		return report, errors.New("report revision integrity check failed")
	}
	report.Revision = id
	return report, nil
}
