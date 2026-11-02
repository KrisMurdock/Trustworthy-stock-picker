package fourlightstudy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	_ "modernc.org/sqlite"
)

// SnapshotSource holds one read transaction for its entire lifetime. Only
// frozen, rollback-journal SQLite backups are accepted; schema is never changed.
type SnapshotSource struct {
	db       *sql.DB
	tx       *sql.Tx
	manifest Manifest
	sessions map[string]bool
	schema   int
	digest   string
}

func OpenSnapshot(ctx context.Context, path string, manifest Manifest) (_ *SnapshotSource, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("snapshot database must be a regular file, not a symlink")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("snapshot database changed while opening")
	}
	var header [100]byte
	if _, err := io.ReadFull(file, header[:]); err != nil || string(header[:16]) != "SQLite format 3\x00" {
		return nil, errors.New("snapshot is not a SQLite database")
	}
	// Check before asking SQLite to open WAL shared memory or recover journals.
	if header[18] != 1 || header[19] != 1 {
		return nil, errors.New("snapshot must use DELETE journal mode; WAL is unsupported")
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return nil, errors.New("snapshot has SQLite sidecars; create a standalone backup first")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	source := &SnapshotSource{db: db, manifest: manifest, sessions: make(map[string]bool, len(manifest.Calendar.Days))}
	for _, day := range manifest.Calendar.Days {
		source.sessions[day] = true
	}
	defer func() {
		if err != nil {
			_ = source.Close()
		}
	}()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, statement := range []string{"PRAGMA query_only=ON", "PRAGMA busy_timeout=1000"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return nil, fmt.Errorf("configure snapshot: %w", err)
		}
	}
	source.tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin snapshot transaction: %w", err)
	}
	// Reading sqlite_master obtains the shared lock before hashing the file.
	var tables int
	if err := source.tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables); err != nil {
		return nil, fmt.Errorf("read snapshot catalog: %w", err)
	}
	var journal string
	if err := source.tx.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil || strings.ToLower(journal) != "delete" {
		return nil, errors.New("snapshot must use DELETE journal mode")
	}
	if err := source.tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&source.schema); err != nil {
		return nil, fmt.Errorf("read snapshot schema: %w", err)
	}
	if source.schema < 1 || source.schema > 3 {
		return nil, fmt.Errorf("unsupported all-market snapshot schema %d", source.schema)
	}
	current, err := os.Stat(path)
	if err != nil || !os.SameFile(opened, current) {
		return nil, errors.New("snapshot database replaced while acquiring read transaction")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	hash := sha256.New()
	if _, err := io.CopyBuffer(hash, contextReader{ctx: ctx, reader: file}, make([]byte, 1<<20)); err != nil {
		return nil, fmt.Errorf("hash snapshot: %w", err)
	}
	source.digest = hex.EncodeToString(hash.Sum(nil))
	if source.digest != manifest.DatabaseSHA256 {
		return nil, errors.New("snapshot database SHA256 does not match manifest")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return source, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (s *SnapshotSource) DatabaseSHA256() string { return s.digest }
func (s *SnapshotSource) SchemaVersion() int     { return s.schema }

func (s *SnapshotSource) Close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.tx != nil {
		err = s.tx.Rollback()
		if errors.Is(err, sql.ErrTxDone) {
			err = nil
		}
		s.tx = nil
	}
	if s.db != nil {
		err = errors.Join(err, s.db.Close())
		s.db = nil
	}
	return err
}

func (s *SnapshotSource) Stocks(ctx context.Context) ([]config.Stock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.tx == nil {
		return nil, errors.New("snapshot source is closed")
	}
	rows, err := s.tx.QueryContext(ctx, "SELECT code, name, listed_from FROM stock WHERE active=1 ORDER BY code")
	if err != nil {
		return nil, fmt.Errorf("read snapshot universe: %w", err)
	}
	defer rows.Close()
	stocks := []config.Stock{}
	for rows.Next() {
		var stock config.Stock
		if err := rows.Scan(&stock.Code, &stock.Name, &stock.ListedFrom); err != nil {
			return nil, fmt.Errorf("decode snapshot universe: %w", err)
		}
		if !config.ValidCode(stock.Code) || strings.TrimSpace(stock.Name) == "" || (stock.ListedFrom != "" && !snapshotDateValid(stock.ListedFrom)) {
			return nil, fmt.Errorf("invalid active stock metadata for %q", stock.Code)
		}
		stocks = append(stocks, stock)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read snapshot universe: %w", err)
	}
	if len(stocks) == 0 {
		return nil, errors.New("snapshot has no active Shanghai/Shenzhen stocks")
	}
	return stocks, nil
}

func (s *SnapshotSource) Load(ctx context.Context, stock config.Stock) (Pair, error) {
	if err := ctx.Err(); err != nil {
		return Pair{}, err
	}
	if s == nil || s.tx == nil {
		return Pair{}, errors.New("snapshot source is closed")
	}
	if !config.ValidCode(stock.Code) {
		return Pair{}, errors.New("invalid snapshot stock code")
	}
	var pair Pair
	var adjustedSource, rawSource, problem string
	var err error
	pair.Adjusted, pair.AdjustedShares, adjustedSource, problem, err = s.loadBars(ctx, stock.Code, "qfq")
	if err != nil {
		return Pair{}, err
	}
	pair.Problem = problem
	pair.Raw, pair.RawShares, rawSource, problem, err = s.loadBars(ctx, stock.Code, "none")
	if err != nil {
		return Pair{}, err
	}
	if problem != "" {
		if pair.Problem != "" {
			pair.Problem += ";"
		}
		pair.Problem += problem
	}
	pair.AdjustedSource, pair.RawSource = adjustedSource, rawSource
	if s.manifest.Version == 1 && len(pair.Adjusted) > 0 && len(pair.Raw) > 0 && adjustedSource != rawSource {
		if pair.Problem != "" {
			pair.Problem += ";"
		}
		pair.Problem += "adjusted_raw_source_mismatch:" + stock.Code + ":" + pair.Raw[0].Date
		pair.Raw = nil
	}
	return pair, nil
}

func (s *SnapshotSource) loadBars(ctx context.Context, code, adjust string) ([]market.Bar, []float64, string, string, error) {
	// The primary key (code, adjust, trade_date) bounds each lookup. In particular
	// no reserved prices are loaded, even if the backup contains later sessions.
	volumeColumn := "volume_raw"
	if s.manifest.Version == 2 {
		volumeColumn = "volume_shares"
	}
	rows, err := s.tx.QueryContext(ctx, `SELECT trade_date, open_micros, close_micros, high_micros, low_micros,
		volume_raw, `+volumeColumn+`, source, data_field FROM daily_bar
		WHERE code=? AND adjust=? AND trade_date>=? AND trade_date<=? ORDER BY trade_date`,
		code, adjust, s.manifest.Calendar.From, s.manifest.ObservationThrough)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("read snapshot bars %s/%s: %w", code, adjust, err)
	}
	defer rows.Close()
	bars := []market.Bar{}
	shares := []float64{}
	var previous, firstSource, firstField, problem, normalizationProblem string
	for rows.Next() {
		var bar market.Bar
		var open, close, high, low int64
		var volumeShares float64
		var source, field string
		if err := rows.Scan(&bar.Date, &open, &close, &high, &low, &bar.Volume, &volumeShares, &source, &field); err != nil {
			return nil, nil, "", "", fmt.Errorf("decode snapshot bars %s/%s: %w", code, adjust, err)
		}
		bar.Open, bar.Close, bar.High, bar.Low = float64(open)/1e6, float64(close)/1e6, float64(high)/1e6, float64(low)/1e6
		if len(bars) == 0 {
			firstSource, firstField = source, field
		}
		switch {
		case !snapshotDateValid(bar.Date) || bar.Date <= previous:
			problem = "invalid_bar_date_" + adjust
		case !s.sessions[bar.Date]:
			problem = "non_trading_bar_date_" + adjust
		case open <= 0 || close <= 0 || low <= 0 || high < open || high < close || low > open || low > close || math.IsNaN(bar.Volume) || math.IsInf(bar.Volume, 0) || bar.Volume < 0:
			problem = "invalid_bar_values_" + adjust
		case !snapshotProviderValid(source) || (field != "day" && (adjust != "qfq" || field != "qfqday")):
			problem = "invalid_bar_provenance_" + adjust
		case source != firstSource || field != firstField:
			problem = "mixed_bar_provenance_" + adjust
		}
		if problem != "" {
			// Preserve everything verifiable before the first bad session. A later
			// provenance defect must not retrospectively erase an earlier signal or
			// entry. The bad session and its suffix are quarantined, so only paths
			// that need that suffix become unknown.
			problem += ":" + code + ":" + bar.Date
			break
		}
		if s.manifest.Version == 2 && !validNormalizedVolume(code, source, bar.Volume, volumeShares) {
			// Share metadata cannot erase a valid raw-volume four-light signal.
			// Poison only the evaluator's volume at this date, preserving other
			// observations and the signal denominator.
			volumeShares = math.NaN()
			if normalizationProblem == "" {
				normalizationProblem = "invalid_volume_shares_" + adjust + ":" + code + ":" + bar.Date
			}
		}
		previous = bar.Date
		bars = append(bars, bar)
		shares = append(shares, volumeShares)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, "", "", fmt.Errorf("read snapshot bars %s/%s: %w", code, adjust, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, "", "", err
	}
	if len(bars) == 0 && problem == "" {
		problem = "missing_bars_" + adjust + ":" + code
	}
	if normalizationProblem != "" {
		if problem != "" {
			problem += ";"
		}
		problem += normalizationProblem
	}
	return bars, shares, firstSource, problem, nil
}

func snapshotDateValid(value string) bool {
	parsed, err := time.Parse(time.DateOnly, value)
	return err == nil && parsed.Format(time.DateOnly) == value
}

func snapshotProviderValid(value string) bool {
	switch value {
	case "tencent", "sina", "baostock", "tushare", "eastmoney":
		return true
	default:
		return false
	}
}

// Stored shares must agree with provider units, independently of cross-provider
// rounding (handled by the event validator). Never silently repair bad storage.
func validNormalizedVolume(code, source string, raw, shares float64) bool {
	if math.IsNaN(shares) || math.IsInf(shares, 0) || shares < 0 {
		return false
	}
	expected := math.Trunc(market.ProviderVolumeShares(source, code, raw))
	return !math.IsInf(expected, 0) && shares == expected
}
