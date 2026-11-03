package storage

import (
	"context"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestHKMarketUsesIndependentDatabaseAndWriterLock(t *testing.T) {
	root := t.TempDir()
	aShares, err := OpenAllMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer aShares.Close()
	hk, err := OpenHKMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer hk.Close()
	if second, err := OpenHKMarket(root, false); err == nil {
		second.Close()
		t.Fatal("second HK market writer unexpectedly acquired the lock")
	}
	ctx := context.Background()
	stock := config.Stock{Code: "hk00700", Name: "腾讯控股"}
	h := allMarketHistoryTest(stock.Code, "none", "2026-10-28", "2026-10-29", "2026-10-30")
	h.Name = stock.Name
	h.Source = "tencent_hk"
	if err := hk.SaveHistory(ctx, stock, h, true); err != nil {
		t.Fatal(err)
	}
	bars, err := hk.LoadBars(ctx, stock.Code, "none", "2026-10-01", "2026-10-31")
	if err != nil || len(bars) != 3 || bars[2].Date != "2026-10-30" {
		t.Fatalf("HK bars: %+v %v", bars, err)
	}
	status, err := hk.SyncStatus(ctx, stock.Code, "none", "2026-10-30")
	if err != nil || status.Source != "tencent_hk" || status.Status != "ok" {
		t.Fatalf("HK status: %+v %v", status, err)
	}
	if _, err := aShares.LastBarDate(ctx, stock.Code, "none"); err == nil {
		t.Fatal("HK history leaked into the A-share market database")
	}
}

func TestHKMarketViewRequiresInitializedDatabase(t *testing.T) {
	missing := t.TempDir()
	if view, err := OpenHKMarketView(missing, false); err == nil {
		view.Close()
		t.Fatal("HK market view unexpectedly opened a missing database")
	}
	root := t.TempDir()
	db, err := OpenHKMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	view, err := OpenHKMarketView(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if _, err := view.LastBarDate(context.Background(), "hk00700", "none"); err == nil {
		t.Fatal("empty HK view unexpectedly returned a bar date")
	}
}

func TestHKMarketHistoryAcceptsTencentHKSource(t *testing.T) {
	db, err := OpenHKMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stock := config.Stock{Code: "hk00700", Name: "腾讯控股"}
	h := market.History{Version: 1, Code: stock.Code, Name: stock.Name, Source: "tencent_hk", Adjust: "none",
		DataField: "day", VolumeUnit: "provider_raw", AsOf: "2026-10-30", FetchedAt: time.Now().In(market.Shanghai)}
	h.Bars = []market.Bar{{Date: "2026-10-30", Open: 100, High: 101, Low: 99, Close: 100.5, Volume: 1000}}
	if err := db.SaveHistory(context.Background(), stock, h, true); err != nil {
		t.Fatalf("Tencent HK source rejected: %v", err)
	}
}

func TestHKMarketSchemaMigrationAddsRecommendationAndPreservesBars(t *testing.T) {
	root := t.TempDir()
	db, err := OpenHKMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	stock := config.Stock{Code: "hk00700", Name: "腾讯控股"}
	history := allMarketHistoryTest(stock.Code, "qfq", "2026-10-28", "2026-10-29", "2026-10-30")
	history.Name = stock.Name
	history.Source = "tencent_hk"
	history.DataField = "qfqday"
	if err := db.SaveHistory(context.Background(), stock, history, true); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.db.Exec("DROP TABLE recommendation"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.db.Exec("PRAGMA user_version=1"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenHKMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var version int
	if err := reopened.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatalf("schema version = %d, err=%v", version, err)
	}
	if _, err := reopened.db.Exec("SELECT market FROM recommendation LIMIT 1"); err != nil {
		t.Fatalf("recommendation table was not created: %v", err)
	}
	bars, err := reopened.LoadBars(context.Background(), stock.Code, "qfq", "2026-10-01", "2026-10-31")
	if err != nil || len(bars) != 3 || bars[2].Date != "2026-10-30" {
		t.Fatalf("migration lost bars: %+v %v", bars, err)
	}
}

func TestHKMarketRecommendationRoundTripAndSuccessfulHistoryFilter(t *testing.T) {
	db, err := OpenHKMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	okStock := config.Stock{Code: "hk00700", Name: "腾讯控股"}
	failedStock := config.Stock{Code: "hk00005", Name: "汇丰控股"}
	for _, stock := range []config.Stock{okStock, failedStock} {
		history := allMarketHistoryTest(stock.Code, "qfq", "2026-10-28", "2026-10-29", "2026-10-30")
		history.Name = stock.Name
		history.Source = "tencent_hk"
		history.DataField = "qfqday"
		if err := db.SaveHistory(ctx, stock, history, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.Exec("UPDATE stock_sync SET status='failed', reason='forced' WHERE code=? AND adjust='qfq' AND as_of='2026-10-30'", failedStock.Code); err != nil {
		t.Fatal(err)
	}
	stocks, histories, err := db.LoadAdjustedHistories(ctx, "2026-10-30", "2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(stocks) != 1 || stocks[0].Code != okStock.Code || len(histories) != 1 || histories[0].Adjust != "qfq" || histories[0].AsOf != "2026-10-30" || len(histories[0].Bars) != 3 {
		t.Fatalf("loaded recommendation histories = %+v %+v", stocks, histories)
	}

	artifact := RecommendationArtifact{Market: "hk_connect", AsOf: "2026-10-30", ID: "rec-1", Target: "2026-11-02",
		BuiltAt: time.Date(2026, 10, 30, 18, 50, 0, 0, market.Shanghai), Payload: []byte(`{"schema_version":1}`)}
	if err := db.SaveRecommendation(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	got, err := db.LatestRecommendation(ctx, "hk_connect")
	if err != nil || got.ID != artifact.ID || got.AsOf != artifact.AsOf || got.Target != artifact.Target || string(got.Payload) != string(artifact.Payload) {
		t.Fatalf("recommendation artifact = %+v, err=%v", got, err)
	}
	artifact.Target = "2026-11-03"
	if err := db.SaveRecommendation(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	got, err = db.LatestRecommendation(ctx, "hk_connect")
	if err != nil || got.Target != "2026-11-03" {
		t.Fatalf("recommendation was not updated: %+v %v", got, err)
	}
	if _, err := db.LatestRecommendation(ctx, "missing"); err == nil {
		t.Fatal("missing recommendation market unexpectedly returned an artifact")
	}
}
