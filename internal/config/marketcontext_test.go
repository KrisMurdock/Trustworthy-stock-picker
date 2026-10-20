package config

import "testing"

func TestMarketContextDefaultsAndValidation(t *testing.T) {
	cfg := Defaults()
	if !cfg.MarketContext.Enabled || cfg.MarketContext.Workers < 1 || cfg.MarketContext.EastmoneyKlineEndpoint == "" {
		t.Fatalf("missing market context defaults: %+v", cfg.MarketContext)
	}
	if err := cfg.MarketContext.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := cfg.MarketContext
	bad.EastmoneyListEndpoint = "http://example.com/api"
	if err := bad.Validate(); err == nil {
		t.Fatal("remote HTTP endpoint accepted")
	}
	bad = cfg.MarketContext
	bad.RequestInterval = "100ms"
	if err := bad.Validate(); err == nil {
		t.Fatal("unsafe request interval accepted")
	}
	bad = cfg.MarketContext
	bad.Workers = 9
	if err := bad.Validate(); err == nil {
		t.Fatal("unsafe worker count accepted")
	}
	disabled := bad
	disabled.Enabled = false
	if err := disabled.Validate(); err != nil {
		t.Fatalf("disabled market context still validated endpoints: %v", err)
	}
}
