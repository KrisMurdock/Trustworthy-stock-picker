package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIntradayConfiguration(t *testing.T) {
	for _, tc := range []struct {
		extra string
		ok    bool
	}{
		{"", true}, {"intraday:\n  enabled: false\n", true},
		{"intraday:\n  poll_interval: 1s\n", false}, {"intraday:\n  request_interval: 1ms\n", false},
		{"intraday:\n  calibration_at: '14:50'\n", false}, {"intraday:\n  workers: 50\n", false},
		{"intraday:\n  tencent_endpoint: http://example.com/test\n", false},
		{"intraday:\n  tencent_endpoint: https://u:secret@example.com/test\n", false},
		{"intraday:\n  tencent_endpoint: http://127.0.0.1:1234/test\n", true},
		{"intraday:\n  periods: [1]\n", false},
	} {
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(valid+tc.extra), 0600); err != nil {
			t.Fatal(err)
		}
		c, err := Load(p)
		if (err == nil) != tc.ok {
			t.Fatalf("%q: %v", tc.extra, err)
		}
		if tc.extra == "" && (!c.Intraday.Enabled || c.Intraday.Workers != 3) {
			t.Fatal("default minute behavior lost")
		}
	}
}
