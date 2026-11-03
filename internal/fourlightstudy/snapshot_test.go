package fourlightstudy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
)

func snapshotTestManifest(t *testing.T) Manifest {
	t.Helper()
	manifest := Manifest{
		Version: 1, DatabaseSHA256: strings.Repeat("0", 64), AsOf: "2026-02-06", From: "2026-02-02", Through: "2026-02-03",
		ObservationThrough: "2026-02-04", ReservedFrom: "2026-02-05", UniversePolicy: "current_active_sh_sz",
		Calendar: experiment.Calendar{Version: 1, From: "2026-01-01", Through: "2026-02-06", Source: "fixture", Digest: "fixture",
			Days: []string{"2026-01-30", "2026-02-02", "2026-02-03", "2026-02-04", "2026-02-05", "2026-02-06"}},
		Parameters: config.Defaults().Strategy,
	}
	history, err := calendar.NewHistory(manifest.Calendar.Source, manifest.Calendar.From, manifest.Calendar.Through, manifest.Calendar.Days, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Calendar.Digest = history.Digest
	return manifest
}

func snapshotTestDatabase(t *testing.T, version int, edits string) (string, Manifest) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(fmt.Sprintf(`PRAGMA synchronous=OFF; PRAGMA journal_mode=DELETE; PRAGMA user_version=%d;
		CREATE TABLE stock(code TEXT PRIMARY KEY, name TEXT, listed_from TEXT, active INTEGER);
		CREATE TABLE daily_bar(code TEXT, adjust TEXT, trade_date TEXT, open_micros INTEGER, close_micros INTEGER,
		 high_micros INTEGER, low_micros INTEGER, volume_raw REAL, source TEXT, data_field TEXT,
		 PRIMARY KEY(code,adjust,trade_date));
		INSERT INTO stock VALUES('sz000001','Fixture','1991-04-03',1),('sh600000','Inactive','1999-11-10',0);`, version))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, adjust := range []string{"qfq", "none"} {
		for _, day := range []string{"2026-01-30", "2026-02-02", "2026-02-03", "2026-02-04", "2026-02-05", "2026-02-06"} {
			field := "day"
			if adjust == "qfq" {
				field = "qfqday"
			}
			if _, err := tx.Exec("INSERT INTO daily_bar VALUES(?,?,?,?,?,?,?,?,?,?)", "sz000001", adjust, day, 10_000_000, 10_100_000, 10_200_000, 9_900_000, 1200, "tencent", field); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE daily_bar ADD COLUMN volume_shares INTEGER; UPDATE daily_bar SET volume_shares=CAST(volume_raw*100 AS INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if edits != "" {
		if _, err := db.Exec(edits); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	m := snapshotTestManifest(t)
	m.DatabaseSHA256 = hex.EncodeToString(hash[:])
	return path, m
}

func TestSnapshotSchemasBoundsAndReadOnly(t *testing.T) {
	for _, version := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path, manifest := snapshotTestDatabase(t, version, "")
			source, err := OpenSnapshot(context.Background(), path, manifest)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			stocks, err := source.Stocks(context.Background())
			if err != nil || len(stocks) != 1 || stocks[0].Code != "sz000001" {
				t.Fatalf("stocks=%+v err=%v", stocks, err)
			}
			pair, err := source.Load(context.Background(), stocks[0])
			if err != nil || pair.Problem != "" || len(pair.Adjusted) != 4 || len(pair.Raw) != 4 {
				t.Fatalf("pair=%+v err=%v", pair, err)
			}
			if pair.Raw[0].Close != 10.1 || pair.Raw[3].Date != manifest.ObservationThrough {
				t.Fatalf("price conversion or observation boundary failed: %+v", pair.Raw)
			}
			if source.SchemaVersion() != version || source.DatabaseSHA256() != manifest.DatabaseSHA256 {
				t.Fatal("snapshot provenance not preserved")
			}
			if _, err := source.tx.Exec("DELETE FROM stock"); err == nil {
				t.Fatal("snapshot transaction accepted a write")
			}
			if err := source.Close(); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(path)
			hash := sha256.Sum256(data)
			if hex.EncodeToString(hash[:]) != manifest.DatabaseSHA256 {
				t.Fatal("read altered snapshot bytes")
			}
			if _, err := source.Stocks(context.Background()); err == nil {
				t.Fatal("closed source accepted read")
			}
		})
	}
}

func TestSnapshotRejectsUnsafeInputs(t *testing.T) {
	path, manifest := snapshotTestDatabase(t, 1, "")
	badHash := manifest
	badHash.DatabaseSHA256 = strings.Repeat("a", 64)
	badVersion, versionManifest := snapshotTestDatabase(t, 5, "")
	walPath, walManifest := snapshotTestDatabase(t, 1, "PRAGMA journal_mode=WAL")
	link := filepath.Join(t.TempDir(), "link.db")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, want string
		manifest         Manifest
	}{
		{"hash", path, "SHA256", badHash},
		{"schema", badVersion, "schema 5", versionManifest},
		{"wal", walPath, "WAL", walManifest},
		{"missing", path + ".missing", "no such file", manifest},
		{"symlink", link, "regular file", manifest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if source, err := OpenSnapshot(context.Background(), tc.path, tc.manifest); err == nil || !strings.Contains(err.Error(), tc.want) {
				if source != nil {
					source.Close()
				}
				t.Fatalf("wanted %s, got %v", tc.want, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenSnapshot(ctx, path, manifest); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := OpenSnapshot(ctx, path, manifest); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout=%v", err)
	}
	if err := os.WriteFile(path+"-journal", []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSnapshot(context.Background(), path, manifest); err == nil || !strings.Contains(err.Error(), "sidecars") {
		t.Fatalf("sidecar=%v", err)
	}
}

func TestSnapshotAuditsStockLevelDataProblems(t *testing.T) {
	for _, tc := range []struct {
		name, edit, want string
		adjusted, raw    int
	}{
		{"missing raw", "DELETE FROM daily_bar WHERE adjust='none'", "missing_bars_none:sz000001", 4, 0},
		{"mixed raw", "UPDATE daily_bar SET source='sina' WHERE adjust='none' AND trade_date='2026-02-03'", "mixed_bar_provenance_none:sz000001:2026-02-03", 4, 2},
		{"mixed adjusted", "UPDATE daily_bar SET source='sina' WHERE adjust='qfq' AND trade_date='2026-02-03'", "mixed_bar_provenance_qfq:sz000001:2026-02-03", 2, 4},
		{"cross provider", "UPDATE daily_bar SET source='sina' WHERE adjust='none'", "adjusted_raw_source_mismatch:sz000001:2026-01-30", 4, 0},
		{"cross provider and later defect", "UPDATE daily_bar SET source='sina' WHERE adjust='none'; UPDATE daily_bar SET data_field='qfqday' WHERE adjust='none' AND trade_date='2026-02-03'", "invalid_bar_provenance_none:sz000001:2026-02-03;adjusted_raw_source_mismatch:sz000001:2026-01-30", 4, 0},
		{"invalid field", "UPDATE daily_bar SET data_field='qfqday' WHERE adjust='none'", "invalid_bar_provenance_none:sz000001:2026-01-30", 4, 0},
		{"invalid price", "UPDATE daily_bar SET low_micros=0 WHERE adjust='none'", "invalid_bar_values_none:sz000001:2026-01-30", 4, 0},
		{"later invalid price", "UPDATE daily_bar SET low_micros=0 WHERE adjust='none' AND trade_date='2026-02-03'", "invalid_bar_values_none:sz000001:2026-02-03", 4, 2},
		{"invalid date", "UPDATE daily_bar SET trade_date='2026-01-32' WHERE adjust='qfq' AND trade_date='2026-01-30'", "invalid_bar_date_qfq:sz000001:2026-01-32", 0, 4},
		{"non trading date", "UPDATE daily_bar SET trade_date='2026-02-01' WHERE adjust='qfq' AND trade_date='2026-02-02'", "non_trading_bar_date_qfq:sz000001:2026-02-01", 1, 4},
		{"reserved bad source ignored", "UPDATE daily_bar SET source='invalid' WHERE trade_date>='2026-02-05'", "", 4, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, manifest := snapshotTestDatabase(t, 1, tc.edit)
			source, err := OpenSnapshot(context.Background(), path, manifest)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			pair, err := source.Load(context.Background(), config.Stock{Code: "sz000001"})
			if err != nil || pair.Problem != tc.want || len(pair.Adjusted) != tc.adjusted || len(pair.Raw) != tc.raw {
				t.Fatalf("pair=%+v err=%v", pair, err)
			}
		})
	}
}

func TestSnapshotUniverseValidationAndCancellation(t *testing.T) {
	for _, edit := range []string{"DELETE FROM stock", "UPDATE stock SET code='bad' WHERE active=1", "UPDATE stock SET listed_from='2026-02-30' WHERE active=1"} {
		path, manifest := snapshotTestDatabase(t, 1, edit)
		source, err := OpenSnapshot(context.Background(), path, manifest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := source.Stocks(context.Background()); err == nil {
			t.Fatalf("accepted %s", edit)
		}
		source.Close()
	}
	path, manifest := snapshotTestDatabase(t, 1, "")
	source, err := OpenSnapshot(context.Background(), path, manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Stocks(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("universe cancellation=%v", err)
	}
	if _, err := source.Load(ctx, config.Stock{Code: "sz000001"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("history cancellation=%v", err)
	}
}

func TestSnapshotHoldsOneConsistentTransaction(t *testing.T) {
	path, manifest := snapshotTestDatabase(t, 1, "")
	source, err := OpenSnapshot(context.Background(), path, manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec("PRAGMA busy_timeout=10"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec("UPDATE stock SET name='Changed' WHERE code='sz000001'"); err == nil {
		t.Fatal("concurrent write committed while snapshot read transaction was active")
	}
	stocks, err := source.Stocks(context.Background())
	if err != nil || stocks[0].Name != "Fixture" {
		t.Fatalf("inconsistent universe: %+v %v", stocks, err)
	}
}

func TestSnapshotV2CrossProviderNormalizationAndLocalDefects(t *testing.T) {
	for _, tc := range []struct {
		name, edit, reason string
		raw                int
	}{
		{"cross provider valid", "UPDATE daily_bar SET source='sina', volume_raw=volume_shares WHERE adjust='none'", "", 4},
		{"cross provider unequal shares retained for event validation", "UPDATE daily_bar SET source='sina', volume_raw=volume_shares+200, volume_shares=volume_shares+200 WHERE adjust='none'", "", 4},
		{"bad normalized shares", "UPDATE daily_bar SET volume_shares=12 WHERE adjust='none' AND trade_date='2026-02-03'", "invalid_volume_shares_none:sz000001:2026-02-03", 4},
		{"qfq shares preserve signal", "UPDATE daily_bar SET volume_shares=12 WHERE adjust='qfq' AND trade_date='2026-02-03'", "invalid_volume_shares_qfq:sz000001:2026-02-03", 4},
		{"reserved defect ignored", "UPDATE daily_bar SET volume_shares=-1 WHERE trade_date>='2026-02-05'", "", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, m := snapshotTestDatabase(t, 1, tc.edit)
			enableV2(&m)
			s, err := OpenSnapshot(context.Background(), path, m)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			pair, err := s.Load(context.Background(), config.Stock{Code: "sz000001"})
			if err != nil || pair.Problem != tc.reason || len(pair.Adjusted) != 4 || len(pair.Raw) != tc.raw || len(pair.RawShares) != tc.raw {
				t.Fatalf("pair=%+v err=%v", pair, err)
			}
			if pair.Adjusted[0].Volume != 1200 || pair.AdjustedShares[0] != 120000 {
				t.Fatal("signal volume modified or shares lost")
			}
			if tc.name == "qfq shares preserve signal" && (!math.IsNaN(pair.AdjustedShares[2]) || pair.Adjusted[2].Volume != 1200 || pair.AdjustedShares[3] != 120000) {
				t.Fatal("bad shares erased signal or contaminated later independent row")
			}
			if tc.name == "cross provider valid" && (pair.RawSource != "sina" || pair.Raw[0].Volume != 120000 || pair.RawShares[0] != 120000) {
				t.Fatal("unit normalization lost")
			}
		})
	}
}

func TestNormalizedVolumeUsesStorageIntegerConversion(t *testing.T) {
	for _, tc := range []struct {
		code, source string
		raw, shares  float64
		valid        bool
	}{
		{"sz000001", "tencent", 12.345, 1234, true},
		{"sh688001", "tencent", 12.345, 12, true},
		{"sz000001", "sina", 1200, 1200, true},
		{"sz000001", "baostock", 1200, 1200, true},
		{"sh600000", "tushare", 12.345, 1234, true},
		{"sh600000", "eastmoney", 12.345, 1234, true},
		{"sz000001", "sina", 1200, 120000, false},
	} {
		if got := validNormalizedVolume(tc.code, tc.source, tc.raw, tc.shares); got != tc.valid {
			t.Fatal(tc, got)
		}
	}
}
