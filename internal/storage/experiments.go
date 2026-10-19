package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"
)

// ExperimentDB is a separate archive: experiments never acquire the market
// writer lock or change market.db. SQLite readers observe atomic run revisions.
type ExperimentDB struct {
	db    *sql.DB
	store *Store
	lock  *os.File
}

func OpenExperiments(ctx context.Context, root string, requireCIFS, writable bool) (*ExperimentDB, error) {
	view, err := OpenView(root, requireCIFS)
	if err != nil {
		return nil, err
	}
	x := &ExperimentDB{store: view}
	path, err := view.path("experiments.db", false)
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(path); errors.Is(err, fs.ErrNotExist) && !writable {
		return x, nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("experiment archive unavailable")
	}
	fail := func(err error) (*ExperimentDB, error) { x.Close(); return nil, err }
	if writable {
		lockPath, err := view.path(".experiments.lock", false)
		if err != nil {
			return nil, err
		}
		x.lock, err = os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, errors.New("experiment lock unavailable")
		}
		if err = syscall.Flock(int(x.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return fail(errors.New("another experiment is running"))
		}
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, err := view.path("experiments.db"+suffix, false); err != nil {
			return fail(err)
		}
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	q := url.Values{}
	if writable {
		q.Set("mode", "rwc")
	} else {
		q.Set("mode", "ro")
	}
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	x.db, err = sql.Open("sqlite", u.String())
	if err != nil {
		return fail(errors.New("cannot open experiment archive"))
	}
	x.db.SetMaxOpenConns(1)
	var version int
	if err = x.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fail(errors.New("cannot read experiment schema"))
	}
	if version != 0 && version != 1 {
		return fail(errors.New("unsupported experiment schema"))
	}
	if writable {
		for _, statement := range []string{"PRAGMA journal_mode=DELETE", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", `CREATE TABLE IF NOT EXISTS experiment_snapshot (id TEXT PRIMARY KEY, payload BLOB NOT NULL)`, `CREATE TABLE IF NOT EXISTS experiment_run (id TEXT PRIMARY KEY, snapshot_id TEXT NOT NULL REFERENCES experiment_snapshot(id), created_at TEXT NOT NULL, status TEXT NOT NULL, summary BLOB NOT NULL, payload BLOB NOT NULL)`, "PRAGMA user_version=1"} {
			if _, err = x.db.ExecContext(ctx, statement); err != nil {
				return fail(errors.New("cannot initialize experiment archive"))
			}
		}
		if err = x.recoverInterrupted(ctx); err != nil {
			return fail(err)
		}
	} else if version != 1 {
		return fail(errors.New("unsupported experiment schema"))
	}
	return x, nil
}
func (x *ExperimentDB) Close() error {
	var err error
	if x.db != nil {
		err = x.db.Close()
	}
	if x.lock != nil {
		err = errors.Join(err, syscall.Flock(int(x.lock.Fd()), syscall.LOCK_UN), x.lock.Close())
		x.lock = nil
	}
	return err
}
func (x *ExperimentDB) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return x.store.Check()
}
func experimentJSON(value any) ([]byte, error) {
	b, err := json.Marshal(value)
	if err != nil || len(b) > 32<<20 {
		return nil, errors.New("invalid or oversized experiment document")
	}
	return b, nil
}
func (x *ExperimentDB) Start(ctx context.Context, snapshot experiment.Snapshot, record experiment.Record) error {
	if x.lock == nil {
		return errors.New("experiment archive is read-only")
	}
	if err := x.check(ctx); err != nil {
		return err
	}
	if !experiment.ValidID(record.ID) || record.Version != 1 || record.Status != "running" || record.ResultSnapshot != nil || snapshot.ID != record.Data.ID || snapshot.Version != 1 {
		return errors.New("invalid experiment record")
	}
	if record.Stage != "" && record.Stage != "development" && record.Stage != "holdout" {
		return errors.New("invalid experiment stage")
	}
	var parentRecord *experiment.Record
	if record.ParentID != "" {
		parent, err := x.Get(ctx, record.ParentID)
		if err != nil {
			return err
		}
		parentRecord = &parent
		baselineID := experiment.BaselineFor(record, parent)
		if parent.Status != "completed" || parent.Data.ID != snapshot.ID || record.BaselineID != baselineID {
			return errors.New("invalid experiment parent")
		}
	}
	if record.Stage == "holdout" {
		if err := validateHoldoutRecord(record, parentRecord); err != nil {
			return err
		}
	}
	if err := experiment.ValidateSnapshot(snapshot); err != nil {
		return err
	}
	data, err := experimentJSON(snapshot)
	if err != nil {
		return err
	}
	payload, err := experimentJSON(record)
	if err != nil {
		return err
	}
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if record.Stage == "holdout" {
		if record.Holdout == nil {
			return errors.New("invalid holdout record")
		}
		var consumed int
		if err = tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM experiment_run
			WHERE json_extract(payload, '$.data.id')=? AND json_extract(payload, '$.stage')='holdout'
		`, record.Data.ID).Scan(&consumed); err != nil {
			return errors.New("cannot inspect holdout consumption")
		}
		if record.Holdout.Contaminated != (consumed > 0) {
			return errors.New("holdout contamination state is stale")
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO experiment_snapshot(id,payload) VALUES(?,?) ON CONFLICT(id) DO NOTHING", snapshot.ID, data); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO experiment_run(id,snapshot_id,created_at,status,summary,payload) VALUES(?,?,?,?,?,?)", record.ID, snapshot.ID, record.CreatedAt.Format(time.RFC3339Nano), record.Status, payload, payload); err != nil {
		return errors.New("experiment already exists or cannot be saved")
	}
	return tx.Commit()
}

// validateHoldoutRecord keeps holdout lineage a storage invariant rather than a
// caller convention. The child must reuse the parent's frozen rule and consume
// exactly the interval reserved by the parent development run.
func validateHoldoutRecord(record experiment.Record, parent *experiment.Record) error {
	if parent == nil || record.ParentID == "" {
		return errors.New("holdout record requires a parent experiment")
	}
	if parent.Stage == "holdout" {
		return errors.New("a holdout experiment cannot parent another holdout")
	}
	if parent.Status != "completed" || parent.Result == nil {
		return errors.New("holdout parent has no completed result")
	}
	if record.Holdout == nil {
		return errors.New("invalid holdout record")
	}
	reserved := parent.Result.Development
	if reserved.HoldoutFrom == "" || reserved.HoldoutThrough == "" ||
		record.Holdout.From != reserved.HoldoutFrom || record.Holdout.Through != reserved.HoldoutThrough {
		return errors.New("holdout interval differs from the parent reservation")
	}
	if record.Engine != parent.Engine || record.CodeVersion != parent.CodeVersion {
		return errors.New("holdout engine differs from its parent")
	}
	expectedSpec := parent.Spec
	expectedSpec.Range = nil
	if !reflect.DeepEqual(record.Spec, expectedSpec) {
		return errors.New("holdout specification differs from its parent")
	}
	if !reflect.DeepEqual(record.Data, parent.Data) {
		return errors.New("holdout data summary differs from its parent")
	}
	return nil
}

func (x *ExperimentDB) Finish(ctx context.Context, record experiment.Record) error {
	if x.lock == nil {
		return errors.New("experiment archive is read-only")
	}
	if err := x.check(ctx); err != nil {
		return err
	}
	if record.Status != "completed" && record.Status != "failed" && record.Status != "canceled" && record.Status != "interrupted" {
		return errors.New("invalid terminal experiment status")
	}
	old, err := x.Get(ctx, record.ID)
	if err != nil {
		return err
	}
	if old.Status != "running" || old.Data.ID != record.Data.ID || old.ParentID != record.ParentID || old.CodeVersion != record.CodeVersion {
		return errors.New("completed experiment is immutable")
	}
	// Only execution fields can change after the hypothesis has been recorded.
	old.Status, old.Verdict, old.Error, old.FinishedAt, old.Result = record.Status, record.Verdict, record.Error, record.FinishedAt, record.Result
	old.Review = nil
	if old.Status == "completed" {
		if old.Result != nil {
			old.ResultSnapshot = experiment.SnapshotResult(old)
		} else {
			old.ResultSnapshot = record.ResultSnapshot
		}
		if old.ResultSnapshot != nil {
			if err := old.ResultSnapshot.Validate(old); err != nil {
				return err
			}
		}
	}
	payload, err := experimentJSON(old)
	if err != nil {
		return err
	}
	old.Result = nil
	summary, err := experimentJSON(old)
	if err != nil {
		return err
	}
	result, err := x.db.ExecContext(ctx, "UPDATE experiment_run SET status=?,summary=?,payload=? WHERE id=? AND status='running'", old.Status, summary, payload, old.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return errors.New("experiment is no longer running")
	}
	return nil
}
func (x *ExperimentDB) List(ctx context.Context) ([]experiment.Record, error) {
	out := []experiment.Record{}
	if err := x.check(ctx); err != nil {
		return nil, err
	}
	if x.db == nil {
		return out, nil
	}
	rows, err := x.db.QueryContext(ctx, "SELECT summary, json_extract(payload, '$.result.development.research') FROM experiment_run ORDER BY created_at DESC,id DESC LIMIT 200")
	if err != nil {
		return nil, errors.New("cannot list experiments")
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		var statsJSON sql.NullString
		var r experiment.Record
		if err = rows.Scan(&b, &statsJSON); err != nil {
			return nil, err
		}
		if len(b) > 32<<20 || json.Unmarshal(b, &r) != nil || r.Version != 1 || !experiment.ValidID(r.ID) {
			return nil, errors.New("invalid stored experiment")
		}
		if r.Status == "completed" && statsJSON.Valid {
			var research strategy.Research
			if err = json.Unmarshal([]byte(statsJSON.String), &research); err != nil {
				return nil, errors.New("invalid stored experiment statistics")
			}
			stats := research.Development.TopRanked
			if r.Stage == "holdout" {
				stats = research.Holdout.TopRanked
			}
			r.Review = experiment.ReviewStats(stats)
			if r.ResultSnapshot == nil {
				r.Result = &experiment.Results{Development: strategy.DevelopmentResult{Research: research}}
				r.ResultSnapshot = experiment.SnapshotResult(r)
				r.Result = nil
			}
		}
		if r.ResultSnapshot != nil {
			if err := r.ResultSnapshot.Validate(r); err != nil {
				return nil, err
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (x *ExperimentDB) Get(ctx context.Context, id string) (experiment.Record, error) {
	var r experiment.Record
	if !experiment.ValidID(id) {
		return r, fs.ErrNotExist
	}
	if err := x.check(ctx); err != nil {
		return r, err
	}
	if x.db == nil {
		return r, fs.ErrNotExist
	}
	var b []byte
	err := x.db.QueryRowContext(ctx, "SELECT payload FROM experiment_run WHERE id=?", id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fs.ErrNotExist
	}
	if err != nil {
		return r, errors.New("cannot read experiment")
	}
	if len(b) > 32<<20 || json.Unmarshal(b, &r) != nil || r.Version != 1 || r.ID != id {
		return r, errors.New("invalid stored experiment")
	}
	if r.ResultSnapshot == nil {
		r.ResultSnapshot = experiment.SnapshotResult(r)
	} else if err := r.ResultSnapshot.Validate(r); err != nil {
		return r, err
	}
	return r, nil
}
func (x *ExperimentDB) Snapshot(ctx context.Context, id string) (experiment.Snapshot, error) {
	var s experiment.Snapshot
	if err := x.check(ctx); err != nil {
		return s, err
	}
	if x.db == nil {
		return s, fs.ErrNotExist
	}
	var b []byte
	err := x.db.QueryRowContext(ctx, "SELECT payload FROM experiment_snapshot WHERE id=?", id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return s, fs.ErrNotExist
	}
	if err != nil {
		return s, errors.New("cannot read experiment snapshot")
	}
	if len(b) > 32<<20 || json.Unmarshal(b, &s) != nil || s.Version != 1 || s.ID != id {
		return s, errors.New("invalid experiment snapshot")
	}
	if err := experiment.ValidateSnapshot(s); err != nil {
		return s, err
	}
	return s, nil
}

// HoldoutConsumed reports whether any holdout child already used this frozen
// snapshot. A later holdout remains inspectable but can no longer claim an
// independent validation.
func (x *ExperimentDB) HoldoutConsumed(ctx context.Context, dataID string) (bool, error) {
	if err := x.check(ctx); err != nil {
		return false, err
	}
	if x.db == nil {
		return false, nil
	}
	var count int
	err := x.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM experiment_run
		WHERE json_extract(payload, '$.data.id')=? AND json_extract(payload, '$.stage')='holdout'
	`, dataID).Scan(&count)
	if err != nil {
		return false, errors.New("cannot inspect holdout consumption")
	}
	return count > 0, nil
}
func (x *ExperimentDB) recoverInterrupted(ctx context.Context) error {
	rows, err := x.db.QueryContext(ctx, "SELECT id FROM experiment_run WHERE status='running'")
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		r, err := x.Get(ctx, id)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		r.Status, r.Verdict, r.Error, r.FinishedAt = "interrupted", "pending", "前次实验进程退出，未生成完整结果。", &now
		if err = x.Finish(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

// ExperimentView opens short-lived read-only connections, so missing archives
// remain an empty state and the web process never initializes or repairs files.
type ExperimentView struct {
	Root        string
	RequireCIFS bool
}

func (v ExperimentView) List(ctx context.Context) ([]experiment.Record, error) {
	db, err := OpenExperiments(ctx, v.Root, v.RequireCIFS, false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return db.List(ctx)
}
func (v ExperimentView) Get(ctx context.Context, id string) (experiment.Record, error) {
	db, err := OpenExperiments(ctx, v.Root, v.RequireCIFS, false)
	if err != nil {
		return experiment.Record{}, err
	}
	defer db.Close()
	r, err := db.Get(ctx, id)
	if err == nil {
		r.Review = experiment.ReviewRecord(r)
	}
	return r, err
}
func (v ExperimentView) History(ctx context.Context, id, code, date string) (market.History, error) {
	db, err := OpenExperiments(ctx, v.Root, v.RequireCIFS, false)
	if err != nil {
		return market.History{}, err
	}
	defer db.Close()
	r, err := db.Get(ctx, id)
	if err != nil {
		return market.History{}, err
	}
	found := false
	if r.Result != nil {
		for _, t := range r.Result.Development.Trades {
			if t.Code == code && t.Date == date {
				found = true
				break
			}
		}
	}
	if !found {
		return market.History{}, fs.ErrNotExist
	}
	s, err := db.Snapshot(ctx, r.Data.ID)
	if err != nil {
		return market.History{}, err
	}
	for _, h := range s.Raw {
		if h.Code != code {
			continue
		}
		for i, b := range h.Bars {
			if b.Date == date {
				first := max(0, i-40)
				last := min(len(h.Bars), i+s.Parameters.MaxHoldingDays+2)
				h.Bars = h.Bars[first:last]
				return h, nil
			}
		}
	}
	return market.History{}, fs.ErrNotExist
}
