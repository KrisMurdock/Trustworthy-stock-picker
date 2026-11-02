package market_test

import (
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestOptionalFundamentalFieldsProduceLowConfidenceBand(t *testing.T) {
	snapshot := market.FundamentalSnapshot{
		Version: 1, Code: "sh600519", Source: "sina", FetchedAt: time.Now().In(market.Shanghai),
		EPS: 1, FinancialStatementBasis: "trailing_four_quarters",
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	estimate := fairvalue.Calculate(config.Stock{Code: "sh600519"}, 10, "2026-10-31", snapshot)
	if !estimate.Available() || estimate.BookValueAnchored || estimate.Confidence != "low" {
		t.Fatalf("optional-field snapshot did not produce a low-confidence PE-only band: %+v", estimate)
	}
}
