package storage

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
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
	history, err := view.LoadBarsThrough(ctx, []string{"sh000001", "BK1036"}, "2026-10-19")
	if err != nil || len(history["sh000001"]) != 2 || history["BK1036"][1].Date != "2026-10-19" {
		t.Fatalf("load history: %v %+v", err, history)
	}
	if _, err := view.LoadBarsThrough(ctx, []string{"sh000001", "sh000001"}, "2026-10-19"); err == nil {
		t.Fatal("duplicate history code was accepted")
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

func TestContextMinuteRoundTrip(t *testing.T) {
	root := t.TempDir()
	db, err := InitContext(root, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	at := time.Date(2026, 10, 19, 19, 0, 0, 0, time.UTC)
	if err := db.SaveInstrument(ctx, ContextInstrument{Code: "sh000001", Name: "上证指数", Kind: "index", Category: "index", Source: "sina", UpdatedAt: at}, []ContextBar{
		{Code: "sh000001", Date: "2026-10-16", Open: 100, Close: 101, High: 102, Low: 99, Volume: 1000},
		{Code: "sh000001", Date: "2026-10-19", Open: 101, Close: 103, High: 104, Low: 100, Volume: 1200},
	}); err != nil {
		t.Fatal(err)
	}
	bars := make([]market.MinuteBar, 0, 48)
	day := time.Date(2026, 10, 19, 0, 0, 0, 0, market.Shanghai)
	for _, start := range []int{570, 780} {
		for minute := start + 5; minute <= start+120; minute += 5 {
			end := time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, market.Shanghai)
			bars = append(bars, market.MinuteBar{End: end, Open: 100, High: 102, Low: 99, Close: 101, Volume: 1000, Final: true})
		}
	}
	if err := db.SaveMinutes(ctx, "sh000001", bars); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMinutes(ctx, "BK1036", bars); err == nil {
		t.Fatal("board minute series was accepted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	view, err := OpenContextView(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	loaded, err := view.LoadMinutesThrough(ctx, []string{"sh000001"}, "2026-10-19")
	if err != nil || len(loaded["sh000001"]) != 48 {
		t.Fatalf("load minutes: %v %+v", err, loaded)
	}
	if loaded["sh000001"][0].End.Format("15:04") != "09:35" || loaded["sh000001"][47].End.Format("15:04") != "15:00" {
		t.Fatalf("unexpected minute bounds: %s %s", loaded["sh000001"][0].End.Format("15:04"), loaded["sh000001"][47].End.Format("15:04"))
	}
	if _, err := view.LoadMinutesThrough(ctx, []string{"sh000001", "sh000001"}, "2026-10-19"); err == nil {
		t.Fatal("duplicate minute code was accepted")
	}
}

func TestContextViewToleratesMissingMinuteTable(t *testing.T) {
	root := t.TempDir()
	db, err := InitContext(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec("DROP TABLE context_minute"); err != nil {
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
	minutes, err := view.LoadMinutesThrough(context.Background(), []string{"sh000001"}, "2026-10-19")
	if err != nil || len(minutes) != 0 {
		t.Fatalf("missing minute table was not tolerated: %v %+v", err, minutes)
	}
}

func TestContextSessionMinutesAndKoreaRoundTrip(t *testing.T) {
	root := t.TempDir()
	db, err := InitContext(root, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	at := time.Date(2026, 10, 19, 19, 0, 0, 0, time.UTC)
	if err := db.SaveInstrument(ctx, ContextInstrument{Code: "sh000001", Name: "上证指数", Kind: "index", Category: "index", Source: "sina", UpdatedAt: at}, []ContextBar{
		{Code: "sh000001", Date: "2026-10-16", Open: 100, Close: 101, High: 102, Low: 99, Volume: 1000},
		{Code: "sh000001", Date: "2026-10-19", Open: 101, Close: 103, High: 104, Low: 100, Volume: 1200},
	}); err != nil {
		t.Fatal(err)
	}
	var sessions []market.MinuteBar
	day := time.Date(2026, 10, 19, 0, 0, 0, 0, market.Shanghai)
	for _, minute := range []int{600, 630, 660, 690, 810, 840, 870, 900} {
		end := time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, market.Shanghai)
		sessions = append(sessions, market.MinuteBar{End: end, Open: 100, High: 102, Low: 99, Close: 101, Volume: 1000, Final: true})
	}
	if err := db.SaveSessionMinutes(ctx, "sh000001", sessions); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSessionMinutes(ctx, "BK1036", sessions); err == nil {
		t.Fatal("board session series was accepted")
	}
	korea := []market.KoreaBar{
		{Date: "2026-10-16", Open: 2600, High: 2620, Low: 2590, Close: 2610, Volume: 1000},
		{Date: "2026-10-19", Open: 2611, High: 2630, Low: 2600, Close: 2625, Volume: 1200},
	}
	if err := db.SaveKoreaBars(ctx, market.KoreaKOSPI, korea); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveKoreaBars(ctx, "NIKKEI", korea); err == nil {
		t.Fatal("unknown korea code was accepted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	view, err := OpenContextView(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	loaded, err := view.LoadSessionMinutesThrough(ctx, []string{"sh000001"}, "2026-10-19")
	if err != nil || len(loaded["sh000001"]) != 8 || loaded["sh000001"][0].End.Format("15:04") != "10:00" {
		t.Fatalf("load session minutes: %v %+v", err, loaded)
	}
	loadedKorea, err := view.LoadKoreaBarsThrough(ctx, []string{market.KoreaKOSPI}, "2026-10-19")
	if err != nil || len(loadedKorea[market.KoreaKOSPI]) != 2 || loadedKorea[market.KoreaKOSPI][1].Close != 2625 {
		t.Fatalf("load korea bars: %v %+v", err, loadedKorea)
	}
}

func TestContextHangSengAndKoreaMinuteRoundTrip(t *testing.T) {
	root := t.TempDir()
	db, err := InitContext(root, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hangSeng := []market.HangSengBar{
		{Date: "2026-10-16", Open: 25800, High: 26000, Low: 25700, Close: 25900, Volume: 1000},
		{Date: "2026-10-19", Open: 25910, High: 26100, Low: 25800, Close: 26010, Volume: 1200},
	}
	if err := db.SaveHangSengBars(ctx, hangSeng); err != nil {
		t.Fatal(err)
	}
	minutes := []market.KoreaMinuteBar{
		{End: time.Date(2026, 10, 19, 9, 0, 0, 0, market.Korea), Open: 2600, High: 2602, Low: 2599, Close: 2601, Volume: 100, Final: true},
		{End: time.Date(2026, 10, 19, 9, 1, 0, 0, market.Korea), Open: 2601, High: 2603, Low: 2600, Close: 2602, Volume: 120, Final: true},
	}
	if err := db.SaveKoreaMinutes(ctx, market.KoreaKOSPI, minutes); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveKoreaMinutes(ctx, "NIKKEI", minutes); err == nil {
		t.Fatal("unknown korea minute code was accepted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	view, err := OpenContextView(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	loadedHangSeng, err := view.LoadHangSengBarsThrough(ctx, "2026-10-19")
	if err != nil || len(loadedHangSeng) != 2 || loadedHangSeng[1].Close != 26010 {
		t.Fatalf("load hang seng: %v %+v", err, loadedHangSeng)
	}
	if filtered, err := view.LoadHangSengBarsThrough(ctx, "2026-10-16"); err != nil || len(filtered) != 1 {
		t.Fatalf("filter hang seng: %v %+v", err, filtered)
	}
	loadedMinutes, err := view.LoadKoreaMinutesThrough(ctx, []string{market.KoreaKOSPI}, "2026-10-19")
	if err != nil || len(loadedMinutes[market.KoreaKOSPI]) != 2 || loadedMinutes[market.KoreaKOSPI][1].End.Format("15:04") != "09:01" {
		t.Fatalf("load korea minutes: %v %+v", err, loadedMinutes)
	}
}
