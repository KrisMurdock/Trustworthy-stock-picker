package storage

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"time"
)

func TestContextDatabaseRoundTrip(t *testing.T) {
	root := t.TempDir()
	db, err := InitContext(root, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	at := time.Date(2026, 10, 19, 19, 0, 0, 0, time.UTC)
	indices := []ContextInstrument{
		{Code: "sh000001", Name: "上证指数", Kind: "index", Category: "index", Source: "tencent", UpdatedAt: at},
		{Code: "BK1036", Name: "半导体", Kind: "board", Category: "industry,concept", Source: "eastmoney", UpdatedAt: at},
	}
	for _, instrument := range indices {
		bars := []ContextBar{
			{Code: instrument.Code, Date: "2026-10-16", Open: 100, Close: 101, High: 102, Low: 99, Volume: 1000, Amount: 2000},
			{Code: instrument.Code, Date: "2026-10-19", Open: 101, Close: 103, High: 104, Low: 100, Volume: 1200, Amount: 2400},
		}
		if err := db.SaveInstrument(ctx, instrument, bars); err != nil {
			t.Fatal(err)
		}
	}
	status := ContextSyncStatus{AsOf: "2026-10-19", StartedAt: at.Add(-time.Minute), FinishedAt: at, Status: "complete", Total: 2, Succeeded: 2}
	if err := db.SaveSyncStatus(ctx, status); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	view, err := OpenContextView(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	items, err := view.LoadInstruments(ctx)
	if err != nil || len(items) != 2 || items[0].LastBarDate != "2026-10-19" {
		t.Fatalf("load instruments: %v %+v", err, items)
	}
	bars, err := view.LoadRecentBars(ctx, "2026-10-19", 5)
	if err != nil || len(bars["BK1036"]) != 2 || bars["BK1036"][1].Close != 103 {
		t.Fatalf("load bars: %v %+v", err, bars)
	}
	selected, err := view.LoadRecentBarsForCodes(ctx, "2026-10-19", []string{"sh000001", "missing", "sh000001"}, 5)
	if err != nil || len(selected) != 1 || len(selected["sh000001"]) != 2 || selected["sh000001"][1].Close != 103 {
		t.Fatalf("load selected bars: %v %+v", err, selected)
	}
	limited, err := view.LoadRecentBarsForCodes(ctx, "2026-10-19", []string{"sh000001"}, 1)
	if err != nil || len(limited["sh000001"]) != 1 || limited["sh000001"][0].Date != "2026-10-19" {
		t.Fatalf("load recent selected bar: %v %+v", err, limited)
	}
	got, err := view.LoadSyncStatus(ctx)
	if err != nil || got.Status != "complete" || got.Succeeded != 2 {
		t.Fatalf("load status: %v %+v", err, got)
	}
	if err := view.SaveInstrument(ctx, indices[0], nil); err == nil {
		t.Fatal("read-only context database accepted a write")
	}
}

func TestContextDatabaseMissingAndInvalidVersion(t *testing.T) {
	root := t.TempDir()
	if _, err := OpenContextView(root, false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing context view: %v", err)
	}
	if _, err := OpenContext(root, false); err == nil {
		t.Fatal("opened uninitialized context database")
	}
	db, err := InitContext(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := OpenContextView(root, false); err == nil {
		t.Fatal("unsupported context schema was accepted")
	}
}
