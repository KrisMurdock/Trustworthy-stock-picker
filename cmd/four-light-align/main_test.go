package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsIncompleteOrUnsupportedRequests(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), nil, &stdout, &stderr); err == nil {
		t.Fatal("missing arguments accepted")
	}
	if err := run(context.Background(), []string{
		"-data-dir", t.TempDir(), "-reference", "sample.csv",
		"-software", "target", "-software-version", "1", "-period", "5m", "-adjust", "qfq",
	}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "daily") {
		t.Fatalf("unsupported period accepted: %v", err)
	}
}

func TestRunRejectsInvalidReferenceBeforeOpeningMarket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reference.csv")
	if err := os.WriteFile(path, []byte("code,date,trend,energy,mid_term\nsz000001,2026-05-06,red,red,red\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"-data-dir", filepath.Join(t.TempDir(), "missing"), "-reference", path,
		"-software", "target", "-software-version", "1", "-period", "daily", "-adjust", "qfq",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "required column") {
		t.Fatalf("invalid reference was not rejected first: %v", err)
	}
}
