package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

const dropFourLightArchive = `DROP TABLE four_light_event; DROP TABLE four_light_daily; DROP TABLE four_light_run;`
const downgradeFourLightCounts = `ALTER TABLE four_light_run DROP COLUMN transition_unknown; ALTER TABLE four_light_run RENAME COLUMN state_unknown TO unknown;`

func TestAllMarketMigrationDistinguishesHistoricalSchemas(t *testing.T) {
	for _, tc := range []struct {
		name           string
		legacySQL      string
		recommendation bool
		archive        bool
	}{
		{"v1", dropFourLightArchive + `DROP TABLE recommendation; PRAGMA user_version=1`, false, false},
		{"main_v2", dropFourLightArchive + `PRAGMA user_version=2`, true, false},
		{"four_light_v2", downgradeFourLightCounts + `DROP TABLE recommendation; PRAGMA user_version=2`, false, true},
		{"four_light_v3", `DROP TABLE recommendation; PRAGMA user_version=3`, false, true},
		{"current_v4", `PRAGMA user_version=4`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := migrationFixture(t, tc.legacySQL)
			if tc.name != "current_v4" {
				if view, err := OpenAllMarketView(root, false); err == nil {
					view.Close()
					t.Fatal("unmigrated A-share schema accepted by the current archive view")
				}
			}
			db, err := OpenAllMarketExisting(root, false)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			assertMigrationVersion(t, db.db, 4)
			ctx := context.Background()
			bars, err := db.LoadBars(ctx, "sz000001", "qfq", "2026-10-12", "2026-10-13")
			if err != nil || len(bars) != 2 || bars[0].Close != 10.5 || bars[1].Close != 11.5 {
				t.Fatalf("migration changed history: %+v, %v", bars, err)
			}
			artifact, err := db.LatestRecommendation(ctx, "a_low_valuation")
			if tc.recommendation {
				if err != nil || artifact.ID != "preserve-valuation" || string(artifact.Payload) != `{"fixture":true}` {
					t.Fatalf("migration changed recommendation: %+v, %v", artifact, err)
				}
			} else if err == nil {
				t.Fatal("migration fabricated a recommendation")
			}
			if tc.archive {
				summary, err := db.LatestFourLightSummary(ctx, "2026-10-13", "four_light_approx_v1", 1, DefaultFourLightPageSize)
				if err != nil || summary.Run.ID != "preserve-lights" || summary.Run.StateUnknown != 1 || summary.Run.TransitionUnknown != 2 || len(summary.First) != 1 || summary.TransitionTotal != 2 {
					t.Fatalf("migration changed four-light archive: %+v, %v", summary, err)
				}
				var events int
				if err := db.db.QueryRow(`SELECT count(*) FROM four_light_event WHERE run_id='preserve-lights' AND event='first_all_red'`).Scan(&events); err != nil || events != 1 {
					t.Fatalf("migration lost four-light events: %d, %v", events, err)
				}
			}
			// Both families of features must work even when that family did not
			// exist in the legacy database.
			for _, table := range []string{"recommendation", "four_light_run", "four_light_daily", "four_light_event"} {
				assertMigrationTable(t, db.db, table, true)
			}
			assertMigrationTable(t, db.db, "intraday_bar", false)
			view, err := OpenAllMarketView(root, false)
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			if _, err := view.db.Exec(`DELETE FROM stock`); err == nil {
				t.Fatal("migration view accepted a write")
			}
		})
	}
}

func TestAllMarketMigrationRollsBackDDLAndBackfill(t *testing.T) {
	root := migrationFixture(t, downgradeFourLightCounts+`
		DROP TABLE recommendation;
		CREATE TRIGGER reject_migration BEFORE UPDATE ON four_light_run BEGIN SELECT RAISE(ABORT, 'fixture migration failure'); END;
		PRAGMA user_version=2;
	`)
	if db, err := OpenAllMarketExisting(root, false); err == nil || !strings.Contains(err.Error(), "backfill") {
		if db != nil {
			db.Close()
		}
		t.Fatalf("migration did not report the injected backfill failure: %v", err)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(root, allMarketDBName))+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	assertMigrationVersion(t, raw, 2)
	assertMigrationTable(t, raw, "recommendation", false)
	var unknown, renamedColumns int
	if err := raw.QueryRow(`SELECT unknown FROM four_light_run WHERE id='preserve-lights'`).Scan(&unknown); err != nil || unknown != 1 {
		t.Fatalf("failed migration changed original unknown count: %d, %v", unknown, err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM pragma_table_info('four_light_run') WHERE name IN ('state_unknown','transition_unknown')`).Scan(&renamedColumns); err != nil || renamedColumns != 0 {
		t.Fatalf("failed migration retained partial DDL: %d, %v", renamedColumns, err)
	}
	if _, err := raw.Exec(`DROP TRIGGER reject_migration`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	// The failed opener must release its writer lock, allowing a clean retry.
	retry, err := OpenAllMarketExisting(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer retry.Close()
	assertMigrationVersion(t, retry.db, 4)
}

func TestAllMarketMigrationEmptyMissingAndFutureDatabases(t *testing.T) {
	root := t.TempDir()
	if db, err := OpenAllMarketExisting(root, false); err == nil {
		db.Close()
		t.Fatal("existing-only opener created a missing database")
	}
	if _, err := os.Stat(filepath.Join(root, allMarketDBName)); !os.IsNotExist(err) {
		t.Fatalf("existing-only opener left a database: %v", err)
	}
	db, err := OpenAllMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	assertMigrationVersion(t, db.db, 4)
	for _, table := range []string{"recommendation", "four_light_run", "four_light_daily", "four_light_event"} {
		assertMigrationTable(t, db.db, table, true)
	}
	if _, err := db.db.Exec(`PRAGMA user_version=5`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, open := range []func(string, bool) (*AllMarketDB, error){OpenAllMarketExisting, OpenAllMarketView} {
		if db, err := open(root, false); err == nil {
			db.Close()
			t.Fatal("future A-share database version was accepted")
		}
	}
}

func TestHKSchemaRemainsIndependentAndLegacyViewsRemainReadable(t *testing.T) {
	for _, version := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			root := t.TempDir()
			db, err := OpenHKMarket(root, false)
			if err != nil {
				t.Fatal(err)
			}
			assertMigrationVersion(t, db.db, 3)
			stock := config.Stock{Code: "hk00700", Name: "HK fixture"}
			history := allMarketHistoryTest(stock.Code, "none", "2026-10-13")
			history.Source = "tencent_hk"
			if err := db.SaveHistory(context.Background(), stock, history, true); err != nil {
				t.Fatal(err)
			}
			if version < 3 {
				if _, err := db.db.Exec(`DROP TABLE intraday_bar; DROP TABLE intraday_sync`); err != nil {
					t.Fatal(err)
				}
			}
			if version == 1 {
				if _, err := db.db.Exec(`DROP TABLE recommendation`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			view, err := OpenHKMarketView(root, false)
			if version == 4 {
				if err == nil {
					view.Close()
					t.Fatal("HK view accepted the A-share schema version")
				}
				if writer, err := OpenHKMarket(root, false); err == nil {
					writer.Close()
					t.Fatal("HK writer accepted the A-share schema version")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertMigrationVersion(t, view.db, version)
			bars, err := view.LoadBars(context.Background(), stock.Code, "none", "2026-10-13", "2026-10-13")
			if err != nil || len(bars) != 1 {
				t.Fatalf("legacy HK daily view failed: %+v, %v", bars, err)
			}
			if err := view.Close(); err != nil {
				t.Fatal(err)
			}
			writer, err := OpenHKMarket(root, false)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			assertMigrationVersion(t, writer.db, 3)
			assertMigrationTable(t, writer.db, "four_light_run", false)
			assertMigrationTable(t, writer.db, "intraday_bar", true)
			assertMigrationTable(t, writer.db, "recommendation", true)
		})
	}
}

func migrationFixture(t *testing.T, legacySQL string) string {
	t.Helper()
	root := t.TempDir()
	db, err := OpenAllMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	stocks := []config.Stock{{Code: "sz000001", Name: "first"}, {Code: "sz000002", Name: "state unknown"}, {Code: "sz000003", Name: "transition unknown"}}
	for _, stock := range stocks {
		if err := db.SaveHistory(ctx, stock, allMarketHistoryTest(stock.Code, "qfq", "2026-10-12", "2026-10-13"), true); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	artifact := RecommendationArtifact{Market: "a_low_valuation", AsOf: "2026-10-13", ID: "preserve-valuation", Target: "2026-10-14", BuiltAt: now, Payload: []byte(`{"fixture":true}`)}
	if err := db.SaveRecommendation(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	run := fourLightRunFixture("preserve-lights", now, stocks)
	if err := db.StartFourLightRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	run.Status, run.Ready, run.AllRed, run.FirstAllRed = "complete", 1, 2, 1
	run.StateUnknown, run.TransitionUnknown = 1, 2
	rows := []FourLightRecord{
		fourLightRecordFixture(stocks[0].Code, stocks[0].Name, "first_all_red", "all_red", "ready", run.ID, now),
		fourLightRecordFixture(stocks[1].Code, stocks[1].Name, "unknown", "unknown", "unknown", run.ID, now),
		fourLightRecordFixture(stocks[2].Code, stocks[2].Name, "unknown", "all_red", "partial", run.ID, now),
	}
	if err := db.CompleteFourLightRun(ctx, run, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(legacySQL); err != nil {
		t.Fatal(err)
	}
	return root
}

func assertMigrationVersion(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != want {
		t.Fatalf("schema = %d, want %d: %v", version, want, err)
	}
}

func assertMigrationTable(t *testing.T, db *sql.DB, name string, want bool) {
	t.Helper()
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&tables); err != nil || (tables == 1) != want {
		t.Fatalf("table %s present=%t, want %t: %v", name, tables == 1, want, err)
	}
}
