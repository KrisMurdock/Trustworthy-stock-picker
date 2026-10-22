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
	if !c.Strategy.Enabled || !c.Selection.Enabled || !c.Feishu.Enabled || c.Feishu.CredentialsFile != "" {
		t.Fatalf("unexpected builtin feature flags: %+v", c)
	}
	if c.Calendar.ValidFrom != "2026-01-01" || c.Calendar.ValidThrough != "2026-12-31" || len(c.Calendar.Holidays) == 0 {
		t.Fatalf("unexpected builtin calendar: %+v", c.Calendar)
	}
}

func TestEnvironmentOnlyConfiguration(t *testing.T) {
	root := t.TempDir()
	pool := filepath.Join(root, "leaders.yaml")
	if err := os.WriteFile(pool, []byte("schema_version: 1\nstocks:\n  - code: sz000001\n    name: 平安银行\n  - code: sh600111\n    name: 北方稀土\n    sector: 稀土\n    listed_from: '1997-09-24'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STOCK_WATCHER_DATA_DIR", root)
	t.Setenv("STOCK_WATCHER_REQUIRE_CIFS", "false")
	t.Setenv("STOCK_WATCHER_POOL_FILE", pool)
	t.Setenv("STOCK_WATCHER_FEISHU_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_CALENDAR_VALID_FROM", "2020-01-01")
	t.Setenv("STOCK_WATCHER_CALENDAR_VALID_THROUGH", "2099-12-31")
	t.Setenv("STOCK_WATCHER_CALENDAR_HOLIDAYS", "2026-10-01,2026-10-02")
	t.Setenv("STOCK_WATCHER_POLL_INTERVAL", "6m")
	t.Setenv("STOCK_WATCHER_REQUEST_TIMEOUT", "12s")
	t.Setenv("STOCK_WATCHER_ANALYSIS_ENGINE", "auto")
	t.Setenv("STOCK_WATCHER_ANALYSIS_TIMEOUT", "11m")
	t.Setenv("STOCK_WATCHER_ANALYSIS_SYNC_TIMEOUT", "2h")
	t.Setenv("STOCK_WATCHER_STRUCTURE_GUARD_MODE", "enforce")
	t.Setenv("STOCK_WATCHER_DAILY_RUN_AT", "18:15")
	t.Setenv("STOCK_WATCHER_DAILY_HISTORY_DAYS", "500")
	t.Setenv("STOCK_WATCHER_DAILY_ADJUST", "qfq")
	t.Setenv("STOCK_WATCHER_WEB_ENABLED", "true")
	t.Setenv("STOCK_WATCHER_WEB_ADDR", "127.0.0.1:8089")
	t.Setenv("STOCK_WATCHER_SELECTION_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_SELECTION_MIN_SCORE", "75")
	t.Setenv("STOCK_WATCHER_SELECTION_TOP_N", "7")
	t.Setenv("STOCK_WATCHER_SELECTION_MIN_VOLUME_RATIO", "1.5")
	t.Setenv("STOCK_WATCHER_SELECTION_RSI_MIN", "45")
	t.Setenv("STOCK_WATCHER_SELECTION_RSI_MAX", "85")
	t.Setenv("STOCK_WATCHER_SELECTION_MIN_DAY_CHANGE", "-2.5")
	t.Setenv("STOCK_WATCHER_SELECTION_MAX_DAY_CHANGE", "6.5")
	t.Setenv("STOCK_WATCHER_SYNC_MIN_COVERAGE_PCT", "96")
	t.Setenv("STOCK_WATCHER_SYNC_MIN_ANALYZABLE_PCT", "91")
	t.Setenv("STOCK_WATCHER_SYNC_INCREMENTAL_BARS", "6")
	t.Setenv("STOCK_WATCHER_SYNC_STATUS_REFRESH_DAYS", "31")
	t.Setenv("STOCK_WATCHER_STRATEGY_ENABLED", "true")
	t.Setenv("STOCK_WATCHER_STRATEGY_TARGET_NET_PCT", "2.5")
	t.Setenv("STOCK_WATCHER_STRATEGY_STOP_NET_PCT", "-2")
	t.Setenv("STOCK_WATCHER_STRATEGY_MAX_HOLDING_DAYS", "4")
	t.Setenv("STOCK_WATCHER_STRATEGY_TRADE_CASH", "25000")
	t.Setenv("STOCK_WATCHER_STRATEGY_COMMISSION_RATE", "0.00012")
	t.Setenv("STOCK_WATCHER_STRATEGY_MIN_COMMISSION", "6")
	t.Setenv("STOCK_WATCHER_STRATEGY_TRANSFER_RATE", "0.00002")
	t.Setenv("STOCK_WATCHER_STRATEGY_SELL_STAMP_RATE", "0.0006")
	t.Setenv("STOCK_WATCHER_STRATEGY_SLIPPAGE_RATE", "0.0007")
	t.Setenv("STOCK_WATCHER_STRATEGY_MAX_ENTRY_GAP_PCT", "4")
	t.Setenv("STOCK_WATCHER_STRATEGY_MIN_EVIDENCE", "61")
	t.Setenv("STOCK_WATCHER_STRATEGY_MIN_TRAINING_DAYS", "41")
	t.Setenv("STOCK_WATCHER_STRATEGY_HOLDOUT_DAYS", "42")
	t.Setenv("STOCK_WATCHER_STRATEGY_MIN_RANK_SCORE", "61")
	t.Setenv("STOCK_WATCHER_MARKET_PROVIDERS", "tencent,sina")
	t.Setenv("STOCK_WATCHER_MARKET_REQUEST_INTERVAL", "2s")
	t.Setenv("STOCK_WATCHER_MARKET_ATTEMPT_TIMEOUT", "2m")
	t.Setenv("STOCK_WATCHER_MARKET_FAILURE_THRESHOLD", "4")
	t.Setenv("STOCK_WATCHER_MARKET_FAILURE_COOLDOWN", "6m")
	t.Setenv("STOCK_WATCHER_MARKET_TUSHARE_TOKEN_ENV", "TEST_TUSHARE_TOKEN")
	t.Setenv("TEST_TUSHARE_TOKEN", "test-token")
	t.Setenv("STOCK_WATCHER_MARKET_TENCENT_ENDPOINT", "https://example.com/tencent")
	t.Setenv("STOCK_WATCHER_MARKET_SINA_ENDPOINT", "https://example.com/sina")
	t.Setenv("STOCK_WATCHER_MARKET_SINA_DAILY_ENDPOINT", "https://example.com/sina-daily")
	t.Setenv("STOCK_WATCHER_MARKET_TUSHARE_ENDPOINT", "https://example.com/tushare")
	t.Setenv("STOCK_WATCHER_MARKET_EASTMONEY_ENDPOINT", "https://example.com/eastmoney")
	t.Setenv("STOCK_WATCHER_MARKET_LISTING_ENDPOINT", "https://example.com/listing")
	t.Setenv("STOCK_WATCHER_INTRADAY_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_INTRADAY_POLL_INTERVAL", "2m")
	t.Setenv("STOCK_WATCHER_INTRADAY_REQUEST_INTERVAL", "500ms")
	t.Setenv("STOCK_WATCHER_INTRADAY_WORKERS", "4")
	t.Setenv("STOCK_WATCHER_INTRADAY_CALIBRATION_AT", "15:20")
	t.Setenv("STOCK_WATCHER_INTRADAY_TENCENT_ENDPOINT", "https://example.com/minute")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != root || c.RequireCIFS || c.Feishu.Enabled || c.PoolFile != pool || c.Feishu.CredentialsFile != "" {
		t.Fatalf("identity environment not applied: %+v", c)
	}
	if len(c.Stocks) != 2 || c.Stocks[0].Code != "sz000001" || c.Stocks[1].ListedFrom != "1997-09-24" {
		t.Fatalf("stock pool file not applied: %+v", c.Stocks)
	}
	if c.PollInterval != "6m" || c.Timeout != "12s" || c.Analysis.Timeout != "11m" || c.Analysis.SyncTimeout != "2h" || c.StructureGuard.Mode != "enforce" {
		t.Fatalf("runtime environment not applied: %+v", c)
	}
	if !c.Web.Enabled || c.Web.Listen != "127.0.0.1:8089" || c.Daily.RunAt != "18:15" || c.Daily.HistoryDays != 500 || c.Daily.Adjust != "qfq" {
		t.Fatalf("web/daily environment not applied: %+v", c)
	}
	if c.Selection.Enabled || c.Selection.MinScore != 75 || c.Selection.TopN != 7 || c.Selection.MinVolumeRatio != 1.5 || c.Selection.RSIMin != 45 || c.Selection.RSIMax != 85 || c.Selection.MinDayChange != -2.5 || c.Selection.MaxDayChange != 6.5 {
		t.Fatalf("selection environment not applied: %+v", c.Selection)
	}
	if c.Sync.MinCoveragePct != 96 || c.Sync.MinAnalyzablePct != 91 || c.Sync.IncrementalBars != 6 || c.Sync.StatusRefreshDays != 31 {
		t.Fatalf("sync environment not applied: %+v", c.Sync)
	}
	if !c.Strategy.Enabled || c.Strategy.TargetNetPct != 2.5 || c.Strategy.StopNetPct != -2 || c.Strategy.MaxHoldingDays != 4 || c.Strategy.TradeCash != 25000 || c.Strategy.MinEvidence != 61 || c.Strategy.HoldoutDays != 42 {
		t.Fatalf("strategy environment not applied: %+v", c.Strategy)
	}
	if len(c.Market.Providers) != 2 || c.Market.FailureThreshold != 4 || c.Market.TushareTokenEnv != "TEST_TUSHARE_TOKEN" || c.Market.TushareToken != "test-token" || c.Market.ListingEndpoint != "https://example.com/listing" || c.Market.SinaDailyEndpoint != "https://example.com/sina-daily" {
		t.Fatalf("market environment not applied: %+v", c.Market)
	}
	if c.Intraday.Enabled || c.Intraday.Workers != 4 || c.Intraday.CalibrationAt != "15:20" || c.Intraday.TencentEndpoint != "https://example.com/minute" {
		t.Fatalf("intraday environment not applied: %+v", c.Intraday)
	}
	if c.Calendar.ValidFrom != "2020-01-01" || c.Calendar.ValidThrough != "2099-12-31" || len(c.Calendar.Holidays) != 2 {
		t.Fatalf("calendar environment not applied: %+v", c.Calendar)
	}
}

func TestEnvironmentNotifyChannels(t *testing.T) {
	root := t.TempDir()
	pool := filepath.Join(root, "leaders.yaml")
	if err := os.WriteFile(pool, []byte("schema_version: 1\nstocks:\n  - code: sz000001\n    name: 平安银行\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STOCK_WATCHER_DATA_DIR", root)
	t.Setenv("STOCK_WATCHER_REQUIRE_CIFS", "false")
	t.Setenv("STOCK_WATCHER_POOL_FILE", pool)
	t.Setenv("STOCK_WATCHER_FEISHU_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_CALENDAR_VALID_FROM", "2020-01-01")
	t.Setenv("STOCK_WATCHER_CALENDAR_VALID_THROUGH", "2099-12-31")
	t.Setenv("STOCK_WATCHER_CALENDAR_HOLIDAYS", "")
	t.Setenv("TEST_FEISHU_WEBHOOK", "https://open.feishu.cn/open-apis/bot/v2/hook/env-only")
	t.Setenv("TEST_FEISHU_SECRET", "env-secret")
	t.Setenv("TEST_PREMARKET_EXECUTION_WEBHOOK", "https://open.feishu.cn/open-apis/bot/v2/hook/execution-env-only")
	t.Setenv("STOCK_WATCHER_NOTIFY_CHANNELS", "feishu-main|feishu|true|TEST_FEISHU_WEBHOOK|TEST_FEISHU_SECRET|selection;premarket-execution|feishu|true|TEST_PREMARKET_EXECUTION_WEBHOOK||premarket_execution")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	channels := c.NotificationChannels()
	if len(channels) != 2 || channels[0].Name != "feishu-main" || channels[0].Route != NotifyRouteSelection || channels[0].Webhook != os.Getenv("TEST_FEISHU_WEBHOOK") || channels[0].Secret != "env-secret" {
		t.Fatalf("channel environment not applied: %+v", channels)
	}
	if channels[1].Name != "premarket-execution" || channels[1].Route != NotifyRoutePremarketExecution || channels[1].Webhook != os.Getenv("TEST_PREMARKET_EXECUTION_WEBHOOK") {
		t.Fatalf("premarket execution route environment not applied: %+v", channels[1])
	}
}

func TestNotificationChannelRoutes(t *testing.T) {
	for _, route := range []string{NotifyRouteFirstStage, NotifyRouteSecondStage, NotifyRoutePremarketExecution, NotifyRouteSelection, NotifyRoutePredictorRealtime, NotifyRoutePredictorDayrange} {
		t.Setenv("TEST_"+strings.ToUpper(route)+"_WEBHOOK", "https://open.feishu.cn/open-apis/bot/v2/hook/"+strings.ReplaceAll(route, "_", "-")+"-test")
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	text := valid + `notify:
  channels:
    - name: first-stage
      type: feishu
      enabled: true
      route: first_stage
      webhook_env: TEST_FIRST_STAGE_WEBHOOK
    - name: selection
      type: feishu
      enabled: true
      route: selection
      webhook_env: TEST_SELECTION_WEBHOOK
    - name: second-stage
      type: feishu
      enabled: true
      route: second_stage
      webhook_env: TEST_SECOND_STAGE_WEBHOOK
    - name: premarket-execution
      type: feishu
      enabled: true
      route: premarket_execution
      webhook_env: TEST_PREMARKET_EXECUTION_WEBHOOK
    - name: predictor-realtime
      type: feishu
      enabled: true
      route: predictor_realtime
      webhook_env: TEST_PREDICTOR_REALTIME_WEBHOOK
    - name: predictor-dayrange
      type: feishu
      enabled: true
      route: predictor_dayrange
      webhook_env: TEST_PREDICTOR_DAYRANGE_WEBHOOK
`
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	for route, want := range map[string]string{
		NotifyRouteFirstStage:         "first-stage",
		NotifyRouteSecondStage:        "second-stage",
		NotifyRoutePremarketExecution: "premarket-execution",
		NotifyRouteSelection:          "selection",
		NotifyRoutePredictorRealtime:  "predictor-realtime",
		NotifyRoutePredictorDayrange:  "predictor-dayrange",
	} {
		names := c.ChannelNamesForRoute(route)
		if len(names) != 1 || names[0] != want {
			t.Fatalf("route %s = %v, want %s", route, names, want)
		}
	}
	if err := os.WriteFile(p, []byte(valid+`notify:
  channels:
    - name: invalid
      type: feishu
      enabled: false
      route: unsupported
`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "route") {
		t.Fatalf("invalid route accepted: %v", err)
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

func TestWechatDraftChannelRequiresCredentialsAndRoutesSecondStage(t *testing.T) {
	t.Setenv("STOCK_WATCHER_WECHAT_ENABLED", "true")
	t.Setenv("WECHAT_APP_ID", "wx-test-app")
	t.Setenv("WECHAT_APP_SECRET", "test-app-secret")
	t.Setenv("STOCK_WATCHER_WECHAT_AUTHOR", "Stock Watcher")
	p := filepath.Join(t.TempDir(), "config.yaml")
	text := valid + `first_stage:
  enabled: true
  push_enabled: true
  budget: 10
second_stage:
  enabled: true
  push_enabled: true
`
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Wechat.Enabled || c.Wechat.AppID != "wx-test-app" || c.Wechat.AppSecret != "test-app-secret" {
		t.Fatalf("wechat environment not applied: %+v", c.Wechat)
	}
	channels := c.NotificationChannels()
	if len(channels) != 1 || channels[0].Name != WechatDraftChannelName || channels[0].Type != "wechat" || channels[0].Route != NotifyRouteSecondStage {
		t.Fatalf("unexpected wechat channel: %+v", channels)
	}
	if got := c.ChannelNamesForRoute(NotifyRouteSecondStage); len(got) != 1 || got[0] != WechatDraftChannelName {
		t.Fatalf("second-stage route = %v", got)
	}
	if got := c.ChannelNamesForRoute(NotifyRouteSelection); len(got) != 0 {
		t.Fatalf("selection route unexpectedly contains %v", got)
	}

	t.Setenv("WECHAT_APP_SECRET", "")
	if _, err := Load(p); err == nil || strings.Contains(err.Error(), "test-app-secret") {
		t.Fatalf("missing secret error leaked or was accepted: %v", err)
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
