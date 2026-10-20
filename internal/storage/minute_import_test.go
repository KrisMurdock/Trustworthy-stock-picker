package storage

import (
	"context"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func minuteArchive() market.MinuteArchive {
	h := minuteHistory()
	return market.MinuteArchive{
		Version: 1, Code: h.Code, Name: h.Name, Source: "baostock", Period: 5, BasePeriod: 5,
		Adjust: "none", VolumeUnit: "shares", From: "2026-10-15", Through: "2026-10-15",
		FetchedAt: h.FetchedAt, Bars: append([]market.MinuteBar(nil), h.Bars...),
	}
}

func TestImportMinuteArchiveIsAdditiveAndConvertsVolume(t *testing.T) {
	ctx := context.Background()
	db, err := InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	archive := minuteArchive()
	stock := config.Stock{Code: archive.Code, Name: archive.Name}
	first, err := db.ImportMinuteArchive(ctx, stock, archive)
	if err != nil {
		t.Fatal(err)
	}
	if first.Inserted != 48 || first.Existing != 0 || first.Mismatch != 0 || first.DayBars["2026-10-15"] != 48 {
		t.Fatalf("first import = %+v", first)
	}
	var volume float64
	if err := db.db.QueryRow(`SELECT volume_raw FROM intraday_bar WHERE code=? ORDER BY end_unix LIMIT 1`, archive.Code).Scan(&volume); err != nil {
		t.Fatal(err)
	}
	if volume != 1 {
		t.Fatalf("main-board volume raw = %v, want 1 hand", volume)
	}
	counts, err := db.LoadMinuteDayCounts(ctx, archive.Code, archive.From, archive.Through)
	if err != nil || counts["2026-10-15"] != 48 {
		t.Fatalf("day counts = %+v, %v", counts, err)
	}
	second, err := db.ImportMinuteArchive(ctx, stock, archive)
	if err != nil {
		t.Fatal(err)
	}
	if second.Inserted != 0 || second.Existing != 48 || second.Mismatch != 0 || second.DayBars["2026-10-15"] != 48 {
		t.Fatalf("second import = %+v", second)
	}
}

func TestImportMinuteArchiveKeepsRealtimeOverlap(t *testing.T) {
	ctx := context.Background()
	db, err := InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	archive := minuteArchive()
	stock := config.Stock{Code: archive.Code, Name: archive.Name}
	if _, err := db.ImportMinuteArchive(ctx, stock, archive); err != nil {
		t.Fatal(err)
	}
	corrected := archive
	corrected.Bars = append([]market.MinuteBar(nil), archive.Bars...)
	corrected.Bars[0].Close = 11.5
	result, err := db.ImportMinuteArchive(ctx, stock, corrected)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mismatch != 1 || result.Existing != 47 || result.Inserted != 0 {
		t.Fatalf("overlap result = %+v", result)
	}
	var closeMicros int64
	if err := db.db.QueryRow(`SELECT close_micros FROM intraday_bar WHERE code=? ORDER BY end_unix LIMIT 1`, archive.Code).Scan(&closeMicros); err != nil {
		t.Fatal(err)
	}
	if closeMicros != 11_000_000 {
		t.Fatalf("realtime overlap overwritten: %d", closeMicros)
	}
}

func TestLoadDailyDates(t *testing.T) {
	ctx := context.Background()
	db, err := InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.db.Exec(`INSERT INTO stock(code,name,updated_at) VALUES('sz000001','测试',?)`, time.Now().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`INSERT INTO daily_bar(code,trade_date,adjust,open_micros,high_micros,low_micros,close_micros,volume_raw,volume_shares,data_field,source)
		VALUES('sz000001','2026-10-15','qfq',10000000,11000000,9000000,10500000,100,10000,'qfqday','tencent')`); err != nil {
		t.Fatal(err)
	}
	dates, err := db.LoadDailyDates(ctx, []string{"sz000001"}, "2026-10-01", "2026-10-31", "qfq")
	if err != nil || !dates["sz000001"]["2026-10-15"] || len(dates["sz000001"]) != 1 {
		t.Fatalf("dates = %+v, %v", dates, err)
	}
}
