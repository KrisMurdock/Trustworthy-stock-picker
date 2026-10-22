package health

import (
	"context"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBusinessFreshnessAndReadinessAreSeparate(t *testing.T) {
	c := config.Defaults()
	c.DataDir = t.TempDir()
	c.RequireCIFS = false
	c.Daily.Adjust = "none"
	c.Intraday.Enabled = false
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "test"}}
	db, err := storage.InitMarket(c.DataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 16, 19, 0, 0, 0, market.Shanghai)
	empty, err := Read(context.Background(), c, db, now)
	if err != nil || !empty.Ready || empty.Business != "degraded" {
		t.Fatalf("empty readiness: %+v %v", empty, err)
	}
	h := market.History{Version: 1, Code: c.Stocks[0].Code, Name: "test", Source: "tencent", Adjust: "none", DataField: "day", VolumeUnit: "provider_raw", AsOf: "2026-10-16", FetchedAt: now, Bars: []market.Bar{{Date: "2026-10-16", Open: 10, High: 11, Low: 9, Close: 10, Volume: 1}}}
	report := selection.Report{Version: 1, ID: "friday", AsOf: h.AsOf, Target: "2026-10-19", Adjust: "none", GeneratedAt: now}
	if err := db.CommitSnapshot(context.Background(), storage.SyncRun{ID: "friday", AsOf: h.AsOf, StartedAt: now, FinishedAt: now, CoveragePct: 100}, []storage.SyncItem{{Stock: c.Stocks[0], Adjusted: &h, Status: "ok"}}, report); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(24 * time.Hour), now.Add(48 * time.Hour), time.Date(2026, 10, 19, 14, 59, 0, 0, market.Shanghai)} {
		got, err := Read(context.Background(), c, db, at)
		if err != nil || got.Business != "healthy" || got.ExpectedDate != "2026-10-16" {
			t.Fatalf("normal closure false alarm: %+v %v", got, err)
		}
	}
	stale, err := Read(context.Background(), c, db, now.Add(72*time.Hour))
	if err != nil || !stale.Ready || stale.Business != "degraded" || stale.ExpectedDate != "2026-10-19" {
		t.Fatalf("stale data hidden: %+v %v", stale, err)
	}
	// A startup refresh can publish today's data before the normal run time.
	h.AsOf = "2026-10-19"
	h.Bars[0].Date = h.AsOf
	h.FetchedAt = time.Date(2026, 10, 19, 15, 30, 0, 0, market.Shanghai)
	report.ID = "monday"
	report.AsOf = h.AsOf
	report.Target = "2026-10-20"
	report.GeneratedAt = h.FetchedAt
	if err := db.CommitSnapshot(context.Background(), storage.SyncRun{ID: "monday", AsOf: h.AsOf, StartedAt: h.FetchedAt, FinishedAt: h.FetchedAt, CoveragePct: 100}, []storage.SyncItem{{Stock: c.Stocks[0], Adjusted: &h, Status: "ok"}}, report); err != nil {
		t.Fatal(err)
	}
	early, err := Read(context.Background(), c, db, time.Date(2026, 10, 19, 16, 0, 0, 0, market.Shanghai))
	if err != nil || early.Business != "healthy" || early.ExpectedDate != "2026-10-19" {
		t.Fatalf("early published session ignored: %+v %v", early, err)
	}
	if _, err := os.Stat(filepath.Join(c.DataDir, "state/delivery.json")); !os.IsNotExist(err) {
		t.Fatal("health wrote a ledger")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Read(canceled, c, db, now); err == nil {
		t.Fatal("ignored canceled probe")
	}
	c.Calendar.ValidThrough = "2026-10-15"
	if _, err := Read(context.Background(), c, db, now); err == nil {
		t.Fatal("expired calendar ready")
	}
}
