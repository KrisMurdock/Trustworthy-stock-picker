package storage

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fundamentals"
)

func TestFundamentalsInitSchema(t *testing.T) {
	root := t.TempDir()
	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !db.HasDatabase() {
		t.Fatal("fresh fundamentals database is not available")
	}
	if got := filepath.Base(db.Path()); got != fundamentalsDBName {
		t.Fatalf("unexpected fundamentals path: %s", got)
	}
	if _, err := os.Stat(filepath.Join(root, fundamentalsDBName)); err != nil {
		t.Fatalf("fundamentals database was not created independently: %v", err)
	}
	var version int
	if err := db.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != fundamentalsSchemaVersion {
		t.Fatalf("wrong fundamentals schema version: %d %v", version, err)
	}
	for _, object := range []struct {
		kind string
		name string
	}{
		{"table", "fundamental_period"},
		{"table", "fundamental_sync"},
		{"table", "fundamental_fetch"},
		{"table", "fundamental_fetch_period"},
		{"index", "fundamental_period_visible"},
		{"index", "fundamental_period_report"},
		{"index", "fundamental_fetch_code"},
	} {
		if !fundamentalsSchemaObjectExists(t, db, object.kind, object.name) {
			t.Fatalf("missing fundamentals schema %s %s", object.kind, object.name)
		}
	}
}

func TestFundamentalsMissingDatabaseAndView(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	view, err := OpenFundamentalsView(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if view.HasDatabase() {
		t.Fatal("missing fundamentals database reported as available")
	}
	if _, err := view.LoadSeries(ctx, "sh600519"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing series did not report absence: %v", err)
	}
	audit, err := view.Audit(ctx, "2025-06-30")
	if err != nil || audit.Codes != 0 || audit.VisibleCodes != 0 || audit.StaleCodes != 0 || audit.Periods != 0 || audit.AsOf != "2025-06-30" {
		t.Fatalf("empty audit failed: %+v %v", audit, err)
	}
	syncs, err := view.ListSync(ctx)
	if err != nil || len(syncs) != 0 {
		t.Fatalf("empty sync list failed: %+v %v", syncs, err)
	}
	if _, err := OpenFundamentals(root, false); err == nil {
		t.Fatal("writable open created an uninitialized fundamentals database")
	}
	for _, name := range []string{fundamentalsDBName, fundamentalsLockName} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("missing open created %s: %v", name, err)
		}
	}
}

func TestFundamentalsAppendIdempotentAndRevisions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	firstFetched := time.Date(2024, 4, 20, 12, 0, 0, 123456789, time.FixedZone("CST", 8*60*60))
	first := fundamentalsPeriodFixture("sh600519", "2023-12-31", "2024-04-20", "2024-04-22", "rev-1", "hash-1", firstFetched)
	inserted, err := db.AppendSeries(ctx, fundamentalsSeriesFixture(first))
	if err != nil || inserted != 1 {
		t.Fatalf("append first series: inserted=%d err=%v", inserted, err)
	}
	reloadedFirst, err := db.LoadSeries(ctx, "sh600519")
	if err != nil {
		t.Fatal(err)
	}
	inserted, err = db.AppendSeries(ctx, reloadedFirst)
	if err != nil || inserted != 0 {
		t.Fatalf("reloaded series was not idempotent: inserted=%d err=%v", inserted, err)
	}
	fetches, err := db.ListFetches(ctx, "sh600519", 10)
	if err != nil || len(fetches) != 1 {
		t.Fatalf("time-zone canonicalized append created a duplicate batch: %+v %v", fetches, err)
	}
	inserted, err = db.AppendSeries(ctx, fundamentalsSeriesFixture(first))
	if err != nil || inserted != 0 {
		t.Fatalf("append was not idempotent: inserted=%d err=%v", inserted, err)
	}
	if _, err := db.LoadSeries(ctx, "sz000001"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing code did not report absence: %v", err)
	}

	secondFetched := time.Date(2025, 4, 20, 12, 0, 0, 987654321, time.UTC)
	second := fundamentalsPeriodFixture("sh600519", "2023-12-31", "2025-04-20", "2025-04-22", "rev-2", "hash-2", secondFetched)
	second.NetProfit = fundamentalsFloatPtr(2000)
	inserted, err = db.AppendSeries(ctx, fundamentalsSeriesFixture(second))
	if err != nil || inserted != 1 {
		t.Fatalf("append revision: inserted=%d err=%v", inserted, err)
	}
	loaded, err := db.LoadSeries(ctx, "sh600519")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Periods) != 2 || loaded.Periods[0].Revision != "rev-1" || loaded.Periods[1].Revision != "rev-2" {
		t.Fatalf("unexpected stored revisions: %+v", loaded.Periods)
	}
	if !loaded.FetchedAt.Equal(secondFetched) {
		t.Fatalf("series fetched_at was not the latest period: %s", loaded.FetchedAt)
	}
	visible, err := db.LoadVisibleSeries(ctx, "sh600519", "2024-06-30")
	if err != nil || len(visible.Periods) != 1 || visible.Periods[0].Revision != "rev-1" || *visible.Periods[0].NetProfit != 1000 {
		t.Fatalf("early point-in-time view was polluted: %+v %v", visible.Periods, err)
	}
	visible, err = db.LoadVisibleSeries(ctx, "sh600519", "2025-06-30")
	if err != nil || len(visible.Periods) != 1 || visible.Periods[0].Revision != "rev-2" || *visible.Periods[0].NetProfit != 2000 {
		t.Fatalf("later point-in-time revision was not selected: %+v %v", visible.Periods, err)
	}
	if !visible.FetchedAt.Equal(secondFetched) {
		t.Fatalf("visible view dropped latest fetched_at: %s", visible.FetchedAt)
	}
	if _, err := db.LoadVisibleSeries(ctx, "sh600519", "not-a-date"); err == nil {
		t.Fatal("invalid as-of date was accepted")
	}
	syncs, err := db.ListSync(ctx)
	if err != nil || len(syncs) != 1 {
		t.Fatalf("list sync: %+v %v", syncs, err)
	}
	lastFetched, err := time.Parse(time.RFC3339Nano, syncs[0].LastFetchedAt)
	if err != nil || syncs[0].Periods != 2 || syncs[0].LastReportEnd != "2023-12-31" || syncs[0].Status != "ok" || syncs[0].LastError != "" || !lastFetched.Equal(secondFetched) {
		t.Fatalf("unexpected sync summary: %+v", syncs[0])
	}
}

func TestFundamentalsNullAndZeroRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fetched := time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)
	period := fundamentalsPeriodFixture("sz000001", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-null", fetched)
	period.Revenue = nil
	period.NetProfit = fundamentalsFloatPtr(0)
	period.TotalLiabilities = nil
	period.CashDividend = fundamentalsFloatPtr(0)
	if _, err := db.AppendSeries(ctx, fundamentalsSeriesFixture(period)); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.LoadSeries(ctx, "sz000001")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Periods) != 1 {
		t.Fatalf("unexpected period count: %d", len(loaded.Periods))
	}
	got := loaded.Periods[0]
	if got.Revenue != nil {
		t.Fatalf("NULL revenue became %v", *got.Revenue)
	}
	if got.NetProfit == nil || *got.NetProfit != 0 {
		t.Fatalf("zero net profit was not preserved: %v", got.NetProfit)
	}
	if got.TotalLiabilities != nil {
		t.Fatalf("NULL liabilities became %v", *got.TotalLiabilities)
	}
	if got.CashDividend == nil || *got.CashDividend != 0 {
		t.Fatalf("zero cash dividend was not preserved: %v", got.CashDividend)
	}
}

func TestFundamentalsFetchBatchesAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	firstFetched := time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)
	first := fundamentalsPeriodFixture("sh600519", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-batch-1", firstFetched)
	series := fundamentalsSeriesFixture(first)
	if _, err := db.AppendSeries(ctx, series); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AppendSeries(ctx, series); err != nil {
		t.Fatal(err)
	}
	fetches, err := db.ListFetches(ctx, "sh600519", 10)
	if err != nil || len(fetches) != 1 {
		t.Fatalf("idempotent fetch count = %d, err=%v", len(fetches), err)
	}
	if len(fetches[0].BatchID) != 64 || fetches[0].BatchID != fetches[0].InputDigest || fetches[0].Periods != 1 ||
		fetches[0].Code != "sh600519" || fetches[0].Source != fundamentals.SourceTushare {
		t.Fatalf("unexpected fetch batch: %+v", fetches[0])
	}

	secondFetched := firstFetched.AddDate(1, 0, 0)
	second := fundamentalsPeriodFixture("sh600519", "2024-12-31", "2025-04-20", "2025-04-22", "rev-2", "hash-batch-2", secondFetched)
	if _, err := db.AppendSeries(ctx, fundamentalsSeriesFixture(second)); err != nil {
		t.Fatal(err)
	}
	fetches, err = db.ListFetches(ctx, "sh600519", 10)
	if err != nil || len(fetches) != 2 || fetches[0].FetchedAt != secondFetched.Format(time.RFC3339Nano) {
		t.Fatalf("newest-first fetch batches = %+v, err=%v", fetches, err)
	}
	var members int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fundamental_fetch_period`).Scan(&members); err != nil || members != 2 {
		t.Fatalf("fetch batch members = %d, err=%v", members, err)
	}
	if _, err := db.ListFetches(ctx, "sh600519", 0); err == nil {
		t.Fatal("invalid fetch limit was accepted")
	}
}

func TestFundamentalsUnchangedRefetchUpdatesSyncTime(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	firstFetched := time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)
	first := fundamentalsPeriodFixture("sh600519", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-sync-1", firstFetched)
	if _, err := db.AppendSeries(ctx, fundamentalsSeriesFixture(first)); err != nil {
		t.Fatal(err)
	}
	secondFetched := firstFetched.AddDate(0, 0, 1)
	second := first
	second.FetchedAt = secondFetched
	if _, err := db.AppendSeries(ctx, fundamentalsSeriesFixture(second)); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.LoadSeries(ctx, "sh600519")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Periods) != 1 || !loaded.Periods[0].FetchedAt.Equal(firstFetched) {
		t.Fatalf("unchanged fact lost its first observation time: %+v", loaded.Periods)
	}
	syncs, err := db.ListSync(ctx)
	if err != nil || len(syncs) != 1 {
		t.Fatalf("sync records = %+v, err=%v", syncs, err)
	}
	lastFetched, err := time.Parse(time.RFC3339Nano, syncs[0].LastFetchedAt)
	if err != nil || !lastFetched.Equal(secondFetched) || syncs[0].Periods != 1 {
		t.Fatalf("sync summary did not record the successful refetch: %+v", syncs[0])
	}
}

func TestFundamentalsAvailabilityCorrectionPreservesEarlierObservation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	firstFetched := time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)
	first := fundamentalsPeriodFixture("sh600519", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-available-1", firstFetched)
	if _, err := db.AppendSeries(ctx, fundamentalsSeriesFixture(first)); err != nil {
		t.Fatal(err)
	}
	corrected := first
	corrected.AvailableFrom = "2025-04-23"
	corrected.FetchedAt = firstFetched.AddDate(0, 0, 1)
	changed, err := db.AppendSeries(ctx, fundamentalsSeriesFixture(corrected))
	if err != nil || changed != 1 {
		t.Fatalf("availability correction changed=%d err=%v", changed, err)
	}
	before, err := db.LoadVisibleSeries(ctx, "sh600519", "2025-04-22")
	if err != nil || len(before.Periods) != 1 || before.Periods[0].AvailableFrom != "2025-04-22" {
		t.Fatalf("earlier observation was overwritten by correction: %+v %v", before.Periods, err)
	}
	after, err := db.LoadVisibleSeries(ctx, "sh600519", "2025-04-23")
	if err != nil || len(after.Periods) != 1 || after.Periods[0].AvailableFrom != "2025-04-23" {
		t.Fatalf("corrected available_from not visible: %+v %v", after.Periods, err)
	}
	// Replaying the older batch must not undo the corrected observation.
	if _, err := db.AppendSeries(ctx, fundamentalsSeriesFixture(first)); err != nil {
		t.Fatal(err)
	}
	after, err = db.LoadVisibleSeries(ctx, "sh600519", "2025-04-23")
	if err != nil || len(after.Periods) != 1 || after.Periods[0].AvailableFrom != "2025-04-23" {
		t.Fatalf("replaying the old series reverted the correction: %+v %v", after.Periods, err)
	}
}

func TestFundamentalsReadOnlyRejectsWrites(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	fetched := time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)
	period := fundamentalsPeriodFixture("sh600519", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-readonly", fetched)
	if _, err := db.AppendSeries(ctx, fundamentalsSeriesFixture(period)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	view, err := OpenFundamentalsView(root, false)
	if err != nil || !view.HasDatabase() {
		t.Fatalf("open view: %v", err)
	}
	defer view.Close()
	if _, err := view.AppendSeries(ctx, fundamentalsSeriesFixture(period)); err == nil {
		t.Fatal("read-only fundamentals view accepted an append")
	}
	if err := view.RecordSync(ctx, FundamentalsSyncRecord{Code: "sh600519", Source: fundamentals.SourceTushare, Status: "ok"}); err == nil {
		t.Fatal("read-only fundamentals view accepted a sync record")
	}
	loaded, err := view.LoadSeries(ctx, "sh600519")
	if err != nil || len(loaded.Periods) != 1 {
		t.Fatalf("read-only load failed: %+v %v", loaded.Periods, err)
	}
}

func TestFundamentalsWriterLockAndClose(t *testing.T) {
	root := t.TempDir()
	first, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFundamentals(root, false); err == nil {
		t.Fatal("second fundamentals writer acquired the lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenFundamentals(root, false)
	if err != nil {
		t.Fatalf("Close did not release the fundamentals lock: %v", err)
	}
	if !second.HasDatabase() {
		t.Fatal("reopened writer has no database")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close was not idempotent: %v", err)
	}
}

func TestFundamentalsRejectsUnsupportedVersion(t *testing.T) {
	root := t.TempDir()
	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFundamentals(root, false); err == nil {
		t.Fatal("unsupported version was accepted by writable open")
	}
	if _, err := OpenFundamentalsView(root, false); err == nil {
		t.Fatal("unsupported version was accepted by read-only open")
	}
	if _, err := InitFundamentals(root, false); err == nil {
		t.Fatal("unsupported version was accepted by init")
	}
}

func TestFundamentalsMigratesV1WithoutLosingPeriods(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	fetched := time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)
	period := fundamentalsPeriodFixture("sh600519", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-v1", fetched)
	if _, err := db.AppendSeries(ctx, fundamentalsSeriesFixture(period)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw := openRawFundamentals(t, root)
	for _, statement := range []string{
		`DROP TABLE fundamental_fetch_period`,
		`DROP TABLE fundamental_fetch`,
		`PRAGMA user_version=1`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var version int
	if err := migrated.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != fundamentalsSchemaVersion {
		t.Fatalf("migrated version = %d, err=%v", version, err)
	}
	loaded, err := migrated.LoadSeries(ctx, "sh600519")
	if err != nil || len(loaded.Periods) != 1 || loaded.Periods[0].RowHash != "hash-v1" {
		t.Fatalf("v1 period was not preserved: %+v %v", loaded.Periods, err)
	}
}

func TestFundamentalsRejectsMalformedV1WithoutVersionBump(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	fetched := time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)
	period := fundamentalsPeriodFixture("sh600519", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-bad-v1", fetched)
	if _, err := db.AppendSeries(ctx, fundamentalsSeriesFixture(period)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw := openRawFundamentals(t, root)
	for _, statement := range []string{
		`DROP TABLE fundamental_fetch_period`,
		`DROP TABLE fundamental_fetch`,
		`DROP INDEX fundamental_period_visible`,
		`ALTER TABLE fundamental_period DROP COLUMN comp_type`,
		`PRAGMA user_version=1`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InitFundamentals(root, false); err == nil {
		t.Fatal("malformed v1 schema was upgraded")
	}
	raw = openRawFundamentals(t, root)
	defer raw.Close()
	var version int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("failed migration changed version to %d, err=%v", version, err)
	}
}

func TestFundamentalsRejectsSymlinksAndSidecars(t *testing.T) {
	for _, name := range []string{
		fundamentalsDBName,
		fundamentalsLockName,
		fundamentalsDBName + "-journal",
		fundamentalsDBName + "-wal",
		fundamentalsDBName + "-shm",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, []byte("target"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			if _, err := InitFundamentals(root, false); err == nil {
				t.Fatalf("symlink %s was accepted", name)
			}
		})
	}
}

func TestFundamentalsAuditPointInTime(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, period := range []fundamentals.Period{
		fundamentalsPeriodFixture("sh600519", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-fresh", time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)),
		fundamentalsPeriodFixture("sz000001", "2022-12-31", "2023-04-20", "2023-04-22", "rev-1", "hash-stale", time.Date(2023, 4, 20, 12, 0, 0, 0, time.UTC)),
		fundamentalsPeriodFixture("sh601318", "2025-12-31", "2026-04-20", "2026-04-22", "rev-1", "hash-future", time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)),
		fundamentalsPeriodFixture("sh600000", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-late-fetch", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)),
	} {
		if _, err := db.AppendSeries(ctx, fundamentalsSeriesFixture(period)); err != nil {
			t.Fatal(err)
		}
	}
	audit, err := db.Audit(ctx, "2025-06-30")
	if err != nil {
		t.Fatal(err)
	}
	if audit.Codes != 4 || audit.VisibleCodes != 2 || audit.VendorVisibleCodes != 3 || audit.StaleCodes != 1 ||
		audit.Periods != 4 || audit.FutureFetchedPeriods != 2 || audit.Fetches != 4 || audit.AsOf != "2025-06-30" {
		t.Fatalf("unexpected audit totals: %+v", audit)
	}
	wantProblems := []string{
		"code=sh600000:future_fetched_only",
		"code=sh601318:no_visible_period",
		"code=sz000001:stale_report_end=2022-12-31",
	}
	if !reflect.DeepEqual(audit.Problems, wantProblems) {
		t.Fatalf("unexpected audit problems: %+v", audit.Problems)
	}
	observed, err := db.LoadVisibleSeries(ctx, "sh600000", "2025-06-30")
	if err != nil || len(observed.Periods) != 0 || !observed.FetchedAt.IsZero() {
		t.Fatalf("future-fetched row entered observed view: %+v %v", observed, err)
	}
	vendor, err := db.LoadVisibleSeriesMode(ctx, "sh600000", "2025-06-30", fundamentals.VisibilityVendorBackfill)
	if err != nil || len(vendor.Periods) != 1 || vendor.Periods[0].Revision != "rev-1" ||
		!vendor.FetchedAt.Equal(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("vendor backfill view = %+v, err=%v", vendor, err)
	}
}

func TestFundamentalsSyncRecords(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fetched := time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)
	rec := FundamentalsSyncRecord{
		Code:          "sh600519",
		Source:        fundamentals.SourceTushare,
		Status:        "failed",
		LastReportEnd: "2023-12-31",
		LastFetchedAt: fetched.Format(time.RFC3339Nano),
		LastError:     "temporary upstream failure",
		Periods:       1,
	}
	if err := db.RecordSync(ctx, rec); err != nil {
		t.Fatal(err)
	}
	syncs, err := db.ListSync(ctx)
	if err != nil || len(syncs) != 1 || syncs[0] != rec {
		t.Fatalf("record sync round trip: %+v %v", syncs, err)
	}
	rec.Status = "stale"
	rec.LastError = ""
	if err := db.RecordSync(ctx, rec); err != nil {
		t.Fatal(err)
	}
	syncs, err = db.ListSync(ctx)
	if err != nil || len(syncs) != 1 || syncs[0].Status != "stale" || syncs[0].LastError != "" {
		t.Fatalf("sync update: %+v %v", syncs, err)
	}
	other := rec
	other.Source = "legacy"
	if err := db.RecordSync(ctx, other); err == nil {
		t.Fatal("mixed fundamental sources were accepted")
	}
}

func fundamentalsPeriodFixture(code, reportEnd, announcement, available, revision, rowHash string, fetchedAt time.Time) fundamentals.Period {
	return fundamentals.Period{
		SchemaVersion:     fundamentals.Version,
		Code:              code,
		Source:            fundamentals.SourceTushare,
		ReportEnd:         reportEnd,
		AnnouncementDate:  announcement,
		AvailableFrom:     available,
		StatementBasis:    fundamentals.StatementAnnualConsolidated,
		ReportType:        "1",
		CompType:          1,
		UpdateFlag:        0,
		Revision:          revision,
		FetchedAt:         fetchedAt,
		RowHash:           rowHash,
		Revenue:           fundamentalsFloatPtr(1000),
		NetProfit:         fundamentalsFloatPtr(1000),
		Equity:            fundamentalsFloatPtr(5000),
		OperatingCashFlow: fundamentalsFloatPtr(800),
		TotalAssets:       fundamentalsFloatPtr(10000),
		TotalLiabilities:  fundamentalsFloatPtr(5000),
		TotalShares:       fundamentalsFloatPtr(100),
		BasicEPS:          fundamentalsFloatPtr(10),
		CashDividend:      fundamentalsFloatPtr(2),
	}
}

func fundamentalsSeriesFixture(periods ...fundamentals.Period) fundamentals.Series {
	fetchedAt := periods[0].FetchedAt
	for _, period := range periods[1:] {
		if period.FetchedAt.After(fetchedAt) {
			fetchedAt = period.FetchedAt
		}
	}
	return fundamentals.Series{
		SchemaVersion: fundamentals.Version,
		Code:          periods[0].Code,
		Source:        periods[0].Source,
		FetchedAt:     fetchedAt,
		Periods:       periods,
	}
}

func fundamentalsFloatPtr(value float64) *float64 {
	return &value
}

func fundamentalsSchemaObjectExists(t *testing.T, db *FundamentalsDB, kind, name string) bool {
	t.Helper()
	var count int
	if err := db.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?", kind, name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count == 1
}

func openRawFundamentals(t *testing.T, root string) *sql.DB {
	t.Helper()
	path := filepath.Join(root, fundamentalsDBName)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}
