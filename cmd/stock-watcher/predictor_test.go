package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/predictor"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	runErr := fn()
	_ = writer.Close()
	os.Stdout = original
	data, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(data), runErr
}

func initializedPredictorConfig(t *testing.T) string {
	t.Helper()
	data := filepath.Join(t.TempDir(), "data")
	useBuiltinConfig(t, data, "sz000001")
	if _, err := captureStdout(t, func() error { return run([]string{"init"}) }); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPredictorStatusIsReadableWhileWriterOwnsLock(t *testing.T) {
	data := initializedPredictorConfig(t)
	writer, err := storage.Open(data, false)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	out, err := captureStdout(t, func() error { return run([]string{"predictor-status"}) })
	if err != nil {
		t.Fatal(err)
	}
	var view predictor.View
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("invalid status output %q: %v", out, err)
	}
	if !view.Enabled || view.WriteMode != "loopback" || view.WriteEnabled {
		t.Fatalf("unexpected status view: %+v", view)
	}
}

func TestTrainPredictorFailsWhenRunOwnsLock(t *testing.T) {
	data := initializedPredictorConfig(t)
	writer, err := storage.Open(data, false)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	err = run([]string{"train-predictor"})
	if err == nil || !strings.Contains(err.Error(), "another stock-watcher process owns the data directory") {
		t.Fatalf("train command ignored writer lock: %v", err)
	}
}

func TestTrainPredictorRequiresEnabledConfiguration(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	useBuiltinConfig(t, data, "sz000001")
	t.Setenv("STOCK_WATCHER_PREDICTOR_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_PREDICTOR_PUSH_ENABLED", "false")
	err := run([]string{"train-predictor"})
	if err == nil || !strings.Contains(err.Error(), "predictor is disabled") {
		t.Fatalf("disabled predictor training accepted: %v", err)
	}
}

func TestPredictorStatusReportsMissingStateWithoutWriting(t *testing.T) {
	data := initializedPredictorConfig(t)
	out, err := captureStdout(t, func() error { return run([]string{"predictor-status"}) })
	if err != nil {
		t.Fatal(err)
	}
	var view predictor.View
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Enabled || view.ModelID != "" {
		t.Fatalf("unexpected fresh status: %+v", view)
	}
	if _, err := os.Stat(filepath.Join(data, "state/predictor-status.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("predictor-status wrote state")
	}
}
