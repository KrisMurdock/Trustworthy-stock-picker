package storage

import (
	"context"
	"errors"
	"io/fs"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"stock-watcher/internal/selection"
	"testing"
	"time"
)

func minuteHistory() market.MinuteHistory {
	h := market.MinuteHistory{Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: time.Date(2026, 10, 15, 15, 10, 0, 0, market.Shanghai)}
	for _, start := range []int{570, 780} {
		for i := 5; i <= 120; i += 5 {
			h.Bars = append(h.Bars, market.MinuteBar{End: time.Date(2026, 10, 15, (start+i)/60, (start+i)%60, 0, 0, market.Shanghai), Open: 10, High: 12, Low: 9, Close: 11, Volume: 100, Final: true})
		}
	}
	return h
}

func TestMinuteMigrationReadAndCorrection(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	daily := dbHistory("sz000001", "qfq", "qfqday")
	report := selection.Report{Version: 1, ID: "prior", AsOf: daily.AsOf, Target: "2026-10-14", GeneratedAt: daily.FetchedAt, Adjust: "qfq"}
	if err := db.CommitSnapshot(ctx, SyncRun{ID: "prior", AsOf: daily.AsOf, StartedAt: daily.FetchedAt, FinishedAt: daily.FetchedAt, CoveragePct: 100}, []SyncItem{{Stock: config.Stock{Code: daily.Code, Name: daily.Name}, Adjusted: &daily, Status: "ok"}}, report); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec("DROP TABLE intraday_bar; DROP TABLE intraday_sync; PRAGMA user_version=2;"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if view, err := OpenMarketView(root, false); err == nil {
		view.Close()
		t.Fatal("old schema accepted without migration")
	}
	db, err = OpenMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got, err := db.LoadReport(ctx, daily.AsOf); err != nil || got.ID != "prior" {
		t.Fatal("migration lost daily report", err)
	}
	h := minuteHistory()
	stock := config.Stock{Code: h.Code, Name: h.Name}
	target := h.Bars[47].End
	if err := db.SaveMinutes(ctx, stock, target, h.FetchedAt, &h, "ok", "", true); err != nil {
		t.Fatal(err)
	}
	for period, count := range map[int]int{5: 48, 15: 16, 30: 8, 60: 4} {
		snap, err := db.LoadMinutes(ctx, stock, period)
		if err != nil || len(snap.Bars) != count || snap.State.CalibratedDate != "2026-10-15" {
			t.Fatalf("period%d: %+v %v", period, snap, err)
		}
	}
	// Repeated overlap is a replacement, and native corrections propagate to
	// derived candles without doubling volume or losing earlier history.
	corrected := h
	corrected.Bars = append([]market.MinuteBar{}, h.Bars[36:]...)
	corrected.FetchedAt = h.FetchedAt.Add(time.Minute)
	corrected.Bars[11].Close = 11.5
	corrected.Bars[11].Volume = 200
	if err := db.SaveMinutes(ctx, stock, target, corrected.FetchedAt, &corrected, "ok", "", false); err != nil {
		t.Fatal(err)
	}
	snap, err := db.LoadMinutes(ctx, stock, 60)
	if err != nil || len(snap.Bars) != 4 || snap.Bars[3].Close != 11.5 || snap.Bars[3].Volume != 1300 {
		t.Fatalf("correction failed: %+v %v", snap, err)
	}
	if err := db.SaveMinutes(ctx, stock, target, corrected.FetchedAt.Add(time.Minute), nil, "failed", "http_403", false); err != nil {
		t.Fatal(err)
	}
	snap, err = db.LoadMinutes(ctx, stock, 5)
	if err != nil || len(snap.Bars) != 48 || snap.State.Status != "failed" || snap.Bars[47].Close != 11.5 {
		t.Fatal("failure replaced saved bars", err)
	}
	if err := db.SaveMinutes(ctx, stock, target, h.FetchedAt, &h, "ok", "", true); err == nil {
		t.Fatal("old observation overwrote correction")
	}
	view, err := OpenMarketView(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if err := view.SaveMinutes(ctx, stock, target, time.Now(), &h, "ok", "", false); err == nil {
		t.Fatal("read-only database wrote")
	}
	if _, err := view.LoadMinutes(ctx, config.Stock{Code: "sz000002"}, 5); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("missing stock not distinguished", err)
	}
	if _, err := view.LoadMinutes(ctx, stock, 1); err == nil {
		t.Fatal("one minute allowed")
	}
}

func TestMinuteAtomicValidationAndGapCorrection(t *testing.T) {
	ctx := context.Background()
	db, err := InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := minuteHistory()
	stock := config.Stock{Code: h.Code, Name: h.Name}
	target := h.Bars[47].End
	if err := db.SaveMinutes(ctx, stock, target, h.FetchedAt, &h, "ok", "", true); err != nil {
		t.Fatal(err)
	}
	h.Bars = append(h.Bars[:2:2], h.Bars[3:]...)
	h.FetchedAt = h.FetchedAt.Add(time.Minute)
	if err := db.SaveMinutes(ctx, stock, target, h.FetchedAt, &h, "ok", "", true); err == nil {
		t.Fatal("incomplete day calibrated")
	}
	if err := db.SaveMinutes(ctx, stock, target, h.FetchedAt, &h, "partial", "intraday_gaps", false); err != nil {
		t.Fatal(err)
	}
	snap, err := db.LoadMinutes(ctx, stock, 15)
	if err != nil || len(snap.Bars) != 15 {
		t.Fatal("removed base candle survived calibration overlap", err, len(snap.Bars))
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := db.SaveMinutes(canceled, stock, target, h.FetchedAt.Add(time.Minute), &h, "ok", "", false); err == nil {
		t.Fatal("canceled transaction committed")
	}
	h.Bars[0].High = 1
	if err := db.SaveMinutes(ctx, stock, target, h.FetchedAt.Add(time.Minute), &h, "ok", "", false); err == nil {
		t.Fatal("bad OHLC stored")
	}
	snap, err = db.LoadMinutes(ctx, stock, 5)
	if err != nil || snap.Bars[0].High != 12 {
		t.Fatal("validation corrupted stored history", err)
	}
	db.requireCIFS = true
	if err := db.SaveMinutes(ctx, stock, target, time.Now(), nil, "failed", "network", false); err == nil {
		t.Fatal("CIFS loss allowed local write")
	}
}

func TestStaleMinuteWindowKeepsNewerObservations(t *testing.T) {
	ctx := context.Background()
	db, e := InitMarket(t.TempDir(), false)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	h := minuteHistory()
	stock := config.Stock{Code: h.Code}
	target := h.Bars[47].End
	if e := db.SaveMinutes(ctx, stock, target, h.FetchedAt, &h, "ok", "", false); e != nil {
		t.Fatal(e)
	}
	h.Bars = h.Bars[:40]
	h.FetchedAt = h.FetchedAt.Add(time.Minute)
	if e := db.SaveMinutes(ctx, stock, target, h.FetchedAt, &h, "stale", "latest_bar_missing", false); e != nil {
		t.Fatal(e)
	}
	s, e := db.LoadMinutes(ctx, stock, 5)
	if e != nil || len(s.Bars) != 48 || !s.Bars[47].End.Equal(target) || s.State.Status != "stale" {
		t.Fatal("lagging provider erased newer bars", s, e)
	}
	h.FetchedAt = h.FetchedAt.Add(-2 * time.Minute)
	if e := db.SaveMinutes(ctx, stock, target, time.Now(), &h, "stale", "", false); e == nil {
		t.Fatal("older observation accepted")
	}
}
