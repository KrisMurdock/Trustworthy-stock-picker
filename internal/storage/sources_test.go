package storage

import (
	"context"
	"fmt"
	"testing"

	"stock-watcher/internal/config"
)

func TestMultiSourceVolumePersistenceAndDashboard(t *testing.T) {
	ctx := context.Background()
	db, err := InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, source := range []string{"tencent", "sina", "baostock", "tushare", "eastmoney"} {
		h := dbHistory("sh688981", "none", "day")
		h.Source = source
		stock := config.Stock{Code: h.Code, Name: h.Name}
		run := SyncRun{ID: fmt.Sprintf("source-%d", i), AsOf: h.AsOf, StartedAt: h.FetchedAt, FinishedAt: h.FetchedAt, CoveragePct: 90}
		if err := db.CommitMarketData(ctx, run, []SyncItem{{Stock: stock, Adjusted: &h, Status: "ok"}}, "none"); err != nil {
			t.Fatal(err)
		}
		loaded, err := db.LoadDashboardHistory(ctx, stock, "none")
		if err != nil || loaded.Source != source || loaded.Bars[1].Volume != 200 {
			t.Fatalf("lost source or raw volume: %v %+v", err, loaded)
		}
		want := int64(200)
		if source == "tushare" || source == "eastmoney" {
			want = 20000
		}
		var shares int64
		if err := db.db.QueryRowContext(ctx, "SELECT volume_shares FROM daily_bar WHERE code=? AND adjust='none' AND trade_date=?", h.Code, h.AsOf).Scan(&shares); err != nil || shares != want {
			t.Fatalf("wrong shares for %s: got=%d want=%d err=%v", source, shares, want, err)
		}
		snapshot, err := db.LoadDashboard(ctx, []config.Stock{stock}, "none")
		if err != nil || snapshot.Stocks[0].Source != source || snapshot.Stocks[0].Bar.VolumeShares != float64(want) {
			t.Fatalf("wrong UI units: %+v %v", snapshot, err)
		}
	}
	if _, err := db.db.ExecContext(ctx, "UPDATE daily_bar SET source='tencent' WHERE trade_date='2026-10-12'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LoadDashboardHistory(ctx, config.Stock{Code: "sh688981", Name: "测试"}, "none"); err == nil {
		t.Fatal("silently read a mixed-source history")
	}
}
