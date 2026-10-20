package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/predictor"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func predictorStatus(c config.Config) error {
	view, err := storage.OpenView(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer view.Close()
	status, err := predictor.ReadStatus(view)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		status = predictor.Status{Version: predictor.StatusSchemaVersion, Enabled: c.Predictor.Enabled, PushEnabled: c.Predictor.PushEnabled}
	default:
		return err
	}
	if runtimeConfig, err := predictor.ReadRuntimeConfig(view); err == nil && !runtimeConfig.UpdatedAt.IsZero() {
		status.PushEnabled = runtimeConfig.PushEnabled
	}
	return json.NewEncoder(os.Stdout).Encode(predictor.View{Status: status, WriteMode: c.Predictor.WriteMode})
}

func trainPredictor(ctx context.Context, c config.Config, log *logging.Logger) error {
	if !c.Predictor.Enabled {
		return errors.New("predictor is disabled")
	}
	store, err := storage.Open(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer store.Close()
	marketDB, err := storage.OpenMarket(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer marketDB.Close()
	monitor, err := predictor.New(c, store, marketDB, nil, log)
	if err != nil {
		return err
	}
	if monitor == nil {
		return errors.New("predictor is disabled")
	}
	defer monitor.Close()
	info, err := monitor.Train(ctx, time.Now())
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(info)
}
