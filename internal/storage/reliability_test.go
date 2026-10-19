package storage

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoricalWindowSurvivesRevisionAndSourceSwitch(t *testing.T) {
	ctx := context.Background()
	db, err := InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stock := config.Stock{Code: "sz000001", Name: "test"}
	original := dbHistory(stock.Code, "qfq", "qfqday")
	save := func(id string, h market.History) {
		t.Helper()
		if err := db.CommitMarketData(ctx, SyncRun{ID: id, AsOf: h.AsOf, StartedAt: h.FetchedAt, FinishedAt: h.FetchedAt, CoveragePct: 100}, []SyncItem{{Stock: stock, Adjusted: &h, Status: "ok"}}, "qfq"); err != nil {
			t.Fatal(err)
		}
	}
	save("original", original)
	revised := original
	revised.AsOf = "2026-10-14"
	revised.Bars = append([]market.Bar{}, original.Bars...)
	revised.Bars[0].Close = 10.1
	revised.Bars = append(revised.Bars, market.Bar{Date: revised.AsOf, Open: 10, High: 11, Low: 9, Close: 10, Volume: 1})
	for _, source := range []string{"tencent", "eastmoney"} {
		revised.Source = source
		save(source, revised)
		got, err := db.LoadHistories(ctx, []config.Stock{stock}, original.AsOf, "qfq")
		if err != nil {
			t.Fatal(err)
		}
		if got[0].Bars[0] != original.Bars[0] || got[0].Source != original.Source {
			t.Fatalf("old input mutated after %s revision: %+v", source, got[0])
		}
	}
}

func TestResearchMinuteRangeSurvivesLatestWindowRollover(t *testing.T) {
	ctx := context.Background()
	db, err := InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stock := config.Stock{Code: "sz000001", Name: "test"}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, market.Shanghai)
	var first, last string
	for day, n := start, 0; n < 15; day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		n++
		h := minuteHistory()
		delta := day.Sub(time.Date(2026, 10, 15, 0, 0, 0, 0, market.Shanghai))
		h.FetchedAt = h.FetchedAt.Add(delta)
		for i := range h.Bars {
			h.Bars[i].End = h.Bars[i].End.Add(delta)
		}
		if err := db.SaveMinutes(ctx, stock, h.Bars[47].End, h.FetchedAt, &h, "ok", "", true); err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = day.Format("2006-01-02")
		}
		last = day.Format("2006-01-02")
	}
	recent, err := db.LoadMinutes(ctx, stock, 5)
	if err != nil || len(recent.Bars) != 640 {
		t.Fatalf("display limit: %d %v", len(recent.Bars), err)
	}
	all, err := db.LoadMinutesRange(ctx, stock, first, last)
	if err != nil || len(all.Bars) != 720 {
		t.Fatalf("archive range: %d %v", len(all.Bars), err)
	}
	old, err := db.LoadMinutesRange(ctx, stock, first, first)
	if err != nil || len(old.Bars) != 48 {
		t.Fatalf("old day missing: %d %v", len(old.Bars), err)
	}
	if err := all.ValidateArchive(); err != nil {
		t.Fatal(err)
	}
	if err := all.Validate(); err == nil {
		t.Fatal("provider request limit was removed")
	}
	for _, dates := range [][2]string{{last, first}, {"bad", last}} {
		if _, err := db.LoadMinutesRange(ctx, stock, dates[0], dates[1]); err == nil {
			t.Fatal(fmt.Sprint("invalid range accepted", dates))
		}
	}
}

func TestLegacyWindowRejectsUndetectableRevision(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	h := dbHistory("sz000001", "qfq", "qfqday")
	stock := config.Stock{Code: h.Code, Name: h.Name}
	if err := db.CommitMarketData(ctx, SyncRun{ID: "legacy", AsOf: h.AsOf, StartedAt: h.FetchedAt, FinishedAt: h.FetchedAt}, []SyncItem{{Stock: stock, Adjusted: &h, Status: "ok"}}, "qfq"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DROP TABLE history_revision; DROP TABLE history_snapshot; DROP TABLE report_input; DROP TABLE report_archive; PRAGMA user_version=3;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = OpenMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.LoadHistories(ctx, []config.Stock{stock}, h.AsOf, "qfq"); err != nil {
		t.Fatal("unchanged legacy input rejected", err)
	}
	if _, err := db.db.Exec(`UPDATE daily_bar SET close_micros=10100000 WHERE trade_date='2026-10-12'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LoadHistories(ctx, []config.Stock{stock}, h.AsOf, "qfq"); err == nil {
		t.Fatal("revised legacy input relabeled as original")
	}
}

func TestUnsupportedLegacySchemaIsNotRelabeled(t *testing.T) {
	root := t.TempDir()
	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if reopened, err := OpenMarket(root, false); err == nil {
		reopened.Close()
		t.Fatal("unsupported schema 1 was relabeled as current")
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(root, "market.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal("unsupported schema was mutated", version, err)
	}
}
