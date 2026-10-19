package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"stock-watcher/internal/config"
	"stock-watcher/internal/logging"
	"stock-watcher/internal/market"
	"stock-watcher/internal/storage"
	"stock-watcher/internal/strategy"
	"testing"
	"time"
)

func TestExperimentCLIUsesSavedDataAndFrozenParent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	pool := filepath.Join(root, "leaders.yaml")
	if err := os.WriteFile(pool, []byte("schema_version: 1\nstocks:\n  - code: sz000001\n    name: 测试\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STOCK_WATCHER_DATA_DIR", root)
	t.Setenv("STOCK_WATCHER_REQUIRE_CIFS", "false")
	t.Setenv("STOCK_WATCHER_FEISHU_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_POOL_FILE", pool)
	t.Setenv("STOCK_WATCHER_CALENDAR_HOLIDAYS", "")
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	// A running market writer must not prevent independent experiment runs.
	writer, err := storage.Open(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	db, err := storage.InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	date, _ := time.Parse("2006-01-02", "2026-01-01")
	var bars []market.Bar
	for len(bars) < 190 {
		if date.Weekday() != time.Saturday && date.Weekday() != time.Sunday {
			i := float64(len(bars))
			p := 10 + i*.02 + .1*math.Sin(i)
			bars = append(bars, market.Bar{Date: date.Format("2006-01-02"), Open: p, Close: p + .02, High: p + .15, Low: p - .1, Volume: 1000})
		}
		date = date.AddDate(0, 0, 1)
	}
	h := market.History{Version: 1, Code: c.Stocks[0].Code, Name: "测试", Source: "tencent", Adjust: "qfq", DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: bars[len(bars)-1].Date, FetchedAt: time.Now(), Bars: bars}
	r := h
	r.Adjust, r.DataField = "none", "day"
	err = db.CommitMarketData(ctx, storage.SyncRun{ID: "cli-fixture", AsOf: h.AsOf, StartedAt: time.Now(), FinishedAt: time.Now(), CoveragePct: 100}, []storage.SyncItem{{Stock: c.Stocks[0], Adjusted: &h, Raw: &r, Status: "ok"}}, "qfq")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	marketPath := filepath.Join(root, "market.db")
	before, err := os.ReadFile(marketPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = run([]string{"experiment"}); err != nil {
		t.Fatal(err)
	}
	view := storage.ExperimentView{Root: root}
	items, err := view.List(ctx)
	if err != nil || len(items) != 1 || items[0].Status != "completed" {
		t.Fatalf("baseline was not archived: %v %v", items, err)
	}
	parent, err := view.Get(ctx, items[0].ID)
	if err != nil || parent.Result == nil || len(parent.Result.Development.Trades) == 0 {
		t.Fatalf("baseline evidence missing: %v", err)
	}
	original, _ := json.Marshal(parent)
	// A failed evaluation remains inspectable; validation errors create no runs.
	c.Strategy.HoldoutDays = 120
	if err = runExperiment(ctx, c, h.AsOf, "", "baseline", logging.Nop()); err == nil {
		t.Fatal("insufficient signal dates accepted")
	}
	items, err = view.List(ctx)
	if err != nil || len(items) != 2 || items[0].Status != "failed" || items[0].Error == "" {
		t.Fatal("evaluation failure was not archived", err)
	}
	after, err := os.ReadFile(marketPath)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("experiment modified market database")
	}
	// Parent reruns need only their frozen archive, even if the live DB is gone
	// and today's strategy parameters have changed.
	if err = os.Rename(marketPath, marketPath+".offline"); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{"upper-wick", "baseline"} {
		if err = runExperiment(ctx, c, "", parent.ID, policy, logging.Nop()); err != nil {
			t.Fatal(err)
		}
	}
	items, err = view.List(ctx)
	if err != nil || len(items) != 4 || items[0].ID == parent.ID || items[0].ParentID != parent.ID || items[0].Data.ID != parent.Data.ID {
		t.Fatal("rerun lost lineage or overwrote its parent", err)
	}
	rerun, _ := view.Get(ctx, items[0].ID)
	a, _ := json.Marshal(parent.Result.Development)
	b, _ := json.Marshal(rerun.Result.Development)
	if string(a) != string(b) {
		t.Fatal("identical frozen rerun changed its result")
	}
	unchanged, _ := view.Get(ctx, parent.ID)
	b, _ = json.Marshal(unchanged)
	if string(original) != string(b) {
		t.Fatal("parent changed after child runs")
	}
	if err = runExperiment(ctx, c, "", parent.ID, "next-day-baseline", logging.Nop()); err != nil {
		t.Fatal(err)
	}
	items, _ = view.List(ctx)
	nextDay, err := view.Get(ctx, items[0].ID)
	if err != nil || nextDay.BaselineID != nextDay.ID || nextDay.Result.Development.Research.Parameters.MaxHoldingDays != 1 || nextDay.Data.Parameters.MaxHoldingDays != 5 || nextDay.Result.Development.HoldoutFrom != parent.Result.Development.HoldoutFrom {
		t.Fatal("next-day CLI did not preserve snapshot and boundary", err)
	}
	if err = runExperiment(ctx, c, "", nextDay.ID, "next-day-low-volatility", logging.Nop()); err != nil {
		t.Fatal(err)
	}
	interval := &strategy.BacktestRange{From: nextDay.Result.Development.Trades[0].Date, Through: nextDay.Result.Development.Trades[5].Date}
	if err = run([]string{"experiment", "-parent", nextDay.ID, "-policy", "next-day-baseline", "-from", interval.From, "-through", interval.Through}); err != nil {
		t.Fatal(err)
	}
	items, _ = view.List(ctx)
	ranged, err := view.Get(ctx, items[0].ID)
	if err != nil || ranged.Spec.Range == nil || *ranged.Spec.Range != *interval || ranged.Result.Development.HoldoutFrom != "" {
		t.Fatal("requested range not persisted", err)
	}
	if err = runExperiment(ctx, c, "", ranged.ID, "next-day-low-volatility", logging.Nop()); err != nil {
		t.Fatal(err)
	}
	items, _ = view.List(ctx)
	inherited, err := view.Get(ctx, items[0].ID)
	if err != nil || inherited.Spec.Range == nil || *inherited.Spec.Range != *interval {
		t.Fatal("child lost parent range", err)
	}
	if err = run([]string{"experiment", "-parent", parent.ID, "-holdout"}); err != nil {
		t.Fatal(err)
	}
	items, _ = view.List(ctx)
	holdoutRecord := items[0]
	if holdoutRecord.Stage != "holdout" || holdoutRecord.ParentID != parent.ID || holdoutRecord.Holdout == nil || holdoutRecord.Holdout.Contaminated {
		t.Fatal("holdout CLI did not create a clean child record", holdoutRecord)
	}
	for _, args := range [][]string{
		{"experiment", "-policy", "unknown"},
		{"experiment", "-policy", "upper-wick"},
		{"experiment", "-policy", "next-day-low-volatility"},
		{"experiment", "-policy", "next-day-typo"},
		{"experiment", "-policy", "conditional-rsi"},
		{"experiment", "-policy", "conditional-unknown"},
		{"experiment", "-parent", parent.ID, "-policy", "conditional-macd"},
		{"experiment", "-parent", "invalid"},
		{"experiment", "-parent", parent.ID, "-as-of", "2026-01-01"},
		{"experiment", "-holdout"},
		{"experiment", "-parent", parent.ID, "-holdout", "-policy", "baseline"},
		{"experiment", "-parent", parent.ID, "-holdout", "-as-of", h.AsOf},
		{"experiment", "-parent", parent.ID, "-holdout", "-from", "2026-04-01", "-through", "2026-05-01"},
		{"sync", "-policy", "upper-wick"},
		{"sync", "-holdout"},
		{"experiment", "-config", "config.yaml"},
		{"experiment", "-from", "2026-04-01"},
		{"experiment", "-parent", nextDay.ID, "-from", "2026-05-02", "-through", "2026-05-01"},
		{"experiment", "-parent", nextDay.ID, "-from", h.AsOf, "-through", h.AsOf},
		{"experiment", "-parent", nextDay.ID, "-policy", "conditional-entry"},
		{"sync", "-from", "2026-04-01", "-through", "2026-05-01"},
	} {
		if err = run(args); err == nil {
			t.Fatalf("invalid command accepted: %v", args)
		}
	}
	items, _ = view.List(ctx)
	if len(items) != 9 {
		t.Fatal("validation error created an experiment")
	}
}
