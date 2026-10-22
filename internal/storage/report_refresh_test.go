package storage

import (
	"context"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
)

func TestReplaceReportKeepsFrozenInputAndArchivesBothRevisions(t *testing.T) {
	db, err := InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := dbHistory("sz000001", "qfq", "qfqday")
	original := selection.Report{
		Version: 1, ID: "event-original", AsOf: h.AsOf, Target: "2026-10-14",
		GeneratedAt: time.Date(2026, 10, 13, 19, 0, 0, 0, time.UTC),
		Strategy:    "leader_daily_v1", Adjust: "qfq",
		SecondStage: &secondstage.Report{Version: secondstage.ReportSchemaVersion, Status: secondstage.StatusOK, Degraded: true, AsOf: h.AsOf, Target: "2026-10-14", Candidates: []secondstage.Candidate{{Rank: 1, Code: h.Code}}},
	}
	run := SyncRun{ID: "run-original", AsOf: h.AsOf, StartedAt: h.FetchedAt.Add(-time.Minute), FinishedAt: h.FetchedAt, CoveragePct: 100}
	if err := db.CommitSnapshot(context.Background(), run, []SyncItem{{Stock: config.Stock{Code: h.Code, Name: h.Name}, Adjusted: &h, Status: "ok"}}, original); err != nil {
		t.Fatal(err)
	}
	stored, err := db.LoadReport(context.Background(), h.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	inputID := stored.InputID
	originalRevision := stored.Revision
	refreshed := stored
	refreshed.ID = "event-refreshed"
	refreshed.GeneratedAt = stored.GeneratedAt.Add(time.Minute)
	refreshed.SecondStage = &secondstage.Report{Version: secondstage.ReportSchemaVersion, Status: secondstage.StatusOK, AsOf: h.AsOf, Target: "2026-10-14", Candidates: []secondstage.Candidate{{Rank: 1, Code: h.Code}}}
	if err := db.ReplaceReport(context.Background(), refreshed); err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadReport(context.Background(), h.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != refreshed.ID || got.InputID != inputID || got.Revision == "" || got.Revision == originalRevision {
		t.Fatalf("derived report identity changed incorrectly: %+v", got)
	}
	if got.SecondStage == nil || got.SecondStage.Degraded {
		t.Fatalf("derived second stage was not persisted: %+v", got.SecondStage)
	}
	var archived int
	if err := db.db.QueryRow(`SELECT count(*) FROM report_archive WHERE as_of=?`, h.AsOf).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if archived != 2 {
		t.Fatalf("archived revisions = %d, want 2", archived)
	}
	var selectedID string
	if err := db.db.QueryRow(`SELECT id FROM selection_run WHERE as_of=?`, h.AsOf).Scan(&selectedID); err != nil {
		t.Fatal(err)
	}
	if selectedID != refreshed.ID {
		t.Fatalf("selection_run points to %s, want %s", selectedID, refreshed.ID)
	}
}

func TestReplaceReportRejectsDifferentFrozenInput(t *testing.T) {
	db, err := InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := dbHistory("sz000001", "qfq", "qfqday")
	report := selection.Report{Version: 1, ID: "event-original", AsOf: h.AsOf, Target: "2026-10-14", GeneratedAt: time.Now(), Strategy: "leader_daily_v1", Adjust: "qfq"}
	run := SyncRun{ID: "run-original", AsOf: h.AsOf, StartedAt: h.FetchedAt.Add(-time.Minute), FinishedAt: h.FetchedAt, CoveragePct: 100}
	if err := db.CommitSnapshot(context.Background(), run, []SyncItem{{Stock: config.Stock{Code: h.Code, Name: h.Name}, Adjusted: &h, Status: "ok"}}, report); err != nil {
		t.Fatal(err)
	}
	stored, err := db.LoadReport(context.Background(), h.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	stored.InputID = "different-input"
	if err := db.ReplaceReport(context.Background(), stored); err == nil {
		t.Fatal("derived report replaced a different frozen input")
	}
}
