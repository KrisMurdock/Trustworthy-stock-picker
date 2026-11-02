package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fundamentals"
)

const fundamentalsV2TestSchema = `
	CREATE TABLE fundamental_period (
		code TEXT NOT NULL,
		source TEXT NOT NULL,
		report_end_date TEXT NOT NULL,
		statement_basis TEXT NOT NULL,
		report_type TEXT NOT NULL,
		announcement_date TEXT NOT NULL,
		revision TEXT NOT NULL,
		row_hash TEXT NOT NULL,
		schema_version INTEGER NOT NULL,
		available_from TEXT NOT NULL,
		comp_type INTEGER NOT NULL,
		update_flag INTEGER NOT NULL,
		fetched_at TEXT NOT NULL,
		revenue REAL,
		net_profit_attr REAL,
		equity_attr REAL,
		operating_cash_flow REAL,
		total_assets REAL,
		total_liabilities REAL,
		total_shares REAL,
		basic_eps REAL,
		cash_dividend REAL,
		PRIMARY KEY(code, source, report_end_date, statement_basis, report_type,
			announcement_date, revision, row_hash)
	);
	CREATE INDEX fundamental_period_visible
		ON fundamental_period(code, available_from, report_end_date);
	CREATE INDEX fundamental_period_report
		ON fundamental_period(code, source, report_end_date);
	CREATE TABLE fundamental_sync (
		code TEXT NOT NULL,
		source TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT '',
		periods INTEGER NOT NULL DEFAULT 0 CHECK(periods >= 0),
		last_report_end TEXT NOT NULL DEFAULT '',
		last_fetched_at TEXT NOT NULL DEFAULT '',
		last_error TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(code, source)
	);
	CREATE TABLE fundamental_fetch (
		batch_id TEXT PRIMARY KEY,
		code TEXT NOT NULL,
		source TEXT NOT NULL,
		fetched_at TEXT NOT NULL,
		periods INTEGER NOT NULL CHECK(periods > 0),
		input_digest TEXT NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE INDEX fundamental_fetch_code ON fundamental_fetch(code, fetched_at);
	CREATE TABLE fundamental_fetch_period (
		batch_id TEXT NOT NULL REFERENCES fundamental_fetch(batch_id) ON DELETE CASCADE,
		report_end_date TEXT NOT NULL,
		announcement_date TEXT NOT NULL,
		revision TEXT NOT NULL,
		row_hash TEXT NOT NULL,
		PRIMARY KEY(batch_id, report_end_date, announcement_date, revision, row_hash)
	);
`

func TestFundamentalsMigratesV2PreservingFactsAndBatchMembership(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fetched := time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)
	period := fundamentalsPeriodFixture("sh600519", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-v2-migrate", fetched)
	batchID := strings.Repeat("a", 64)
	raw := newFundamentalsV2(t, root)
	insertFundamentalsV2Period(t, raw, period, batchID)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != fundamentalsSchemaVersion {
		t.Fatalf("migrated version = %d, err=%v", version, err)
	}
	loaded, err := db.LoadSeries(ctx, "sh600519")
	if err != nil || len(loaded.Periods) != 1 || loaded.Periods[0].RowHash != period.RowHash {
		t.Fatalf("v2 period was not preserved: %+v %v", loaded.Periods, err)
	}
	if loaded.Periods[0].TotalShares == nil || *loaded.Periods[0].TotalShares != 1_000_000 {
		t.Fatalf("legacy Tushare shares were not converted from ten-thousand shares: %+v", loaded.Periods[0])
	}
	var statementBasis, reportType, availableFrom, memberFetched string
	if err := db.db.QueryRow(`
		SELECT statement_basis, report_type, available_from, fetched_at
		FROM fundamental_fetch_period WHERE batch_id=?`, batchID,
	).Scan(&statementBasis, &reportType, &availableFrom, &memberFetched); err != nil {
		t.Fatal(err)
	}
	if statementBasis != period.StatementBasis || reportType != period.ReportType ||
		availableFrom != period.AvailableFrom || memberFetched != fetched.Format(time.RFC3339Nano) {
		t.Fatalf("migrated batch membership lost fact identity: %s %s %s %s", statementBasis, reportType, availableFrom, memberFetched)
	}

	corrected := period
	corrected.AvailableFrom = "2025-04-23"
	corrected.FetchedAt = fetched.Add(time.Hour)
	corrected.TotalShares = fundamentalsFloatPtr(1_000_000)
	if _, err := db.AppendSeries(ctx, fundamentalsSeriesFixture(corrected)); err != nil {
		t.Fatal(err)
	}
	before, err := db.LoadVisibleSeries(ctx, "sh600519", "2025-04-22")
	if err != nil || len(before.Periods) != 1 || before.Periods[0].AvailableFrom != "2025-04-22" {
		t.Fatalf("migrated fact lost its earlier visibility: %+v %v", before.Periods, err)
	}
	after, err := db.LoadVisibleSeries(ctx, "sh600519", "2025-04-23")
	if err != nil || len(after.Periods) != 1 || after.Periods[0].AvailableFrom != "2025-04-23" {
		t.Fatalf("corrected visibility was not selected after migration: %+v %v", after.Periods, err)
	}
}

func TestFundamentalsMigratesV3BackfillingDividendVisibility(t *testing.T) {
	root := t.TempDir()
	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	fetched := time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)
	period := fundamentalsPeriodFixture("sh600519", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-wrong-pk", fetched)
	if _, err := db.AppendSeries(context.Background(), fundamentalsSeriesFixture(period)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw := openRawFundamentals(t, root)
	for _, statement := range []string{
		`CREATE TABLE fundamental_period_v3 (
			code TEXT NOT NULL, source TEXT NOT NULL, report_end_date TEXT NOT NULL,
			statement_basis TEXT NOT NULL, report_type TEXT NOT NULL, announcement_date TEXT NOT NULL,
			revision TEXT NOT NULL, row_hash TEXT NOT NULL, schema_version INTEGER NOT NULL,
			available_from TEXT NOT NULL, comp_type INTEGER NOT NULL, update_flag INTEGER NOT NULL,
			fetched_at TEXT NOT NULL, revenue REAL, net_profit_attr REAL, equity_attr REAL,
			operating_cash_flow REAL, total_assets REAL, total_liabilities REAL, total_shares REAL,
			basic_eps REAL, cash_dividend REAL,
			PRIMARY KEY(code, source, report_end_date, statement_basis, report_type,
				announcement_date, revision, row_hash, available_from)
		)`,
		`INSERT INTO fundamental_period_v3(
			code, source, report_end_date, statement_basis, report_type, announcement_date,
			revision, row_hash, schema_version, available_from, comp_type, update_flag,
			fetched_at, revenue, net_profit_attr, equity_attr, operating_cash_flow,
			total_assets, total_liabilities, total_shares, basic_eps, cash_dividend)
		SELECT code, source, report_end_date, statement_basis, report_type, announcement_date,
			revision, row_hash, schema_version, available_from, comp_type, update_flag,
			fetched_at, revenue, net_profit_attr, equity_attr, operating_cash_flow,
			total_assets, total_liabilities, total_shares, basic_eps, cash_dividend
		FROM fundamental_period`,
		`DROP TABLE fundamental_period`,
		`ALTER TABLE fundamental_period_v3 RENAME TO fundamental_period`,
		`CREATE INDEX fundamental_period_visible ON fundamental_period(code, available_from, report_end_date)`,
		`CREATE INDEX fundamental_period_report ON fundamental_period(code, source, report_end_date)`,
		`ALTER TABLE fundamental_fetch_period RENAME TO fundamental_fetch_period_old`,
		`CREATE TABLE fundamental_fetch_period (
			batch_id TEXT NOT NULL REFERENCES fundamental_fetch(batch_id) ON DELETE CASCADE,
			report_end_date TEXT NOT NULL, statement_basis TEXT NOT NULL, report_type TEXT NOT NULL,
			announcement_date TEXT NOT NULL, revision TEXT NOT NULL, row_hash TEXT NOT NULL,
			available_from TEXT NOT NULL, fetched_at TEXT NOT NULL,
			PRIMARY KEY(batch_id, report_end_date, statement_basis, report_type,
				announcement_date, revision, row_hash, available_from)
		)`,
		`INSERT INTO fundamental_fetch_period(
			batch_id, report_end_date, statement_basis, report_type, announcement_date,
			revision, row_hash, available_from, fetched_at)
		SELECT batch_id, report_end_date, statement_basis, report_type, announcement_date,
			revision, row_hash, available_from, fetched_at
		FROM fundamental_fetch_period_old`,
		`DROP TABLE fundamental_fetch_period_old`,
		`PRAGMA user_version=3`,
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
	loaded, err := migrated.LoadSeries(context.Background(), "sh600519")
	if err != nil || len(loaded.Periods) != 1 {
		t.Fatalf("v3 period was not preserved: %+v %v", loaded.Periods, err)
	}
	if loaded.Periods[0].CashDividendAnnouncementDate != period.AnnouncementDate ||
		loaded.Periods[0].CashDividendAvailableFrom != period.AvailableFrom {
		t.Fatalf("dividend visibility was not conservatively backfilled: %+v", loaded.Periods[0])
	}
	if loaded.Periods[0].TotalShares == nil || *loaded.Periods[0].TotalShares != 1_000_000 {
		t.Fatalf("legacy Tushare shares were not converted from ten-thousand shares: %+v", loaded.Periods[0])
	}
}

func TestFundamentalsMigratesV3AndRestoresFetchForeignKey(t *testing.T) {
	root := t.TempDir()
	db, err := InitFundamentals(root, false)
	if err != nil {
		t.Fatal(err)
	}
	fetched := time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)
	period := fundamentalsPeriodFixture("sh600519", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-no-fk", fetched)
	if _, err := db.AppendSeries(context.Background(), fundamentalsSeriesFixture(period)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw := openRawFundamentals(t, root)
	for _, statement := range []string{
		`DROP TABLE fundamental_fetch_period`,
		`CREATE TABLE fundamental_fetch_period (
			batch_id TEXT NOT NULL, report_end_date TEXT NOT NULL, statement_basis TEXT NOT NULL,
			report_type TEXT NOT NULL, announcement_date TEXT NOT NULL, revision TEXT NOT NULL,
			row_hash TEXT NOT NULL, available_from TEXT NOT NULL,
			cash_dividend_announcement_date TEXT NOT NULL, cash_dividend_available_from TEXT NOT NULL,
			fetched_at TEXT NOT NULL,
			PRIMARY KEY(batch_id, report_end_date, statement_basis, report_type,
				announcement_date, revision, row_hash, available_from,
				cash_dividend_announcement_date, cash_dividend_available_from)
		)`,
		`PRAGMA user_version=3`,
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
	if err := requireFundamentalsForeignKey(migrated.db, "fundamental_fetch_period", "fundamental_fetch", "batch_id", "batch_id", "CASCADE"); err != nil {
		t.Fatalf("migration did not restore the fetch-period foreign key: %v", err)
	}
}

func TestFundamentalsRejectsMalformedV2MigrationWithoutVersionBump(t *testing.T) {
	root := t.TempDir()
	fetched := time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)
	period := fundamentalsPeriodFixture("sh600519", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-v2-good", fetched)
	batchID := strings.Repeat("b", 64)
	raw := newFundamentalsV2(t, root)
	insertFundamentalsV2Period(t, raw, period, batchID)
	if _, err := raw.Exec(`
		INSERT INTO fundamental_fetch_period(batch_id, report_end_date, announcement_date, revision, row_hash)
		VALUES(?, '2024-12-31', '2025-04-20', 'rev-1', 'hash-missing')`, batchID); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InitFundamentals(root, false); err == nil {
		t.Fatal("v2 with an orphan batch member was migrated")
	}
	assertFundamentalsVersion(t, root, 2)
	raw = openRawFundamentals(t, root)
	defer raw.Close()
	for _, table := range []string{"fundamental_period", "fundamental_fetch_period"} {
		var count int
		if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("rollback left %s unavailable: count=%d err=%v", table, count, err)
		}
	}
	var periods, members, legacy int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM fundamental_period`).Scan(&periods); err != nil || periods != 1 {
		t.Fatalf("rollback lost periods: count=%d err=%v", periods, err)
	}
	if err := raw.QueryRow(`SELECT COUNT(*) FROM fundamental_fetch_period`).Scan(&members); err != nil || members != 2 {
		t.Fatalf("rollback lost batch members: count=%d err=%v", members, err)
	}
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%_legacy'`).Scan(&legacy); err != nil || legacy != 0 {
		t.Fatalf("rollback left legacy objects: count=%d err=%v", legacy, err)
	}
}

func TestFundamentalsRejectsV2SyncTableWithoutPrimaryKey(t *testing.T) {
	root := t.TempDir()
	fetched := time.Date(2025, 4, 20, 12, 0, 0, 0, time.UTC)
	period := fundamentalsPeriodFixture("sh600519", "2024-12-31", "2025-04-20", "2025-04-22", "rev-1", "hash-v2-sync", fetched)
	raw := newFundamentalsV2(t, root)
	insertFundamentalsV2Period(t, raw, period, strings.Repeat("c", 64))
	if _, err := raw.Exec(`
		INSERT INTO fundamental_sync(code, source, status, periods, last_report_end, last_fetched_at, last_error)
		VALUES('sh600519', 'tushare', 'ok', 1, '2024-12-31', ?, '')`, fetched.Format(time.RFC3339Nano)); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE fundamental_sync_bad (
			code TEXT NOT NULL, source TEXT NOT NULL, status TEXT NOT NULL DEFAULT '',
			periods INTEGER NOT NULL DEFAULT 0 CHECK(periods >= 0),
			last_report_end TEXT NOT NULL DEFAULT '', last_fetched_at TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT INTO fundamental_sync_bad SELECT * FROM fundamental_sync`,
		`DROP TABLE fundamental_sync`,
		`ALTER TABLE fundamental_sync_bad RENAME TO fundamental_sync`,
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
		t.Fatal("v2 with an unkeyed sync table was migrated")
	}
	assertFundamentalsVersion(t, root, 2)
}

func newFundamentalsV2(t *testing.T, root string) *sql.DB {
	t.Helper()
	path := filepath.Join(root, fundamentalsDBName)
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fundamentalsV2TestSchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version=2`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func insertFundamentalsV2Period(t *testing.T, db *sql.DB, period fundamentals.Period, batchID string) {
	t.Helper()
	fetched := period.FetchedAt.UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO fundamental_period(
			code, source, report_end_date, statement_basis, report_type, announcement_date,
			revision, row_hash, schema_version, available_from, comp_type, update_flag,
			fetched_at, revenue, net_profit_attr, equity_attr, operating_cash_flow,
			total_assets, total_liabilities, total_shares, basic_eps, cash_dividend)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		period.Code, period.Source, period.ReportEnd, period.StatementBasis, period.ReportType,
		period.AnnouncementDate, period.Revision, period.RowHash, period.SchemaVersion,
		period.AvailableFrom, period.CompType, period.UpdateFlag, fetched, period.Revenue,
		period.NetProfit, period.Equity, period.OperatingCashFlow, period.TotalAssets,
		period.TotalLiabilities, period.TotalShares, period.BasicEPS, period.CashDividend); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO fundamental_fetch(batch_id, code, source, fetched_at, periods, input_digest, created_at)
		VALUES(?, ?, ?, ?, 1, ?, ?)`,
		batchID, period.Code, period.Source, fetched, batchID, fetched); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO fundamental_fetch_period(batch_id, report_end_date, announcement_date, revision, row_hash)
		VALUES(?, ?, ?, ?, ?)`,
		batchID, period.ReportEnd, period.AnnouncementDate, period.Revision, period.RowHash); err != nil {
		t.Fatal(err)
	}
}

func assertFundamentalsVersion(t *testing.T, root string, want int) {
	t.Helper()
	raw := openRawFundamentals(t, root)
	defer raw.Close()
	var version int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != want {
		t.Fatalf("fundamentals version = %d, want %d, err=%v", version, want, err)
	}
}
