package health

import (
	"context"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/predictor"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func predictorTestConfig(t *testing.T) config.Config {
	t.Helper()
	c := config.Defaults()
	c.DataDir = t.TempDir()
	c.RequireCIFS = false
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "test"}}
	c.Predictor.Enabled = true
	c.Predictor.PushEnabled = true
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	return c
}

func containsIssue(issues []string, target string) bool {
	for _, issue := range issues {
		if issue == target {
			return true
		}
	}
	return false
}

func TestPredictorIssuesTrackModelPredictionAndPushState(t *testing.T) {
	c := predictorTestConfig(t)
	cal := calendar.New(c)
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, market.Shanghai)
	healthy := predictor.Status{
		Version:          predictor.StatusSchemaVersion,
		Enabled:          true,
		PushEnabled:      true,
		ModelID:          "model-1",
		ModelValidFor:    "2026-10-16",
		LastPredictionAt: now.Add(-5 * time.Minute),
	}
	if issues := predictorIssues(healthy, c, cal, now); len(issues) != 0 {
		t.Fatalf("healthy predictor reported issues: %v", issues)
	}
	stale := healthy
	stale.ModelValidFor = "2026-10-15"
	stale.LastPredictionAt = now.Add(-20 * time.Minute)
	stale.LastError = "training failed"
	stale.PushBlocked = 2
	issues := predictorIssues(stale, c, cal, now)
	for _, want := range []string{"predictor_model_stale", "predictor_prediction_stale", "predictor_error", "predictor_push_blocked"} {
		if !containsIssue(issues, want) {
			t.Fatalf("missing %s in %v", want, issues)
		}
	}
	missing := healthy
	missing.ModelID = ""
	if issues := predictorIssues(missing, c, cal, now); !containsIssue(issues, "predictor_model_missing") {
		t.Fatalf("missing model not reported: %v", issues)
	}
	// The first five-minute predictions are not due until after 09:35 and 13:05.
	for _, at := range []time.Time{
		time.Date(2026, 10, 16, 9, 32, 0, 0, market.Shanghai),
		time.Date(2026, 10, 16, 13, 2, 0, 0, market.Shanghai),
	} {
		issues := predictorIssues(predictor.Status{ModelID: "model-1", ModelValidFor: "2026-10-16"}, c, cal, at)
		if containsIssue(issues, "predictor_prediction_stale") {
			t.Fatalf("premature stale alarm at %s: %v", at.Format("15:04"), issues)
		}
	}
	// Outside the trading session a model for the previous close is expected.
	afterHours := time.Date(2026, 10, 16, 18, 0, 0, 0, market.Shanghai)
	issues = predictorIssues(predictor.Status{ModelID: "model-1", ModelValidFor: "2026-10-19"}, c, cal, afterHours)
	if containsIssue(issues, "predictor_model_stale") || containsIssue(issues, "predictor_prediction_stale") {
		t.Fatalf("after-hours false alarm: %v", issues)
	}
}

func TestReadAddsPredictorStatusFromViewStore(t *testing.T) {
	c := predictorTestConfig(t)
	c.Daily.Adjust = "none"
	c.Intraday.Enabled = false
	db, err := storage.InitMarket(c.DataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 16, 19, 0, 0, 0, market.Shanghai)
	store, err := storage.Open(c.DataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	status := predictor.Status{
		Version:          predictor.StatusSchemaVersion,
		Enabled:          true,
		PushEnabled:      false,
		ModelID:          "model-1",
		ModelValidFor:    "2026-10-19",
		TrainedAt:        now.Add(-time.Hour),
		LastPredictionAt: now.Add(-30 * time.Minute),
		PredictionsToday: 12,
		PushDelivered:    3,
		UpdatedAt:        now.Add(-time.Minute),
	}
	if err := predictor.WriteStatus(store, status); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := Read(context.Background(), c, db, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Predictor == nil || !got.Predictor.StatusFilePresent || got.Predictor.ModelID != "model-1" ||
		got.Predictor.PushEnabled || got.Predictor.PredictionsToday != 12 || got.Predictor.PushDelivered != 3 {
		t.Fatalf("predictor status not included: %+v", got.Predictor)
	}
}
