package predictor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

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
