package config

import (
	"strings"
	"testing"
)

func TestHKIntradayConfiguration(t *testing.T) {
	base := DefaultHKIntraday()
	base.Enabled = true
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*HKIntraday){
		"poll interval":    func(c *HKIntraday) { c.PollInterval = "1s" },
		"request interval": func(c *HKIntraday) { c.RequestInterval = "1ms" },
		"workers":          func(c *HKIntraday) { c.Workers = 9 },
		"timeout":          func(c *HKIntraday) { c.CycleTimeout = "1s" },
		"missing calendar": func(c *HKIntraday) { c.ValidThrough = "" },
		"reversed calendar": func(c *HKIntraday) {
			c.ValidFrom, c.ValidThrough = "2026-12-31", "2026-01-01"
		},
		"invalid early close": func(c *HKIntraday) { c.EarlyCloses = []string{"2026-11-02", "2026-11-02"} },
		"out of range early close": func(c *HKIntraday) {
			c.EarlyCloses = []string{"2027-12-24"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid HK intraday configuration accepted")
			}
		})
	}
}

func TestHKIntradayAlwaysValidatesItsEndpoint(test *testing.T) {
	candidate := Defaults()
	candidate.Stocks = []Stock{{Code: "sz000001"}}
	candidate.LLM.Enabled = false
	candidate.Calendar.ValidFrom, candidate.Calendar.ValidThrough = "2026-01-01", "2026-12-31"
	candidate.HKIntraday.Enabled = true
	candidate.Market.Providers = []string{"tencent"}
	if err := candidate.Validate(); err != nil {
		test.Fatalf("valid configuration rejected: %v", err)
	}
	for _, endpoint := range []string{"", "http://example.com/api", "https://user:password@example.com/api", "https://example.com/api?token=secret", "https://example.com/api#fragment"} {
		candidate.Market.EastmoneyEndpoint = endpoint
		if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "eastmoney") {
			test.Fatalf("invalid HK minute endpoint accepted: %q, %v", endpoint, err)
		}
	}
	candidate.Market.EastmoneyEndpoint = "http://127.0.0.1:18080/minutes"
	if err := candidate.Validate(); err != nil {
		test.Fatalf("loopback fixture endpoint rejected: %v", err)
	}
}
