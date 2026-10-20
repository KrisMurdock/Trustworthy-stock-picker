package predictor

import (
	"errors"
	"io/fs"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

const (
	statusPath = "state/predictor-status.json"
	configPath = "state/predictor.json"
)

type RuntimeConfig struct {
	SchemaVersion int       `json:"schema_version"`
	PushEnabled   bool      `json:"push_enabled"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func ReadStatus(store *storage.Store) (Status, error) {
	var status Status
	err := store.Read(statusPath, &status)
	if err != nil {
		return Status{}, err
	}
	if status.Version != StatusSchemaVersion {
		return Status{}, errors.New("unsupported predictor status schema")
	}
	return status, nil
}

func WriteStatus(store *storage.Store, status Status) error {
	status.Version = StatusSchemaVersion
	if status.UpdatedAt.IsZero() {
		status.UpdatedAt = time.Now().UTC()
	}
	return store.Write(statusPath, status)
}

func ReadRuntimeConfig(store *storage.Store) (RuntimeConfig, error) {
	var cfg RuntimeConfig
	err := store.Read(configPath, &cfg)
	if errors.Is(err, fs.ErrNotExist) {
		return RuntimeConfig{SchemaVersion: 1}, nil
	}
	if err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.SchemaVersion != 1 {
		return RuntimeConfig{}, errors.New("unsupported predictor runtime config schema")
	}
	return cfg, nil
}

func WriteRuntimeConfig(store *storage.Store, cfg RuntimeConfig) error {
	cfg.SchemaVersion = 1
	cfg.UpdatedAt = time.Now().UTC()
	return store.Write(configPath, cfg)
}
