package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/dayrange"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
)

func TestDayRangeDatabaseRoundTripAndReadOnlyView(t *testing.T) {
	root := t.TempDir()
	db, err := InitDayRange(root, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 10, 21, 8, 45, 0, 0, time.UTC)
	value := .25
	features := make([]dayrange.Feature, len(dayrange.FeatureNames()))
	for i, name := range dayrange.FeatureNames() {
		features[i] = dayrange.Feature{Name: name}
	}
	features[0].Value = &value
	snapshot := dayrange.Snapshot{SchemaVersion: dayrange.SnapshotSchemaVersion, Code: "sz000001", TargetDate: "2026-10-21",
		Mode: dayrange.ModePremarket, AsOf: now, InputThrough: "2026-10-20", ReferencePrice: 10, FeatureVersion: dayrange.FeatureVersion,
		Features: features, Availability: dayrange.Availability{Stock: "available"}, CreatedAt: now}
	snapshotID, err := db.PutSnapshot(ctx, "prediction", snapshot.Code, snapshot.TargetDate, snapshot.Mode, now, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	again, err := db.PutSnapshot(ctx, "prediction", snapshot.Code, snapshot.TargetDate, snapshot.Mode, now, snapshot)
	if err != nil || again != snapshotID {
		t.Fatalf("snapshot is not idempotent: %s %v", again, err)
	}
	var restored dayrange.Snapshot
	if err := db.LoadSnapshot(ctx, snapshotID, &restored); err != nil || restored.Code != snapshot.Code || restored.Values()[0] != value {
		t.Fatalf("snapshot round-trip failed: %+v %v", restored, err)
	}

	baseline := dayrange.ModelRun{ModelID: dayrange.BaselineModelID, Kind: "baseline", Status: dayrange.ModelStatusValidated,
		FeatureVersion: dayrange.FeatureVersion, CodeVersion: "test", CreatedAt: now, TrainFrom: "2025-01-01", TrainTo: "2026-10-20", ValidFor: "2026-10-21"}
	if err := db.SaveModelRun(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	loadedRun, err := db.LoadModelRun(ctx, baseline.ModelID)
	if err != nil || loadedRun.Status != baseline.Status || loadedRun.ValidFor != baseline.ValidFor {
		t.Fatalf("model round-trip failed: %+v %v", loadedRun, err)
	}
	if run, err := db.LatestModelForValidFor(ctx, baseline.ValidFor); err != nil || run.ModelID != baseline.ModelID {
		t.Fatalf("completed baseline was not recognized: %+v %v", run, err)
	}
	baseline.ValidFor = "2026-10-22"
	baseline.TrainTo = "2026-10-21"
	baseline.CreatedAt = now.Add(24 * time.Hour)
	if err := db.SaveModelRun(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	if run, err := db.LatestModelForValidFor(ctx, baseline.ValidFor); err != nil || run.TrainTo != baseline.TrainTo {
		t.Fatalf("baseline refresh did not update its validity: %+v %v", run, err)
	}

	interval := &dayrange.Interval{Lower: 9.5, Upper: 10.8, NominalCoverage: .8, Method: "baseline"}
	prediction := dayrange.Prediction{PredictionID: "p1", Code: snapshot.Code, TargetDate: snapshot.TargetDate, Mode: dayrange.ModePremarket,
		AsOf: now, InputThrough: snapshot.InputThrough, ModelID: dayrange.BaselineModelID, Source: "baseline", FeatureVersion: dayrange.FeatureVersion,
		ReferencePrice: 10, PointHigh: 10.5, PointLow: 9.6, QuantilesHigh: []dayrange.Quantile{{Level: .1, Price: 9.9}, {Level: .5, Price: 10.5}, {Level: .9, Price: 10.8}},
		QuantilesLow: []dayrange.Quantile{{Level: .1, Price: 9.5}, {Level: .5, Price: 9.6}, {Level: .9, Price: 9.9}}, IntervalHigh: interval, IntervalLow: interval,
		InputSnapshotID: snapshotID, BaselineModelID: dayrange.BaselineModelID, Status: "published", CreatedAt: now}
	inserted, err := db.SavePrediction(ctx, prediction)
	if err != nil || !inserted {
		t.Fatalf("save prediction: %v %v", inserted, err)
	}
	inserted, err = db.SavePrediction(ctx, prediction)
	if err != nil || inserted {
		t.Fatalf("duplicate prediction inserted: %v %v", inserted, err)
	}
	actual := dayrange.ActualRecord{Code: snapshot.Code, TargetDate: snapshot.TargetDate, Actual: &dayrange.Extremes{High: 10.6, Low: 9.7},
		Source: "tencent", ObservedThrough: snapshot.TargetDate, ResolvedAt: now, Status: "resolved"}
	if err := db.SaveActual(ctx, actual); err != nil {
		t.Fatal(err)
	}
	records, err := db.LoadPredictions(ctx, snapshot.TargetDate, snapshot.Code)
	if err != nil || len(records) != 1 || records[0].Actual == nil || records[0].Actual.Actual.High != 10.6 {
		t.Fatalf("prediction/actual read failed: %+v %v", records, err)
	}
	if loaded, err := db.LoadPrediction(ctx, "p1"); err != nil || loaded.Code != snapshot.Code {
		t.Fatalf("single prediction read failed: %+v %v", loaded, err)
	}

	evaluation := dayrange.Evaluation{ModelID: "model", BaselineID: dayrange.BaselineModelID, Mode: dayrange.ModePremarket,
		AsOf: "2026-10-20", ValidationFrom: "2026-09-01", ValidationTo: "2026-10-20", MetricsVersion: dayrange.EvaluationSchemaVersion,
		Gate: dayrange.Gate{Passed: false, Reasons: []string{"insufficient_validation"}}, CreatedAt: now,
		Candidate: dayrange.Report{SchemaVersion: 1, Subject: "model", Status: "ok", Metrics: []modeleval.MetricRecord{}},
		Baseline:  dayrange.Report{SchemaVersion: 1, Subject: "baseline", Status: "ok", Metrics: []modeleval.MetricRecord{}}}
	evaluationID, err := db.SaveEvaluation(ctx, evaluation)
	if err != nil {
		t.Fatal(err)
	}
	loadedEvaluation, err := db.LoadEvaluation(ctx, evaluationID)
	if err != nil || loadedEvaluation.Gate.Passed || loadedEvaluation.ValidationTo != evaluation.ValidationTo {
		t.Fatalf("evaluation round-trip failed: %+v %v", loadedEvaluation, err)
	}
	if _, err := db.SaveEvaluation(ctx, evaluation); err != nil {
		t.Fatalf("idempotent evaluation save failed: %v", err)
	}

	audit := dayrange.Audit{SchemaVersion: dayrange.SnapshotSchemaVersion, AsOf: "2026-10-20", From: "2025-10-20", To: "2026-10-20", Candidates: 1, Resolved: 1}
	auditID, err := db.SaveAudit(ctx, snapshotID, audit.AsOf, now, audit)
	if err != nil {
		t.Fatal(err)
	}
	loadedAudit, err := db.LatestAudit(ctx, audit.AsOf)
	if err != nil || loadedAudit.ID != auditID || loadedAudit.Audit.Resolved != 1 {
		t.Fatalf("audit round-trip failed: %+v %v", loadedAudit, err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.LoadPredictions(canceled, snapshot.TargetDate, snapshot.Code); err == nil {
		t.Fatal("canceled context was ignored")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	view, err := OpenDayRangeView(root, false)
	if err != nil || !view.Available() {
		t.Fatalf("read-only view failed: %v available=%v", err, view.Available())
	}
	defer view.Close()
	if _, err := view.LoadPredictions(ctx, snapshot.TargetDate, snapshot.Code); err != nil {
		t.Fatal(err)
	}
	if _, err := view.SavePrediction(ctx, prediction); err == nil {
		t.Fatal("read-only dayrange view accepted a write")
	}
}

func TestDayRangePruneModelsKeepsNewestAndReferenced(t *testing.T) {
	root := t.TempDir()
	db, err := InitDayRange(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	snapshotID, err := db.PutSnapshot(ctx, "prediction", "sz000001", "2026-10-21", dayrange.ModePremarket, now, map[string]string{"fixture": "prune"})
	if err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"old-referenced", "middle", "newest"} {
		if err := db.SaveModelRun(ctx, dayrange.ModelRun{ModelID: id, Kind: "gbdt_conformal", Status: dayrange.ModelStatusResearch,
			FeatureVersion: dayrange.FeatureVersion, CreatedAt: now.Add(time.Duration(index) * time.Minute), ValidFor: "2026-10-22"}); err != nil {
			t.Fatal(err)
		}
	}
	interval := &dayrange.Interval{Lower: 9, Upper: 11, NominalCoverage: .8, Method: "fixture"}
	if _, err := db.SavePrediction(ctx, dayrange.Prediction{PredictionID: "referenced", Code: "sz000001", TargetDate: "2026-10-21",
		Mode: dayrange.ModePremarket, AsOf: now, InputThrough: "2026-10-20", ModelID: "old-referenced", Source: "model",
		FeatureVersion: dayrange.FeatureVersion, ReferencePrice: 10, PointHigh: 10.5, PointLow: 9.5,
		IntervalHigh: interval, IntervalLow: interval, InputSnapshotID: snapshotID, Status: "published", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneModelRuns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"old-referenced": true, "middle": false, "newest": true} {
		_, err := db.LoadModelRun(ctx, id)
		if want && err != nil {
			t.Fatalf("retained model %s missing: %v", id, err)
		}
		if !want && !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("unreferenced model %s was not pruned: %v", id, err)
		}
	}
}

func TestDayRangePredictionReadPropagatesActualStorageFailure(t *testing.T) {
	root := t.TempDir()
	db, err := InitDayRange(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 21, 8, 45, 0, 0, time.UTC)
	snapshot := dayrange.Snapshot{SchemaVersion: dayrange.SnapshotSchemaVersion, Code: "sz000001", TargetDate: "2026-10-21",
		Mode: dayrange.ModePremarket, AsOf: now, InputThrough: "2026-10-20", ReferencePrice: 10, FeatureVersion: dayrange.FeatureVersion,
		Features: make([]dayrange.Feature, len(dayrange.FeatureNames())), Availability: dayrange.Availability{Stock: "available"}, CreatedAt: now}
	snapshotID, err := db.PutSnapshot(ctx, "prediction", snapshot.Code, snapshot.TargetDate, snapshot.Mode, now, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	interval := &dayrange.Interval{Lower: 9.5, Upper: 10.5, NominalCoverage: .8, Method: "baseline"}
	prediction := dayrange.Prediction{PredictionID: "p1", Code: snapshot.Code, TargetDate: snapshot.TargetDate, Mode: dayrange.ModePremarket,
		AsOf: now, InputThrough: snapshot.InputThrough, ModelID: dayrange.BaselineModelID, Source: "baseline", FeatureVersion: dayrange.FeatureVersion,
		ReferencePrice: 10, PointHigh: 10.5, PointLow: 9.6, IntervalHigh: interval, IntervalLow: interval,
		InputSnapshotID: snapshotID, BaselineModelID: dayrange.BaselineModelID, Status: "published", CreatedAt: now}
	if _, err := db.SavePrediction(ctx, prediction); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DROP TABLE dayrange_actual`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LoadPredictions(ctx, snapshot.TargetDate, snapshot.Code); err == nil || err.Error() != "cannot read dayrange actual" {
		t.Fatalf("prediction list swallowed actual storage failure: %v", err)
	}
	if _, err := db.LoadPrediction(ctx, prediction.PredictionID); err == nil || err.Error() != "cannot read dayrange actual" {
		t.Fatalf("single prediction swallowed actual storage failure: %v", err)
	}
}

func TestDayRangeUninitializedViewAndSchemaVersion(t *testing.T) {
	root := t.TempDir()
	view, err := OpenDayRangeView(root, false)
	if err != nil || view.Available() {
		t.Fatalf("uninitialized view should be an empty read model: %v available=%v", err, view.Available())
	}
	defer view.Close()
	if _, err := view.LoadPredictions(context.Background(), "2026-10-21", ""); err == nil {
		t.Fatal("unavailable view read a database")
	}
	db, err := InitDayRange(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDayRangeView(root, false); err == nil {
		t.Fatal("unsupported schema version was accepted")
	}
}

func TestDayRangeDBRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := root + "/target.db"
	other, err := sql.Open("sqlite", "file:"+target+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
	if err := os.Symlink(target, dayrangePath(root)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := InitDayRange(root, false); err == nil || !errors.Is(err, sql.ErrNoRows) && err.Error() != "dayrange database path must not be a symlink" {
		t.Fatalf("symlink accepted: %v", err)
	}
}

func dayrangePath(root string) string { return root + "/" + dayrangeDBName }
