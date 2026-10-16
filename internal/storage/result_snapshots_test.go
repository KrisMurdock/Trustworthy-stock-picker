package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"stock-watcher/internal/experiment"
	"stock-watcher/internal/strategy"
	"testing"
	"time"
)

func TestResultSnapshotsPersistAndLegacyReadsDoNotWrite(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := OpenExperiments(ctx, root, false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot, r := experimentFixture(t)
	if err = db.Start(ctx, snapshot, r); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r.Status, r.FinishedAt = "completed", &now
	r.Result = &experiment.Results{Development: strategy.DevelopmentResult{Research: strategy.Research{Parameters: snapshot.Parameters, Development: strategy.Evaluation{TopRanked: strategy.Stats{Samples: 1, Resolved: 1, WorstNet: -4, MeanDays: 2, HighTouchSamples: 1, HighTouch25: 1, HighTouch25Days: 1}}}}}
	if err = db.Finish(ctx, r); err != nil {
		t.Fatal(err)
	}
	get, err := db.Get(ctx, r.ID)
	if err != nil || get.ResultSnapshot == nil {
		t.Fatal("result not persisted", err)
	}
	list, err := db.List(ctx)
	if err != nil || !reflect.DeepEqual(get.ResultSnapshot, list[0].ResultSnapshot) {
		t.Fatal("list and detail differ", err)
	}
	// Simulate an existing database written before the additive snapshot field.
	var payload, summary []byte
	if err = db.db.QueryRowContext(ctx, "SELECT payload,summary FROM experiment_run WHERE id=?", r.ID).Scan(&payload, &summary); err != nil {
		t.Fatal(err)
	}
	remove := func(b []byte) []byte {
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) != nil {
			t.Fatal("bad fixture")
		}
		delete(m, "result_snapshot")
		out, _ := json.Marshal(m)
		return out
	}
	payload, summary = remove(payload), remove(summary)
	if _, err = db.db.ExecContext(ctx, "UPDATE experiment_run SET payload=?,summary=? WHERE id=?", payload, summary, r.ID); err != nil {
		t.Fatal(err)
	}
	view := ExperimentView{Root: root}
	legacy, err := view.Get(ctx, r.ID)
	if err != nil || !reflect.DeepEqual(legacy.ResultSnapshot, get.ResultSnapshot) {
		t.Fatal("legacy snapshot lost frozen metrics", err)
	}
	list, err = view.List(ctx)
	if err != nil || !reflect.DeepEqual(list[0].ResultSnapshot, get.ResultSnapshot) {
		t.Fatal("legacy list differs", err)
	}
	var after []byte
	if err = db.db.QueryRowContext(ctx, "SELECT payload FROM experiment_run WHERE id=?", r.ID).Scan(&after); err != nil || string(payload) != string(after) {
		t.Fatal("legacy read rewrote archive", err)
	}
	if db.Finish(ctx, r) == nil {
		t.Fatal("completed results overwritten")
	}
	get.ResultSnapshot.Rows[0].MaxLoss = experiment.Value(100.0)
	get.Review = nil
	bad, _ := json.Marshal(get)
	if _, err = db.db.ExecContext(ctx, "UPDATE experiment_run SET payload=? WHERE id=?", bad, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = view.Get(ctx, r.ID); err == nil {
		t.Fatal("tampered result was readable")
	}
}
