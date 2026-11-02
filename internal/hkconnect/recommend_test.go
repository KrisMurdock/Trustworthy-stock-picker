package hkconnect

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/hkrecommend"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

const recommendationTestAsOf = "2026-10-30"

func recommendationWeekdays(asOf string, count int) []string {
	end, _ := time.Parse("2006-01-02", asOf)
	dates := make([]string, 0, count)
	for day := end; len(dates) < count; day = day.AddDate(0, 0, -1) {
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			dates = append(dates, day.Format("2006-01-02"))
		}
	}
	for left, right := 0, len(dates)-1; left < right; left, right = left+1, right-1 {
		dates[left], dates[right] = dates[right], dates[left]
	}
	return dates
}

func recommendationHistory(code, name, asOf string, bullish bool, lastVolume float64) market.History {
	dates := recommendationWeekdays(asOf, 120)
	bars := make([]market.Bar, 0, len(dates))
	for index, date := range dates {
		close := 100 + float64(index)*0.35 + 2*math.Sin(float64(index)*0.7)
		if !bullish {
			close = 200 - float64(index)*0.4
		}
		bars = append(bars, market.Bar{Date: date, Open: close - 0.2, High: close + 0.5, Low: close - 0.5, Close: close, Volume: 1000})
	}
	last := len(bars) - 1
	if bullish {
		bars[last].Close = bars[last-1].Close + 0.8
	} else {
		bars[last].Close = bars[last-1].Close - 0.4
	}
	bars[last].Open = bars[last].Close - 0.2
	bars[last].High = bars[last].Close + 0.5
	bars[last].Low = bars[last].Close - 0.5
	bars[last].Volume = lastVolume
	return market.History{Version: 1, Code: code, Name: name, Source: "tencent_hk", Adjust: "qfq",
		DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: asOf,
		FetchedAt: time.Date(2026, 10, 30, 18, 55, 0, 0, market.Shanghai), Bars: bars}
}

func recommendationTestConfig(topN int) config.Config {
	c := config.Defaults()
	c.HKConnect.Enabled = true
	c.HKConnect.PushEnabled = true
	c.HKConnect.TopN = topN
	return c
}

func TestBuildRecommendationFiltersRanksAndKeepsStableIdentity(t *testing.T) {
	stocks := []config.Stock{
		{Code: "hk00001", Name: "弱势股"},
		{Code: "hk00002", Name: "停牌股"},
		{Code: "hk00003", Name: "高分股"},
		{Code: "hk00004", Name: "达标股"},
		{Code: "hk00005", Name: "缺数据股"},
	}
	weak := recommendationHistory("hk00001", "弱势股", recommendationTestAsOf, false, 1300)
	stale := recommendationHistory("hk00002", "停牌股", recommendationTestAsOf, true, 2000)
	stale.Bars = stale.Bars[:len(stale.Bars)-1]
	high := recommendationHistory("hk00003", "高分股", recommendationTestAsOf, true, 2000)
	low := recommendationHistory("hk00004", "达标股", recommendationTestAsOf, true, 1300)
	now := time.Date(2026, 10, 30, 18, 50, 0, 0, market.Shanghai)
	rec, err := BuildRecommendation(recommendationTestConfig(5), stocks, []market.History{weak, stale, high, low}, recommendationTestAsOf, "2026-11-02", now)
	if err != nil {
		t.Fatal(err)
	}
	if rec.UniverseTotal != 5 || rec.Eligible != 3 || rec.Passed != 2 || rec.Selected != 2 {
		t.Fatalf("recommendation counts = %+v", rec)
	}
	if len(rec.Candidates) != 2 || rec.Candidates[0].Code != "hk00003" || rec.Candidates[0].Rank != 1 || rec.Candidates[1].Rank != 2 {
		t.Fatalf("candidates = %+v", rec.Candidates)
	}
	if rec.Candidates[0].Metrics.Score < rec.Candidates[1].Metrics.Score {
		t.Fatalf("candidates are not ranked by score: %+v", rec.Candidates)
	}
	reasons := map[string]int{}
	for _, item := range rec.Filtered {
		reasons[item.Reason] = item.Count
	}
	for _, want := range []string{"missing_data", "stale_or_suspended", "no_bullish_alignment"} {
		if reasons[want] != 1 {
			t.Fatalf("filtered reason %s = %d: %+v", want, reasons[want], rec.Filtered)
		}
	}
	if rec.Candidates[0].Chart == nil || rec.Candidates[0].Chart.Code != rec.Candidates[0].Code {
		t.Fatalf("candidate chart is missing: %+v", rec.Candidates[0].Chart)
	}

	again, err := BuildRecommendation(recommendationTestConfig(5), stocks, []market.History{weak, stale, high, low}, recommendationTestAsOf, "2026-11-02", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID == "" || rec.ID != again.ID {
		t.Fatalf("recommendation identity is not stable: %q %q", rec.ID, again.ID)
	}

	limited, err := BuildRecommendation(recommendationTestConfig(1), stocks, []market.History{weak, stale, high, low}, recommendationTestAsOf, "2026-11-02", now)
	if err != nil {
		t.Fatal(err)
	}
	if limited.Selected != 1 || limited.Candidates[0].Code != "hk00003" || limited.Passed != 2 {
		t.Fatalf("top_n was not applied: %+v", limited)
	}
}

func TestGenerateRecommendationUsesNextHKSessionAndPersistsArtifact(t *testing.T) {
	root := t.TempDir()
	db, err := storage.OpenHKMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	stock := config.Stock{Code: "hk00700", Name: "腾讯控股"}
	history := recommendationHistory(stock.Code, stock.Name, recommendationTestAsOf, true, 2000)
	if err := db.SaveHistory(context.Background(), stock, history, true); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 30, 18, 50, 0, 0, market.Shanghai)
	rec, err := GenerateRecommendation(context.Background(), recommendationTestConfig(5), db, fakeTradingDays{days: []string{"2026-11-02"}}, recommendationTestAsOf, now)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Target != "2026-11-02" || rec.Selected != 1 || rec.Candidates[0].Code != stock.Code {
		t.Fatalf("generated recommendation = %+v", rec)
	}
	artifact, err := db.LatestRecommendation(context.Background(), hkrecommend.Market)
	if err != nil || artifact.ID != rec.ID || artifact.AsOf != rec.AsOf || artifact.Target != rec.Target {
		t.Fatalf("stored artifact = %+v, err=%v", artifact, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadLatestRecommendation(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != rec.ID || loaded.Target != rec.Target || loaded.Selected != 1 || loaded.Candidates[0].Code != stock.Code {
		t.Fatalf("loaded recommendation = %+v", loaded)
	}

	missing, err := LoadLatestRecommendation(context.Background(), t.TempDir(), false)
	if !errors.Is(err, fs.ErrNotExist) || missing.ID != "" {
		t.Fatalf("missing recommendation = %+v, err=%v", missing, err)
	}
}

func TestBuildRecommendationRejectsInvalidInputs(t *testing.T) {
	stocks := []config.Stock{{Code: "hk00700", Name: "腾讯控股"}}
	history := recommendationHistory("hk00700", "腾讯控股", recommendationTestAsOf, true, 2000)
	now := time.Date(2026, 10, 30, 18, 50, 0, 0, market.Shanghai)
	if _, err := BuildRecommendation(recommendationTestConfig(5), stocks, []market.History{history}, recommendationTestAsOf, "2026-10-30", now); err == nil || !strings.Contains(err.Error(), "dates") {
		t.Fatalf("target not after as-of error = %v", err)
	}
	duplicate := history
	if _, err := BuildRecommendation(recommendationTestConfig(5), stocks, []market.History{history, duplicate}, recommendationTestAsOf, "2026-11-02", now); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate history error = %v", err)
	}
	if _, err := BuildRecommendation(recommendationTestConfig(0), stocks, []market.History{history}, recommendationTestAsOf, "2026-11-02", now); err == nil || !strings.Contains(err.Error(), "top_n") {
		t.Fatalf("invalid top_n error = %v", err)
	}
}

type brokenTradingDays struct{}

func (brokenTradingDays) TradingDays(context.Context, string, string) ([]string, error) {
	return nil, errors.New("calendar unavailable")
}

func TestGenerateRecommendationFallsBackToNextWeekdayWhenFutureCalendarIsEmpty(t *testing.T) {
	db, err := storage.OpenHKMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stock := config.Stock{Code: "hk00700", Name: "腾讯控股"}
	if err := db.SaveHistory(context.Background(), stock, recommendationHistory(stock.Code, stock.Name, recommendationTestAsOf, true, 2000), true); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 30, 18, 50, 0, 0, market.Shanghai)
	for name, source := range map[string]TradingDaysSource{
		"empty": fakeTradingDays{},
		"error": brokenTradingDays{},
	} {
		t.Run(name, func(t *testing.T) {
			rec, err := GenerateRecommendation(context.Background(), recommendationTestConfig(5), db, source, recommendationTestAsOf, now)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Target != "2026-11-02" {
				t.Fatalf("target = %q, want next weekday 2026-11-02", rec.Target)
			}
		})
	}
}

func TestNextWeekdayTargetSkipsWeekend(t *testing.T) {
	for _, tc := range []struct{ asOf, want string }{
		{"2026-10-29", "2026-10-30"},
		{"2026-10-30", "2026-11-02"},
		{"2026-11-01", "2026-11-02"},
	} {
		got, err := nextWeekdayTarget(tc.asOf)
		if err != nil || got != tc.want {
			t.Fatalf("nextWeekdayTarget(%s) = %q, err=%v; want %q", tc.asOf, got, err, tc.want)
		}
	}
	if _, err := nextWeekdayTarget("invalid"); err == nil {
		t.Fatal("invalid as-of date accepted")
	}
}
