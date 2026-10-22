package main

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fileChecksum(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

func TestPredictorEvalRejectsInvalidFlags(t *testing.T) {
	for _, args := range [][]string{
		{"predictor-eval", "-windows", "0"},
		{"predictor-eval", "-windows", "bad"},
		{"predictor-eval", "-validation-days", "0"},
		{"predictor-eval", "-half-life", "0"},
		{"predictor-eval", "-loss", "huber"},
		{"predictor-eval", "-close-loss", "huber"},
		{"predictor-eval", "-as-of", "2026-10-18"},
		{"run", "-windows", "5"},
		{"run", "-validation-days", "5"},
		{"run", "-half-life", "30"},
		{"run", "-allow-late-reports"},
		{"run", "-loss", "l1"},
		{"run", "-close-loss", "l1"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestPredictorEvalIsDeterministicAndReadOnly(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	os.Mkdir(data, 0700)
	useBuiltinConfig(t, data, "sz000001")
	if err := run([]string{"init"}); err != nil {
		t.Fatal(err)
	}
	marketBefore := fileChecksum(t, filepath.Join(data, "market.db"))
	contextBefore := fileChecksum(t, filepath.Join(data, "context.db"))

	args := []string{"predictor-eval", "-as-of", "2026-10-19", "-windows", "2", "-validation-days", "1"}
	first, err := captureStdout(t, func() error { return run(args) })
	if err != nil {
		t.Fatalf("predictor-eval: %v", err)
	}
	second, err := captureStdout(t, func() error { return run(args) })
	if err != nil {
		t.Fatalf("predictor-eval repeat: %v", err)
	}
	if first != second {
		t.Fatal("predictor-eval output is not deterministic")
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(first), &report); err != nil {
		t.Fatalf("predictor-eval did not emit one JSON object: %v", err)
	}
	if report["schema_version"] != float64(2) || report["windows"] == nil || report["metrics"] == nil {
		t.Fatalf("unexpected evaluation report: %s", first)
	}

	if marketBefore != fileChecksum(t, filepath.Join(data, "market.db")) {
		t.Fatal("predictor-eval modified market.db")
	}
	if contextBefore != fileChecksum(t, filepath.Join(data, "context.db")) {
		t.Fatal("predictor-eval modified context.db")
	}
	for _, path := range []string{"predictions.db", "models", "state"} {
		if _, err := os.Stat(filepath.Join(data, path)); !os.IsNotExist(err) {
			t.Fatalf("predictor-eval created %s", path)
		}
	}
}

func TestPredictorEvalRefusesToShortenValidation(t *testing.T) {
	useBuiltinConfig(t, filepath.Join(t.TempDir(), "data"), "sz000001")
	output, err := captureStdout(t, func() error {
		return run([]string{"predictor-eval", "-as-of", "2026-10-19", "-windows", "7300", "-validation-days", "60"})
	})
	if err != nil {
		t.Fatalf("predictor-eval: %v", err)
	}
	if !strings.Contains(output, "insufficient calendar history") {
		t.Fatalf("unexpected output: %s", output)
	}
}
