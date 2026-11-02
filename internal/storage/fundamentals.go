package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fundamentals"

	_ "modernc.org/sqlite"
)

const (
	fundamentalsDBName        = "fundamentals.db"
	fundamentalsLockName      = ".fundamentals.lock"
	fundamentalsSchemaVersion = 4
)

const fundamentalPeriodInsertColumns = `code, source, report_end_date, statement_basis, report_type,
	announcement_date, revision, row_hash, schema_version, available_from,
	cash_dividend_announcement_date, cash_dividend_available_from, comp_type, update_flag,
	fetched_at, revenue, net_profit_attr, equity_attr, operating_cash_flow, total_assets,
	total_liabilities, total_shares, basic_eps, cash_dividend`

// Pre-v4 Tushare rows stored total_share in ten-thousand shares. The adapter
// now canonicalizes the value to shares before it reaches Period, so the v4
// migration must convert existing Tushare rows inside the same transaction.
const fundamentalPeriodLegacySelect = `code, source, report_end_date, statement_basis, report_type,
	announcement_date, revision, row_hash, schema_version, available_from,
	announcement_date, available_from, comp_type, update_flag,
	fetched_at, revenue, net_profit_attr, equity_attr, operating_cash_flow, total_assets,
	total_liabilities,
	CASE WHEN source='tushare' AND total_shares IS NOT NULL THEN total_shares * 10000 ELSE total_shares END,
	basic_eps, cash_dividend`

// FundamentalsDB owns the independent point-in-time financial fact store in
// <data_dir>/fundamentals.db. It never opens or extends market.db.
type FundamentalsDB struct {
	db          *sql.DB
	root        string
	writable    bool
	requireCIFS bool
	lock        *os.File
}

// FundamentalsSyncRecord is the latest fetch outcome for one stock/source.
type FundamentalsSyncRecord struct {
	Code          string `json:"code"`
	Source        string `json:"source"`
	Status        string `json:"status"`
	LastReportEnd string `json:"last_report_end,omitempty"`
	LastFetchedAt string `json:"last_fetched_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	Periods       int    `json:"periods"`
}

// FundamentalsAudit summarizes point-in-time visibility and freshness.
type FundamentalsAudit struct {
	Codes                int      `json:"codes"`
	VisibleCodes         int      `json:"visible_codes"`
	VendorVisibleCodes   int      `json:"vendor_visible_codes"`
	StaleCodes           int      `json:"stale_codes"`
	Periods              int      `json:"periods"`
	FutureFetchedPeriods int      `json:"future_fetched_periods"`
	Fetches              int      `json:"fetches"`
	AsOf                 string   `json:"as_of"`
	Problems             []string `json:"problems"`
}

// FundamentalsFetch records one complete append-only fetch batch. It is an
// audit trail, not the source selected for valuation; period rows retain their
// own fetched_at boundary used by observed visibility.
type FundamentalsFetch struct {
	BatchID     string `json:"batch_id"`
	Code        string `json:"code"`
	Source      string `json:"source"`
	FetchedAt   string `json:"fetched_at"`
	Periods     int    `json:"periods"`
	InputDigest string `json:"input_digest"`
	CreatedAt   string `json:"created_at"`
}

// OpenFundamentals opens an initialized database for writing and holds the
// independent .fundamentals.lock. It never creates the database; use
// InitFundamentals for explicit initialization.
func OpenFundamentals(root string, requireCIFS bool) (*FundamentalsDB, error) {
	return openFundamentals(root, requireCIFS, true, false)
}

// OpenFundamentalsView opens a read-only view. A missing database returns an
// empty view with HasDatabase false.
func OpenFundamentalsView(root string, requireCIFS bool) (*FundamentalsDB, error) {
	return openFundamentals(root, requireCIFS, false, false)
}

// InitFundamentals creates or verifies the independent fundamentals store.
func InitFundamentals(root string, requireCIFS bool) (*FundamentalsDB, error) {
	return openFundamentals(root, requireCIFS, true, true)
}

func openFundamentals(root string, requireCIFS bool, writable, create bool) (*FundamentalsDB, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	view := &Store{root: root, requireCIFS: requireCIFS}
	if err := view.Check(); err != nil {
		return nil, err
	}
	path, exists, err := validateFundamentalsLayout(view, writable)
	if err != nil {
		return nil, err
	}
	if !exists {
		if !writable {
			return &FundamentalsDB{root: root, requireCIFS: requireCIFS}, nil
		}
		if !create {
			return nil, errors.New("fundamentals database is not initialized; run stock-watcher init")
		}
	}
	f := &FundamentalsDB{root: root, writable: writable, requireCIFS: requireCIFS}
	fail := func(err error) (*FundamentalsDB, error) {
		_ = f.Close()
		return nil, err
	}
	if writable {
		lockPath, err := view.path(fundamentalsLockName, false)
		if err != nil {
			return nil, err
		}
		if _, err := inspectFundamentalsFile(lockPath, "fundamentals lock"); err != nil {
			return nil, err
		}
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, errors.New("fundamentals lock is not writable")
		}
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			lock.Close()
			return nil, errors.New("another fundamentals synchronization owns the data directory")
		}
		f.lock = lock
	}
	mode := "ro"
	if writable {
		mode = "rw"
		if create {
			mode = "rwc"
		}
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := url.Values{}
	query.Set("mode", mode)
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return fail(errors.New("cannot open fundamentals database"))
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	f.db = db
	if err := f.configure(create); err != nil {
		return fail(err)
	}
	if create {
		_ = os.Chmod(path, 0600)
	}
	return f, nil
}

func validateFundamentalsLayout(view *Store, includeLock bool) (string, bool, error) {
	path, err := view.path(fundamentalsDBName, false)
	if err != nil {
		return "", false, err
	}
	exists, err := inspectFundamentalsFile(path, "fundamentals database")
	if err != nil {
		return "", false, err
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		sidecar, err := view.path(fundamentalsDBName+suffix, false)
		if err != nil {
			return "", false, err
		}
		if _, err := inspectFundamentalsFile(sidecar, "fundamentals database "+suffix); err != nil {
			return "", false, err
		}
	}
	if includeLock {
		lockPath, err := view.path(fundamentalsLockName, false)
		if err != nil {
			return "", false, err
		}
		if _, err := inspectFundamentalsFile(lockPath, "fundamentals lock"); err != nil {
			return "", false, err
		}
	}
	return path, exists, nil
}

func inspectFundamentalsFile(path, label string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("cannot inspect " + label)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New(label + " must be a regular file")
	}
	return true, nil
}

func (f *FundamentalsDB) configure(create bool) error {
	if f == nil || f.db == nil {
		return errors.New("fundamentals database is unavailable")
	}
	if f.writable {
		for _, statement := range []string{
			"PRAGMA journal_mode=DELETE",
			"PRAGMA synchronous=FULL",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=5000",
		} {
			if _, err := f.db.Exec(statement); err != nil {
				return errors.New("cannot configure fundamentals database")
			}
		}
		if create {
			if err := f.initSchema(); err != nil {
				return err
			}
		} else {
			if err := f.requireSchemaVersion(); err != nil {
				return err
			}
			if err := verifyFundamentalsSchema(f.db); err != nil {
				return err
			}
		}
		var result string
		if err := f.db.QueryRow("PRAGMA quick_check").Scan(&result); err != nil || result != "ok" {
			return errors.New("fundamentals database quick_check failed")
		}
		return nil
	}
	for _, statement := range []string{
		"PRAGMA query_only=ON",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := f.db.Exec(statement); err != nil {
			return errors.New("cannot configure fundamentals database view")
		}
	}
	if err := f.requireSchemaVersion(); err != nil {
		return err
	}
	return verifyFundamentalsSchema(f.db)
}

func (f *FundamentalsDB) requireSchemaVersion() error {
	var version int
	if err := f.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != fundamentalsSchemaVersion {
		return errors.New("unsupported fundamentals database version")
	}
	return nil
}

func (f *FundamentalsDB) initSchema() error {
	var version int
	if err := f.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return errors.New("cannot read fundamentals database version")
	}
	if version < 0 || version > fundamentalsSchemaVersion {
		return errors.New("unsupported fundamentals database version")
	}
	tx, err := f.db.Begin()
	if err != nil {
		return errors.New("cannot initialize fundamentals database")
	}
	defer tx.Rollback()
	switch version {
	case 0:
		if err := createFundamentalsSchema(tx); err != nil {
			return err
		}
	case 1, 2, 3:
		if err := migrateFundamentalsSchema(tx, version); err != nil {
			return err
		}
	}
	if err := verifyFundamentalsSchema(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", fundamentalsSchemaVersion)); err != nil {
		return errors.New("cannot update fundamentals database version")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot initialize or migrate fundamentals database")
	}
	return f.verifySchema()
}

const fundamentalsSchemaSQL = `
	CREATE TABLE IF NOT EXISTS fundamental_period (
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
		cash_dividend_announcement_date TEXT NOT NULL,
		cash_dividend_available_from TEXT NOT NULL,
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
			announcement_date, revision, row_hash, available_from,
			cash_dividend_announcement_date, cash_dividend_available_from)
	);
	CREATE INDEX IF NOT EXISTS fundamental_period_visible
		ON fundamental_period(code, available_from, report_end_date);
	CREATE INDEX IF NOT EXISTS fundamental_period_report
		ON fundamental_period(code, source, report_end_date);
	CREATE TABLE IF NOT EXISTS fundamental_sync (
		code TEXT NOT NULL,
		source TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT '',
		periods INTEGER NOT NULL DEFAULT 0 CHECK(periods >= 0),
		last_report_end TEXT NOT NULL DEFAULT '',
		last_fetched_at TEXT NOT NULL DEFAULT '',
		last_error TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(code, source)
	);
	CREATE TABLE IF NOT EXISTS fundamental_fetch (
		batch_id TEXT PRIMARY KEY,
		code TEXT NOT NULL,
		source TEXT NOT NULL,
		fetched_at TEXT NOT NULL,
		periods INTEGER NOT NULL CHECK(periods > 0),
		input_digest TEXT NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS fundamental_fetch_code
		ON fundamental_fetch(code, fetched_at);
	CREATE TABLE IF NOT EXISTS fundamental_fetch_period (
		batch_id TEXT NOT NULL REFERENCES fundamental_fetch(batch_id) ON DELETE CASCADE,
		report_end_date TEXT NOT NULL,
		statement_basis TEXT NOT NULL,
		report_type TEXT NOT NULL,
		announcement_date TEXT NOT NULL,
		revision TEXT NOT NULL,
		row_hash TEXT NOT NULL,
		available_from TEXT NOT NULL,
		cash_dividend_announcement_date TEXT NOT NULL,
		cash_dividend_available_from TEXT NOT NULL,
		fetched_at TEXT NOT NULL,
		PRIMARY KEY(batch_id, report_end_date, statement_basis, report_type,
			announcement_date, revision, row_hash, available_from,
			cash_dividend_announcement_date, cash_dividend_available_from)
	);
`

func createFundamentalsSchema(tx *sql.Tx) error {
	if _, err := tx.Exec(fundamentalsSchemaSQL); err != nil {
		return errors.New("cannot initialize or migrate fundamentals database")
	}
	return nil
}

// migrateFundamentalsSchema rebuilds the two append-only fact tables so that a
// corrected available_from is preserved as a new observation. The migration is
// transactional; a malformed legacy database fails before user_version changes.
func migrateFundamentalsSchema(tx *sql.Tx, version int) error {
	if err := verifyFundamentalsLegacySchema(tx, version); err != nil {
		return err
	}
	legacyFetchPeriod, err := fundamentalsTableExists(tx, "fundamental_fetch_period")
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE fundamental_period RENAME TO fundamental_period_legacy`); err != nil {
		return errors.New("cannot migrate fundamentals periods")
	}
	if legacyFetchPeriod {
		if _, err := tx.Exec(`ALTER TABLE fundamental_fetch_period RENAME TO fundamental_fetch_period_legacy`); err != nil {
			return errors.New("cannot migrate fundamental fetch periods")
		}
	}
	for _, name := range []string{"fundamental_period_visible", "fundamental_period_report"} {
		if _, err := tx.Exec(`DROP INDEX IF EXISTS ` + name); err != nil {
			return errors.New("cannot migrate fundamentals indexes")
		}
	}
	if err := createFundamentalsSchema(tx); err != nil {
		return err
	}
	var before, after int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM fundamental_period_legacy`).Scan(&before); err != nil {
		return errors.New("cannot count legacy fundamental periods")
	}
	if _, err := tx.Exec(`INSERT INTO fundamental_period(` + fundamentalPeriodInsertColumns + `)
		SELECT ` + fundamentalPeriodLegacySelect + ` FROM fundamental_period_legacy`); err != nil {
		return errors.New("cannot copy legacy fundamental periods")
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM fundamental_period`).Scan(&after); err != nil || before != after {
		return errors.New("cannot preserve legacy fundamental periods")
	}
	if legacyFetchPeriod {
		if err := tx.QueryRow(`SELECT COUNT(*) FROM fundamental_fetch_period_legacy`).Scan(&before); err != nil {
			return errors.New("cannot count legacy fundamental fetch periods")
		}
		if _, err := tx.Exec(`
			INSERT INTO fundamental_fetch_period(
				batch_id, report_end_date, statement_basis, report_type, announcement_date,
				revision, row_hash, available_from, cash_dividend_announcement_date,
				cash_dividend_available_from, fetched_at)
			SELECT fp.batch_id, fp.report_end_date, p.statement_basis, p.report_type,
				fp.announcement_date, fp.revision, fp.row_hash, p.available_from,
				p.announcement_date, p.available_from, p.fetched_at
			FROM fundamental_fetch_period_legacy fp
			JOIN fundamental_fetch f ON f.batch_id=fp.batch_id
			JOIN fundamental_period_legacy p ON p.code=f.code AND p.source=f.source
				AND p.report_end_date=fp.report_end_date
				AND p.announcement_date=fp.announcement_date
				AND p.revision=fp.revision AND p.row_hash=fp.row_hash
		`); err != nil {
			return errors.New("cannot copy legacy fundamental fetch periods")
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM fundamental_fetch_period`).Scan(&after); err != nil || before != after {
			return errors.New("cannot preserve legacy fundamental fetch periods")
		}
		if _, err := tx.Exec(`DROP TABLE fundamental_fetch_period_legacy`); err != nil {
			return errors.New("cannot remove legacy fundamental fetch periods")
		}
	}
	if _, err := tx.Exec(`DROP TABLE fundamental_period_legacy`); err != nil {
		return errors.New("cannot remove legacy fundamental periods")
	}
	return nil
}

func verifyFundamentalsLegacySchema(reader sqlReader, version int) error {
	if err := requireFundamentalsColumns(reader, "fundamental_period", []string{
		"code", "source", "report_end_date", "statement_basis", "report_type", "announcement_date",
		"revision", "row_hash", "schema_version", "available_from", "comp_type", "update_flag",
		"fetched_at", "revenue", "net_profit_attr", "equity_attr", "operating_cash_flow",
		"total_assets", "total_liabilities", "total_shares", "basic_eps", "cash_dividend",
	}); err != nil {
		return err
	}
	fetchExists, err := fundamentalsTableExists(reader, "fundamental_fetch")
	if err != nil {
		return err
	}
	fetchPeriodExists, err := fundamentalsTableExists(reader, "fundamental_fetch_period")
	if err != nil {
		return err
	}
	if version >= 2 && (!fetchExists || !fetchPeriodExists) {
		return errors.New("fundamentals database schema is incomplete")
	}
	syncExists, err := fundamentalsTableExists(reader, "fundamental_sync")
	if err != nil {
		return err
	}
	if version >= 2 && !syncExists {
		return errors.New("fundamentals database schema is incomplete")
	}
	if syncExists {
		if err := requireFundamentalsColumns(reader, "fundamental_sync", []string{
			"code", "source", "status", "periods", "last_report_end", "last_fetched_at", "last_error",
		}); err != nil {
			return err
		}
		if err := requireFundamentalsPrimaryKey(reader, "fundamental_sync", []string{"code", "source"}); err != nil {
			return err
		}
	}
	if fetchExists {
		if err := requireFundamentalsColumns(reader, "fundamental_fetch", []string{
			"batch_id", "code", "source", "fetched_at", "periods", "input_digest", "created_at",
		}); err != nil {
			return err
		}
	}
	if fetchPeriodExists {
		if !fetchExists {
			return errors.New("fundamentals database schema is incomplete")
		}
		if err := requireFundamentalsColumns(reader, "fundamental_fetch_period", []string{
			"batch_id", "report_end_date", "announcement_date", "revision", "row_hash",
		}); err != nil {
			return err
		}
	}
	return nil
}

func (f *FundamentalsDB) verifySchema() error {
	return verifyFundamentalsSchema(f.db)
}

func verifyFundamentalsSchema(reader sqlReader) error {
	rows, err := reader.QueryContext(context.Background(), `
		SELECT name FROM sqlite_master
		WHERE (type='table' AND name IN (
			'fundamental_period', 'fundamental_sync', 'fundamental_fetch', 'fundamental_fetch_period'))
		   OR (type='index' AND name IN (
			'fundamental_period_visible', 'fundamental_period_report', 'fundamental_fetch_code'))
	`)
	if err != nil {
		return errors.New("cannot inspect fundamentals database schema")
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return errors.New("cannot inspect fundamentals database schema")
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return errors.New("cannot inspect fundamentals database schema")
	}
	for _, name := range []string{
		"fundamental_period", "fundamental_sync", "fundamental_fetch", "fundamental_fetch_period",
		"fundamental_period_visible", "fundamental_period_report", "fundamental_fetch_code",
	} {
		if !found[name] {
			return errors.New("fundamentals database schema is incomplete")
		}
	}
	for _, table := range []struct {
		name    string
		columns []string
	}{
		{"fundamental_period", []string{
			"code", "source", "report_end_date", "statement_basis", "report_type", "announcement_date",
			"revision", "row_hash", "schema_version", "available_from", "comp_type", "update_flag",
			"cash_dividend_announcement_date", "cash_dividend_available_from",
			"fetched_at", "revenue", "net_profit_attr", "equity_attr", "operating_cash_flow",
			"total_assets", "total_liabilities", "total_shares", "basic_eps", "cash_dividend",
		}},
		{"fundamental_sync", []string{"code", "source", "status", "periods", "last_report_end", "last_fetched_at", "last_error"}},
		{"fundamental_fetch", []string{"batch_id", "code", "source", "fetched_at", "periods", "input_digest", "created_at"}},
		{"fundamental_fetch_period", []string{
			"batch_id", "report_end_date", "statement_basis", "report_type", "announcement_date",
			"revision", "row_hash", "available_from", "cash_dividend_announcement_date",
			"cash_dividend_available_from", "fetched_at",
		}},
	} {
		if err := requireFundamentalsColumns(reader, table.name, table.columns); err != nil {
			return err
		}
	}
	for _, table := range []struct {
		name    string
		columns []string
	}{
		{"fundamental_period", []string{
			"code", "source", "report_end_date", "statement_basis", "report_type",
			"announcement_date", "revision", "row_hash", "available_from",
			"cash_dividend_announcement_date", "cash_dividend_available_from",
		}},
		{"fundamental_fetch", []string{"batch_id"}},
		{"fundamental_sync", []string{"code", "source"}},
		{"fundamental_fetch_period", []string{
			"batch_id", "report_end_date", "statement_basis", "report_type",
			"announcement_date", "revision", "row_hash", "available_from",
			"cash_dividend_announcement_date", "cash_dividend_available_from",
		}},
	} {
		primaryKey, err := fundamentalsPrimaryKey(reader, table.name)
		if err != nil {
			return err
		}
		if !equalStrings(primaryKey, table.columns) {
			return errors.New("fundamentals database schema has an invalid primary key")
		}
	}
	if err := requireFundamentalsForeignKey(reader, "fundamental_fetch_period", "fundamental_fetch", "batch_id", "batch_id", "CASCADE"); err != nil {
		return err
	}
	return nil
}

func requireFundamentalsColumns(reader sqlReader, table string, required []string) error {
	columns, err := fundamentalsTableColumns(reader, table)
	if err != nil {
		return err
	}
	for _, column := range required {
		if !columns[column] {
			return errors.New("fundamentals database schema is incomplete")
		}
	}
	return nil
}

func requireFundamentalsPrimaryKey(reader sqlReader, table string, expected []string) error {
	primaryKey, err := fundamentalsPrimaryKey(reader, table)
	if err != nil {
		return err
	}
	if !equalStrings(primaryKey, expected) {
		return errors.New("fundamentals database schema has an invalid primary key")
	}
	return nil
}

func fundamentalsTableExists(reader sqlReader, table string) (bool, error) {
	var count int
	if err := reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
		return false, errors.New("cannot inspect fundamentals database schema")
	}
	return count == 1, nil
}

func fundamentalsTableColumns(reader sqlReader, table string) (map[string]bool, error) {
	rows, err := reader.QueryContext(context.Background(), "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, errors.New("cannot inspect fundamentals database columns")
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var fallback sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &fallback, &primaryKey); err != nil {
			return nil, errors.New("cannot inspect fundamentals database columns")
		}
		columns[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot inspect fundamentals database columns")
	}
	return columns, nil
}

func fundamentalsPrimaryKey(reader sqlReader, table string) ([]string, error) {
	rows, err := reader.QueryContext(context.Background(), "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, errors.New("cannot inspect fundamentals database primary key")
	}
	defer rows.Close()
	keyed := map[int]string{}
	maxOrder := 0
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var fallback sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &fallback, &primaryKey); err != nil {
			return nil, errors.New("cannot inspect fundamentals database primary key")
		}
		if primaryKey > 0 {
			keyed[primaryKey] = strings.ToLower(name)
			if primaryKey > maxOrder {
				maxOrder = primaryKey
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot inspect fundamentals database primary key")
	}
	key := make([]string, 0, maxOrder)
	for order := 1; order <= maxOrder; order++ {
		name, ok := keyed[order]
		if !ok {
			return nil, errors.New("fundamentals database schema has a sparse primary key")
		}
		key = append(key, name)
	}
	return key, nil
}

func requireFundamentalsForeignKey(reader sqlReader, table, target, from, to, onDelete string) error {
	rows, err := reader.QueryContext(context.Background(), "PRAGMA foreign_key_list("+table+")")
	if err != nil {
		return errors.New("cannot inspect fundamentals database foreign keys")
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var id, seq int
		var parent, fromColumn, toColumn, onUpdate, onDeleteValue, match string
		if err := rows.Scan(&id, &seq, &parent, &fromColumn, &toColumn, &onUpdate, &onDeleteValue, &match); err != nil {
			return errors.New("cannot inspect fundamentals database foreign keys")
		}
		if parent == target && fromColumn == from && toColumn == to && strings.EqualFold(onDeleteValue, onDelete) {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return errors.New("cannot inspect fundamentals database foreign keys")
	}
	if !found {
		return errors.New("fundamentals database schema is missing a required foreign key")
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (f *FundamentalsDB) Close() error {
	if f == nil {
		return nil
	}
	var err error
	if f.db != nil {
		err = f.db.Close()
		f.db = nil
	}
	if f.lock != nil {
		err = errors.Join(err, syscall.Flock(int(f.lock.Fd()), syscall.LOCK_UN), f.lock.Close())
		f.lock = nil
	}
	return err
}

func (f *FundamentalsDB) Path() string {
	if f == nil {
		return ""
	}
	return filepath.Join(f.root, fundamentalsDBName)
}

func (f *FundamentalsDB) HasDatabase() bool {
	return f != nil && f.db != nil
}

func (f *FundamentalsDB) checkReader(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f == nil {
		return fmt.Errorf("fundamentals database is not initialized: %w", fs.ErrNotExist)
	}
	if err := (&Store{root: f.root, requireCIFS: f.requireCIFS}).Check(); err != nil {
		return err
	}
	if f.db == nil {
		return fmt.Errorf("fundamentals database is not initialized: %w", fs.ErrNotExist)
	}
	return nil
}

func (f *FundamentalsDB) checkWriter(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f == nil || !f.writable || f.db == nil || f.lock == nil {
		return errors.New("fundamentals database is read-only")
	}
	view := &Store{root: f.root, requireCIFS: f.requireCIFS}
	if err := view.Check(); err != nil {
		return err
	}
	_, exists, err := validateFundamentalsLayout(view, true)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("fundamentals database is not initialized")
	}
	return nil
}

func (f *FundamentalsDB) AppendSeries(ctx context.Context, series fundamentals.Series) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := series.Validate(); err != nil {
		return 0, fmt.Errorf("invalid fundamental series: %w", err)
	}
	if err := f.checkWriter(ctx); err != nil {
		return 0, err
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, errors.New("cannot begin fundamentals transaction")
	}
	rollback := func(err error) (int, error) {
		_ = tx.Rollback()
		return 0, err
	}
	if err := ensureFundamentalsSource(ctx, tx, series.Code, series.Source); err != nil {
		return rollback(err)
	}
	batchID, err := series.BatchID()
	if err != nil {
		return rollback(errors.New("cannot identify fundamental fetch batch"))
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO fundamental_fetch(batch_id, code, source, fetched_at, periods, input_digest, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(batch_id) DO NOTHING
	`, batchID, series.Code, series.Source, series.FetchedAt.UTC().Format(time.RFC3339Nano),
		len(series.Periods), batchID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return rollback(errors.New("cannot persist fundamental fetch batch"))
	}
	inserted := 0
	for _, period := range series.Periods {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO fundamental_period(
				code, source, report_end_date, statement_basis, report_type, announcement_date,
				revision, row_hash, schema_version, available_from, cash_dividend_announcement_date,
				cash_dividend_available_from, comp_type, update_flag,
				fetched_at, revenue, net_profit_attr, equity_attr, operating_cash_flow,
				total_assets, total_liabilities, total_shares, basic_eps, cash_dividend)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(code, source, report_end_date, statement_basis, report_type,
				announcement_date, revision, row_hash, available_from,
				cash_dividend_announcement_date, cash_dividend_available_from) DO NOTHING
		`, period.Code, period.Source, period.ReportEnd, period.StatementBasis, period.ReportType,
			period.AnnouncementDate, period.Revision, period.RowHash, period.SchemaVersion,
			period.AvailableFrom, period.CashDividendAnnouncementDate, period.CashDividendAvailableFrom,
			period.CompType, period.UpdateFlag,
			period.FetchedAt.UTC().Format(time.RFC3339Nano), nullableFundamentalFloat(period.Revenue),
			nullableFundamentalFloat(period.NetProfit), nullableFundamentalFloat(period.Equity),
			nullableFundamentalFloat(period.OperatingCashFlow), nullableFundamentalFloat(period.TotalAssets),
			nullableFundamentalFloat(period.TotalLiabilities), nullableFundamentalFloat(period.TotalShares),
			nullableFundamentalFloat(period.BasicEPS), nullableFundamentalFloat(period.CashDividend))
		if err != nil {
			return rollback(errors.New("cannot persist fundamental period"))
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return rollback(errors.New("cannot count persisted fundamental periods"))
		}
		inserted += int(affected)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO fundamental_fetch_period(
				batch_id, report_end_date, statement_basis, report_type, announcement_date,
				revision, row_hash, available_from, cash_dividend_announcement_date,
				cash_dividend_available_from, fetched_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(batch_id, report_end_date, statement_basis, report_type,
				announcement_date, revision, row_hash, available_from,
				cash_dividend_announcement_date, cash_dividend_available_from) DO NOTHING
		`, batchID, period.ReportEnd, period.StatementBasis, period.ReportType,
			period.AnnouncementDate, period.Revision, period.RowHash, period.AvailableFrom,
			period.CashDividendAnnouncementDate, period.CashDividendAvailableFrom,
			period.FetchedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return rollback(errors.New("cannot persist fundamental fetch batch period"))
		}
	}
	periods, lastReportEnd, _, err := summarizeFundamentals(ctx, tx, series.Code, series.Source)
	if err != nil {
		return rollback(err)
	}
	lastFetchedAt := series.FetchedAt.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO fundamental_sync(code, source, status, periods, last_report_end, last_fetched_at, last_error)
		VALUES(?, ?, 'ok', ?, ?, ?, '')
		ON CONFLICT(code, source) DO UPDATE SET status='ok', periods=excluded.periods,
			last_report_end=excluded.last_report_end, last_fetched_at=excluded.last_fetched_at,
			last_error=''
	`, series.Code, series.Source, periods, lastReportEnd, lastFetchedAt); err != nil {
		return rollback(errors.New("cannot update fundamentals synchronization summary"))
	}
	if err := tx.Commit(); err != nil {
		return 0, errors.New("cannot commit fundamentals series")
	}
	return inserted, nil
}

func summarizeFundamentals(ctx context.Context, tx *sql.Tx, code, source string) (int, string, string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT report_end_date, fetched_at FROM fundamental_period WHERE code=? AND source=?
	`, code, source)
	if err != nil {
		return 0, "", "", errors.New("cannot summarize fundamental periods")
	}
	periods := 0
	var lastReportEnd string
	var lastFetchedAt time.Time
	var lastFetchedRaw string
	for rows.Next() {
		var reportEnd, fetched string
		if err := rows.Scan(&reportEnd, &fetched); err != nil {
			rows.Close()
			return 0, "", "", errors.New("cannot summarize fundamental periods")
		}
		parsed, err := time.Parse(time.RFC3339Nano, fetched)
		if err != nil {
			rows.Close()
			return 0, "", "", errors.New("invalid fundamental fetched_at")
		}
		periods++
		if reportEnd > lastReportEnd {
			lastReportEnd = reportEnd
		}
		if parsed.After(lastFetchedAt) {
			lastFetchedAt = parsed
			lastFetchedRaw = fetched
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, "", "", errors.New("cannot summarize fundamental periods")
	}
	if err := rows.Close(); err != nil {
		return 0, "", "", errors.New("cannot summarize fundamental periods")
	}
	if periods == 0 {
		return 0, "", "", errors.New("cannot summarize empty fundamental series")
	}
	return periods, lastReportEnd, lastFetchedRaw, nil
}

func ensureFundamentalsSource(ctx context.Context, tx *sql.Tx, code, source string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT source FROM fundamental_period WHERE code=?
		UNION
		SELECT source FROM fundamental_sync WHERE code=?
	`, code, code)
	if err != nil {
		return errors.New("cannot inspect fundamentals source")
	}
	defer rows.Close()
	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return errors.New("cannot inspect fundamentals source")
		}
		if existing != source {
			return fmt.Errorf("fundamentals for %s already use source %s", code, existing)
		}
	}
	if err := rows.Err(); err != nil {
		return errors.New("cannot inspect fundamentals source")
	}
	return nil
}

func (f *FundamentalsDB) LoadSeries(ctx context.Context, code string) (fundamentals.Series, error) {
	if err := f.checkReader(ctx); err != nil {
		return fundamentals.Series{}, err
	}
	if !config.ValidCode(code) {
		return fundamentals.Series{}, errors.New("invalid fundamental stock code")
	}
	rows, err := f.db.QueryContext(ctx, `
		SELECT code, source, report_end_date, statement_basis, report_type, announcement_date,
			revision, row_hash, schema_version, available_from, cash_dividend_announcement_date,
			cash_dividend_available_from, comp_type, update_flag, fetched_at, revenue,
			net_profit_attr, equity_attr, operating_cash_flow, total_assets,
			total_liabilities, total_shares, basic_eps, cash_dividend
		FROM fundamental_period
		WHERE code=?
		ORDER BY report_end_date, announcement_date, revision, row_hash, fetched_at
	`, code)
	if err != nil {
		return fundamentals.Series{}, errors.New("cannot read fundamental series")
	}
	defer rows.Close()
	series := fundamentals.Series{SchemaVersion: fundamentals.Version, Code: code}
	var fetchedAt time.Time
	for rows.Next() {
		var period fundamentals.Period
		var fetched string
		var revenue, netProfit, equity, operatingCashFlow sql.NullFloat64
		var totalAssets, totalLiabilities, totalShares, basicEPS, cashDividend sql.NullFloat64
		if err := rows.Scan(
			&period.Code, &period.Source, &period.ReportEnd, &period.StatementBasis,
			&period.ReportType, &period.AnnouncementDate, &period.Revision, &period.RowHash,
			&period.SchemaVersion, &period.AvailableFrom, &period.CashDividendAnnouncementDate,
			&period.CashDividendAvailableFrom, &period.CompType, &period.UpdateFlag, &fetched,
			&revenue, &netProfit, &equity, &operatingCashFlow, &totalAssets,
			&totalLiabilities, &totalShares, &basicEPS, &cashDividend,
		); err != nil {
			return fundamentals.Series{}, errors.New("cannot read fundamental series")
		}
		parsed, err := time.Parse(time.RFC3339Nano, fetched)
		if err != nil {
			return fundamentals.Series{}, errors.New("invalid fundamental fetched_at")
		}
		period.FetchedAt = parsed
		period.Revenue = fundamentalFloatPtr(revenue)
		period.NetProfit = fundamentalFloatPtr(netProfit)
		period.Equity = fundamentalFloatPtr(equity)
		period.OperatingCashFlow = fundamentalFloatPtr(operatingCashFlow)
		period.TotalAssets = fundamentalFloatPtr(totalAssets)
		period.TotalLiabilities = fundamentalFloatPtr(totalLiabilities)
		period.TotalShares = fundamentalFloatPtr(totalShares)
		period.BasicEPS = fundamentalFloatPtr(basicEPS)
		period.CashDividend = fundamentalFloatPtr(cashDividend)
		if series.Source == "" {
			series.Source = period.Source
		} else if series.Source != period.Source {
			return fundamentals.Series{}, errors.New("fundamentals database contains multiple sources for stock")
		}
		if parsed.After(fetchedAt) {
			fetchedAt = parsed
		}
		series.Periods = append(series.Periods, period)
	}
	if err := rows.Err(); err != nil {
		return fundamentals.Series{}, errors.New("cannot read fundamental series")
	}
	if len(series.Periods) == 0 {
		return fundamentals.Series{}, fmt.Errorf("no fundamental series for %s: %w", code, fs.ErrNotExist)
	}
	series.FetchedAt = fetchedAt
	sort.Slice(series.Periods, func(i, j int) bool {
		left, right := series.Periods[i], series.Periods[j]
		if left.ReportEnd != right.ReportEnd {
			return left.ReportEnd < right.ReportEnd
		}
		if left.AnnouncementDate != right.AnnouncementDate {
			return left.AnnouncementDate < right.AnnouncementDate
		}
		if left.Revision != right.Revision {
			return left.Revision < right.Revision
		}
		if left.RowHash != right.RowHash {
			return left.RowHash < right.RowHash
		}
		if left.AvailableFrom != right.AvailableFrom {
			return left.AvailableFrom < right.AvailableFrom
		}
		if left.CashDividendAnnouncementDate != right.CashDividendAnnouncementDate {
			return left.CashDividendAnnouncementDate < right.CashDividendAnnouncementDate
		}
		if left.CashDividendAvailableFrom != right.CashDividendAvailableFrom {
			return left.CashDividendAvailableFrom < right.CashDividendAvailableFrom
		}
		return left.FetchedAt.Before(right.FetchedAt)
	})
	if err := series.Validate(); err != nil {
		return fundamentals.Series{}, fmt.Errorf("invalid stored fundamental series: %w", err)
	}
	return series, nil
}

func (f *FundamentalsDB) LoadVisibleSeries(ctx context.Context, code, asOf string) (fundamentals.Series, error) {
	return f.LoadVisibleSeriesMode(ctx, code, asOf, fundamentals.VisibilityObserved)
}

// LoadVisibleSeriesMode applies the requested point-in-time visibility mode.
// Observed is the safe default; VendorBackfill must be requested explicitly.
func (f *FundamentalsDB) LoadVisibleSeriesMode(ctx context.Context, code, asOf string, mode fundamentals.VisibilityMode) (fundamentals.Series, error) {
	if _, err := time.Parse("2006-01-02", asOf); err != nil {
		return fundamentals.Series{}, errors.New("invalid fundamentals as-of date")
	}
	if !mode.Valid() {
		return fundamentals.Series{}, errors.New("invalid fundamentals visibility mode")
	}
	series, err := f.LoadSeries(ctx, code)
	if err != nil {
		return fundamentals.Series{}, err
	}
	series.Periods = series.Visible(asOf, mode)
	series.FetchedAt = time.Time{}
	for _, period := range series.Periods {
		if period.FetchedAt.After(series.FetchedAt) {
			series.FetchedAt = period.FetchedAt
		}
	}
	return series, nil
}

// ListFetches returns newest fetch batches first. It is intended for audits of
// historical coverage and data provenance, not for selecting valuation inputs.
func (f *FundamentalsDB) ListFetches(ctx context.Context, code string, limit int) ([]FundamentalsFetch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if code != "" && !config.ValidCode(code) {
		return nil, errors.New("invalid fundamentals stock code")
	}
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("fundamentals fetch limit must be between 1 and 1000")
	}
	if f == nil {
		return nil, errors.New("fundamentals database is unavailable")
	}
	if err := (&Store{root: f.root, requireCIFS: f.requireCIFS}).Check(); err != nil {
		return nil, err
	}
	if f.db == nil {
		return []FundamentalsFetch{}, nil
	}
	rows, err := f.db.QueryContext(ctx, `
		SELECT batch_id, code, source, fetched_at, periods, input_digest, created_at
		FROM fundamental_fetch
		WHERE (?='' OR code=?)
		ORDER BY fetched_at DESC, batch_id DESC
		LIMIT ?
	`, code, code, limit)
	if err != nil {
		return nil, errors.New("cannot list fundamental fetch batches")
	}
	defer rows.Close()
	out := make([]FundamentalsFetch, 0)
	for rows.Next() {
		var fetch FundamentalsFetch
		if err := rows.Scan(&fetch.BatchID, &fetch.Code, &fetch.Source, &fetch.FetchedAt,
			&fetch.Periods, &fetch.InputDigest, &fetch.CreatedAt); err != nil {
			return nil, errors.New("cannot list fundamental fetch batches")
		}
		out = append(out, fetch)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot list fundamental fetch batches")
	}
	return out, nil
}

func (f *FundamentalsDB) RecordSync(ctx context.Context, rec FundamentalsSyncRecord) error {
	if err := f.checkWriter(ctx); err != nil {
		return err
	}
	if !config.ValidCode(rec.Code) || strings.TrimSpace(rec.Source) == "" || strings.TrimSpace(rec.Status) == "" || rec.Periods < 0 {
		return errors.New("invalid fundamentals synchronization record")
	}
	if rec.LastReportEnd != "" {
		if _, err := time.Parse("2006-01-02", rec.LastReportEnd); err != nil {
			return errors.New("invalid fundamentals last report end")
		}
	}
	if rec.LastFetchedAt != "" {
		fetched, err := time.Parse(time.RFC3339Nano, rec.LastFetchedAt)
		if err != nil {
			return errors.New("invalid fundamentals last fetched time")
		}
		rec.LastFetchedAt = fetched.UTC().Format(time.RFC3339Nano)
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin fundamentals synchronization transaction")
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	if err := ensureFundamentalsSource(ctx, tx, rec.Code, rec.Source); err != nil {
		return rollback(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO fundamental_sync(code, source, status, periods, last_report_end, last_fetched_at, last_error)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(code, source) DO UPDATE SET status=excluded.status,
			periods=CASE
				WHEN excluded.status='ok' OR excluded.periods>0 THEN excluded.periods
				ELSE fundamental_sync.periods
			END,
			last_report_end=CASE
				WHEN excluded.status='ok' OR excluded.last_report_end<>'' THEN excluded.last_report_end
				ELSE fundamental_sync.last_report_end
			END,
			last_fetched_at=CASE
				WHEN excluded.status='ok' OR excluded.last_fetched_at<>'' THEN excluded.last_fetched_at
				ELSE fundamental_sync.last_fetched_at
			END,
			last_error=excluded.last_error
	`, rec.Code, rec.Source, rec.Status, rec.Periods, rec.LastReportEnd, rec.LastFetchedAt, rec.LastError); err != nil {
		return rollback(errors.New("cannot record fundamentals synchronization"))
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit fundamentals synchronization")
	}
	return nil
}

func (f *FundamentalsDB) ListSync(ctx context.Context) ([]FundamentalsSyncRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errors.New("fundamentals database is unavailable")
	}
	if err := (&Store{root: f.root, requireCIFS: f.requireCIFS}).Check(); err != nil {
		return nil, err
	}
	if f.db == nil {
		return []FundamentalsSyncRecord{}, nil
	}
	rows, err := f.db.QueryContext(ctx, `
		SELECT code, source, status, periods, last_report_end, last_fetched_at, last_error
		FROM fundamental_sync ORDER BY code, source
	`)
	if err != nil {
		return nil, errors.New("cannot list fundamentals synchronization")
	}
	defer rows.Close()
	out := make([]FundamentalsSyncRecord, 0)
	for rows.Next() {
		var rec FundamentalsSyncRecord
		if err := rows.Scan(&rec.Code, &rec.Source, &rec.Status, &rec.Periods, &rec.LastReportEnd, &rec.LastFetchedAt, &rec.LastError); err != nil {
			return nil, errors.New("cannot list fundamentals synchronization")
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot list fundamentals synchronization")
	}
	return out, nil
}

func (f *FundamentalsDB) Audit(ctx context.Context, asOf string) (FundamentalsAudit, error) {
	audit := FundamentalsAudit{AsOf: asOf, Problems: make([]string, 0)}
	asOfTime, err := time.Parse("2006-01-02", asOf)
	if err != nil {
		return audit, errors.New("invalid fundamentals as-of date")
	}
	if err := ctx.Err(); err != nil {
		return audit, err
	}
	if f == nil {
		return audit, errors.New("fundamentals database is unavailable")
	}
	if err := (&Store{root: f.root, requireCIFS: f.requireCIFS}).Check(); err != nil {
		return audit, err
	}
	if f.db == nil {
		return audit, nil
	}
	if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fundamental_fetch`).Scan(&audit.Fetches); err != nil {
		return audit, errors.New("cannot count fundamental fetch batches")
	}
	rows, err := f.db.QueryContext(ctx, `
		SELECT code, report_end_date, statement_basis, report_type, announcement_date,
			revision, available_from, cash_dividend_announcement_date,
			cash_dividend_available_from, update_flag, fetched_at, row_hash
		FROM fundamental_period
		ORDER BY code, report_end_date, announcement_date, revision, row_hash, fetched_at
	`)
	if err != nil {
		return audit, errors.New("cannot audit fundamentals database")
	}
	defer rows.Close()
	byCode := map[string][]fundamentals.Period{}
	for rows.Next() {
		var period fundamentals.Period
		var fetched string
		if err := rows.Scan(&period.Code, &period.ReportEnd, &period.StatementBasis, &period.ReportType,
			&period.AnnouncementDate, &period.Revision, &period.AvailableFrom,
			&period.CashDividendAnnouncementDate, &period.CashDividendAvailableFrom,
			&period.UpdateFlag, &fetched, &period.RowHash); err != nil {
			return audit, errors.New("cannot audit fundamentals database")
		}
		parsed, err := time.Parse(time.RFC3339Nano, fetched)
		if err != nil {
			return audit, errors.New("invalid fundamental fetched_at")
		}
		period.FetchedAt = parsed
		if !period.ObservedBy(asOf) {
			audit.FutureFetchedPeriods++
		}
		byCode[period.Code] = append(byCode[period.Code], period)
		audit.Periods++
	}
	if err := rows.Err(); err != nil {
		return audit, errors.New("cannot audit fundamentals database")
	}
	codes := make([]string, 0, len(byCode))
	for code := range byCode {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	audit.Codes = len(codes)
	staleBefore := asOfTime.AddDate(0, -18, 0)
	for _, code := range codes {
		series := fundamentals.Series{Periods: byCode[code]}
		vendorVisible := series.Visible(asOf, fundamentals.VisibilityVendorBackfill)
		if len(vendorVisible) > 0 {
			audit.VendorVisibleCodes++
		}
		visible := series.Visible(asOf, fundamentals.VisibilityObserved)
		if len(visible) == 0 {
			if len(vendorVisible) > 0 {
				audit.Problems = append(audit.Problems, "code="+code+":future_fetched_only")
			} else {
				audit.Problems = append(audit.Problems, "code="+code+":no_visible_period")
			}
			continue
		}
		audit.VisibleCodes++
		latest, err := time.Parse("2006-01-02", visible[len(visible)-1].ReportEnd)
		if err != nil {
			return audit, errors.New("invalid fundamental report end")
		}
		if latest.Before(staleBefore) {
			audit.StaleCodes++
			audit.Problems = append(audit.Problems, "code="+code+":stale_report_end="+visible[len(visible)-1].ReportEnd)
		}
	}
	return audit, nil
}

func nullableFundamentalFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func fundamentalFloatPtr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	out := value.Float64
	return &out
}
