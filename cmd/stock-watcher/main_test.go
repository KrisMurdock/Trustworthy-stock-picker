package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	logs, err := os.MkdirTemp("", "stock-watcher-cli-logs-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("STOCK_WATCHER_LOG_DIR", logs); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(logs)
	os.Exit(code)
}

func TestCLIInvalidAndMissingConfiguration(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"check", "-config", "config.yaml"}, {"run", "-as-of", "2026-10-13"}, {"run", "-entry-price", "10"}, {"plan", "-entry-price", "NaN"}} {
		if err := run(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func useBuiltinConfig(t *testing.T, dataDir string, codes ...string) {
	t.Helper()
	pool := filepath.Join(t.TempDir(), "leaders.yaml")
	var text strings.Builder
	text.WriteString("schema_version: 1\nstocks:\n")
	for _, code := range codes {
		text.WriteString("  - code: " + code + "\n")
	}
	if err := os.WriteFile(pool, []byte(text.String()), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STOCK_WATCHER_DATA_DIR", dataDir)
	t.Setenv("STOCK_WATCHER_REQUIRE_CIFS", "false")
	t.Setenv("STOCK_WATCHER_FEISHU_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_POOL_FILE", pool)
}

func TestPlanUsesActualFillWithoutMarketMount(t *testing.T) {
	root := t.TempDir()
	useBuiltinConfig(t, filepath.Join(root, "nonexistent-share"), "sz000001")
	if err := run([]string{"plan", "-code", "sz000001", "-entry-price", "10"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"plan", "-code", "sh688981", "-entry-price", "1000"}); err == nil {
		t.Fatal("unfundable STAR order accepted")
	}
}
func TestCLIProductionShapeAndMissingMount(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	os.Mkdir(data, 0700)
	useBuiltinConfig(t, data, "sz000001")
	if err := run([]string{"check"}); err == nil {
		t.Fatal("check reported ready before market database initialization")
	}
	if err := run([]string{"init"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"check"}); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(data)
	if err := run([]string{"check"}); err == nil {
		t.Fatal("missing root accepted")
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatal("fallback created")
	}
}

func TestCLIInitCreatesMarketDatabase(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	useBuiltinConfig(t, data, "sz000001")
	if err := run([]string{"init"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(data); err != nil || !info.IsDir() {
		t.Fatalf("local data directory was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "market.db")); err != nil {
		t.Fatalf("market database was not initialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "context.db")); err != nil {
		t.Fatalf("context database was not initialized: %v", err)
	}
}

func TestCLIInitRequiresExistingCIFSMount(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "missing-mount")
	useBuiltinConfig(t, data, "sz000001")
	t.Setenv("STOCK_WATCHER_REQUIRE_CIFS", "true")
	if err := run([]string{"init"}); err == nil {
		t.Fatal("initialized without a required CIFS mount")
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatal("created a local fallback for a required CIFS mount")
	}
}

func TestCheckConfigBeforeStoppingService(t *testing.T) {
	useBuiltinConfig(t, filepath.Join(t.TempDir(), "missing"), "sz000001")
	if err := run([]string{"check-config"}); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing-leaders.yaml")
	t.Setenv("STOCK_WATCHER_POOL_FILE", missing)
	if err := run([]string{"check-config"}); err == nil {
		t.Fatal("missing stock pool accepted")
	}
	t.Setenv("STOCK_WATCHER_LOG_MAX_MB", "invalid")
	useBuiltinConfig(t, filepath.Join(t.TempDir(), "missing"), "sz000001")
	if err := run([]string{"check-config"}); err == nil {
		t.Fatal("invalid logging settings accepted")
	}
}

func TestCLISyncContextRejectsNonTradingDayBeforeNetwork(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	os.Mkdir(data, 0700)
	useBuiltinConfig(t, data, "sz000001")
	if err := run([]string{"sync-context", "-as-of", "2026-10-18"}); err == nil || !strings.Contains(err.Error(), "configured trading day") {
		t.Fatalf("non-trading context day accepted: %v", err)
	}
}
