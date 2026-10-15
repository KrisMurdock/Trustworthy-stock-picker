package storage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"stock-watcher/internal/config"
	"stock-watcher/internal/experiment"
	"stock-watcher/internal/market"
	"stock-watcher/internal/strategy"
	"testing"
	"time"
)

func TestLegacyExperimentReviewDoesNotRewriteArchive(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := OpenExperiments(ctx, root, false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot, r := experimentFixture(t)
	r.Engine = "development_top_n_v1"
	if err = db.Start(ctx, snapshot, r); err != nil {
		t.Fatal(err)
	}
	r.Status, r.Verdict = "completed", "promising"
	r.Result = &experiment.Results{Development: strategy.DevelopmentResult{
		Research: strategy.Research{Development: strategy.Evaluation{TopRanked: strategy.Stats{Samples: 2, Resolved: 1, Unresolved: 1, WorstNet: -3.2}}},
		Trades: []strategy.ExperimentTrade{
			{ID: "loss", Selected: true, Date: "2026-01-05", Outcome: strategy.Outcome{EntryDate: "2026-01-06", ExitDate: "2026-01-07", Status: "stop_gap", NetPct: -3.2}},
			{ID: "censored", Selected: true, Date: "2026-01-05", Outcome: strategy.Outcome{EntryDate: "2026-01-06", ExitDate: "2026-01-07", Status: "corporate_action", NetPct: -90}},
		},
	}}
	if err = db.Finish(ctx, r); err != nil {
		t.Fatal(err)
	}
	var before, after []byte
	if err = db.db.QueryRowContext(ctx, "SELECT payload FROM experiment_run WHERE id=?", r.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	view := ExperimentView{Root: root}
	items, err := view.List(ctx)
	if err != nil || len(items) != 1 || items[0].Review == nil || items[0].Review.Risk.Status != "breached" || items[0].Review.Risk.ExactCount {
		t.Fatal("legacy summary treated as safe", err, items)
	}
	got, err := view.Get(ctx, r.ID)
	if err != nil || got.Verdict != "promising" || got.Review == nil || got.Review.Risk.Breaches != 1 || !got.Review.Risk.ExactCount || got.Review.Risk.Unresolved != 1 || got.Review.Stats.WorstNet != -3.2 {
		t.Fatal("incorrect current review", err, got.Review)
	}
	if err = db.db.QueryRowContext(ctx, "SELECT payload FROM experiment_run WHERE id=?", r.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("read-time review rewrote archived evidence")
	}
}

func experimentFixture(t *testing.T) (experiment.Snapshot, experiment.Record) {
	t.Helper()
	c := config.Defaults()
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试"}, {Code: "sh600519", Name: "未保存"}}
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	h := dbHistory("sz000001", "qfq", "qfqday")
	r := h
	r.Adjust, r.DataField = "none", "day"
	s, err := experiment.NewSnapshot(c, h.AsOf, []market.History{h}, []market.History{r})
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := experiment.Specification("baseline")
	record, err := experiment.NewRecord(s, spec, nil, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	return s, record
}
func TestExperimentImmutableArchiveAndIndependentLock(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	marketWriter, err := Open(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer marketWriter.Close()
	db, err := OpenExperiments(ctx, root, false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if other, err := OpenExperiments(ctx, root, false, true); err == nil {
		other.Close()
		t.Fatal("two experiment writers")
	}
	snapshot, record := experimentFixture(t)
	if snapshot.Included != 1 || len(snapshot.Excluded) != 1 {
		t.Fatal("missing stock silently discarded")
	}
	if err = db.Start(ctx, snapshot, record); err != nil {
		t.Fatal(err)
	}
	view := ExperimentView{Root: root}
	list, err := view.List(ctx)
	if err != nil || len(list) != 1 || list[0].Status != "running" {
		t.Fatalf("live reader: %v %v", list, err)
	}
	now := time.Now()
	record.Status, record.Verdict, record.FinishedAt, record.Result = "completed", "baseline", &now, &experiment.Results{}
	if err = db.Finish(ctx, record); err != nil {
		t.Fatal(err)
	}
	if db.Finish(ctx, record) == nil || db.Start(ctx, snapshot, record) == nil {
		t.Fatal("completed record overwritten")
	}
	got, err := db.Snapshot(ctx, snapshot.ID)
	if err != nil || got.Adjusted[0].Bars[0] != snapshot.Adjusted[0].Bars[0] {
		t.Fatal("snapshot changed")
	}
	got.Raw[0].Bars[0].Volume++
	attempt, err := experiment.NewRecord(got, record.Spec, nil, record.CodeVersion)
	if err != nil {
		t.Fatal(err)
	}
	if db.Start(ctx, got, attempt) == nil {
		t.Fatal("changed data accepted under its original snapshot ID")
	}
	damaged, err := experimentJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.ExecContext(ctx, "UPDATE experiment_snapshot SET payload=? WHERE id=?", damaged, snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Snapshot(ctx, snapshot.ID); err == nil {
		t.Fatal("corrupted snapshot was readable")
	}
	read, err := view.Get(ctx, record.ID)
	if err != nil || read.Result == nil {
		t.Fatal("missing result")
	}
	list, err = view.List(ctx)
	if err != nil || list[0].Result != nil {
		t.Fatal("list leaked large result")
	}
	if _, err = view.Get(ctx, "../../credentials"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("unsafe ID")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = view.List(canceled); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestExperimentEmptyRecoveryAndUnsafeFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	view := ExperimentView{Root: root}
	list, err := view.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatal("empty archive not readable", err)
	}
	if _, err = os.Stat(filepath.Join(root, "experiments.db")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("reader created database")
	}
	db, err := OpenExperiments(ctx, root, false, true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, record := experimentFixture(t)
	if err = db.Start(ctx, snapshot, record); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = OpenExperiments(ctx, root, false, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Get(ctx, record.ID)
	db.Close()
	if err != nil || got.Status != "interrupted" {
		t.Fatal("interrupted run untracked")
	}
	for _, name := range []string{"experiments.db", ".experiments.lock", "experiments.db-journal"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(root, name)); err != nil {
				t.Fatal(err)
			}
			if db, err := OpenExperiments(ctx, root, false, true); err == nil {
				db.Close()
				t.Fatal("symlink accepted")
			}
		})
	}
	if db, err := OpenExperiments(ctx, t.TempDir(), true, true); err == nil {
		db.Close()
		t.Fatal("CIFS requirement bypassed")
	}
}
