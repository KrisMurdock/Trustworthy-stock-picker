package storage

import (
	"context"
	"database/sql"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusAndCalendarPersistence(t *testing.T) {
	root := t.TempDir()
	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, market.Shanghai)
	if err := db.SaveCalendar(ctx, "2026-10-09", "2026-10-13", "exchange", []string{"2026-10-09", "2026-10-12", "2026-10-13"}, now); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.CalendarCoverage(ctx, "2026-10-09", "2026-10-13"); err != nil || !ok {
		t.Fatalf("calendar coverage: %v %v", ok, err)
	}
	days, err := db.CalendarDays(ctx, "2026-10-09", "2026-10-13")
	if err != nil || len(days) != 3 || days[0] != "2026-10-09" || days[2] != "2026-10-13" {
		t.Fatalf("calendar days: %+v %v", days, err)
	}
	window := market.StatusWindow{Code: "sz000001", Start: "2026-10-09", End: "2026-10-13", Source: "baostock", FetchedAt: now, Full: true, Statuses: []market.SessionStatus{
		{Code: "sz000001", Date: "2026-10-09", State: market.SessionTraded, Source: "baostock", FetchedAt: now},
		{Code: "sz000001", Date: "2026-10-12", State: market.SessionSuspended, Source: "baostock", FetchedAt: now},
		{Code: "sz000001", Date: "2026-10-13", State: market.SessionTraded, Source: "baostock", FetchedAt: now},
	}}
	stock := config.Stock{Code: "sz000001", Name: "测试银行"}
	if err := db.SaveStatusWindow(ctx, stock, window); err != nil {
		t.Fatal(err)
	}
	statuses, err := db.LoadSessionStatuses(ctx, stock.Code, window.Start, window.End)
	if err != nil || len(statuses) != 3 || statuses["2026-10-12"].State != market.SessionSuspended {
		t.Fatalf("load statuses: %+v %v", statuses, err)
	}
	if ok, err := db.StatusCoverage(ctx, stock.Code, "2026-10-09", "2026-10-13"); err != nil || !ok {
		t.Fatalf("status coverage: %v %v", ok, err)
	}
	if ok, err := db.StatusCoverage(ctx, stock.Code, "2026-10-08", "2026-10-13"); err != nil || ok {
		t.Fatalf("status coverage accepted an unaudited prefix: %v %v", ok, err)
	}
	if _, err := db.StatusSyncEntries(ctx, stock.Code); err != nil {
		t.Fatal(err)
	}
}

func TestStatusFailureSplitsExistingCoverage(t *testing.T) {
	root := t.TempDir()
	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, market.Shanghai)
	stock := config.Stock{Code: "sz000001", Name: "测试银行"}
	window := market.StatusWindow{Code: stock.Code, Start: "2026-10-01", End: "2026-10-13", Source: "baostock", FetchedAt: now, Full: true}
	if err := db.SaveStatusWindow(ctx, stock, window); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordStatusFailure(ctx, stock, "2026-10-05", "2026-10-06", "market", "network"); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		start, end string
		want       bool
	}{
		{"2026-10-01", "2026-10-04", true},
		{"2026-10-05", "2026-10-06", false},
		{"2026-10-07", "2026-10-13", true},
		{"2026-10-01", "2026-10-13", false},
	} {
		got, err := db.StatusCoverage(ctx, stock.Code, tt.start, tt.end)
		if err != nil || got != tt.want {
			t.Fatalf("coverage %s..%s = %v, %v; want %v", tt.start, tt.end, got, err, tt.want)
		}
	}
	entries, err := db.StatusSyncEntries(ctx, stock.Code)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Status != "complete" || entries[1].Status != "failed" || entries[2].Status != "complete" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestSparseStatusWindowPreservesFullAnswers(t *testing.T) {
	root := t.TempDir()
	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, market.Shanghai)
	stock := config.Stock{Code: "sz000001", Name: "测试银行"}
	full := market.StatusWindow{Code: stock.Code, Start: "2026-10-01", End: "2026-10-13", Source: "baostock", FetchedAt: now, Full: true, Statuses: []market.SessionStatus{
		{Code: stock.Code, Date: "2026-10-01", State: market.SessionTraded, Source: "baostock", FetchedAt: now},
	}}
	if err := db.SaveStatusWindow(ctx, stock, full); err != nil {
		t.Fatal(err)
	}
	sparse := market.StatusWindow{Code: stock.Code, Start: "2026-10-05", End: "2026-10-06", Source: "tushare", FetchedAt: now, Statuses: []market.SessionStatus{
		{Code: stock.Code, Date: "2026-10-05", State: market.SessionSuspended, Source: "tushare", FetchedAt: now},
	}}
	if err := db.SaveStatusWindow(ctx, stock, sparse); err != nil {
		t.Fatal(err)
	}
	statuses, err := db.LoadSessionStatuses(ctx, stock.Code, full.Start, full.End)
	if err != nil || len(statuses) != 2 || statuses["2026-10-01"].State != market.SessionTraded || statuses["2026-10-05"].State != market.SessionSuspended {
		t.Fatalf("statuses = %+v, %v", statuses, err)
	}
	if ok, err := db.StatusCoverage(ctx, stock.Code, sparse.Start, sparse.End); err != nil || ok {
		t.Fatalf("sparse window claimed coverage: %v %v", ok, err)
	}
	if ok, err := db.StatusCoverage(ctx, stock.Code, full.Start, full.End); err != nil || ok {
		t.Fatalf("sparse window left the full range complete: %v %v", ok, err)
	}
	if ok, err := db.StatusCoverage(ctx, stock.Code, full.Start, "2026-10-04"); err != nil || !ok {
		t.Fatalf("sparse window discarded earlier coverage: %v %v", ok, err)
	}
	if ok, err := db.StatusCoverage(ctx, stock.Code, "2026-10-07", full.End); err != nil || !ok {
		t.Fatalf("sparse window discarded later coverage: %v %v", ok, err)
	}
}

func TestSparseStatusWindowWithoutFullAnswerHasNoCoverage(t *testing.T) {
	root := t.TempDir()
	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, market.Shanghai)
	stock := config.Stock{Code: "sz000001", Name: "测试银行"}
	window := market.StatusWindow{Code: stock.Code, Start: "2026-10-12", End: "2026-10-13", Source: "tushare", FetchedAt: now, Statuses: []market.SessionStatus{
		{Code: stock.Code, Date: "2026-10-13", State: market.SessionSuspended, Source: "tushare", FetchedAt: now},
	}}
	if err := db.SaveStatusWindow(ctx, stock, window); err != nil {
		t.Fatal(err)
	}
	statuses, err := db.LoadSessionStatuses(ctx, stock.Code, window.Start, window.End)
	if err != nil || len(statuses) != 1 || statuses["2026-10-13"].State != market.SessionSuspended {
		t.Fatalf("statuses = %+v, %v", statuses, err)
	}
	if ok, err := db.StatusCoverage(ctx, stock.Code, window.Start, window.End); err != nil || ok {
		t.Fatalf("sparse-only window claimed coverage: %v %v", ok, err)
	}
	entries, err := db.StatusSyncEntries(ctx, stock.Code)
	if err != nil || len(entries) != 1 || entries[0].Status != "partial" || entries[0].ErrorKind != "sparse_status_source" {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
}

func TestSparseStatusWindowFailsClosedAgainstExistingAnswer(t *testing.T) {
	root := t.TempDir()
	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, market.Shanghai)
	stock := config.Stock{Code: "sz000001", Name: "测试银行"}
	full := market.StatusWindow{Code: stock.Code, Start: "2026-10-13", End: "2026-10-13", Source: "baostock", FetchedAt: now, Full: true, Statuses: []market.SessionStatus{
		{Code: stock.Code, Date: "2026-10-13", State: market.SessionTraded, Source: "baostock", FetchedAt: now},
	}}
	if err := db.SaveStatusWindow(ctx, stock, full); err != nil {
		t.Fatal(err)
	}
	sparse := market.StatusWindow{Code: stock.Code, Start: "2026-10-13", End: "2026-10-13", Source: "tushare", FetchedAt: now.Add(time.Hour), Statuses: []market.SessionStatus{
		{Code: stock.Code, Date: "2026-10-13", State: market.SessionSuspended, Source: "tushare", FetchedAt: now.Add(time.Hour)},
	}}
	if err := db.SaveStatusWindow(ctx, stock, sparse); err != nil {
		t.Fatal(err)
	}
	statuses, err := db.LoadSessionStatuses(ctx, stock.Code, sparse.Start, sparse.End)
	if err != nil || statuses["2026-10-13"].State != market.SessionUnknown || statuses["2026-10-13"].Detail != "status_conflict" {
		t.Fatalf("conflict was not fail-closed: %+v, %v", statuses, err)
	}
}

func TestMarketDBMigratesVersionFourStatusColumns(t *testing.T) {
	root := t.TempDir()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(root, marketDBName))+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
		CREATE TABLE stock_sync (
			code TEXT NOT NULL,
			adjust TEXT NOT NULL,
			as_of TEXT NOT NULL,
			window_start TEXT NOT NULL DEFAULT '',
			window_hash TEXT NOT NULL DEFAULT '',
			bars INTEGER NOT NULL DEFAULT 0,
			data_field TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			fetched_at TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (code, adjust, as_of)
		);
		CREATE TABLE sync_run (
			id TEXT PRIMARY KEY,
			as_of TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT NOT NULL,
			status TEXT NOT NULL,
			coverage_pct REAL NOT NULL,
			error_kind TEXT NOT NULL DEFAULT ''
		);
		PRAGMA user_version=4;
	`)
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := OpenMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, column := range []string{"analyzable", "sessions", "suspended", "gaps"} {
		if !hasColumn(t, db, "stock_sync", column) {
			t.Fatalf("missing migrated column %s", column)
		}
	}
	if !hasColumn(t, db, "sync_run", "analyzable_pct") {
		t.Fatal("missing analyzable_pct")
	}
	var version int
	if err := db.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != marketSchemaVersion {
		t.Fatalf("version = %d, %v", version, err)
	}
}

func hasColumn(t *testing.T, db *MarketDB, table, column string) bool {
	t.Helper()
	rows, err := db.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var fallback sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &fallback, &pk); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	return false
}
