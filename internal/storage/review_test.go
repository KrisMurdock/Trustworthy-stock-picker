package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
)

func TestReportArchiveMigrationBackfillsAndPreservesPayloads(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	history := dbHistory("sz000001", "qfq", "qfqday")
	report := selection.Report{
		Version: 3, ID: "legacy-report", AsOf: history.AsOf, Target: "2026-10-14",
		GeneratedAt: history.FetchedAt, Strategy: "short_horizon_v1", Adjust: "qfq",
	}
	err = db.CommitSnapshot(ctx, SyncRun{ID: "legacy-run", AsOf: history.AsOf, StartedAt: history.FetchedAt, FinishedAt: history.FetchedAt, CoveragePct: 100},
		[]SyncItem{{Stock: config.Stock{Code: history.Code, Name: history.Name}, Adjusted: &history, Status: "ok"}}, report)
	if err != nil {
		t.Fatal(err)
	}
	var revision string
	var payload []byte
	if err := db.db.QueryRow(`SELECT id, report_json FROM report_archive`).Scan(&revision, &payload); err != nil {
		t.Fatal(err)
	}
	badRevision := strings.Repeat("f", 64)
	if _, err := db.db.Exec(`
		DROP TABLE report_archive;
		CREATE TABLE report_archive(id TEXT PRIMARY KEY, report_json BLOB NOT NULL);
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`INSERT INTO report_archive(id, report_json) VALUES(?, ?)`, revision, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`INSERT INTO report_archive(id, report_json) VALUES(?, ?)`, badRevision, []byte("not-json")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`PRAGMA user_version=5`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if view, err := OpenMarketView(root, false); err == nil {
		view.Close()
		t.Fatal("schema 5 was accepted by the read-only entry")
	}
	db, err = OpenMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != marketSchemaVersion {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	for _, column := range []string{"as_of", "generated_at", "schema_version"} {
		if !hasColumn(t, db, "report_archive", column) {
			t.Fatalf("missing report_archive column %s", column)
		}
	}
	if !hasReviewIndex(t, db) {
		t.Fatal("missing report_archive index columns")
	}
	var gotPayload []byte
	if err := db.db.QueryRow(`SELECT report_json FROM report_archive WHERE id=?`, revision).Scan(&gotPayload); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatal("migration changed the archived report payload")
	}
	var asOf, generated string
	var schemaVersion int
	if err := db.db.QueryRow(`SELECT as_of, generated_at, schema_version FROM report_archive WHERE id=?`, revision).Scan(&asOf, &generated, &schemaVersion); err != nil {
		t.Fatal(err)
	}
	if asOf != report.AsOf || generated != report.GeneratedAt.UTC().Format(time.RFC3339Nano) || schemaVersion != report.Version {
		t.Fatalf("backfilled index = %q %q %d", asOf, generated, schemaVersion)
	}
	var badAsOf, badGenerated string
	var badSchema int
	if err := db.db.QueryRow(`SELECT as_of, generated_at, schema_version FROM report_archive WHERE id=?`, badRevision).Scan(&badAsOf, &badGenerated, &badSchema); err != nil {
		t.Fatal(err)
	}
	if badAsOf != "" || badGenerated != "" || badSchema != 0 {
		t.Fatalf("invalid payload was indexed: %q %q %d", badAsOf, badGenerated, badSchema)
	}
	revisions, err := db.ListReportRevisions(ctx, report.AsOf)
	if err != nil || len(revisions) != 1 || revisions[0].ID != revision || revisions[0].SchemaVersion != report.Version {
		t.Fatalf("migrated revisions = %+v, %v", revisions, err)
	}
	loaded, err := db.LoadReportRevision(ctx, revision)
	if err != nil || loaded.ID != report.ID || loaded.Version != report.Version {
		t.Fatalf("archived report changed: %+v, %v", loaded, err)
	}
}

func TestListReportRevisionsOrderingAndInvalidRows(t *testing.T) {
	ctx := context.Background()
	db, err := InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	asOf := "2026-10-14"
	base := time.Date(2026, 10, 14, 10, 0, 0, 0, market.Shanghai)
	insert := func(id string, generated time.Time, indexedAsOf, indexedGenerated string, data []byte) {
		t.Helper()
		if _, err := db.db.Exec(`
			INSERT INTO report_archive(id, report_json, as_of, generated_at, schema_version) VALUES(?, ?, ?, ?, 2)
		`, id, data, indexedAsOf, indexedGenerated); err != nil {
			t.Fatal(err)
		}
	}
	reportData := func(reportAsOf string, generated time.Time) []byte {
		data, err := json.Marshal(selection.Report{Version: 2, ID: reportAsOf, AsOf: reportAsOf, GeneratedAt: generated, Adjust: "qfq"})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	idA, idB, idC := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	insert(idB, base.Add(time.Hour), asOf, base.Add(time.Hour).UTC().Format(time.RFC3339Nano), reportData(asOf, base.Add(time.Hour)))
	insert(idA, base.Add(time.Hour), asOf, base.Add(time.Hour).UTC().Format(time.RFC3339Nano), reportData(asOf, base.Add(time.Hour)))
	insert(idC, base.Add(2*time.Hour), asOf, base.Add(2*time.Hour).UTC().Format(time.RFC3339Nano), reportData(asOf, base.Add(2*time.Hour)))
	insert(strings.Repeat("d", 64), base.Add(3*time.Hour), asOf, "not-a-time", reportData(asOf, base.Add(3*time.Hour)))
	insert(strings.Repeat("e", 64), base.Add(4*time.Hour), asOf, base.Add(4*time.Hour).UTC().Format(time.RFC3339Nano), []byte("not-json"))
	insert(strings.Repeat("f", 64), base.Add(5*time.Hour), asOf, base.Add(5*time.Hour).UTC().Format(time.RFC3339Nano), reportData("2026-10-13", base.Add(5*time.Hour)))
	got, err := db.ListReportRevisions(ctx, asOf)
	if err != nil || len(got) != 3 || got[0].ID != idA || got[1].ID != idB || got[2].ID != idC {
		t.Fatalf("ordered revisions = %+v, %v", got, err)
	}
	for _, invalid := range []string{"", "2026-1-1", "2026-13-01"} {
		got, err := db.ListReportRevisions(ctx, invalid)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("invalid date %q = %+v, %v", invalid, got, err)
		}
	}
	got, err = db.ListReportRevisions(ctx, "2026-10-12")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty date result = %+v, %v", got, err)
	}
}

func TestLatestReviewDataDateAndHistories(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stocks := map[string]config.Stock{
		"a": {Code: "sz000001", Name: "测试一"},
		"b": {Code: "sh600519", Name: "测试二"},
		"c": {Code: "sz000002", Name: "测试三"},
	}
	history := func(code, date string) market.History {
		h := dbHistory(code, "qfq", "qfqday")
		day, err := time.ParseInLocation("2006-01-02", date, market.Shanghai)
		if err != nil {
			t.Fatal(err)
		}
		h.Name = stocks["a"].Name
		if code == stocks["b"].Code {
			h.Name = stocks["b"].Name
		}
		h.AsOf = date
		h.FetchedAt = day.Add(19 * time.Hour)
		h.Bars = []market.Bar{{Date: date, Open: 10, Close: 10.1, High: 10.2, Low: 9.9, Volume: 100}}
		return h
	}
	commit := func(stock config.Stock, h market.History, status string) {
		t.Helper()
		item := SyncItem{Stock: stock, Status: status}
		if h.Code != "" {
			item.Adjusted = &h
		}
		err := db.CommitMarketData(ctx, SyncRun{ID: stock.Code + "-" + h.AsOf, AsOf: h.AsOf, StartedAt: h.FetchedAt, FinishedAt: h.FetchedAt, CoveragePct: 100}, []SyncItem{item}, "qfq")
		if err != nil {
			t.Fatal(err)
		}
	}
	a12, a15 := history(stocks["a"].Code, "2026-10-12"), history(stocks["a"].Code, "2026-10-15")
	b15 := history(stocks["b"].Code, "2026-10-15")
	commit(stocks["a"], a12, "ok")
	commit(stocks["a"], a15, "ok")
	commit(stocks["b"], b15, "ok")
	commit(stocks["b"], market.History{AsOf: "2026-10-16"}, "isolated")
	view, err := OpenMarketView(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	latest, err := view.LatestReviewDataDate(ctx, []string{stocks["a"].Code, stocks["b"].Code, stocks["c"].Code, stocks["a"].Code}, "qfq")
	if err != nil || latest != "2026-10-15" {
		t.Fatalf("latest review date = %q, %v", latest, err)
	}
	if _, err := view.LatestReviewDataDate(ctx, []string{stocks["c"].Code}, "qfq"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing latest date = %v", err)
	}
	histories, err := view.LoadLatestReviewHistories(ctx, []string{stocks["a"].Code, stocks["b"].Code, stocks["c"].Code}, "qfq", "2026-10-14")
	if err != nil || len(histories) != 1 || histories[stocks["a"].Code].AsOf != "2026-10-12" {
		t.Fatalf("limited histories = %+v, %v", histories, err)
	}
	histories, err = view.LoadLatestReviewHistories(ctx, []string{stocks["a"].Code, stocks["b"].Code, stocks["c"].Code}, "qfq", "2026-10-15")
	if err != nil || len(histories) != 2 || histories[stocks["a"].Code].AsOf != "2026-10-15" || histories[stocks["b"].Code].AsOf != "2026-10-15" {
		t.Fatalf("latest histories = %+v, %v", histories, err)
	}
	if _, exists := histories[stocks["c"].Code]; exists {
		t.Fatal("missing stock was added to review histories")
	}
	empty, err := view.LoadLatestReviewHistories(ctx, []string{stocks["c"].Code}, "qfq", "2026-10-15")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty histories = %+v, %v", empty, err)
	}
	if _, err := db.db.Exec(`
		UPDATE history_snapshot SET payload=x'00'
		WHERE id=(SELECT snapshot_id FROM history_revision WHERE code=? AND adjust='qfq' AND as_of=?)
	`, stocks["b"].Code, "2026-10-15"); err != nil {
		t.Fatal(err)
	}
	if _, err := view.LoadLatestReviewHistories(ctx, []string{stocks["a"].Code, stocks["b"].Code}, "qfq", "2026-10-15"); err == nil {
		t.Fatal("corrupt review history error was swallowed")
	}
}

func TestMarketDBViewRejectsMissingReviewIndex(t *testing.T) {
	root := t.TempDir()
	db, err := InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DROP INDEX ` + reportArchiveIndexName); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if view, err := OpenMarketView(root, false); err == nil {
		view.Close()
		t.Fatal("missing report archive index was accepted by the read-only entry")
	}
	db, err = OpenMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !hasReviewIndex(t, db) {
		t.Fatal("writable open did not repair the review index")
	}
}

func hasReviewIndex(t *testing.T, db *MarketDB) bool {
	t.Helper()
	rows, err := db.db.Query("PRAGMA index_info(" + reportArchiveIndexName + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var seq, cid int
		var name string
		if err := rows.Scan(&seq, &cid, &name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return len(columns) == 2 && columns[0] == "as_of" && columns[1] == "generated_at"
}
