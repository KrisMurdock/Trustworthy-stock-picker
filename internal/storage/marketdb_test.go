package storage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"stock-watcher/internal/selection"
	"testing"
	"time"
)

func dbHistory(code, adjust, field string) market.History {
	return market.History{
		Version: 1, Code: code, Name: "测试", Source: "tencent", Adjust: adjust,
		DataField: field, VolumeUnit: "provider_raw", AsOf: "2026-10-13",
		FetchedAt: time.Date(2026, 10, 13, 19, 0, 0, 0, market.Shanghai),
		Bars: []market.Bar{
			{Date: "2026-10-12", Open: 10.005, Close: 10.015, High: 10.2, Low: 9.8, Volume: 100},
			{Date: "2026-10-13", Open: 10.015, Close: 10.025, High: 10.3, Low: 9.9, Volume: 200},
		},
	}
}

func TestMarketDBCommitReadAndRollback(t *testing.T) {
	root := t.TempDir()
	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	report := selection.Report{Version: 1, ID: "event-1", AsOf: "2026-10-13", Target: "2026-10-14", GeneratedAt: time.Now(), Strategy: "leader_daily_v1", Adjust: "qfq"}
	h := dbHistory("sz000001", "qfq", "qfqday")
	run := SyncRun{ID: "run-1", AsOf: h.AsOf, StartedAt: h.FetchedAt.Add(-time.Minute), FinishedAt: h.FetchedAt, CoveragePct: 100}
	if err := db.CommitSnapshot(context.Background(), run, []SyncItem{{Stock: config.Stock{Code: h.Code, Name: h.Name}, Adjusted: &h, Status: "ok"}}, report); err != nil {
		t.Fatal(err)
	}
	histories, err := db.LoadHistories(context.Background(), []config.Stock{{Code: h.Code, Name: h.Name}}, h.AsOf, "qfq")
	if err != nil || len(histories) != 1 {
		t.Fatalf("load histories: %v %+v", err, histories)
	}
	if histories[0].Bars[0].Open != 10.005 || histories[0].Bars[1].Volume != 200 {
		t.Fatalf("unexpected normalized history: %+v", histories[0].Bars)
	}
	got, err := db.LoadReport(context.Background(), h.AsOf)
	if err != nil || got.ID != report.ID {
		t.Fatalf("load report: %v %+v", err, got)
	}
	if _, err := db.LatestRun(context.Background(), h.AsOf); err != nil {
		t.Fatal(err)
	}
	paired, raw, err := db.LoadHistoriesPair(context.Background(), []config.Stock{{Code: h.Code, Name: h.Name}}, h.AsOf, "qfq", false)
	if err != nil || len(paired) != 1 || len(raw) != 0 {
		t.Fatalf("read transaction pair failed: %v adjusted=%d raw=%d", err, len(paired), len(raw))
	}

	bad := h
	bad.Bars[1].High = 1
	if err := db.CommitSnapshot(context.Background(), SyncRun{ID: "run-bad", AsOf: h.AsOf, StartedAt: h.FetchedAt, FinishedAt: h.FetchedAt, CoveragePct: 100}, []SyncItem{{Stock: config.Stock{Code: h.Code, Name: h.Name}, Adjusted: &bad, Status: "ok"}}, report); err == nil {
		t.Fatal("invalid history committed")
	}
	if got, err := db.LoadReport(context.Background(), h.AsOf); err != nil || got.ID != report.ID {
		t.Fatalf("transaction rollback lost prior snapshot: %v %+v", err, got)
	}
}

func TestMarketDBReadOnlyViewAndMissingDatabase(t *testing.T) {
	root := t.TempDir()
	if _, err := OpenMarket(root, false); err == nil {
		t.Fatal("opened uninitialized market database")
	}
	view, err := OpenMarketView(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := view.LoadReport(context.Background(), "2026-10-13"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing db should fall back: %v", err)
	}
	_ = view.Close()

	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if db, err := OpenMarket(root, false); err != nil {
		t.Fatal(err)
	} else {
		_ = db.Close()
	}
	view, err = OpenMarketView(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if err := view.RecordFailedRun(context.Background(), SyncRun{}); err == nil {
		t.Fatal("read-only view wrote to database")
	}
}

func TestMarketDBSkipsIsolatedHistory(t *testing.T) {
	root := t.TempDir()
	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	report := selection.Report{
		Version:     1,
		ID:          "event-isolated",
		AsOf:        "2026-10-13",
		Target:      "2026-10-14",
		GeneratedAt: time.Now(),
		Adjust:      "qfq",
	}
	err = db.CommitSnapshot(context.Background(), SyncRun{
		ID: "run-isolated", AsOf: report.AsOf, StartedAt: time.Now(), FinishedAt: time.Now(), CoveragePct: 95,
	}, []SyncItem{{
		Stock:  config.Stock{Code: "sz000001", Name: "测试"},
		Status: "isolated",
		Reason: "http_403",
	}}, report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.LoadHistories(context.Background(), []config.Stock{{Code: "sz000001", Name: "测试"}}, report.AsOf, "qfq"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("isolated history should be skipped: %v", err)
	}
}

func TestMarketDBRejectsSymlink(t *testing.T) {
	target := t.TempDir()
	targetDB, err := InitMarket(target, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = targetDB.Close()
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(target, "market.db"), filepath.Join(root, "market.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenMarket(root, false); err == nil {
		t.Fatal("opened symlinked market database")
	}
	if _, err := OpenMarketView(root, false); err == nil {
		t.Fatal("opened symlinked market database view")
	}
}

func TestMarketDBViewRejectsUninitializedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "market.db"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenMarketView(root, false); err == nil {
		t.Fatal("opened an uninitialized SQLite file")
	}
}
