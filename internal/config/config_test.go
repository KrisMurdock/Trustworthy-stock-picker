package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `schema_version: 1
stocks:
  - code: sz000001
calendar:
  valid_from: '2026-01-01'
  valid_through: '2026-12-31'
  holidays: []
`

func TestLoadValidation(t *testing.T) {
	for _, tt := range []struct {
		name, text string
		ok         bool
	}{
		{"valid", valid, true}, {"unknown", valid + "python_command: secret\n", false}, {"version", strings.Replace(valid, "schema_version: 1", "schema_version: 2", 1), false},
		{"duplicate", strings.Replace(valid, "calendar:", "  - code: sz000001\ncalendar:", 1), false}, {"hk", strings.Replace(valid, "sz000001", "hk00700", 1), false}, {"bad_time", valid + "daily:\n  run_at: '14:00'\n", false},
		{"missing_webhook", valid + "selection:\n  enabled: true\nfeishu:\n  enabled: true\n", false}, {"multidocument", valid + "---\nstocks: []\n", false},
		{"strategy_missing_version", valid + "strategy:\n  enabled: true\n", false},
		{"strategy_unknown_disabled_version", valid + "strategy:\n  schema_version: 2\n  enabled: false\n", false},
		{"strategy_valid", valid + "strategy:\n  schema_version: 1\n  enabled: true\n  commission_rate: 0.0001\n", true},
		{"strategy_fee_percent", valid + "strategy:\n  schema_version: 1\n  enabled: true\n  commission_rate: 1\n", false},
		{"strategy_positive_stop", valid + "strategy:\n  schema_version: 1\n  enabled: true\n  stop_net_pct: 3\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			os.WriteFile(p, []byte(tt.text), 0600)
			c, err := Load(p)
			if (err == nil) != tt.ok {
				t.Fatalf("error=%v", err)
			}
			if tt.ok && c.DataDir != filepath.Join(filepath.Dir(p), "data") {
				t.Fatal("data path not config relative")
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("config error leaked field value")
			}
		})
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("accepted missing config")
	}
}
func TestEnvironmentCredentials(t *testing.T) {
	t.Setenv("FEISHU_WEBHOOK", "https://open.feishu.cn/open-apis/bot/v2/hook/test-only-hook")
	t.Setenv("FEISHU_SECRET", "test-value")
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte(valid+"selection:\n  enabled: true\nfeishu:\n  enabled: true\n"), 0600)
	c, err := Load(p)
	if err != nil || c.Feishu.Secret != "test-value" {
		t.Fatal("environment credentials not applied")
	}
	t.Setenv("STOCK_WATCHER_DATA_DIR", "")
	if _, err := Load(p); err == nil {
		t.Fatal("accepted empty data dir")
	}
}
