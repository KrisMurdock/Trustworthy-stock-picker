package predictor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	_ "modernc.org/sqlite"
)

type DB struct {
	db   *sql.DB
	path string
}

func OpenDB(dataDir string) (*DB, error) {
	path := filepath.Join(dataDir, "predictions.db")
	handle, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, errors.New("cannot open predictions database")
	}
	handle.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=DELETE",
		"PRAGMA synchronous=FULL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := handle.Exec(pragma); err != nil {
			handle.Close()
			return nil, errors.New("cannot configure predictions database")
		}
	}
	if _, err := handle.Exec(predictionSchema); err != nil {
		handle.Close()
		return nil, errors.New("cannot initialize predictions database")
	}
	if err := migratePredictionSchema(handle); err != nil {
		handle.Close()
		return nil, err
	}
	if err := handle.Ping(); err != nil {
		handle.Close()
		return nil, errors.New("predictions database is unavailable")
	}
	_ = os.Chmod(path, 0600)
	return &DB{db: handle, path: path}, nil
}

func (d *DB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

func (d *DB) Path() string { return d.path }

const predictionSchema = `
PRAGMA user_version=2;
CREATE TABLE IF NOT EXISTS model_run (
	model_id TEXT PRIMARY KEY,
	created_at TEXT NOT NULL,
	data_from TEXT NOT NULL,
	data_to TEXT NOT NULL,
	valid_for TEXT NOT NULL,
	feature_version INTEGER NOT NULL,
	info_json TEXT NOT NULL,
	checksum TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS prediction (
	code TEXT NOT NULL,
	date TEXT NOT NULL,
	input_end_unix INTEGER NOT NULL,
	target_end_unix INTEGER NOT NULL,
	model_id TEXT NOT NULL,
	base_price_micros INTEGER NOT NULL CHECK(base_price_micros>0),
	high_pct REAL NOT NULL,
	low_pct REAL NOT NULL,
	close_pct REAL NOT NULL,
	trend TEXT NOT NULL,
	confidence TEXT NOT NULL,
	trend_score REAL NOT NULL,
	inference_ms REAL NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	PRIMARY KEY(code,target_end_unix,model_id)
);
CREATE INDEX IF NOT EXISTS prediction_date_target ON prediction(date,target_end_unix);
CREATE TABLE IF NOT EXISTS prediction_actual (
	code TEXT NOT NULL,
	target_end_unix INTEGER NOT NULL,
	model_id TEXT NOT NULL,
	high_pct REAL NOT NULL,
	low_pct REAL NOT NULL,
	close_pct REAL NOT NULL,
	status TEXT NOT NULL,
	resolved_at TEXT NOT NULL,
	PRIMARY KEY(code,target_end_unix,model_id)
);
CREATE TABLE IF NOT EXISTS kalman_state (
	code TEXT NOT NULL,
	date TEXT NOT NULL,
	bar_end_unix INTEGER NOT NULL,
	level REAL NOT NULL,
	slope REAL NOT NULL,
	p00 REAL NOT NULL,
	p01 REAL NOT NULL,
	p11 REAL NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(code,date)
);
`

func migratePredictionSchema(handle *sql.DB) error {
	rows, err := handle.Query(`PRAGMA table_info(prediction)`)
	if err != nil {
		return errors.New("cannot inspect predictions database")
	}
	hasInference := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return errors.New("cannot inspect predictions database")
		}
		if name == "inference_ms" {
			hasInference = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return errors.New("cannot inspect predictions database")
	}
	rows.Close()
	if !hasInference {
		if _, err := handle.Exec(`ALTER TABLE prediction ADD COLUMN inference_ms REAL NOT NULL DEFAULT 0`); err != nil {
			return errors.New("cannot migrate predictions database")
		}
	}
	return nil
}

func (d *DB) InsertPrediction(ctx context.Context, p Prediction) (bool, error) {
	if err := p.Validate(); err != nil {
		return false, err
	}
	result, err := d.db.ExecContext(ctx, `
		INSERT INTO prediction(code,date,input_end_unix,target_end_unix,model_id,base_price_micros,
			high_pct,low_pct,close_pct,trend,confidence,trend_score,inference_ms,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(code,target_end_unix,model_id) DO NOTHING`,
		p.Code, p.Date, p.InputEnd.Unix(), p.TargetEnd.Unix(), p.ModelID, int64(mathRound(p.BasePrice*1e6)),
		p.HighPct, p.LowPct, p.ClosePct, p.Trend, p.Confidence, p.TrendScore, p.InferenceMS,
		p.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, errors.New("cannot insert predictor result")
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

func (d *DB) InsertActual(ctx context.Context, a Actual) error {
	if a.Code == "" || a.ModelID == "" || a.TargetEnd.IsZero() || (a.Status != "resolved" && a.Status != "unresolved") {
		return errors.New("invalid predictor actual")
	}
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO prediction_actual(code,target_end_unix,model_id,high_pct,low_pct,close_pct,status,resolved_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(code,target_end_unix,model_id) DO UPDATE SET
			high_pct=excluded.high_pct, low_pct=excluded.low_pct, close_pct=excluded.close_pct,
			status=excluded.status, resolved_at=excluded.resolved_at`,
		a.Code, a.TargetEnd.Unix(), a.ModelID, a.HighPct, a.LowPct, a.ClosePct, a.Status,
		a.ResolvedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return errors.New("cannot record predictor actual")
	}
	return nil
}

func (d *DB) PendingForTarget(ctx context.Context, date string, targetEnd time.Time) ([]Prediction, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT code,date,input_end_unix,target_end_unix,model_id,base_price_micros,
		       high_pct,low_pct,close_pct,trend,confidence,trend_score,inference_ms,created_at
		FROM prediction WHERE date=? AND target_end_unix=? ORDER BY code`, date, targetEnd.Unix())
	if err != nil {
		return nil, errors.New("cannot read predictor results")
	}
	defer rows.Close()
	return scanPredictions(rows)
}

func (d *DB) ForDate(ctx context.Context, date string) ([]Prediction, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT code,date,input_end_unix,target_end_unix,model_id,base_price_micros,
		       high_pct,low_pct,close_pct,trend,confidence,trend_score,inference_ms,created_at
		FROM prediction WHERE date=? ORDER BY target_end_unix,code`, date)
	if err != nil {
		return nil, errors.New("cannot read predictor history")
	}
	defer rows.Close()
	return scanPredictions(rows)
}

func (d *DB) LatestForCodes(ctx context.Context, date string, codes []string) ([]Prediction, error) {
	if len(codes) == 0 {
		return []Prediction{}, nil
	}
	all, err := d.ForDate(ctx, date)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, code := range codes {
		wanted[code] = true
	}
	latest := map[string]Prediction{}
	for _, p := range all {
		if !wanted[p.Code] {
			continue
		}
		if previous, ok := latest[p.Code]; !ok || p.TargetEnd.After(previous.TargetEnd) {
			latest[p.Code] = p
		}
	}
	out := make([]Prediction, 0, len(latest))
	for _, code := range codes {
		if p, ok := latest[code]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func scanPredictions(rows *sql.Rows) ([]Prediction, error) {
	out := []Prediction{}
	for rows.Next() {
		var p Prediction
		var input, target, base int64
		var created string
		if err := rows.Scan(&p.Code, &p.Date, &input, &target, &p.ModelID, &base,
			&p.HighPct, &p.LowPct, &p.ClosePct, &p.Trend, &p.Confidence, &p.TrendScore, &p.InferenceMS, &created); err != nil {
			return nil, errors.New("cannot decode predictor result")
		}
		p.InputEnd = time.Unix(input, 0).In(market.Shanghai)
		p.TargetEnd = time.Unix(target, 0).In(market.Shanghai)
		p.BasePrice = float64(base) / 1e6
		parsed, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, errors.New("invalid predictor result timestamp")
		}
		p.CreatedAt = parsed
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot read predictor results")
	}
	return out, nil
}

// LatencyStats returns count, mean and nearest-rank P95 for per-stock inference.
func (d *DB) LatencyStats(ctx context.Context, modelID string) (LatencyStats, error) {
	if modelID == "" {
		return LatencyStats{}, nil
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT inference_ms FROM prediction
		WHERE model_id=? AND inference_ms>0 ORDER BY inference_ms`, modelID)
	if err != nil {
		return LatencyStats{}, errors.New("cannot read predictor latency")
	}
	defer rows.Close()
	values := []float64{}
	for rows.Next() {
		var value float64
		if err := rows.Scan(&value); err != nil {
			return LatencyStats{}, errors.New("cannot decode predictor latency")
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return LatencyStats{}, errors.New("cannot read predictor latency")
	}
	if len(values) == 0 {
		return LatencyStats{}, nil
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	index := (len(values)*95+99)/100 - 1
	if index < 0 {
		index = 0
	}
	return LatencyStats{Count: len(values), AvgMS: sum / float64(len(values)), P95MS: values[index]}, nil
}

func (d *DB) SaveKalman(ctx context.Context, s KalmanState) error {
	if s.Code == "" || s.Date == "" || s.BarEnd.IsZero() {
		return errors.New("invalid kalman state")
	}
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO kalman_state(code,date,bar_end_unix,level,slope,p00,p01,p11,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(code,date) DO UPDATE SET
			bar_end_unix=excluded.bar_end_unix, level=excluded.level, slope=excluded.slope,
			p00=excluded.p00, p01=excluded.p01, p11=excluded.p11, updated_at=excluded.updated_at`,
		s.Code, s.Date, s.BarEnd.Unix(), s.Level, s.Slope, s.P00, s.P01, s.P11,
		s.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return errors.New("cannot save kalman state")
	}
	return nil
}

func (d *DB) Kalman(ctx context.Context, code, date string) (KalmanState, error) {
	var s KalmanState
	var bar int64
	var updated string
	err := d.db.QueryRowContext(ctx, `
		SELECT code,date,bar_end_unix,level,slope,p00,p01,p11,updated_at
		FROM kalman_state WHERE code=? AND date=?`, code, date).
		Scan(&s.Code, &s.Date, &bar, &s.Level, &s.Slope, &s.P00, &s.P01, &s.P11, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return KalmanState{}, nil
	}
	if err != nil {
		return KalmanState{}, errors.New("cannot read kalman state")
	}
	s.BarEnd = time.Unix(bar, 0).In(market.Shanghai)
	s.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return KalmanState{}, errors.New("invalid kalman timestamp")
	}
	return s, nil
}

func (d *DB) RecordModel(ctx context.Context, info ModelInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return errors.New("cannot encode predictor model info")
	}
	_, err = d.db.ExecContext(ctx, `
		INSERT INTO model_run(model_id,created_at,data_from,data_to,valid_for,feature_version,info_json,checksum)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(model_id) DO UPDATE SET info_json=excluded.info_json`,
		info.ModelID, info.CreatedAt.UTC().Format(time.RFC3339Nano), info.DataFrom, info.DataTo,
		info.ValidFor, info.FeatureVersion, string(data), info.Checksum)
	if err != nil {
		return errors.New("cannot record predictor model")
	}
	return nil
}

func (d *DB) ModelInfo(ctx context.Context, modelID string) (ModelInfo, error) {
	var info ModelInfo
	var raw string
	err := d.db.QueryRowContext(ctx, `SELECT info_json FROM model_run WHERE model_id=?`, modelID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelInfo{}, sql.ErrNoRows
	}
	if err != nil {
		return ModelInfo{}, errors.New("cannot read predictor model")
	}
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		return ModelInfo{}, errors.New("invalid predictor model metadata")
	}
	return info, nil
}

func (d *DB) Counts(ctx context.Context, date string) (predictions, resolved int, err error) {
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM prediction WHERE date=?`, date).Scan(&predictions); err != nil {
		return 0, 0, errors.New("cannot count predictor results")
	}
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM prediction_actual WHERE status='resolved' AND target_end_unix IN (SELECT target_end_unix FROM prediction WHERE date=?)`, date).Scan(&resolved); err != nil {
		return 0, 0, errors.New("cannot count predictor actuals")
	}
	return predictions, resolved, nil
}

func (p Prediction) Validate() error {
	if p.Code == "" || p.Date == "" || p.ModelID == "" || p.InputEnd.IsZero() || p.TargetEnd.IsZero() ||
		!p.InputEnd.Before(p.TargetEnd) || p.BasePrice <= 0 {
		return errors.New("invalid predictor result")
	}
	for _, value := range []float64{p.HighPct, p.LowPct, p.ClosePct, p.TrendScore} {
		if value != value || value > 1e6 || value < -1e6 {
			return errors.New("invalid predictor result value")
		}
	}
	if p.Trend == "" || p.Confidence == "" {
		return errors.New("invalid predictor result state")
	}
	return nil
}

func mathRound(value float64) float64 {
	if value >= 0 {
		return float64(int64(value + 0.5))
	}
	return float64(int64(value - 0.5))
}

func (d *DB) String() string { return fmt.Sprintf("predictions:%s", d.path) }
