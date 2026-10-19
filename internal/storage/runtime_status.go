package storage

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"stock-watcher/internal/config"
	"time"
)

type RuntimeData struct {
	Available   int                    `json:"available"`
	ReportReady bool                   `json:"report_ready"`
	Minutes     map[string]MinuteState `json:"minutes"`
	Task        *SyncTask              `json:"task,omitempty"`
}

// RuntimeStatus uses indexed metadata only; no history decoding or integrity scan.
func (m *MarketDB) RuntimeStatus(ctx context.Context, stocks []config.Stock, asof, adjust string) (RuntimeData, error) {
	out := RuntimeData{Minutes: map[string]MinuteState{}}
	if m == nil || m.db == nil {
		return out, fs.ErrNotExist
	}
	if err := (&Store{root: m.root, requireCIFS: m.requireCIFS}).Check(); err != nil {
		return out, err
	}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	allowed := map[string]bool{}
	for _, s := range stocks {
		allowed[s.Code] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT code FROM stock_sync WHERE as_of=? AND adjust=? AND status='ok'`, asof, adjust)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			rows.Close()
			return out, err
		}
		if allowed[code] {
			out.Available++
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	var state string
	err = tx.QueryRowContext(ctx, `SELECT status FROM sync_run WHERE as_of=? ORDER BY julianday(finished_at) DESC,rowid DESC LIMIT 1`, asof).Scan(&state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	var reports int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM selection_run WHERE as_of=?`, asof).Scan(&reports); err != nil {
		return out, err
	}
	out.ReportReady = state == "published" && reports > 0
	rows, err = tx.QueryContext(ctx, `SELECT code,target_unix,attempt_at,fetched_at,status,reason,calibrated_date FROM intraday_sync`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var code, attempt, fetched string
		var target int64
		var s MinuteState
		if err := rows.Scan(&code, &target, &attempt, &fetched, &s.Status, &s.Reason, &s.CalibratedDate); err != nil {
			rows.Close()
			return out, err
		}
		if !allowed[code] {
			continue
		}
		s.Target = time.Unix(target, 0)
		s.AttemptAt, err = time.Parse(time.RFC3339Nano, attempt)
		if err != nil {
			rows.Close()
			return out, err
		}
		if fetched != "" {
			at, e := time.Parse(time.RFC3339Nano, fetched)
			if e != nil {
				rows.Close()
				return out, e
			}
			s.FetchedAt = &at
		}
		out.Minutes[code] = s
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	task, err := m.LatestSyncTask(ctx)
	if err == nil {
		out.Task = &task
	} else if !errors.Is(err, fs.ErrNotExist) {
		return out, err
	}
	return out, nil
}

func (m *MarketDB) TouchSyncTask(ctx context.Context, id, at string) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	_, err := m.db.ExecContext(ctx, `UPDATE sync_task SET updated_at=? WHERE id=? AND status='running'`, at, id)
	return err
}
