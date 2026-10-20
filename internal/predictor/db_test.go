package predictor

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const legacyV2PredictionSchema = `
CREATE TABLE model_run (
	model_id TEXT PRIMARY KEY,
	created_at TEXT NOT NULL,
	data_from TEXT NOT NULL,
	data_to TEXT NOT NULL,
	valid_for TEXT NOT NULL,
	feature_version INTEGER NOT NULL,
	info_json TEXT NOT NULL,
	checksum TEXT NOT NULL
);
CREATE TABLE prediction (
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
	created_at TEXT NOT NULL,
	PRIMARY KEY(code,target_end_unix,model_id)
);
CREATE INDEX prediction_date_target ON prediction(date,target_end_unix);
CREATE TABLE prediction_actual (
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
CREATE TABLE kalman_state (
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
PRAGMA user_version=2;
`

func createLegacyV2Database(t *testing.T, dir string, prediction Prediction) {
	t.Helper()
	handle, err := sql.Open("sqlite", filepath.Join(dir, "predictions.db"))
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	handle.SetMaxOpenConns(1)
	transaction, err := handle.Begin()
	if err != nil {
		handle.Close()
		t.Fatalf("begin legacy fixture: %v", err)
	}
	if _, err := transaction.Exec(legacyV2PredictionSchema); err != nil {
		transaction.Rollback()
		handle.Close()
		t.Fatalf("create legacy schema: %v", err)
	}
	createdAt := prediction.CreatedAt.UTC().Format(time.RFC3339Nano)
	if _, err := transaction.Exec(`
		INSERT INTO model_run(model_id,created_at,data_from,data_to,valid_for,feature_version,info_json,checksum)
		VALUES(?,?,?,?,?,?,?,?)`,
		prediction.ModelID, createdAt, "2026-09-01", prediction.Date, "2026-10-21", 1, `{"schema_version":1}`, "legacy-checksum"); err != nil {
		transaction.Rollback()
		handle.Close()
		t.Fatalf("insert legacy model run: %v", err)
	}
	if _, err := transaction.Exec(`
		INSERT INTO prediction(code,date,input_end_unix,target_end_unix,model_id,base_price_micros,
			high_pct,low_pct,close_pct,trend,confidence,trend_score,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		prediction.Code, prediction.Date, prediction.InputEnd.Unix(), prediction.TargetEnd.Unix(),
		prediction.ModelID, int64(prediction.BasePrice*1e6), prediction.HighPct, prediction.LowPct,
		prediction.ClosePct, prediction.Trend, prediction.Confidence, prediction.TrendScore, createdAt); err != nil {
		transaction.Rollback()
		handle.Close()
		t.Fatalf("insert legacy prediction: %v", err)
	}
	if _, err := transaction.Exec(`
		INSERT INTO prediction_actual(code,target_end_unix,model_id,high_pct,low_pct,close_pct,status,resolved_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		prediction.Code, prediction.TargetEnd.Unix(), prediction.ModelID, 0.02, -0.005, 0.01,
		"resolved", prediction.CreatedAt.Add(5*time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		transaction.Rollback()
		handle.Close()
		t.Fatalf("insert legacy actual: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		handle.Close()
		t.Fatalf("commit legacy fixture: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}
}

func mustPredictionUserVersion(t *testing.T, handle *sql.DB) int {
	t.Helper()
	version, err := predictionUserVersion(handle)
	if err != nil {
		t.Fatalf("read user version: %v", err)
	}
	return version
}

func predictionTableCount(t *testing.T, handle *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := handle.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func monitorTestPrediction() Prediction {
	return Prediction{
		Code: "sh600000", Date: "2026-10-20",
		InputEnd:  time.Date(2026, 10, 20, 9, 35, 0, 0, market.Shanghai),
		TargetEnd: time.Date(2026, 10, 20, 9, 40, 0, 0, market.Shanghai),
		ModelID:   "m1", BasePrice: 10,
		HighPct: 0.01, LowPct: -0.01, ClosePct: 0.005,
		Trend: "偏强", Confidence: "中", TrendScore: 1.5,
		InferenceMS: 1.25,
		CreatedAt:   time.Date(2026, 10, 20, 9, 35, 10, 0, market.Shanghai),
	}
}

func TestDBPredictionLifecycle(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	prediction := monitorTestPrediction()
	added, err := db.InsertPrediction(ctx, prediction)
	if err != nil || !added {
		t.Fatalf("insert prediction added=%v err=%v", added, err)
	}
	added, err = db.InsertPrediction(ctx, prediction)
	if err != nil || added {
		t.Fatalf("duplicate insert added=%v err=%v", added, err)
	}
	all, err := db.ForDate(ctx, prediction.Date)
	if err != nil || len(all) != 1 {
		t.Fatalf("for date len=%d err=%v", len(all), err)
	}
	pending, err := db.PendingForTarget(ctx, prediction.Date, prediction.TargetEnd)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending target len=%d err=%v", len(pending), err)
	}
	latency, err := db.LatencyStats(ctx, prediction.ModelID)
	if err != nil || latency.Count != 1 || latency.AvgMS != 1.25 || latency.P95MS != 1.25 {
		t.Fatalf("latency=%+v err=%v", latency, err)
	}
	actual := Actual{Code: prediction.Code, TargetEnd: prediction.TargetEnd, ModelID: prediction.ModelID,
		HighPct: 0.02, LowPct: -0.005, ClosePct: 0.01, Status: "resolved", ResolvedAt: prediction.CreatedAt.Add(5 * time.Minute)}
	if err := db.InsertActual(ctx, actual); err != nil {
		t.Fatalf("insert actual: %v", err)
	}
	actuals, err := db.ActualsForTarget(ctx, prediction.TargetEnd)
	if err != nil || len(actuals) != 1 || actuals[0].Status != "resolved" || actuals[0].HighPct != 0.02 {
		t.Fatalf("actuals=%+v err=%v", actuals, err)
	}
	actual.Status = "unresolved"
	if err := db.InsertActual(ctx, actual); err != nil {
		t.Fatalf("upsert actual: %v", err)
	}
	var stored string
	if err := db.db.QueryRowContext(ctx, `SELECT status FROM prediction_actual WHERE code=? AND target_end_unix=?`,
		prediction.Code, prediction.TargetEnd.Unix()).Scan(&stored); err != nil {
		t.Fatalf("read actual: %v", err)
	}
	if stored != "unresolved" {
		t.Fatalf("stored status=%q want unresolved", stored)
	}
	state := KalmanState{Code: prediction.Code, Date: prediction.Date, BarEnd: prediction.InputEnd,
		Level: 2.3, Slope: 0.01, P00: 1, P01: 0, P11: 0.1, UpdatedAt: prediction.CreatedAt}
	if err := db.SaveKalman(ctx, state); err != nil {
		t.Fatalf("save kalman: %v", err)
	}
	loadedKalman, err := db.Kalman(ctx, prediction.Code, prediction.Date)
	if err != nil || loadedKalman.Level != state.Level || !loadedKalman.BarEnd.Equal(state.BarEnd) {
		t.Fatalf("kalman=%+v err=%v", loadedKalman, err)
	}
	info := ModelInfo{SchemaVersion: ArtifactSchemaVersion, ModelID: "m1", CreatedAt: prediction.CreatedAt,
		DataFrom: "2026-09-18", DataTo: prediction.Date, ValidFor: "2026-10-21",
		FeatureVersion: FeatureVersion, FeatureNames: FeatureNames(), Checksum: "abc", ArtifactPath: "models/intraday/m1/model.json"}
	if err := db.RecordModel(ctx, info); err != nil {
		t.Fatalf("record model: %v", err)
	}
	recorded, err := db.ModelInfo(ctx, info.ModelID)
	if err != nil || recorded.ArtifactPath != info.ArtifactPath || recorded.ValidFor != info.ValidFor {
		t.Fatalf("model info=%+v err=%v", recorded, err)
	}
	predictions, resolved, err := db.Counts(ctx, prediction.Date)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if predictions != 1 {
		t.Fatalf("predictions=%d want 1", predictions)
	}
	if resolved != 0 {
		t.Fatalf("resolved=%d want 0 after unresolved upsert", resolved)
	}
}

func TestOpenDBMigratesV2SchemaAndDropsModelRuns(t *testing.T) {
	dir := t.TempDir()
	expected := monitorTestPrediction()
	createLegacyV2Database(t, dir, expected)

	database, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	defer database.Close()

	if version := mustPredictionUserVersion(t, database.db); version != PredictionSchemaVersion {
		t.Fatalf("user_version=%d want %d", version, PredictionSchemaVersion)
	}
	if count := predictionTableCount(t, database.db, "model_run"); count != 0 {
		t.Fatalf("model_run rows=%d want 0", count)
	}
	if count := predictionTableCount(t, database.db, "prediction"); count != 1 {
		t.Fatalf("prediction rows=%d want 1", count)
	}
	if count := predictionTableCount(t, database.db, "prediction_actual"); count != 1 {
		t.Fatalf("prediction_actual rows=%d want 1", count)
	}

	ctx := context.Background()
	predictions, err := database.ForDate(ctx, expected.Date)
	if err != nil {
		t.Fatalf("read migrated prediction: %v", err)
	}
	if len(predictions) != 1 {
		t.Fatalf("migrated predictions=%d want 1", len(predictions))
	}
	if predictions[0].InferenceMS != 0 {
		t.Fatalf("migrated inference_ms=%v want 0", predictions[0].InferenceMS)
	}
	actuals, err := database.ActualsForTarget(ctx, expected.TargetEnd)
	if err != nil {
		t.Fatalf("read migrated actual: %v", err)
	}
	if len(actuals) != 1 || actuals[0].Status != "resolved" {
		t.Fatalf("migrated actuals=%+v", actuals)
	}
}

func TestOpenDBFreshSchemaUsesVersion3(t *testing.T) {
	database, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatalf("open fresh db: %v", err)
	}
	defer database.Close()

	if version := mustPredictionUserVersion(t, database.db); version != PredictionSchemaVersion {
		t.Fatalf("user_version=%d want %d", version, PredictionSchemaVersion)
	}
	if count := predictionTableCount(t, database.db, "model_run"); count != 0 {
		t.Fatalf("model_run rows=%d want 0", count)
	}
}

func TestOpenDBReopenKeepsV3Data(t *testing.T) {
	dir := t.TempDir()
	prediction := monitorTestPrediction()
	info := ModelInfo{
		SchemaVersion: ArtifactSchemaVersion, ModelID: prediction.ModelID, CreatedAt: prediction.CreatedAt,
		DataFrom: "2026-09-18", DataTo: prediction.Date, ValidFor: "2026-10-21",
		FeatureVersion: FeatureVersion, FeatureNames: FeatureNames(), Checksum: "abc",
		ArtifactPath: "models/intraday/m1/model.json",
	}

	ctx := context.Background()
	first, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("open fresh db: %v", err)
	}
	if _, err := first.InsertPrediction(ctx, prediction); err != nil {
		first.Close()
		t.Fatalf("insert prediction: %v", err)
	}
	actual := Actual{
		Code: prediction.Code, TargetEnd: prediction.TargetEnd, ModelID: prediction.ModelID,
		HighPct: 0.02, LowPct: -0.005, ClosePct: 0.01, Status: "resolved",
		ResolvedAt: prediction.CreatedAt.Add(5 * time.Minute),
	}
	if err := first.InsertActual(ctx, actual); err != nil {
		first.Close()
		t.Fatalf("insert actual: %v", err)
	}
	if err := first.RecordModel(ctx, info); err != nil {
		first.Close()
		t.Fatalf("record model: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first db: %v", err)
	}

	second, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("reopen v3 db: %v", err)
	}
	defer second.Close()
	if version := mustPredictionUserVersion(t, second.db); version != PredictionSchemaVersion {
		t.Fatalf("user_version=%d want %d", version, PredictionSchemaVersion)
	}
	if count := predictionTableCount(t, second.db, "model_run"); count != 1 {
		t.Fatalf("model_run rows=%d want 1", count)
	}
	if count := predictionTableCount(t, second.db, "prediction"); count != 1 {
		t.Fatalf("prediction rows=%d want 1", count)
	}
	if count := predictionTableCount(t, second.db, "prediction_actual"); count != 1 {
		t.Fatalf("prediction_actual rows=%d want 1", count)
	}
	recorded, err := second.ModelInfo(ctx, info.ModelID)
	if err != nil {
		t.Fatalf("read model after reopen: %v", err)
	}
	if recorded.ArtifactPath != info.ArtifactPath {
		t.Fatalf("artifact path=%q want %q", recorded.ArtifactPath, info.ArtifactPath)
	}
}

func TestLatencyStatsPercentile(t *testing.T) {
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	base := monitorTestPrediction()
	for i := 1; i <= 20; i++ {
		prediction := base
		prediction.Code = fmt.Sprintf("sz%06d", i)
		prediction.InferenceMS = float64(i)
		if _, err := db.InsertPrediction(ctx, prediction); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	stats, err := db.LatencyStats(ctx, base.ModelID)
	if err != nil {
		t.Fatalf("latency stats: %v", err)
	}
	if stats.Count != 20 || stats.AvgMS != 10.5 || stats.P95MS != 19 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestDBRejectsInvalidPrediction(t *testing.T) {
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	prediction := monitorTestPrediction()
	prediction.BasePrice = 0
	if _, err := db.InsertPrediction(context.Background(), prediction); err == nil {
		t.Fatalf("expected validation error")
	}
}
