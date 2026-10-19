package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"stock-watcher/internal/bottomstudy"
	"stock-watcher/internal/experiment"
	"stock-watcher/internal/storage"
	"testing"
)

func TestArchiveSpeedResultIsIndependentAndImmutable(t *testing.T) {
	path, _ := snapshotFixture(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s experiment.Snapshot
	if json.Unmarshal(data, &s) != nil {
		t.Fatal("bad fixture")
	}
	root := t.TempDir()
	pool := filepath.Join(root, "leaders.yaml")
	if err := os.WriteFile(pool, []byte("schema_version: 1\nstocks:\n  - code: sz000001\n    name: 测试\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STOCK_WATCHER_DATA_DIR", root)
	t.Setenv("STOCK_WATCHER_REQUIRE_CIFS", "false")
	t.Setenv("STOCK_WATCHER_FEISHU_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_POOL_FILE", pool)
	stats := bottomstudy.SpeedStats{ComparisonStats: bottomstudy.ComparisonStats{TradeStats: bottomstudy.TradeStats{Samples: 2, Entries: 2, Resolved: 2, Targets: 1, Rate: .5, WorstNet: -8, MeanDays: 8}}, SuccessDays: 2, PenalizedDays: 11.5}
	pair := bottomstudy.SpeedPair{ID: "winner", Policy: bottomstudy.TradePolicy{Rule: "any", Target: 3, Batches: 1, Horizon: 20}, Stock: bottomstudy.SpeedArm{Training: stats, Later: stats}, Combined: bottomstudy.SpeedArm{Training: stats, Later: stats}}
	report := bottomstudy.SpeedReport{Engine: bottomstudy.SpeedEngine, SnapshotID: s.ID, Options: bottomstudy.GridOptions{Options: bottomstudy.Options{From: "2026-01-01", Through: "2026-10-12"}, TrainThrough: "2026-06-30"}, Winners: []bottomstudy.SpeedWinner{{ID: pair.ID}}, Pairs: []bottomstudy.SpeedPair{pair}}
	ctx := context.Background()
	id, err := archiveSpeedResult(ctx, s, report, "stock", "context")
	if err != nil {
		t.Fatal(err)
	}
	view := storage.ExperimentView{Root: root}
	r, err := view.Get(ctx, id)
	if err != nil || r.Status != "completed" || r.ResultSnapshot == nil || r.Review != nil || r.Engine != bottomstudy.SpeedEngine {
		t.Fatal("archive cannot be displayed", err, r)
	}
	if r.ResultSnapshot.InputSHA["context"] != "context" || len(r.ResultSnapshot.Rows) != 4 {
		t.Fatal("lost provenance or matched groups")
	}
	second, err := archiveSpeedResult(ctx, s, report, "stock", "context")
	if err != nil || id == second {
		t.Fatal("rerun overwrote prior experiment", err)
	}
	old, err := view.Get(ctx, id)
	if err != nil || !reflect.DeepEqual(old, r) {
		t.Fatal("old result changed on rerun", err)
	}
	if _, err := os.Stat(filepath.Join(root, "market.db")); !os.IsNotExist(err) {
		t.Fatal("archive touched market store")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := archiveSpeedResult(canceled, s, report, "stock", "context"); err == nil {
		t.Fatal("cancellation ignored")
	}
}
