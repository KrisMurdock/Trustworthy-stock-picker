package storage

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func hkMinuteHistoryForStorage(day string) market.HKMinuteHistory {
	date, _ := time.ParseInLocation("2006-01-02", day, market.Shanghai)
	history := market.HKMinuteHistory{
		Version: 1, Code: "hk00700", Name: "腾讯控股", Source: "eastmoney_hk", Period: 5, BasePeriod: 5,
		Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: date.Add(16*time.Hour + 15*time.Minute),
	}
	for _, session := range [][2]int{{570, 720}, {780, 960}} {
		for minute := session[0] + 5; minute <= session[1]; minute += 5 {
			price := float64(100 + len(history.Bars))
			history.Bars = append(history.Bars, market.MinuteBar{
				End:  time.Date(date.Year(), date.Month(), date.Day(), minute/60, minute%60, 0, 0, market.Shanghai),
				Open: price, High: price + 2, Low: price - 1, Close: price + 1, Volume: 100, Final: true,
			})
		}
	}
	return history
}

func TestHKMinuteSchemaMigratesWithoutChangingOtherMarket(t *testing.T) {
	root := t.TempDir()
	hk, err := OpenHKMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	stock := config.Stock{Code: "hk00700", Name: "腾讯控股"}
	daily := allMarketHistoryTest(stock.Code, "none", "2026-10-30")
	daily.Source = "tencent_hk"
	if err := hk.SaveHistory(context.Background(), stock, daily, true); err != nil {
		t.Fatal(err)
	}
	if _, err := hk.db.Exec("DROP TABLE intraday_bar; DROP TABLE intraday_sync; PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	if err := hk.Close(); err != nil {
		t.Fatal(err)
	}
	hk, err = OpenHKMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer hk.Close()
	var version int
	if err := hk.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatalf("HK schema = %d, %v", version, err)
	}
	if bars, err := hk.LoadBars(context.Background(), stock.Code, "none", "2026-10-30", "2026-10-30"); err != nil || len(bars) != 1 {
		t.Fatalf("migration lost daily data: %+v %v", bars, err)
	}
	aShares, err := OpenAllMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer aShares.Close()
	if err := aShares.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("A-share full-market schema changed: %d, %v", version, err)
	}
}

func TestHKMinuteStorageCorrectionsAndFailures(t *testing.T) {
	root := t.TempDir()
	db, err := OpenHKMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	stock := config.Stock{Code: "hk00700", Name: "腾讯控股"}
	history := hkMinuteHistoryForStorage("2026-11-02")
	if err := db.SaveHKMinutes(ctx, stock, "2026-11-02", "2026-11-02", history.FetchedAt, &history, "ok", "", 0); err != nil {
		t.Fatal(err)
	}
	for period, want := range map[int]int{5: 66, 15: 22, 30: 11, 60: 6} {
		snapshot, err := db.LoadHKMinutes(ctx, stock.Code, period, config.DefaultHKIntraday())
		if err != nil || len(snapshot.Bars) != want || snapshot.Sync.Status != "ok" {
			t.Fatalf("period %d = %+v, %v", period, snapshot, err)
		}
	}
	corrected := history
	corrected.Bars = append([]market.MinuteBar(nil), history.Bars...)
	corrected.FetchedAt = history.FetchedAt.Add(time.Minute)
	corrected.Bars[29].Close += 0.25
	if err := db.SaveHKMinutes(ctx, stock, "2026-11-02", "2026-11-02", corrected.FetchedAt, &corrected, "ok", "", 0); err != nil {
		t.Fatal(err)
	}
	hourly, err := db.LoadHKMinutes(ctx, stock.Code, 60, config.DefaultHKIntraday())
	if err != nil || hourly.Bars[2].Close != corrected.Bars[35].Close || hourly.Bars[2].Volume != 1200 {
		t.Fatalf("corrected aggregate: %+v %v", hourly, err)
	}
	stale := history
	stale.Bars = append([]market.MinuteBar(nil), history.Bars[29:30]...)
	if err := db.SaveHKMinutes(ctx, stock, "2026-11-02", "2026-11-02", corrected.FetchedAt.Add(time.Minute), &stale, "partial", "incomplete_session", 65); err != nil {
		t.Fatal(err)
	}
	native, err := db.LoadHKMinutes(ctx, stock.Code, 5, config.DefaultHKIntraday())
	if err != nil || native.Bars[29].Close != corrected.Bars[29].Close || len(native.Bars) != 66 {
		t.Fatalf("stale response overwrote data: %+v %v", native, err)
	}
	if err := db.SaveHKMinutes(ctx, stock, "2026-11-02", "2026-11-02", corrected.FetchedAt.Add(2*time.Minute), nil, "failed", "http_403", 0); err != nil {
		t.Fatal(err)
	}
	native, err = db.LoadHKMinutes(ctx, stock.Code, 5, config.DefaultHKIntraday())
	if err != nil || len(native.Bars) != 66 || native.Sync.Status != "failed" || native.Sync.Reason != "http_403" {
		t.Fatalf("failed request lost cached data: %+v %v", native, err)
	}
	view, err := OpenHKMarketView(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if err := view.SaveHKMinutes(ctx, stock, "2026-11-02", "2026-11-02", history.FetchedAt, &history, "ok", "", 0); err == nil {
		t.Fatal("read-only store accepted a write")
	}
	if _, err := view.LoadHKMinutes(ctx, "hk00005", 5, config.DefaultHKIntraday()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing stock: %v", err)
	}
}

func TestHKMinuteStorageBackfillAndValidation(t *testing.T) {
	db, err := OpenHKMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	stock := config.Stock{Code: "hk00700", Name: "腾讯控股"}
	current := hkMinuteHistoryForStorage("2026-11-02")
	if err := db.SaveHKMinutes(ctx, stock, "2026-11-02", "2026-11-02", current.FetchedAt, &current, "ok", "", 0); err != nil {
		t.Fatal(err)
	}
	older := hkMinuteHistoryForStorage("2026-10-30")
	older.FetchedAt = current.FetchedAt.Add(time.Minute)
	if err := db.SaveHKMinutes(ctx, stock, "2026-10-30", "2026-10-30", older.FetchedAt, &older, "ok", "", 0); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.LoadHKMinutes(ctx, stock.Code, 5, config.DefaultHKIntraday())
	if err != nil || len(snapshot.Bars) != 132 || snapshot.Bars[0].End.Format("2006-01-02") != "2026-10-30" || snapshot.Bars[131].End.Format("2006-01-02") != "2026-11-02" {
		t.Fatalf("backfill lost data: %+v %v", snapshot, err)
	}
	invalid := current
	invalid.Source = "tencent"
	if err := db.SaveHKMinutes(ctx, stock, "2026-11-02", "2026-11-02", invalid.FetchedAt, &invalid, "ok", "", 0); err == nil {
		t.Fatal("invalid provider accepted")
	}
	if err := db.SaveHKMinutes(ctx, stock, "2026-11-02", "2026-11-02", current.FetchedAt, nil, "ok", "", 0); err == nil {
		t.Fatal("success without observations accepted")
	}
	if _, err := db.LoadHKMinutes(ctx, stock.Code, 1, config.DefaultHKIntraday()); err == nil {
		t.Fatal("unsupported period accepted")
	}
}

func TestHKMinuteFailureRemainsUnresolvedUntilSegmentedRepair(test *testing.T) {
	db, err := OpenHKMarket(test.TempDir(), false)
	if err != nil {
		test.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	stock := config.Stock{Code: "hk00700", Name: "腾讯控股"}
	current := hkMinuteHistoryForStorage("2026-11-02")
	attempt := current.FetchedAt
	if err := db.SaveHKMinutes(ctx, stock, "2026-01-02", "2026-03-31", attempt, nil, "failed", "http_403", 0); err != nil {
		test.Fatal(err)
	}
	assertState := func(status, reason, from, through string) {
		test.Helper()
		state, err := db.HKMinuteState(ctx, stock.Code)
		if err != nil || state.Status != status || state.Reason != reason || state.UnresolvedFrom != from || state.UnresolvedThrough != through {
			test.Fatalf("unexpected state: %+v, %v", state, err)
		}
	}
	assertState("failed", "http_403", "2026-01-02", "2026-03-31")
	saveSuccess := func(from, through, barDay string) {
		test.Helper()
		attempt = attempt.Add(time.Minute)
		history := hkMinuteHistoryForStorage(barDay)
		history.FetchedAt = attempt
		if err := db.SaveHKMinutes(ctx, stock, from, through, attempt, &history, "ok", "", 0); err != nil {
			test.Fatal(err)
		}
	}
	saveSuccess("2026-11-02", "2026-11-02", "2026-11-02")
	assertState("partial", "history_gap_unresolved", "2026-01-02", "2026-03-31")
	saveSuccess("2026-01-02", "2026-01-31", "2026-01-02")
	assertState("partial", "history_gap_unresolved", "2026-02-01", "2026-03-31")
	saveSuccess("2026-03-02", "2026-03-10", "2026-03-02")
	assertState("partial", "history_gap_unresolved", "2026-02-01", "2026-03-31")
	saveSuccess("2026-03-16", "2026-03-31", "2026-03-16")
	assertState("partial", "history_gap_unresolved", "2026-02-01", "2026-03-15")
	saveSuccess("2026-02-02", "2026-03-16", "2026-02-02")
	assertState("partial", "history_gap_unresolved", "2026-02-01", "2026-02-01")
	saveSuccess("2026-02-01", "2026-02-02", "2026-02-02")
	assertState("ok", "", "", "")
}
