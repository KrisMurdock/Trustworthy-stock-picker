package valuetrappedstore

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/valuetrapped"
)

func TestLoadLatestRecommendation(t *testing.T) {
	root := t.TempDir()
	if _, err := LoadLatestRecommendation(context.Background(), root, false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing database = %v", err)
	}
	if err := storage.EnsureRoot(root, false); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenAllMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	rec := valuetrapped.Recommendation{
		Version: valuetrapped.SchemaVersion, ID: "value_trapped-2026-10-30-20", Market: valuetrapped.Market,
		AsOf: "2026-10-30", Target: "2026-11-02", GeneratedAt: time.Date(2026, 10, 30, 19, 0, 0, 0, time.UTC),
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRecommendation(context.Background(), storage.RecommendationArtifact{
		Market: rec.Market, AsOf: rec.AsOf, ID: rec.ID, Target: rec.Target, BuiltAt: rec.GeneratedAt, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadLatestRecommendation(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != rec.ID || loaded.Target != rec.Target {
		t.Fatalf("loaded = %+v", loaded)
	}
}
