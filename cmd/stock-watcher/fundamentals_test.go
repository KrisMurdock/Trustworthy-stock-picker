package main

import (
	"strings"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

func TestFundamentalsReportWindowCoversTenCompletedYears(t *testing.T) {
	from, through, err := fundamentalsReportWindow("2026-11-02")
	if err != nil {
		t.Fatal(err)
	}
	if from != "2016-01-01" || through != "2025-12-31" {
		t.Fatalf("unexpected window: %s..%s", from, through)
	}
	if _, _, err := fundamentalsReportWindow("not-a-date"); err == nil {
		t.Fatal("invalid as-of was accepted")
	}
}

func TestFundamentalsStocksKeepsPoolOrderAndRejectsUnknown(t *testing.T) {
	c := config.Config{Stocks: []config.Stock{{Code: "sh600519"}, {Code: "sz000001"}, {Code: "sh600036"}}}
	stocks, err := fundamentalsStocks(c, "sh600036,sh600519,sh600036")
	if err != nil {
		t.Fatal(err)
	}
	if len(stocks) != 2 || stocks[0].Code != "sh600036" || stocks[1].Code != "sh600519" {
		t.Fatalf("unexpected stocks: %+v", stocks)
	}
	if _, err := fundamentalsStocks(c, "sh600000"); err == nil || !strings.Contains(err.Error(), "not in the configured pool") {
		t.Fatalf("unknown code was accepted: %v", err)
	}
	if _, err := fundamentalsStocks(c, " , "); err == nil {
		t.Fatal("empty code list was accepted")
	}
}
