package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/dayrange"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"

	_ "modernc.org/sqlite"
)

const dayrangeDBName = "dayrange.db"
const dayrangeSchemaVersion = 1

type DayRangeDB struct {
	db          *sql.DB
	root        string
	writable    bool
	requireCIFS bool
}

func OpenDayRange(root string, requireCIFS bool) (*DayRangeDB, error) {
	return openDayRange(root, requireCIFS, true, false)
}

func OpenDayRangeView(root string, requireCIFS bool) (*DayRangeDB, error) {
	return openDayRange(root, requireCIFS, false, false)
}

func InitDayRange(root string, requireCIFS bool) (*DayRangeDB, error) {
	return openDayRange(root, requireCIFS, true, true)
}

func openDayRange(root string, requireCIFS bool, writable, create bool) (*DayRangeDB, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := (&Store{root: root, requireCIFS: requireCIFS}).Check(); err != nil {
		return nil, err
	}
	path := filepath.Join(root, dayrangeDBName)
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) && !writable && !create {
			return &DayRangeDB{root: root}, nil
		}
		if errors.Is(err, fs.ErrNotExist) && !create {
			return nil, errors.New("dayrange database is not initialized; run stock-watcher init")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, errors.New("cannot inspect dayrange database")
		}
	}
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("dayrange database path must not be a symlink")
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
		return nil, errors.New("cannot open dayrange database")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	d := &DayRangeDB{db: db, root: root, writable: writable, requireCIFS: requireCIFS}
	if err := d.configure(); err != nil {
		db.Close()
		return nil, err
	}
	if create {
		_ = os.Chmod(path, 0600)
	}
	return d, nil
}

func (d *DayRangeDB) configure() error {
	if d == nil || d.db == nil {
		return errors.New("dayrange database unavailable")
	}
	if d.writable {
		for _, pragma := range []string{"PRAGMA journal_mode=DELETE", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
			if _, err := d.db.Exec(pragma); err != nil {
				return errors.New("cannot configure dayrange database")
			}
		}
		if err := d.initSchema(); err != nil {
			return err
		}
	} else {
		for _, pragma := range []string{"PRAGMA query_only=ON", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
			if _, err := d.db.Exec(pragma); err != nil {
				return errors.New("cannot configure dayrange view")
			}
		}
	}
	var version int
	if err := d.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != dayrangeSchemaVersion {
		return errors.New("unsupported dayrange database version")
	}
	return nil
}

func (d *DayRangeDB) initSchema() error {
	var version int
	if err := d.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return errors.New("cannot read dayrange schema version")
	}
	if version != 0 && version != dayrangeSchemaVersion {
		return errors.New("unsupported dayrange database version")
	}
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS dayrange_snapshot (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			code TEXT NOT NULL DEFAULT '',
			target_date TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			payload BLOB NOT NULL
		);
		CREATE TABLE IF NOT EXISTS dayrange_model_run (
			model_id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			status TEXT NOT NULL,
			feature_version INTEGER NOT NULL,
			code_version TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			train_from TEXT NOT NULL DEFAULT '',
			train_to TEXT NOT NULL DEFAULT '',
			calibration_from TEXT NOT NULL DEFAULT '',
			calibration_to TEXT NOT NULL DEFAULT '',
			validation_from TEXT NOT NULL DEFAULT '',
			validation_to TEXT NOT NULL DEFAULT '',
			valid_for TEXT NOT NULL DEFAULT '',
			metrics_json TEXT NOT NULL DEFAULT '[]',
			checksum TEXT NOT NULL DEFAULT '',
			artifact BLOB,
			reason TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS dayrange_model_status ON dayrange_model_run(status, valid_for, created_at);
		CREATE TABLE IF NOT EXISTS dayrange_prediction (
			prediction_id TEXT PRIMARY KEY,
			code TEXT NOT NULL,
			target_date TEXT NOT NULL,
			mode TEXT NOT NULL,
			as_of TEXT NOT NULL,
			input_through TEXT NOT NULL,
			model_id TEXT NOT NULL,
			source TEXT NOT NULL,
			feature_version INTEGER NOT NULL,
			reference_price_micros INTEGER NOT NULL CHECK(reference_price_micros > 0),
			point_high_micros INTEGER NOT NULL CHECK(point_high_micros > 0),
			point_low_micros INTEGER NOT NULL CHECK(point_low_micros > 0),
			payload BLOB NOT NULL,
			input_snapshot_id TEXT NOT NULL REFERENCES dayrange_snapshot(id),
			baseline_model_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE(code,target_date,mode,as_of,model_id)
		);
		CREATE INDEX IF NOT EXISTS dayrange_prediction_date ON dayrange_prediction(target_date, code);
		CREATE TABLE IF NOT EXISTS dayrange_actual (
			code TEXT NOT NULL,
			target_date TEXT NOT NULL,
			actual_high_micros INTEGER,
			actual_low_micros INTEGER,
			source TEXT NOT NULL DEFAULT '',
			observed_through TEXT NOT NULL DEFAULT '',
			resolved_at TEXT NOT NULL,
			status TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(code,target_date)
		);
		CREATE TABLE IF NOT EXISTS dayrange_evaluation (
			evaluation_id TEXT PRIMARY KEY,
			model_id TEXT NOT NULL,
			baseline_id TEXT NOT NULL,
			mode TEXT NOT NULL,
			as_of TEXT NOT NULL,
			validation_from TEXT NOT NULL,
			validation_to TEXT NOT NULL,
			metrics_version INTEGER NOT NULL,
			checksum TEXT NOT NULL,
			payload BLOB NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS dayrange_evaluation_asof ON dayrange_evaluation(as_of, created_at);
		CREATE TABLE IF NOT EXISTS dayrange_audit (
			audit_id TEXT PRIMARY KEY,
			snapshot_id TEXT NOT NULL,
			as_of TEXT NOT NULL,
			payload BLOB NOT NULL,
			created_at TEXT NOT NULL
		);
		PRAGMA user_version=1;
	`)
	if err != nil {
		return errors.New("cannot initialize dayrange database")
	}
	return nil
}

func (d *DayRangeDB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

func (d *DayRangeDB) Path() string {
	if d == nil {
		return ""
	}
	return filepath.Join(d.root, dayrangeDBName)
}

func (d *DayRangeDB) Available() bool { return d != nil && d.db != nil }

func (d *DayRangeDB) checkWriter() error {
	if d == nil || d.db == nil {
		return errors.New("dayrange database unavailable")
	}
	if !d.writable {
		return errors.New("dayrange database is read-only")
	}
	return nil
}

func (d *DayRangeDB) PutSnapshot(ctx context.Context, kind, code, targetDate string, mode dayrange.Mode, createdAt time.Time, value any) (string, error) {
	if err := d.checkWriter(); err != nil {
		return "", err
	}
	id, payload, err := encodeSnapshot(value)
	if err != nil {
		return "", err
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO dayrange_snapshot(id,kind,code,target_date,mode,created_at,payload) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, kind, code, targetDate, string(mode), createdAt.UTC().Format(time.RFC3339Nano), payload)
	if err != nil {
		return "", errors.New("cannot save dayrange snapshot")
	}
	return id, nil
}

func (d *DayRangeDB) LoadSnapshot(ctx context.Context, id string, out any) error {
	if d == nil || d.db == nil {
		return errors.New("dayrange database unavailable")
	}
	var payload []byte
	if err := d.db.QueryRowContext(ctx, `SELECT payload FROM dayrange_snapshot WHERE id=?`, id).Scan(&payload); err != nil {
		return err
	}
	return decodeSnapshot(id, payload, out, 128<<20)
}

func (d *DayRangeDB) SaveModelRun(ctx context.Context, run dayrange.ModelRun) error {
	if err := d.checkWriter(); err != nil {
		return err
	}
	metrics, err := json.Marshal(run.Metrics)
	if err != nil {
		return err
	}
	var artifact []byte
	if len(run.Artifact) > 0 {
		artifact = append([]byte(nil), run.Artifact...)
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO dayrange_model_run(model_id,kind,status,feature_version,code_version,created_at,train_from,train_to,calibration_from,calibration_to,validation_from,validation_to,valid_for,metrics_json,checksum,artifact,reason) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(model_id) DO UPDATE SET kind=excluded.kind,status=excluded.status,feature_version=excluded.feature_version,code_version=excluded.code_version,created_at=excluded.created_at,train_from=excluded.train_from,train_to=excluded.train_to,calibration_from=excluded.calibration_from,calibration_to=excluded.calibration_to,validation_from=excluded.validation_from,validation_to=excluded.validation_to,valid_for=excluded.valid_for,metrics_json=excluded.metrics_json,checksum=excluded.checksum,artifact=excluded.artifact,reason=excluded.reason`, run.ModelID, run.Kind, string(run.Status), run.FeatureVersion, run.CodeVersion, run.CreatedAt.UTC().Format(time.RFC3339Nano), run.TrainFrom, run.TrainTo, run.CalibrationFrom, run.CalibrationTo, run.ValidationFrom, run.ValidationTo, run.ValidFor, string(metrics), run.Checksum, artifact, run.Reason)
	if err != nil {
		return errors.New("cannot save dayrange model run")
	}
	return nil
}

func (d *DayRangeDB) LoadModelRun(ctx context.Context, id string) (dayrange.ModelRun, error) {
	run := dayrange.ModelRun{}
	if d == nil || d.db == nil {
		return run, errors.New("dayrange database unavailable")
	}
	var status, created string
	var metrics []byte
	var artifact []byte
	err := d.db.QueryRowContext(ctx, `SELECT model_id,kind,status,feature_version,code_version,created_at,train_from,train_to,calibration_from,calibration_to,validation_from,validation_to,valid_for,metrics_json,checksum,artifact,reason FROM dayrange_model_run WHERE model_id=?`, id).Scan(
		&run.ModelID, &run.Kind, &status, &run.FeatureVersion, &run.CodeVersion, &created, &run.TrainFrom, &run.TrainTo, &run.CalibrationFrom, &run.CalibrationTo, &run.ValidationFrom, &run.ValidationTo, &run.ValidFor, &metrics, &run.Checksum, &artifact, &run.Reason)
	if err != nil {
		return run, err
	}
	run.Status = dayrange.ModelStatus(status)
	run.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if err := json.Unmarshal(metrics, &run.Metrics); err != nil {
		return run, errors.New("invalid dayrange model metrics")
	}
	if len(artifact) > 0 {
		run.Artifact = artifact
	}
	return run, nil
}

func (d *DayRangeDB) LatestValidatedModel(ctx context.Context, validFor string) (dayrange.ModelRun, error) {
	if d == nil || d.db == nil {
		return dayrange.ModelRun{}, errors.New("dayrange database unavailable")
	}
	var id string
	err := d.db.QueryRowContext(ctx, `SELECT model_id FROM dayrange_model_run WHERE status=? AND kind=? AND valid_for=? ORDER BY created_at DESC LIMIT 1`, string(dayrange.ModelStatusValidated), "gbdt_conformal", validFor).Scan(&id)
	if err != nil {
		return dayrange.ModelRun{}, err
	}
	return d.LoadModelRun(ctx, id)
}

func (d *DayRangeDB) LatestValidatedModelBefore(ctx context.Context, targetDate string, maxAgeDays int) (dayrange.ModelRun, error) {
	if d == nil || d.db == nil {
		return dayrange.ModelRun{}, errors.New("dayrange database unavailable")
	}
	cutoff, err := time.ParseInLocation("2006-01-02", targetDate, market.Shanghai)
	if err != nil {
		return dayrange.ModelRun{}, errors.New("invalid dayrange target date")
	}
	cutoffDate := cutoff.AddDate(0, 0, -maxAgeDays).Format("2006-01-02")
	var id string
	err = d.db.QueryRowContext(ctx, `SELECT model_id FROM dayrange_model_run WHERE status=? AND kind=? AND valid_for<=? AND train_to>=? ORDER BY train_to DESC,created_at DESC LIMIT 1`, string(dayrange.ModelStatusValidated), "gbdt_conformal", targetDate, cutoffDate).Scan(&id)
	if err != nil {
		return dayrange.ModelRun{}, err
	}
	return d.LoadModelRun(ctx, id)
}

func (d *DayRangeDB) LatestModelForValidFor(ctx context.Context, validFor string) (dayrange.ModelRun, error) {
	if d == nil || d.db == nil {
		return dayrange.ModelRun{}, errors.New("dayrange database unavailable")
	}
	var id string
	if err := d.db.QueryRowContext(ctx, `SELECT model_id FROM dayrange_model_run WHERE kind IN ('baseline','gbdt_conformal') AND valid_for=? ORDER BY created_at DESC LIMIT 1`, validFor).Scan(&id); err != nil {
		return dayrange.ModelRun{}, err
	}
	return d.LoadModelRun(ctx, id)
}

// PruneModelRuns keeps the newest candidate generations without deleting a
// model that is still referenced by a saved prediction. Baseline rows are kept
// as the stable fallback record.
func (d *DayRangeDB) PruneModelRuns(ctx context.Context, keep int) error {
	if err := d.checkWriter(); err != nil {
		return err
	}
	if keep < 1 {
		return errors.New("dayrange model retention must be positive")
	}
	_, err := d.db.ExecContext(ctx, `DELETE FROM dayrange_model_run
		WHERE kind='gbdt_conformal'
		  AND model_id NOT IN (SELECT model_id FROM dayrange_prediction)
		  AND model_id NOT IN (
			SELECT model_id FROM dayrange_model_run
			WHERE kind='gbdt_conformal'
			ORDER BY created_at DESC, model_id DESC
			LIMIT ?
		  )`, keep)
	if err != nil {
		return errors.New("cannot prune dayrange model runs")
	}
	return nil
}

func (d *DayRangeDB) SavePrediction(ctx context.Context, p dayrange.Prediction) (bool, error) {
	if err := d.checkWriter(); err != nil {
		return false, err
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return false, err
	}
	if p.PredictionID == "" {
		p.PredictionID = dayrange.PredictionID(p)
	}
	payload, err = json.Marshal(p)
	if err != nil {
		return false, err
	}
	result, err := d.db.ExecContext(ctx, `INSERT INTO dayrange_prediction(prediction_id,code,target_date,mode,as_of,input_through,model_id,source,feature_version,reference_price_micros,point_high_micros,point_low_micros,payload,input_snapshot_id,baseline_model_id,status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(prediction_id) DO NOTHING`,
		p.PredictionID, p.Code, p.TargetDate, string(p.Mode), p.AsOf.UTC().Format(time.RFC3339Nano), p.InputThrough, p.ModelID, p.Source, p.FeatureVersion, dayrangeMicros(p.ReferencePrice), dayrangeMicros(p.PointHigh), dayrangeMicros(p.PointLow), payload, p.InputSnapshotID, p.BaselineModelID, p.Status, p.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, errors.New("cannot save dayrange prediction")
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

func (d *DayRangeDB) LoadPredictions(ctx context.Context, targetDate, code string) ([]dayrange.PredictionRecord, error) {
	if d == nil || d.db == nil {
		return nil, errors.New("dayrange database unavailable")
	}
	query := `SELECT payload FROM dayrange_prediction WHERE target_date=?`
	args := []any{targetDate}
	if code != "" {
		query += ` AND code=?`
		args = append(args, code)
	}
	query += ` ORDER BY code,mode,created_at`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.New("cannot read dayrange predictions")
	}
	defer rows.Close()
	var predictions []dayrange.Prediction
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var p dayrange.Prediction
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, errors.New("invalid dayrange prediction")
		}
		predictions = append(predictions, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	out := make([]dayrange.PredictionRecord, 0, len(predictions))
	for _, p := range predictions {
		actual, err := d.LoadActual(ctx, p.Code, p.TargetDate)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("cannot read dayrange actual")
		}
		out = append(out, dayrange.PredictionRecord{Prediction: p, Actual: actual})
	}
	return out, nil
}

func (d *DayRangeDB) LoadPrediction(ctx context.Context, id string) (dayrange.PredictionRecord, error) {
	var payload []byte
	if err := d.db.QueryRowContext(ctx, `SELECT payload FROM dayrange_prediction WHERE prediction_id=?`, id).Scan(&payload); err != nil {
		return dayrange.PredictionRecord{}, err
	}
	var p dayrange.Prediction
	if err := json.Unmarshal(payload, &p); err != nil {
		return dayrange.PredictionRecord{}, errors.New("invalid dayrange prediction")
	}
	actual, err := d.LoadActual(ctx, p.Code, p.TargetDate)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return dayrange.PredictionRecord{}, errors.New("cannot read dayrange actual")
	}
	return dayrange.PredictionRecord{Prediction: p, Actual: actual}, nil
}

func (d *DayRangeDB) SaveActual(ctx context.Context, actual dayrange.ActualRecord) error {
	if err := d.checkWriter(); err != nil {
		return err
	}
	var high, low any
	if actual.Actual != nil {
		high, low = dayrangeMicros(actual.Actual.High), dayrangeMicros(actual.Actual.Low)
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO dayrange_actual(code,target_date,actual_high_micros,actual_low_micros,source,observed_through,resolved_at,status,reason) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(code,target_date) DO UPDATE SET actual_high_micros=excluded.actual_high_micros,actual_low_micros=excluded.actual_low_micros,source=excluded.source,observed_through=excluded.observed_through,resolved_at=excluded.resolved_at,status=excluded.status,reason=excluded.reason`, actual.Code, actual.TargetDate, high, low, actual.Source, actual.ObservedThrough, actual.ResolvedAt.UTC().Format(time.RFC3339Nano), actual.Status, actual.Reason)
	if err != nil {
		return errors.New("cannot save dayrange actual")
	}
	return nil
}

func (d *DayRangeDB) LoadActual(ctx context.Context, code, targetDate string) (*dayrange.ActualRecord, error) {
	if d == nil || d.db == nil {
		return nil, errors.New("dayrange database unavailable")
	}
	var actual dayrange.ActualRecord
	var high, low sql.NullInt64
	var resolved string
	err := d.db.QueryRowContext(ctx, `SELECT code,target_date,actual_high_micros,actual_low_micros,source,observed_through,resolved_at,status,reason FROM dayrange_actual WHERE code=? AND target_date=?`, code, targetDate).Scan(&actual.Code, &actual.TargetDate, &high, &low, &actual.Source, &actual.ObservedThrough, &resolved, &actual.Status, &actual.Reason)
	if err != nil {
		return nil, err
	}
	actual.ResolvedAt, _ = time.Parse(time.RFC3339Nano, resolved)
	if high.Valid && low.Valid && actual.Status == "resolved" {
		actual.Actual = &dayrange.Extremes{High: float64(high.Int64) / 1e6, Low: float64(low.Int64) / 1e6}
	}
	return &actual, nil
}

func (d *DayRangeDB) SaveEvaluation(ctx context.Context, evaluation dayrange.Evaluation) (string, error) {
	if err := d.checkWriter(); err != nil {
		return "", err
	}
	evaluation.ID = ""
	raw, err := json.Marshal(evaluation)
	if err != nil {
		return "", err
	}
	evaluation.ID = sha256Sum(raw)
	checksum, payload, err := encodeSnapshot(evaluation)
	if err != nil {
		return "", err
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO dayrange_evaluation(evaluation_id,model_id,baseline_id,mode,as_of,validation_from,validation_to,metrics_version,checksum,payload,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(evaluation_id) DO NOTHING`, evaluation.ID, evaluation.ModelID, evaluation.BaselineID, string(evaluation.Mode), evaluation.AsOf, evaluation.ValidationFrom, evaluation.ValidationTo, evaluation.MetricsVersion, checksum, payload, evaluation.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return "", errors.New("cannot save dayrange evaluation")
	}
	return evaluation.ID, nil
}

func (d *DayRangeDB) LoadEvaluation(ctx context.Context, id string) (dayrange.Evaluation, error) {
	var evaluation dayrange.Evaluation
	var checksum string
	var payload []byte
	if err := d.db.QueryRowContext(ctx, `SELECT evaluation_id,checksum,payload FROM dayrange_evaluation WHERE evaluation_id=?`, id).Scan(&evaluation.ID, &checksum, &payload); err != nil {
		return evaluation, err
	}
	if err := decodeSnapshot(checksum, payload, &evaluation, 64<<20); err != nil {
		return evaluation, err
	}
	return evaluation, nil
}

func (d *DayRangeDB) LatestEvaluation(ctx context.Context, asof string) (dayrange.Evaluation, error) {
	var id string
	query := `SELECT evaluation_id FROM dayrange_evaluation`
	args := []any{}
	if asof != "" {
		query += ` WHERE as_of=?`
		args = append(args, asof)
	}
	query += ` ORDER BY created_at DESC LIMIT 1`
	if err := d.db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
		return dayrange.Evaluation{}, err
	}
	return d.LoadEvaluation(ctx, id)
}

func (d *DayRangeDB) SaveAudit(ctx context.Context, snapshotID string, asof string, createdAt time.Time, audit dayrange.Audit) (string, error) {
	if err := d.checkWriter(); err != nil {
		return "", err
	}
	id, payload, err := encodeSnapshot(audit)
	if err != nil {
		return "", err
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO dayrange_audit(audit_id,snapshot_id,as_of,payload,created_at) VALUES(?,?,?,?,?) ON CONFLICT(audit_id) DO NOTHING`, id, snapshotID, asof, payload, createdAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return "", errors.New("cannot save dayrange audit")
	}
	return id, nil
}

func (d *DayRangeDB) LatestAudit(ctx context.Context, asof string) (dayrange.AuditRecord, error) {
	var record dayrange.AuditRecord
	var payload []byte
	query := `SELECT audit_id,snapshot_id,as_of,payload,created_at FROM dayrange_audit`
	args := []any{}
	if asof != "" {
		query += ` WHERE as_of=?`
		args = append(args, asof)
	}
	query += ` ORDER BY created_at DESC LIMIT 1`
	var created string
	if err := d.db.QueryRowContext(ctx, query, args...).Scan(&record.ID, &record.SnapshotID, &record.AsOf, &payload, &created); err != nil {
		return record, err
	}
	record.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if err := decodeSnapshot(record.ID, payload, &record.Audit, 64<<20); err != nil {
		return record, err
	}
	return record, nil
}

func dayrangeMicros(value float64) int64 { return int64(math.Round(value * 1e6)) }

// LoadDayRangeData returns only completed daily bars and session statuses. It
// deliberately does not expose the live forming bar or target-day futures.
func (m *MarketDB) LoadDayRangeData(ctx context.Context, stocks []config.Stock, from, through string) (dayrange.MarketData, error) {
	out := dayrange.MarketData{Series: make([]dayrange.InputSeries, 0, len(stocks)), Statuses: map[string]map[string]market.SessionStatus{}}
	if m == nil || m.db == nil {
		return out, errors.New("market database unavailable")
	}
	series := make(map[string]*dayrange.InputSeries, len(stocks))
	for _, stock := range stocks {
		out.Series = append(out.Series, dayrange.InputSeries{Stock: stock, AdjustedSource: map[string]string{}, RawSource: map[string]string{}})
	}
	for i := range out.Series {
		series[out.Series[i].Stock.Code] = &out.Series[i]
	}
	if len(stocks) == 0 {
		return out, errors.New("dayrange pool is empty")
	}
	args := make([]any, 0, len(stocks)+2)
	for _, stock := range stocks {
		args = append(args, stock.Code)
	}
	args = append(args, from, through)
	rows, err := m.db.QueryContext(ctx, `SELECT code,trade_date,adjust,open_micros,close_micros,high_micros,low_micros,volume_raw,source FROM daily_bar WHERE code IN (`+placeholders(len(stocks))+`) AND adjust IN ('qfq','none') AND trade_date>=? AND trade_date<=? ORDER BY code,adjust,trade_date`, args...)
	if err != nil {
		return out, errors.New("cannot read dayrange daily data")
	}
	for rows.Next() {
		var code, date, adjust, source string
		var open, closeValue, high, low, volume int64
		if err := rows.Scan(&code, &date, &adjust, &open, &closeValue, &high, &low, &volume, &source); err != nil {
			rows.Close()
			return out, errors.New("cannot decode dayrange daily data")
		}
		item := series[code]
		if item == nil {
			continue
		}
		bar := market.Bar{Date: date, Open: float64(open) / 1e6, Close: float64(closeValue) / 1e6, High: float64(high) / 1e6, Low: float64(low) / 1e6, Volume: float64(volume)}
		if adjust == "qfq" {
			item.Adjusted = append(item.Adjusted, bar)
			item.AdjustedSource[date] = source
		} else {
			item.Raw = append(item.Raw, bar)
			item.RawSource[date] = source
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, errors.New("cannot read dayrange daily data")
	}
	rows.Close()
	days, err := m.CalendarDays(ctx, from, through)
	if err != nil {
		return out, errors.New("dayrange calendar unavailable")
	}
	out.TradingDays = days
	statusRows, err := m.db.QueryContext(ctx, `SELECT code,trade_date,state,source,fetched_at,detail FROM session_status WHERE code IN (`+placeholders(len(stocks))+`) AND trade_date>=? AND trade_date<=? ORDER BY code,trade_date`, args...)
	if err != nil {
		return out, errors.New("cannot read dayrange session statuses")
	}
	defer statusRows.Close()
	for statusRows.Next() {
		var status market.SessionStatus
		var state, fetched string
		if err := statusRows.Scan(&status.Code, &status.Date, &state, &status.Source, &fetched, &status.Detail); err != nil {
			return out, errors.New("cannot decode dayrange session status")
		}
		status.State = market.SessionState(state)
		status.FetchedAt, _ = time.Parse(time.RFC3339Nano, fetched)
		if out.Statuses[status.Code] == nil {
			out.Statuses[status.Code] = map[string]market.SessionStatus{}
		}
		out.Statuses[status.Code][status.Date] = status
	}
	return out, statusRows.Err()
}
