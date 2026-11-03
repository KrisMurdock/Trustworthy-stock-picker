package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/lowvaluation"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type lowValuationQuoteSource struct {
	quotes []lowvaluation.Quote
}

func (s lowValuationQuoteSource) LatestQuotes(context.Context, string) ([]lowvaluation.Quote, error) {
	return s.quotes, nil
}

type lowValuationFundamentalSource struct {
	snapshots map[string]market.FundamentalSnapshot
}

func (s lowValuationFundamentalSource) FetchFundamentals(_ context.Context, stock config.Stock) (market.FundamentalSnapshot, error) {
	snapshot := s.snapshots[stock.Code]
	snapshot.Code = stock.Code
	return snapshot, nil
}

func lowValuationTestConfig() config.Config {
	c := config.Defaults()
	c.RequireCIFS = false
	c.LowValuation.Enabled = true
	c.LowValuation.PushEnabled = true
	c.LowValuation.TopN = 2
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Calendar.Holidays = []string{"2026-10-01", "2026-10-02"}
	return c
}

func TestGenerateLowValuationRecommendationPersistsNextTradingDay(t *testing.T) {
	root := t.TempDir()
	if err := storage.EnsureRoot(root, false); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenAllMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot := market.FundamentalSnapshot{
		Version: 1, Source: "sina", FetchedAt: time.Date(2026, 10, 30, 18, 0, 0, 0, market.Shanghai),
		EPS: 1, EPSPrior: 0.9, BookValuePerShare: 10, GrowthKnown: true,
		FinancialStatementBasis: "trailing_four_quarters",
	}
	now := time.Date(2026, 10, 30, 19, 0, 0, 0, market.Shanghai)
	rec, err := generateLowValuationRecommendation(context.Background(), lowValuationTestConfig(), logging.Nop(), db,
		lowValuationQuoteSource{quotes: []lowvaluation.Quote{
			{Code: "sh600001", Name: "接近区间", PriceDate: "2026-10-30", Close: 13, Volume: 1000},
			{Code: "sh600002", Name: "深度折价", PriceDate: "2026-10-30", Close: 10, Volume: 1000},
		}},
		lowValuationFundamentalSource{snapshots: map[string]market.FundamentalSnapshot{
			"sh600001": snapshot, "sh600002": snapshot,
		}},
		map[string]string{"sh600001": "制造业", "sh600002": "制造业"}, "2026-10-30", 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Target != "2026-11-02" || rec.Selected != 2 || rec.Candidates[0].Code != "sh600001" {
		t.Fatalf("generated recommendation = %+v", rec)
	}
	artifact, err := db.LatestRecommendation(context.Background(), lowvaluation.Market)
	if err != nil {
		t.Fatal(err)
	}
	var stored lowvaluation.Recommendation
	if err := json.Unmarshal(artifact.Payload, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ID != rec.ID || stored.AsOf != "2026-10-30" || stored.Target != "2026-11-02" || stored.Selected != 2 {
		t.Fatalf("stored recommendation = %+v", stored)
	}
}

func TestGenerateLowValuationRecommendationDoesNotSaveCoverageFailure(t *testing.T) {
	root := t.TempDir()
	if err := storage.EnsureRoot(root, false); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenAllMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	quotes := make([]lowvaluation.Quote, 0, 100)
	for index := 0; index < 100; index++ {
		quotes = append(quotes, lowvaluation.Quote{Code: fmt.Sprintf("sh6%05d", index),
			Name: "测试", PriceDate: "2026-10-30", Close: 10, Volume: 1000})
	}
	_, err = generateLowValuationRecommendation(context.Background(), lowValuationTestConfig(), logging.Nop(), db,
		lowValuationQuoteSource{quotes: quotes}, lowValuationFundamentalSource{snapshots: map[string]market.FundamentalSnapshot{}},
		nil, "2026-10-30", 2, time.Date(2026, 10, 30, 19, 0, 0, 0, market.Shanghai))
	if err == nil {
		t.Fatal("coverage failure was accepted")
	}
	if _, err := db.LatestRecommendation(context.Background(), lowvaluation.Market); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("coverage failure saved an artifact: %v", err)
	}
}
