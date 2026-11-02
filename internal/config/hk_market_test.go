package config

import "testing"

func TestHKStockCodesStayOutsideMainlandPool(t *testing.T) {
	for _, code := range []string{"hk00700", "hk00001", "hk09988"} {
		if !ValidHKStockCode(code) || !ValidMarketStockCode(code) {
			t.Fatalf("HK code rejected: %s", code)
		}
		if ValidCode(code) {
			t.Fatalf("HK code accepted as a mainland stock: %s", code)
		}
	}
	for _, code := range []string{"00700", "HK00700", "hk700", "hk0070x", "hk123456", "sh600519"} {
		if ValidHKStockCode(code) {
			t.Fatalf("invalid HK code accepted: %s", code)
		}
	}
}

func TestHKMarketEndpointsRequireSecureTransport(t *testing.T) {
	m := DefaultMarket()
	if err := m.Validate(); err != nil {
		t.Fatalf("default HK endpoints rejected: %v", err)
	}
	m.HKEndpoint = "http://example.com/hk"
	if err := m.Validate(); err == nil {
		t.Fatal("plain HTTP HK daily endpoint accepted")
	}
	m = DefaultMarket()
	m.HKEndpointFallback = "http://example.com/hk-fallback"
	if err := m.Validate(); err == nil {
		t.Fatal("plain HTTP HK daily fallback endpoint accepted")
	}
	m = DefaultMarket()
	m.HKQuoteEndpoint = "http://example.com/quotes?redirect=1"
	if err := m.Validate(); err == nil {
		t.Fatal("query-bearing HK quote endpoint accepted")
	}
	m = DefaultMarket()
	m.HKUniverseEndpoint = "http://example.com/universe?redirect=1"
	if err := m.Validate(); err == nil {
		t.Fatal("query-bearing HK universe endpoint accepted")
	}
	m = DefaultMarket()
	m.HKUniverseFallback = "http://example.com/fallback"
	if err := m.Validate(); err == nil {
		t.Fatal("plain HTTP HK universe fallback accepted")
	}
}
