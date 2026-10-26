package main

import (
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestAdjustedCacheCoversWindow(t *testing.T) {
	base := adjustedBarCache{Version: adjustedBarCacheVersion, From: "2023-10-26", Through: "2026-10-26", Adjust: "qfq", Source: "sina", Histories: map[string]market.History{"sh600000": {}}}
	if !adjustedCacheCovers(base, "2024-01-01", "2026-01-01") {
		t.Fatal("cache with a wider window must cover a narrower request")
	}
	if adjustedCacheCovers(base, "2023-01-01", "2026-10-26") {
		t.Fatal("cache must not cover an earlier start")
	}
	if adjustedCacheCovers(base, "2024-01-01", "2026-11-01") {
		t.Fatal("cache must not cover a later end")
	}
}

func TestAdjustedHistoryCoversStartAndEnd(t *testing.T) {
	history := market.History{
		Version: 1, Code: "sh600000", Name: "Test", Source: "sina", Adjust: "qfq",
		DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: "2026-10-26", FetchedAt: time.Now(),
		Bars: []market.Bar{{Date: "2024-01-02", Open: 10, High: 11, Low: 9, Close: 10.5, Volume: 100}},
	}
	if err := history.Validate(); err != nil {
		t.Fatalf("fixture is invalid: %v", err)
	}
	if !adjustedHistoryCovers(history, "2023-10-26", "2026-10-26") {
		t.Fatal("history within the start tolerance and end date was rejected")
	}
	if adjustedHistoryCovers(history, "2023-01-01", "2026-10-26") {
		t.Fatal("history starting too late for the requested window was accepted")
	}
	if adjustedHistoryCovers(history, "2023-10-26", "2026-10-27") {
		t.Fatal("history ending before the requested end was accepted")
	}
}

func TestExpectedStartsUsesLaterRawFirstDate(t *testing.T) {
	stocks := []config.Stock{{Code: "sh600000"}, {Code: "sz000001"}, {Code: "sh688999"}}
	raw := map[string][]market.Bar{
		"sh600000": {{Date: "2023-10-26"}},
		"sz000001": {{Date: "2025-01-02"}},
	}
	got := expectedStarts(stocks, "2023-10-26", raw)
	if got["sh600000"] != "2023-10-26" || got["sz000001"] != "2025-01-02" || got["sh688999"] != "" {
		t.Fatalf("unexpected expected starts: %+v", got)
	}
}
