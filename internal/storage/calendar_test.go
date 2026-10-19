package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"stock-watcher/internal/calendar"
)

func calendarWindow(t *testing.T, source, from, through string, days []string) calendar.History {
	t.Helper()
	history, err := calendar.NewHistory(source, from, through, days, time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return history
}

func TestCalendarDBRoundTripCoverageAndFreeze(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := OpenCalendar(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if filepath.Base(db.Path()) != "calendar.db" {
		t.Fatalf("unexpected calendar path: %s", db.Path())
	}
	var version int
	if err := db.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != calendarSchemaVersion {
		t.Fatalf("wrong calendar schema version: %d %v", version, err)
	}
	january := calendarWindow(t, calendar.SourceBaoStock, "2026-01-01", "2026-01-31", []string{"2026-01-02", "2026-01-05"})
	february := calendarWindow(t, calendar.SourceTushare, "2026-02-01", "2026-02-28", []string{"2026-02-02", "2026-02-03"})
	if err := db.Save(ctx, january); err != nil {
		t.Fatal(err)
	}
	if err := db.Save(ctx, february); err != nil {
		t.Fatal(err)
	}
	if err := db.Save(ctx, january); err != nil {
		t.Fatal("idempotent save failed: ", err)
	}
	dataset, err := db.Load(ctx, "2026-01-01", "2026-02-28")
	if err != nil || len(dataset) != 2 {
		t.Fatalf("load failed: %d %v", len(dataset), err)
	}
	coverage, err := db.Coverage(ctx, "2026-01-01", "2026-02-28")
	if err != nil || !coverage.Complete {
		t.Fatalf("complete coverage rejected: %+v %v", coverage, err)
	}
	frozen, err := db.Freeze(ctx, "2026-01-01", "2026-02-28")
	if err != nil || frozen.Source != "baostock+tushare" || len(frozen.Days) != 4 {
		t.Fatalf("freeze failed: %+v %v", frozen, err)
	}
	if err := frozen.Validate(); err != nil {
		t.Fatal(err)
	}
	partial, err := db.Coverage(ctx, "2025-12-31", "2026-02-28")
	if err != nil || partial.Complete || partial.Status != calendar.CoveragePartial {
		t.Fatalf("partial coverage not reported: %+v %v", partial, err)
	}
	if _, err := db.Freeze(ctx, "2025-12-31", "2026-02-28"); err == nil {
		t.Fatal("partial calendar was frozen")
	} else {
		var coverageErr *calendar.CoverageError
		if !errors.As(err, &coverageErr) {
			t.Fatalf("wrong freeze error: %v", err)
		}
	}
	_ = db.Close()

	view, err := OpenCalendarView(root, false)
	if err != nil || !view.HasDatabase() {
		t.Fatalf("view open failed: %v", err)
	}
	defer view.Close()
	if _, err := view.Freeze(ctx, "2026-01-01", "2026-02-28"); err != nil {
		t.Fatal(err)
	}
	if err := view.Save(ctx, january); err == nil {
		t.Fatal("read-only view accepted a save")
	}
	missing, err := OpenCalendarView(t.TempDir(), false)
	if err != nil || missing.HasDatabase() {
		t.Fatalf("missing view should be empty: %v", err)
	}
	if _, err := missing.Load(ctx, "2026-01-01", "2026-01-31"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing cache did not report absence: %v", err)
	}
	coverage, err = missing.Coverage(ctx, "2026-01-01", "2026-01-31")
	if err != nil || coverage.Complete || coverage.Status != calendar.CoverageMissing {
		t.Fatalf("missing coverage semantics wrong: %+v %v", coverage, err)
	}
}

func TestCalendarDBDoesNotModifyMarketDB(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	marketDB, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	marketDB.Close()
	marketPath := filepath.Join(root, "market.db")
	before, err := os.ReadFile(marketPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenCalendar(root, false)
	if err != nil {
		t.Fatal(err)
	}
	history := calendarWindow(t, calendar.SourceBaoStock, "2026-01-01", "2026-01-31", []string{"2026-01-02"})
	if err := db.Save(ctx, history); err != nil {
		t.Fatal(err)
	}
	db.Close()
	after, err := os.ReadFile(marketPath)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("calendar cache modified market database")
	}
	if _, err := os.Stat(filepath.Join(root, "calendar.db")); err != nil {
		t.Fatal("calendar database was not created independently")
	}
}

func TestCalendarDBRejectsInvalidAndTamperedRows(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := OpenCalendar(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	history := calendarWindow(t, calendar.SourceBaoStock, "2026-01-01", "2026-01-31", []string{"2026-01-02"})
	tampered := history
	tampered.Digest = "bad"
	if err := db.Save(ctx, tampered); err == nil {
		t.Fatal("invalid calendar was saved")
	}
	if _, err := db.Load(ctx, "2026-01-01", "2026-01-31"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("invalid save left a row: %v", err)
	}
	if err := db.Save(ctx, history); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, "UPDATE calendar_history SET payload=? WHERE digest=?", []byte(`{}`), history.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Load(ctx, "2026-01-01", "2026-01-31"); err == nil {
		t.Fatal("tampered calendar row accepted")
	}
}
