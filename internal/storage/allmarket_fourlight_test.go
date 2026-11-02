package storage

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

func TestAllMarketFourLightSchemaMigratesV1AndPreservesRows(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, allMarketDBName)
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
		CREATE TABLE stock (code TEXT PRIMARY KEY, name TEXT NOT NULL, listed_from TEXT NOT NULL DEFAULT '', active INTEGER NOT NULL DEFAULT 1, updated_at TEXT NOT NULL);
		CREATE TABLE daily_bar (code TEXT NOT NULL REFERENCES stock(code), adjust TEXT NOT NULL, trade_date TEXT NOT NULL, open_micros INTEGER NOT NULL, close_micros INTEGER NOT NULL, high_micros INTEGER NOT NULL, low_micros INTEGER NOT NULL, volume_raw REAL NOT NULL, volume_shares INTEGER NOT NULL, data_field TEXT NOT NULL, source TEXT NOT NULL, PRIMARY KEY (code, adjust, trade_date));
		CREATE TABLE stock_sync (code TEXT NOT NULL REFERENCES stock(code), adjust TEXT NOT NULL, as_of TEXT NOT NULL, window_start TEXT NOT NULL DEFAULT '', bars INTEGER NOT NULL DEFAULT 0, source TEXT NOT NULL DEFAULT '', data_field TEXT NOT NULL DEFAULT '', fetched_at TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', PRIMARY KEY (code, adjust, as_of));
		CREATE TABLE sync_run (id TEXT PRIMARY KEY, as_of TEXT NOT NULL, started_at TEXT NOT NULL, finished_at TEXT NOT NULL, status TEXT NOT NULL, total INTEGER NOT NULL DEFAULT 0, succeeded INTEGER NOT NULL DEFAULT 0, partial INTEGER NOT NULL DEFAULT 0, failed INTEGER NOT NULL DEFAULT 0, skipped INTEGER NOT NULL DEFAULT 0, error_kind TEXT NOT NULL DEFAULT '');
		INSERT INTO stock(code, name, listed_from, active, updated_at) VALUES('sz000001', '迁移股票', '1991-04-03', 1, '2026-10-13T00:00:00Z');
		INSERT INTO daily_bar(code, adjust, trade_date, open_micros, close_micros, high_micros, low_micros, volume_raw, volume_shares, data_field, source)
			VALUES('sz000001', 'qfq', '2026-10-13', 10000000, 10000000, 10000000, 10000000, 1000, 100000, 'qfqday', 'tencent');
		PRAGMA user_version=1;
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if view, err := OpenAllMarketView(root, false); err == nil {
		view.Close()
		t.Fatal("v1 database unexpectedly opened as a current read-only view")
	}
	db, err := OpenAllMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != allMarketSchemaVersion {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	stocks, err := db.ActiveStocks(context.Background())
	if err != nil || len(stocks) != 1 || stocks[0].Name != "迁移股票" {
		t.Fatalf("migration lost stock rows: %+v %v", stocks, err)
	}
	bars, err := db.LoadBarsThrough(context.Background(), "sz000001", "qfq", "2026-10-13", 10)
	if err != nil || len(bars.Bars) != 1 || bars.Source != "tencent" {
		t.Fatalf("migration lost bar rows: %+v %v", bars, err)
	}
}

func TestAllMarketFourLightRunIsIdempotentAndFailurePreservesSuccess(t *testing.T) {
	db, err := OpenAllMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	stocks := []config.Stock{
		{Code: "sz000001", Name: "首次"},
		{Code: "sh600519", Name: "持续"},
		{Code: "sz000002", Name: "退出"},
		{Code: "sz000004", Name: "未知"},
	}
	for _, stock := range stocks {
		h := allMarketHistoryTest(stock.Code, "qfq", "2026-10-12", "2026-10-13")
		h.Name = stock.Name
		if err := db.SaveHistory(ctx, stock, h, true); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 13, 18, 40, 0, 0, time.UTC)
	first := fourLightRunFixture("run-1", now, stocks)
	if err := db.StartFourLightRun(ctx, first); err != nil {
		t.Fatal(err)
	}
	records := []FourLightRecord{
		fourLightRecordFixture("sz000001", "首次", "first_all_red", "all_red", "ready", first.ID, now),
		fourLightRecordFixture("sh600519", "持续", "continuing_all_red", "all_red", "ready", first.ID, now),
		fourLightRecordFixture("sz000002", "退出", "exit_all_red", "non_all_red", "ready", first.ID, now),
		fourLightRecordFixture("sz000004", "未知", "unknown", "unknown", "unknown", first.ID, now),
	}
	first.Status, first.Ready, first.AllRed, first.FirstAllRed = "complete", 3, 2, 1
	first.ContinuingAllRed, first.ExitAllRed, first.NonAllRed, first.Unknown = 1, 1, 1, 1
	if err := db.CompleteFourLightRun(ctx, first, records); err != nil {
		t.Fatal(err)
	}
	summary, err := db.LatestFourLightSummary(ctx, "2026-10-13", "four_light_approx_v1")
	if err != nil || summary.Run.ID != "run-1" || len(summary.First) != 1 || len(summary.Continuing) != 1 || len(summary.Exited) != 1 || len(summary.Unknown) != 1 {
		t.Fatalf("summary = %+v, %v", summary, err)
	}

	failed := fourLightRunFixture("run-2", now.Add(time.Minute), stocks)
	if err := db.StartFourLightRun(ctx, failed); err != nil {
		t.Fatal(err)
	}
	failed.FinishedAt, failed.Status, failed.ErrorKind = now.Add(2*time.Minute), "failed", "canceled"
	if err := db.FailFourLightRun(ctx, failed); err != nil {
		t.Fatal(err)
	}
	summary, err = db.LatestFourLightSummary(ctx, "2026-10-13", "four_light_approx_v1")
	if err != nil || summary.Run.ID != "run-1" || len(summary.First) != 1 {
		t.Fatalf("failed rerun replaced successful records: %+v %v", summary, err)
	}

	replacement := fourLightRunFixture("run-3", now.Add(3*time.Minute), stocks)
	if err := db.StartFourLightRun(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	replacement.Status, replacement.Total, replacement.NonAllRed = "complete", 1, 1
	replacementRecords := []FourLightRecord{fourLightRecordFixture("sz000001", "首次", "non_all_red", "non_all_red", "ready", replacement.ID, now.Add(3*time.Minute))}
	if err := db.CompleteFourLightRun(ctx, replacement, replacementRecords); err != nil {
		t.Fatal(err)
	}
	var eventCount int
	if err := db.db.QueryRow("SELECT COUNT(*) FROM four_light_event WHERE as_of='2026-10-13' AND rule='four_light_approx_v1'").Scan(&eventCount); err != nil || eventCount != 0 {
		t.Fatalf("stale events retained after replacement: %d %v", eventCount, err)
	}
	history, err := db.FourLightHistory(ctx, "sz000001", "four_light_approx_v1", "2026-10-13", "2026-10-13")
	if err != nil || len(history) != 1 || history[0].Lifecycle != "non_all_red" || history[0].RunID != "run-3" {
		t.Fatalf("history = %+v, %v", history, err)
	}
	if _, err := db.FourLightHistory(ctx, "sh600519", "four_light_approx_v1", "2026-10-13", "2026-10-13"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing history error = %v", err)
	}
}

func fourLightRunFixture(id string, now time.Time, stocks []config.Stock) FourLightRun {
	return FourLightRun{ID: id, AsOf: "2026-10-13", Rule: "four_light_approx_v1", StartedAt: now, FinishedAt: now, Status: "running", Total: len(stocks)}
}

func fourLightRecordFixture(code, name, lifecycle, state, dataStatus, runID string, now time.Time) FourLightRecord {
	return FourLightRecord{AsOf: "2026-10-13", Rule: "four_light_approx_v1", Code: code, Name: name,
		PreviousTradeDate: "2026-10-12", DataStatus: dataStatus, State: state, Lifecycle: lifecycle,
		Trend: "red", Energy: "red", MidTerm: "red", ShortTerm: "red", BarCount: 80, LatestBarDate: "2026-10-13",
		Source: "tencent", DataField: "qfqday", Reason: "fixture", ComputedAt: now, RunID: runID}
}
