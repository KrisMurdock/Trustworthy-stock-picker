package predictor

import (
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func TestStatusAndRuntimeConfigRoundTrip(t *testing.T) {
	root := t.TempDir()
	store, err := storage.Open(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	status := Status{Enabled: true, PushEnabled: true, ModelID: "m1", ModelValidFor: "2026-10-20",
		PredictionsToday: 12, UpdatedAt: time.Now().UTC()}
	if err := WriteStatus(store, status); err != nil {
		t.Fatal(err)
	}
	got, err := ReadStatus(store)
	if err != nil {
		t.Fatal(err)
	}
	if got.ModelID != "m1" || !got.PushEnabled || got.PredictionsToday != 12 || got.Version != StatusSchemaVersion {
		t.Fatalf("unexpected status: %+v", got)
	}

	if cfg, err := ReadRuntimeConfig(store); err != nil || cfg.SchemaVersion != 1 || cfg.PushEnabled {
		t.Fatalf("missing runtime config should be default: %+v %v", cfg, err)
	}
	if err := WriteRuntimeConfig(store, RuntimeConfig{PushEnabled: false}); err != nil {
		t.Fatal(err)
	}
	cfg, err := ReadRuntimeConfig(store)
	if err != nil || cfg.PushEnabled || cfg.UpdatedAt.IsZero() {
		t.Fatalf("unexpected runtime config: %+v %v", cfg, err)
	}
}
