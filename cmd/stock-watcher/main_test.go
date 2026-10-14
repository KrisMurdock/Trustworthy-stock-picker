package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLIInvalidAndMissingConfiguration(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"check", "-config", filepath.Join(t.TempDir(), "missing.yaml")}, {"run", "-as-of", "2026-10-13"}, {"run", "-entry-price", "10"}, {"plan", "-entry-price", "NaN"}} {
		if err := run(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestPlanUsesActualFillWithoutMarketMount(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "config.yaml")
	cfg := `schema_version: 1
data_dir: nonexistent-share
stocks:
  - code: sz000001
calendar:
  valid_from: '2026-01-01'
  valid_through: '2026-12-31'
strategy:
  schema_version: 1
  commission_rate: 0.0001
`
	if err := os.WriteFile(p, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"plan", "-config", p, "-code", "sz000001", "-entry-price", "10"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"plan", "-config", p, "-code", "sh688981", "-entry-price", "1000"}); err == nil {
		t.Fatal("unfundable STAR order accepted")
	}
}
func TestCLIProductionShapeAndMissingMount(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	os.Mkdir(data, 0700)
	cfg := `schema_version: 1
data_dir: data
require_cifs: false
stocks:
  - code: sz000001
calendar:
  valid_from: '2026-01-01'
  valid_through: '2026-12-31'
  holidays: []
`
	p := filepath.Join(root, "config.yaml")
	os.WriteFile(p, []byte(cfg), 0600)
	if err := run([]string{"check", "-config", p}); err != nil {
		t.Fatal(err)
	}
	os.Remove(data)
	if err := run([]string{"check", "-config", p}); err == nil {
		t.Fatal("missing root accepted")
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatal("fallback created")
	}
}
