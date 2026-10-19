package storage

import (
	"context"
	"stock-watcher/internal/config"
	"testing"
	"time"
)

func TestTaskWritesRecheckRequiredSharedFilesystem(t *testing.T) {
	db, err := InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	task := SyncTask{ID: "guard", AsOf: "2026-10-16", Phase: "collecting", Status: "running", Total: 1, StartedAt: now, UpdatedAt: now}
	ctx := context.Background()
	if err := db.SaveSyncTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	// An open connection is not proof that its root still meets the mount
	// contract. Simulate a non-CIFS root after opening the connection.
	db.requireCIFS = true
	run := SyncRun{ID: "guard", AsOf: task.AsOf, StartedAt: now, FinishedAt: now}
	checks := map[string]func() error{
		"begin":    func() error { _, _, err := db.BeginSyncTask(ctx, task); return err },
		"progress": func() error { return db.SaveSyncTask(ctx, task) },
		"checkpoint": func() error {
			return db.CheckpointStock(ctx, task, SyncItem{Stock: config.Stock{Code: "sz000001"}, Status: "isolated"})
		},
		"heartbeat": func() error { return db.TouchSyncTask(ctx, task.ID, now.Format(time.RFC3339Nano)) },
		"running":   func() error { return db.RecordRunningSync(ctx, run) },
		"failed":    func() error { return db.RecordFailedRun(ctx, run) },
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(); err == nil {
				t.Fatal("write bypassed required mount check")
			}
		})
	}
}
