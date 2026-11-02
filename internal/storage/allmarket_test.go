package storage

import (
	"context"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func allMarketHistoryTest(code, adjust string, dates ...string) market.History {
	h := market.History{Version: 1, Code: code, Name: "测试股票", Source: "tencent", Adjust: adjust,
		DataField: "day", VolumeUnit: "provider_raw", AsOf: dates[len(dates)-1], FetchedAt: time.Now().In(market.Shanghai)}
	if adjust != "none" {
		h.DataField = adjust + "day"
	}
	for i, date := range dates {
		h.Bars = append(h.Bars, market.Bar{Date: date, Open: 10 + float64(i), High: 11 + float64(i), Low: 9 + float64(i), Close: 10.5 + float64(i), Volume: 1000 + float64(i)})
	}
	return h
}

func TestAllMarketSaveHistoryAndStatusDoNotOverwriteSuccess(t *testing.T) {
	db, err := OpenAllMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	stock := config.Stock{Code: "sh600000", Name: "测试股票", ListedFrom: "1999-11-10"}
	raw := allMarketHistoryTest(stock.Code, "none", "2026-10-28", "2026-10-29", "2026-10-30")
	adjusted := allMarketHistoryTest(stock.Code, "qfq", "2026-10-28", "2026-10-29", "2026-10-30")
	if err := db.SaveHistory(ctx, stock, raw, true); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveHistory(ctx, stock, adjusted, true); err != nil {
		t.Fatal(err)
	}
	last, err := db.LastBarDate(ctx, stock.Code, "none")
	if err != nil || last != "2026-10-30" {
		t.Fatalf("last raw bar: %q %v", last, err)
	}
	status, err := db.SyncStatus(ctx, stock.Code, "none", "2026-10-30")
	if err != nil || status.Status != "ok" || status.Bars != 3 || status.Source != "tencent" {
		t.Fatalf("raw status: %+v %v", status, err)
	}
	bars, err := db.LoadBars(ctx, stock.Code, "qfq", "2026-10-28", "2026-10-30")
	if err != nil || len(bars) != 3 || bars[2].Close != 12.5 {
		t.Fatalf("loaded qfq bars: %+v %v", bars, err)
	}
	if err := db.RecordStatus(ctx, stock, "qfq", "2026-10-30", "failed", "forced"); err != nil {
		t.Fatal(err)
	}
	status, err = db.SyncStatus(ctx, stock.Code, "qfq", "2026-10-30")
	if err != nil || status.Status != "ok" {
		t.Fatalf("successful status was overwritten: %+v %v", status, err)
	}
	if err := db.RecordStatus(ctx, stock, "qfq", "2026-10-31", "failed", "forced"); err != nil {
		t.Fatal(err)
	}
	status, err = db.SyncStatus(ctx, stock.Code, "qfq", "2026-10-31")
	if err != nil || status.Status != "failed" || status.Reason != "forced" {
		t.Fatalf("failure status: %+v %v", status, err)
	}
}

func TestAllMarketReplaceRemovesOldBarsAndRunLifecycle(t *testing.T) {
	db, err := OpenAllMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	stock := config.Stock{Code: "sz000001", Name: "测试股票"}
	first := allMarketHistoryTest(stock.Code, "none", "2026-10-28", "2026-10-29", "2026-10-30")
	if err := db.SaveHistory(ctx, stock, first, true); err != nil {
		t.Fatal(err)
	}
	replacement := allMarketHistoryTest(stock.Code, "none", "2026-10-30")
	if err := db.SaveHistory(ctx, stock, replacement, true); err != nil {
		t.Fatal(err)
	}
	bars, err := db.LoadBars(ctx, stock.Code, "none", "2000-01-01", "2030-01-01")
	if err != nil || len(bars) != 1 || bars[0].Date != "2026-10-30" {
		t.Fatalf("replacement bars: %+v %v", bars, err)
	}
	started := time.Now().In(market.Shanghai)
	run := AllMarketRun{ID: "run-1", AsOf: "2026-10-30", StartedAt: started, FinishedAt: started, Total: 1}
	if err := db.StartRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	run.Status, run.Succeeded, run.FinishedAt = "complete", 1, time.Now().In(market.Shanghai)
	if err := db.FinishRun(ctx, run); err != nil {
		t.Fatal(err)
	}
}

func TestAllMarketRejectsSecondWriterAndMissingView(t *testing.T) {
	root := t.TempDir()
	first, err := OpenAllMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := OpenAllMarket(root, false); err == nil {
		second.Close()
		t.Fatal("second all-market writer unexpectedly acquired the lock")
	}
	missing := t.TempDir()
	view, err := OpenAllMarketView(missing, false)
	if err == nil {
		view.Close()
		t.Fatal("view unexpectedly opened a missing all-market database")
	}
}

func TestAllMarketStartRunMarksAbandonedRunStale(t *testing.T) {
	db, err := OpenAllMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	started := time.Now().In(market.Shanghai)
	first := AllMarketRun{ID: "run-1", AsOf: "2026-10-30", StartedAt: started, FinishedAt: started, Total: 1}
	if err := db.StartRun(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := AllMarketRun{ID: "run-2", AsOf: "2026-10-30", StartedAt: started.Add(time.Minute), FinishedAt: started.Add(time.Minute), Total: 1}
	if err := db.StartRun(ctx, second); err != nil {
		t.Fatal(err)
	}
	var status, kind string
	if err := db.db.QueryRow("SELECT status, error_kind FROM sync_run WHERE id='run-1'").Scan(&status, &kind); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || kind != "stale" {
		t.Fatalf("abandoned run not marked stale: status=%q kind=%q", status, kind)
	}
}
