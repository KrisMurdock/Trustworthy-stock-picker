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
