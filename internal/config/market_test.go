package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestMarketConfigurationAndToken(t *testing.T) {
	t.Setenv("TUSHARE_TOKEN", "")
	t.Setenv("MY_TEST_TOKEN", "private-token-do-not-print")
	for _, tt := range []struct {
		name, text string
		ok         bool
	}{
		{"legacy_defaults", "", true},
		{"ordered", "providers: [eastmoney, baostock, tencent]", true},
		{"optional_token", "providers: [tushare, tencent]", true},
		{"only_tushare_missing", "providers: [tushare]", false},
		{"custom_token", "providers: [tushare]\n  tushare_token_env: MY_TEST_TOKEN", true},
		{"empty", "providers: []", false},
		{"duplicate", "providers: [tencent, tencent]", false},
		{"python_alias", "providers: [akshare]", false},
		{"token_in_yaml", "tushare_token: private-token-do-not-print", false},
		{"bad_interval", "request_interval: 0s", false},
		{"sina", "providers: [sina]", true},
		{"sina_daily_local", "sina_daily_endpoint: http://127.0.0.1:9999/kline", true},
		{"sina_daily_insecure", "sina_daily_endpoint: http://finance.sina.com.cn/kline", false},
		{"sina_daily_query", "sina_daily_endpoint: https://finance.sina.com.cn/kline?token=secret", false},
		{"listing_local", "listing_endpoint: http://127.0.0.1:9999/listing", true},
		{"listing_empty", "listing_endpoint: ''", false},
		{"listing_insecure", "listing_endpoint: http://finance.sina.com.cn", false},
		{"listing_credentials", "listing_endpoint: https://secret@finance.sina.com.cn", false},
		{"listing_query", "listing_endpoint: https://finance.sina.com.cn?token=secret", false},
		{"sina_insecure", "providers: [sina]\n  sina_endpoint: http://finance.sina.com.cn", false},
		{"timeout_missing_value", "attempt_timeout: ''", false},
		{"timeout_short", "attempt_timeout: 1s", false},
		{"timeout_long", "attempt_timeout: 6m", false},
		{"cooldown_short", "failure_cooldown: 1s", false},
		{"cooldown_long", "failure_cooldown: 2h", false},
		{"threshold_zero", "failure_threshold: 0", false},
		{"threshold_long", "failure_threshold: 21", false},
		{"invalid_env", "tushare_token_env: 'bad env'", false},
		{"url_credentials", "tushare_endpoint: https://secret@api.tushare.pro", false},
		{"url_query", "tushare_endpoint: https://api.tushare.pro?token=secret", false},
		{"insecure_token", "tushare_endpoint: http://api.tushare.pro", false},
		{"local_mock", "tushare_endpoint: http://127.0.0.1:9999", true},
		{"bad_tcp_port", "baostock_address: 'localhost:99999'", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			text := valid
			if tt.text != "" {
				text += "market:\n  " + tt.text + "\n"
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if (err == nil) != tt.ok {
				t.Fatalf("unexpected result: %v", err)
			}
			if err != nil && (strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private-token")) {
				t.Fatal("configuration error exposes credential")
			}
			if tt.name == "custom_token" {
				if c.Market.TushareToken == "" {
					t.Fatal("token was not loaded")
				}
				j, _ := json.Marshal(c.Market)
				y, _ := yaml.Marshal(c.Market)
				if strings.Contains(string(j)+string(y), c.Market.TushareToken) {
					t.Fatal("serialized token")
				}
			}
		})
	}
}
