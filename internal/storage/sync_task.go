package storage

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"stock-watcher/internal/selection"
	"time"
)

type SyncTask struct {
	ID          string    `json:"id"`
	AsOf        string    `json:"as_of"`
	Fingerprint string    `json:"-"`
	Phase       string    `json:"phase"`
	Status      string    `json:"status"`
	Completed   int       `json:"completed"`
	Total       int       `json:"total"`
	StartedAt   time.Time `json:"started_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ErrorKind   string    `json:"error_kind,omitempty"`
}

func (m *MarketDB) BeginSyncTask(ctx context.Context, task SyncTask) (SyncTask, map[string]SyncItem, error) {
	if err := m.checkWriter(ctx); err != nil {
		return task, nil, err
	}
	prior, err := m.readTask(ctx, `WHERE as_of=? AND fingerprint=? AND status!='completed' ORDER BY rowid DESC LIMIT 1`, task.AsOf, task.Fingerprint)
	if err == nil {
		task = prior
		task.Status = "running"
		task.ErrorKind = ""
	} else if !errors.Is(err, fs.ErrNotExist) {
		return task, nil, err
	}
	task.UpdatedAt = time.Now()
	if err := m.SaveSyncTask(ctx, task); err != nil {
		return task, nil, err
	}
	items := map[string]SyncItem{}
	rows, err := m.db.QueryContext(ctx, `SELECT code,digest,payload FROM sync_checkpoint WHERE task_id=?`, task.ID)
	if err != nil {
		return task, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code, id string
		var data []byte
		var item SyncItem
		if err := rows.Scan(&code, &id, &data); err != nil {
			return task, nil, err
		}
		if err := decodeSnapshot(id, data, &item, 16<<20); err != nil {
			return task, nil, err
		}
		if item.Stock.Code != code {
			return task, nil, errors.New("checkpoint stock mismatch")
		}
		items[code] = item
	}
	return task, items, rows.Err()
}
func (m *MarketDB) SaveSyncTask(ctx context.Context, task SyncTask) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	_, err := m.db.ExecContext(ctx, `INSERT INTO sync_task(id,as_of,fingerprint,phase,status,completed,total,started_at,updated_at,error_kind) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET phase=excluded.phase,status=excluded.status,completed=excluded.completed,updated_at=excluded.updated_at,error_kind=excluded.error_kind`, task.ID, task.AsOf, task.Fingerprint, task.Phase, task.Status, task.Completed, task.Total, task.StartedAt.UTC().Format(time.RFC3339Nano), task.UpdatedAt.UTC().Format(time.RFC3339Nano), task.ErrorKind)
	return err
}
func (m *MarketDB) CheckpointStock(ctx context.Context, task SyncTask, item SyncItem) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	id, payload, err := encodeSnapshot(item)
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx, `INSERT INTO sync_checkpoint(task_id,code,digest,payload) VALUES(?,?,?,?) ON CONFLICT(task_id,code) DO UPDATE SET digest=excluded.digest,payload=excluded.payload`, task.ID, item.Stock.Code, id, payload)
	return err
}
func (m *MarketDB) LatestSyncTask(ctx context.Context) (SyncTask, error) {
	return m.readTask(ctx, `ORDER BY julianday(updated_at) DESC,rowid DESC LIMIT 1`)
}
func (m *MarketDB) readTask(ctx context.Context, suffix string, args ...any) (SyncTask, error) {
	var task SyncTask
	var started, updated string
	if m == nil || m.db == nil {
		return task, fs.ErrNotExist
	}
	err := m.db.QueryRowContext(ctx, `SELECT id,as_of,fingerprint,phase,status,completed,total,started_at,updated_at,error_kind FROM sync_task `+suffix, args...).Scan(&task.ID, &task.AsOf, &task.Fingerprint, &task.Phase, &task.Status, &task.Completed, &task.Total, &started, &updated, &task.ErrorKind)
	if errors.Is(err, sql.ErrNoRows) {
		return task, fs.ErrNotExist
	}
	if err != nil {
		return task, err
	}
	task.StartedAt, err = time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return task, err
	}
	task.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return task, err
}
func (m *MarketDB) RecordRunningSync(ctx context.Context, run SyncRun) error {
	if err := m.checkWriter(ctx); err != nil {
		return err
	}
	_, err := m.db.ExecContext(ctx, `INSERT INTO sync_run(id,as_of,started_at,finished_at,status,coverage_pct,error_kind) VALUES(?,?,?,?,'running',0,'collecting') ON CONFLICT(id) DO UPDATE SET status='running',finished_at=excluded.finished_at,error_kind='collecting'`, run.ID, run.AsOf, run.StartedAt.UTC().Format(time.RFC3339Nano), run.FinishedAt.UTC().Format(time.RFC3339Nano))
	return err
}

// CommitCollectedData publishes validated data independently of algorithm success.
func (m *MarketDB) CommitCollectedData(ctx context.Context, run SyncRun, items []SyncItem, adjust string) error {
	run.Status = "running"
	run.ErrorKind = "analyzing"
	return m.commitMarket(ctx, run, items, adjust, nil)
}

// CommitAnalysis changes only the report, preserving the already published data.
func (m *MarketDB) CommitAnalysis(ctx context.Context, run SyncRun, items []SyncItem, report selection.Report) error {
	return m.commitMarketMode(ctx, run, items, report.Adjust, &report, true)
}

// checkWriter is required at every task write, including heartbeat and failure
// recording: a previously opened database does not prove a mount is still valid.
func (m *MarketDB) checkWriter(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m == nil || !m.writable {
		return errors.New("market database is read-only")
	}
	return (&Store{root: m.root, requireCIFS: m.requireCIFS}).Check()
}
