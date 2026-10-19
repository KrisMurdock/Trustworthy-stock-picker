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
		{"ten_years", valid + "daily:\n  history_days: 3650\n", true},
		{"legacy_history", valid + "daily:\n  history_days: 365\n", true},
		{"twenty_years", valid + "daily:\n  history_days: 7300\n", true},
		{"history_too_long", valid + "daily:\n  history_days: 7301\n", false},
		{"valid", valid, true}, {"unknown", valid + "python_command: secret\n", false}, {"version", strings.Replace(valid, "schema_version: 1", "schema_version: 2", 1), false},
		{"duplicate", strings.Replace(valid, "calendar:", "  - code: sz000001\ncalendar:", 1), false}, {"hk", strings.Replace(valid, "sz000001", "hk00700", 1), false}, {"bad_time", valid + "daily:\n  run_at: '14:00'\n", false},
		{"missing_webhook", valid + "selection:\n  enabled: true\nfeishu:\n  enabled: true\n", false}, {"multidocument", valid + "---\nstocks: []\n", false},
		{"strategy_missing_version", valid + "strategy:\n  enabled: true\n", false},
		{"strategy_unknown_disabled_version", valid + "strategy:\n  schema_version: 2\n  enabled: false\n", false},
		{"strategy_valid", valid + "strategy:\n  schema_version: 1\n  enabled: true\n  commission_rate: 0.0001\n", true},
		{"strategy_fee_percent", valid + "strategy:\n  schema_version: 1\n  enabled: true\n  commission_rate: 1\n", false},
		{"strategy_positive_stop", valid + "strategy:\n  schema_version: 1\n  enabled: true\n  stop_net_pct: 3\n", false},
		{"structure_guard_shadow", valid + "structure_guard:\n  mode: shadow\n", true},
		{"structure_guard_enforce", valid + "structure_guard:\n  mode: enforce\n", true},
		{"structure_guard_invalid", valid + "structure_guard:\n  mode: live\n", false},
		{"bad_listed_from", strings.Replace(valid, "sz000001", "sz000001\n    listed_from: '2026-13-01'", 1), false},
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

func TestStructureGuardDefaultsToShadow(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.StructureGuard.Mode != "shadow" {
		t.Fatalf("default mode = %q", c.StructureGuard.Mode)
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

func TestNotificationChannelEnvironmentReferences(t *testing.T) {
	t.Setenv("TEST_FEISHU_WEBHOOK", "https://open.feishu.cn/open-apis/bot/v2/hook/test-only-hook")
	t.Setenv("TEST_FEISHU_SECRET", "test-secret")
	p := filepath.Join(t.TempDir(), "config.yaml")
	text := valid + `notify:
  channels:
    - name: feishu-main
      type: feishu
      enabled: true
      webhook_env: TEST_FEISHU_WEBHOOK
      secret_env: TEST_FEISHU_SECRET
`
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	channels := c.NotificationChannels()
	if len(channels) != 1 || channels[0].Webhook != "https://open.feishu.cn/open-apis/bot/v2/hook/test-only-hook" || channels[0].Secret != "test-secret" {
		t.Fatalf("unexpected channel credentials: %+v", channels)
	}
}

func TestNotificationChannelNamesAndUnknownFields(t *testing.T) {
	for _, text := range []string{
		valid + "notify:\n  channels:\n    - name: Bad Name\n      type: feishu\n      enabled: true\n",
		valid + "notify:\n  channels:\n    - name: one\n      type: feishu\n      enabled: true\n    - name: one\n      type: feishu\n      enabled: true\n",
		valid + "notify:\n  channels:\n    - name: one\n      type: feishu\n      enabled: true\n      webhook: secret\n",
	} {
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("accepted invalid channel config or leaked value: %v", err)
		}
	}
}

func TestSyncDefaultsAndValidation(t *testing.T) {
	c := Defaults()
	if c.Sync.MinCoveragePct != 95 || c.Sync.IncrementalBars != 5 {
		t.Fatalf("unexpected sync defaults: %+v", c.Sync)
	}
	c.Sync.MinCoveragePct = 101
	if err := c.Validate(); err == nil {
		t.Fatal("accepted coverage above 100 percent")
	}
	c = Defaults()
	c.Sync.IncrementalBars = 1
	if err := c.Validate(); err == nil {
		t.Fatal("accepted too-small incremental window")
	}
}

func TestFeishuCredentialsFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	text := valid + "feishu:\n  enabled: true\n  credentials_file: feishu.config\n  webhook: https://open.feishu.cn/open-apis/bot/v2/hook/old-value\n  secret: old-secret\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	credentials := filepath.Join(root, "feishu.config")
	for _, tc := range []struct {
		name, content string
		ok            bool
	}{
		{"webhook only", "webhook: https://open.feishu.cn/open-apis/bot/v2/hook/file-value\n", true},
		{"with secret", "webhook: https://open.feishu.cn/open-apis/bot/v2/hook/file-value\nsecret: private-marker\n", true},
		{"unknown field", "private-marker: hidden\n", false},
		{"malformed", "webhook: [private-marker\n", false},
		{"extra document", "webhook: private-marker\n---\nsecret: hidden\n", false},
		{"invalid URL", "webhook: https://example.com/private-marker\n", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(credentials, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected config validity: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-marker") {
				t.Fatal("credential leaked in error")
			}
			if tc.ok && c.Feishu.Webhook != "https://open.feishu.cn/open-apis/bot/v2/hook/file-value" {
				t.Fatal("file did not override inline value")
			}
			if tc.name == "webhook only" && c.Feishu.Secret != "" {
				t.Fatal("stale inline signature secret retained")
			}
		})
	}
	if err := os.WriteFile(credentials, []byte("webhook: https://open.feishu.cn/open-apis/bot/v2/hook/file-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEISHU_WEBHOOK", "https://open.feishu.cn/open-apis/bot/v2/hook/env-value")
	t.Setenv("FEISHU_SECRET", "env-secret")
	c, err := Load(path)
	if err != nil || c.Feishu.Webhook != os.Getenv("FEISHU_WEBHOOK") || c.Feishu.Secret != "env-secret" {
		t.Fatal("environment did not override credentials file")
	}
	t.Setenv("FEISHU_WEBHOOK", "")
	if _, err := Load(path); err == nil {
		t.Fatal("explicit empty environment silently fell back to file")
	}
	if err := os.Remove(credentials); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("missing explicit credentials file accepted")
	}
}

func TestNamedChannelCredentialsFileAndEnvironment(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	cfg := valid + `notify:
  channels:
    - name: feishu-main
      type: feishu
      enabled: true
      credentials_file: feishu.config
      webhook_env: STARTUP_TEST_WEBHOOK
      secret_env: STARTUP_TEST_SECRET
`
	if err := os.WriteFile(path, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "feishu.config"), []byte("webhook: https://open.feishu.cn/open-apis/bot/v2/hook/file-value\nsecret: file-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || c.NotificationChannels()[0].Secret != "file-secret" {
		t.Fatal("unset environment did not use file")
	}
	t.Setenv("STARTUP_TEST_WEBHOOK", "https://open.feishu.cn/open-apis/bot/v2/hook/env-value")
	t.Setenv("STARTUP_TEST_SECRET", "")
	c, err = Load(path)
	if err != nil || c.NotificationChannels()[0].Webhook != os.Getenv("STARTUP_TEST_WEBHOOK") || c.NotificationChannels()[0].Secret != "" {
		t.Fatal("channel environment precedence incorrect")
	}
	t.Setenv("STARTUP_TEST_WEBHOOK", "")
	if _, err := Load(path); err == nil {
		t.Fatal("empty environment did not clear file webhook")
	}
}

func TestBuiltinConfiguration(t *testing.T) {
	c := Builtin()
	if c.Version != 1 || c.RequireCIFS || c.PoolFile != "leaders.yaml" {
		t.Fatalf("unexpected builtin identity: %+v", c)
	}
	if !c.Strategy.Enabled || !c.Selection.Enabled || !c.Feishu.Enabled || c.Feishu.CredentialsFile != "feishu.config" {
		t.Fatalf("unexpected builtin feature flags: %+v", c)
	}
	if c.Calendar.ValidFrom != "2026-01-01" || c.Calendar.ValidThrough != "2026-12-31" || len(c.Calendar.Holidays) == 0 {
		t.Fatalf("unexpected builtin calendar: %+v", c.Calendar)
	}
}

func TestLoadBuiltinUsesEnvironmentAndPool(t *testing.T) {
	root := t.TempDir()
	pool := filepath.Join(root, "leaders.yaml")
	if err := os.WriteFile(pool, []byte("schema_version: 1\nstocks:\n  - code: sz000001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STOCK_WATCHER_DATA_DIR", root)
	t.Setenv("STOCK_WATCHER_REQUIRE_CIFS", "false")
	t.Setenv("STOCK_WATCHER_FEISHU_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_POOL_FILE", pool)
	t.Setenv("STOCK_WATCHER_CALENDAR_VALID_FROM", "2020-01-01")
	t.Setenv("STOCK_WATCHER_CALENDAR_VALID_THROUGH", "2099-12-31")
	t.Setenv("STOCK_WATCHER_CALENDAR_HOLIDAYS", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != root || c.RequireCIFS || c.Feishu.Enabled || len(c.Stocks) != 1 || c.Stocks[0].Code != "sz000001" {
		t.Fatalf("builtin environment not applied: %+v", c)
	}
	if c.Calendar.ValidFrom != "2020-01-01" || len(c.Calendar.Holidays) != 0 {
		t.Fatalf("calendar environment not applied: %+v", c.Calendar)
	}
}

func TestBuiltinCIFSModeCanBeEnabled(t *testing.T) {
	root := t.TempDir()
	pool := filepath.Join(root, "leaders.yaml")
	if err := os.WriteFile(pool, []byte("schema_version: 1\nstocks:\n  - code: sz000001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STOCK_WATCHER_DATA_DIR", root)
	t.Setenv("STOCK_WATCHER_REQUIRE_CIFS", "true")
	t.Setenv("STOCK_WATCHER_FEISHU_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_POOL_FILE", pool)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.RequireCIFS {
		t.Fatal("CIFS requirement not enabled")
	}
}

func TestPrepareLoadedConfigurationIsIdempotent(t *testing.T) {
	root := t.TempDir()
	pool := filepath.Join(root, "leaders.yaml")
	if err := os.WriteFile(pool, []byte("schema_version: 1\nstocks:\n  - code: sz000001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STOCK_WATCHER_DATA_DIR", root)
	t.Setenv("STOCK_WATCHER_REQUIRE_CIFS", "false")
	t.Setenv("STOCK_WATCHER_FEISHU_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_POOL_FILE", pool)
	t.Setenv("STOCK_WATCHER_CALENDAR_VALID_FROM", "2020-01-01")
	t.Setenv("STOCK_WATCHER_CALENDAR_VALID_THROUGH", "2099-12-31")
	t.Setenv("STOCK_WATCHER_CALENDAR_HOLIDAYS", "")
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare(loaded, ".")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.DataDir != root || len(prepared.Stocks) != 1 || prepared.Stocks[0].Code != "sz000001" {
		t.Fatalf("prepared config changed: %+v", prepared)
	}
}
