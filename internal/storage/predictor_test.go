package storage

import (
	"context"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestLoadPredictionTrainingWindows(t *testing.T) {
	root := t.TempDir()
	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `INSERT INTO stock(code,name,sector,active,updated_at) VALUES('sz000001','测试','',1,'2026-10-20T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 20, 0, 0, 0, 0, market.Shanghai)
	for _, item := range []struct {
		minute int
		final  int
	}{{575, 1}, {580, 1}, {585, 0}} {
		end := day.Add(time.Duration(item.minute) * time.Minute)
		if _, err := db.db.ExecContext(ctx, `INSERT INTO intraday_bar(code,end_unix,open_micros,high_micros,low_micros,close_micros,volume_raw,final) VALUES(?,?,?,?,?,?,?,?)`,
			"sz000001", end.Unix(), int64(10e6), int64(10.1e6), int64(9.9e6), int64(10e6), 100, item.final); err != nil {
			t.Fatal(err)
		}
	}
	for _, date := range []string{"2026-10-19", "2026-10-20"} {
		if _, err := db.db.ExecContext(ctx, `INSERT INTO daily_bar(code,adjust,trade_date,open_micros,close_micros,high_micros,low_micros,volume_raw,volume_shares,data_field,source) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			"sz000001", "qfq", date, int64(10e6), int64(10.2e6), int64(10.3e6), int64(9.8e6), 100, 100, "qfqday", "tencent"); err != nil {
			t.Fatal(err)
		}
	}

	minutes, err := db.LoadMinuteTrainingBars(ctx, []string{"sz000001"}, day.Add(570*time.Minute), day.Add(585*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(minutes["sz000001"]) != 2 {
		t.Fatalf("minute bars = %d, want 2 finalized bars", len(minutes["sz000001"]))
	}
	daily, err := db.LoadDailyTrainingBars(ctx, []string{"sz000001"}, "2026-10-01", "2026-10-20", "qfq")
	if err != nil {
		t.Fatal(err)
	}
	if len(daily["sz000001"]) != 2 || daily["sz000001"][1].Close != 10.2 {
		t.Fatalf("unexpected daily bars: %+v", daily["sz000001"])
	}
}
