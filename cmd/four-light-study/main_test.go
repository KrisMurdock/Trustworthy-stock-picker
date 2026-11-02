package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fourlightstudy"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func cliFixture(t *testing.T, schema int, empty bool) (string, string, fourlightstudy.Manifest) {
	t.Helper()
	dir := t.TempDir()
	databasePath, manifestPath := filepath.Join(dir, "market.db"), filepath.Join(dir, "manifest.json")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA synchronous=OFF; PRAGMA user_version=%d; PRAGMA journal_mode=DELETE;
		CREATE TABLE stock(code TEXT PRIMARY KEY, name TEXT, listed_from TEXT, active INTEGER);
		CREATE TABLE daily_bar(code TEXT, adjust TEXT, trade_date TEXT, open_micros INTEGER, close_micros INTEGER,
		high_micros INTEGER, low_micros INTEGER, volume_raw REAL, source TEXT, data_field TEXT, PRIMARY KEY(code,adjust,trade_date));`, schema)); err != nil {
		t.Fatal(err)
	}
	days := []string{}
	for day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); len(days) < 90; day = day.AddDate(0, 0, 1) {
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			days = append(days, day.Format(time.DateOnly))
		}
	}
	if !empty {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("INSERT INTO stock VALUES('sz000001','Fixture','1991-04-03',1)"); err != nil {
			t.Fatal(err)
		}
		for i, day := range days {
			close := int64(10_000_000 + i*20_000)
			volume := 1000.0
			if i >= 61 && i <= 65 {
				volume *= math.Pow(1.5, float64(i-60))
			}
			for _, adjust := range []string{"qfq", "none"} {
				field := "day"
				if adjust == "qfq" {
					field = "qfqday"
				}
				if _, err := tx.Exec("INSERT INTO daily_bar VALUES(?,?,?,?,?,?,?,?,?,?)", "sz000001", adjust, day, close-10_000, close, close+20_000, close-20_000, volume, "tencent", field); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("ALTER TABLE daily_bar ADD COLUMN volume_shares INTEGER; UPDATE daily_bar SET volume_shares=CAST(volume_raw*100 AS INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(databasePath)
	hash := sha256.Sum256(data)
	manifest := fourlightstudy.Manifest{
		Version: 1, DatabaseSHA256: hex.EncodeToString(hash[:]), AsOf: days[89], From: days[61], Through: days[65],
		ObservationThrough: days[77], ReservedFrom: days[78], UniversePolicy: "current_active_sh_sz",
		Calendar:   experiment.Calendar{Version: 1, From: days[0], Through: days[89], Days: days, Source: "fixture", Digest: "fixture"},
		Parameters: config.Defaults().Strategy,
	}
	history, err := calendar.NewHistory(manifest.Calendar.Source, manifest.Calendar.From, manifest.Calendar.Through, manifest.Calendar.Days, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Calendar.Digest = history.Digest
	writeCLIManifest(t, manifestPath, manifest)
	return manifestPath, databasePath, manifest
}

func TestRunV2ExplicitModelCrossSourceAndCandidate(t *testing.T) {
	manifestPath, databasePath, manifest := cliFixture(t, 3, false)
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	// Broaden price evidence independently of labels, and use provider-normalized
	// shares from a different raw source. Source-name inequality must not erase it.
	if _, err := db.Exec("UPDATE daily_bar SET high_micros=high_micros+200000, low_micros=low_micros-200000; UPDATE daily_bar SET source='sina', volume_raw=volume_shares WHERE adjust='none'"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(databasePath)
	digest := sha256.Sum256(data)
	manifest.DatabaseSHA256 = hex.EncodeToString(digest[:])
	manifest.Version, manifest.WaveModel, manifest.PairingPolicy = 2, strategy.UpwardWaveNoDrawdownModel, fourlightstudy.PairingPolicy
	writeCLIManifest(t, manifestPath, manifest)
	var stdout bytes.Buffer
	eventsPath := filepath.Join(t.TempDir(), "events.jsonl")
	if err := run(context.Background(), []string{"-manifest", manifestPath, "-database", databasePath, "-events", eventsPath}, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	var result output
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Report.Version != 2 || result.Report.Engine != fourlightstudy.EngineV2 || len(result.Report.CandidateComparisons) != 4 || len(result.Report.Boards) == 0 {
		t.Fatal("missing v2 contract")
	}
	for _, row := range result.Report.Groups {
		if row.Wave.Model != strategy.UpwardWaveNoDrawdownModel {
			t.Fatal("incorrect model")
		}
		if row.Name == "first_all_red" && (row.Wave.Windows[0].Selected != 1 || row.Wave.Windows[0].Known != 1) {
			t.Fatalf("cross source wrongly excluded %+v", row.Wave)
		}
	}
	data, err = os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	var e fourlightstudy.Event
	if err := json.Unmarshal(bytes.Split(bytes.TrimSpace(data), []byte("\n"))[0], &e); err != nil {
		t.Fatal(err)
	}
	if e.Wave.PricePairReference == nil || e.VolumeRatio == nil || e.Board != "sz_main" {
		t.Fatal("missing event evidence")
	}
}

func writeCLIManifest(t *testing.T, path string, manifest fourlightstudy.Manifest) {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRunFrozenStudyAndEvents(t *testing.T) {
	manifestPath, databasePath, manifest := cliFixture(t, 1, false)
	events := filepath.Join(t.TempDir(), "events.jsonl")
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"-manifest", manifestPath, "-database", databasePath, "-events", events}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var result output
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	manifestBytes, _ := os.ReadFile(manifestPath)
	manifestHash := sha256.Sum256(manifestBytes)
	if result.CodeVersion != version || result.DatabaseSchema != 1 || result.DatabaseSHA256 != manifest.DatabaseSHA256 || result.ManifestSHA256 != hex.EncodeToString(manifestHash[:]) || result.Report.Status != "complete" {
		t.Fatalf("incorrect output provenance or status: %+v", result)
	}
	data, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 5 || result.Report.Audit.AllRedEventRows != 5 {
		t.Fatalf("events=%d audit=%d", len(lines), result.Report.Audit.AllRedEventRows)
	}
	for _, line := range lines {
		var event fourlightstudy.Event
		if err := json.Unmarshal(line, &event); err != nil || event.Code != "sz000001" || event.Wave == nil || !event.FourLight.AllRed {
			t.Fatalf("bad event: %s, %v", line, err)
		}
		if event.Date > manifest.Through || event.Date < manifest.From {
			t.Fatal("event outside signal period")
		}
	}
	data, _ = os.ReadFile(databasePath)
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != manifest.DatabaseSHA256 {
		t.Fatal("study modified input database")
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestFutureDataDefectsPreserveEarlierSignalsAndEntries(t *testing.T) {
	for _, tc := range []struct {
		name, adjust, assignment string
		dayIndex                 int
	}{
		{"later raw mixed source", "none", "source='sina'", 77},
		{"later adjusted mixed source", "qfq", "source='sina'", 77},
		{"later invalid price", "none", "low_micros=0", 77},
		{"later invalid field", "qfq", "data_field='invalid'", 77},
		{"raw defect inside ten days", "none", "source='sina'", 67},
		{"adjusted defect inside ten days", "qfq", "low_micros=0", 67},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifestPath, databasePath, manifest := cliFixture(t, 1, false)
			runEvents := func() (output, []fourlightstudy.Event, []byte) {
				t.Helper()
				path := filepath.Join(t.TempDir(), "events.jsonl")
				var stdout bytes.Buffer
				if err := run(context.Background(), []string{"-manifest", manifestPath, "-database", databasePath, "-events", path}, &stdout, io.Discard); err != nil {
					t.Fatal(err)
				}
				var report output
				if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				events := []fourlightstudy.Event{}
				for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
					var event fourlightstudy.Event
					if err := json.Unmarshal(line, &event); err != nil {
						t.Fatal(err)
					}
					events = append(events, event)
				}
				return report, events, data
			}
			beforeReport, before, beforeBytes := runEvents()
			db, err := sql.Open("sqlite", databasePath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("UPDATE daily_bar SET "+tc.assignment+" WHERE adjust=? AND trade_date=?", tc.adjust, manifest.Calendar.Days[tc.dayIndex]); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(databasePath)
			hash := sha256.Sum256(data)
			manifest.DatabaseSHA256 = hex.EncodeToString(hash[:])
			writeCLIManifest(t, manifestPath, manifest)
			afterReport, after, afterBytes := runEvents()
			if afterReport.Report.Audit.StateKnown != beforeReport.Report.Audit.StateKnown || len(after) != len(before) || len(afterReport.Report.Audit.StockProblems) != 1 {
				t.Fatal("future defect removed earlier signal denominator or was not audited")
			}
			for i := range before {
				if after[i].Code != before[i].Code || after[i].Date != before[i].Date || after[i].Wave.EntryStatus != "entered" || before[i].Wave.EntryStatus != "entered" || after[i].Wave.EntryPrice != before[i].Wave.EntryPrice {
					t.Fatalf("future defect removed or altered historical entry: before=%+v after=%+v", before[i], after[i])
				}
			}
			if tc.dayIndex == 77 {
				if !bytes.Equal(beforeBytes, afterBytes) {
					t.Fatal("data defect after D10 changed an earlier event or complete path")
				}
			} else {
				firstBefore, firstAfter := before[0].Wave, after[0].Wave
				fiveBefore, _ := json.Marshal(firstBefore.Windows[0])
				fiveAfter, _ := json.Marshal(firstAfter.Windows[0])
				if !bytes.Equal(fiveBefore, fiveAfter) || firstBefore.Windows[1].Status == "unknown" || firstAfter.Windows[1].Status != "unknown" {
					t.Fatal("D6 defect must preserve full D1-D5 evaluation and only invalidate D1-D10")
				}
			}
		})
	}
}

func TestRunInputFailuresPublishNothing(t *testing.T) {
	manifestPath, databasePath, manifest := cliFixture(t, 1, false)
	unknownManifest := filepath.Join(t.TempDir(), "unknown.json")
	data, _ := os.ReadFile(manifestPath)
	data = append(data[:len(data)-1], []byte(",\"unexpected\":1}")...)
	if err := os.WriteFile(unknownManifest, data, 0600); err != nil {
		t.Fatal(err)
	}
	oversize := filepath.Join(t.TempDir(), "oversize.json")
	if err := os.WriteFile(oversize, bytes.Repeat([]byte(" "), (1<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	badHash := filepath.Join(t.TempDir(), "hash.json")
	manifest.DatabaseSHA256 = strings.Repeat("a", 64)
	writeCLIManifest(t, badHash, manifest)
	unknownSchemaManifest, unknownSchemaDatabase, _ := cliFixture(t, 4, false)
	emptyManifest, emptyDatabase, _ := cliFixture(t, 1, true)
	base := []string{"-manifest", manifestPath, "-database", databasePath}
	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"missing flags", "required", nil},
		{"no database", "required", []string{"-manifest", manifestPath}},
		{"positional", "positional", append(append([]string{}, base...), "extra")},
		{"zero timeout", "timeout", append(append([]string{}, base...), "-timeout", "0s")},
		{"large timeout", "timeout", append(append([]string{}, base...), "-timeout", "2h")},
		{"missing manifest", "no such file", []string{"-manifest", manifestPath + ".missing", "-database", databasePath}},
		{"missing database", "no such file", []string{"-manifest", manifestPath, "-database", databasePath + ".missing"}},
		{"unknown field", "unknown field", []string{"-manifest", unknownManifest, "-database", databasePath}},
		{"oversize", "exceeds", []string{"-manifest", oversize, "-database", databasePath}},
		{"hash", "SHA256", []string{"-manifest", badHash, "-database", databasePath}},
		{"schema", "schema 4", []string{"-manifest", unknownSchemaManifest, "-database", unknownSchemaDatabase}},
		{"empty", "no active", []string{"-manifest", emptyManifest, "-database", emptyDatabase}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			events := filepath.Join(dir, "events.jsonl")
			args := append(append([]string{}, tc.args...), "-events", events)
			var stdout, stderr bytes.Buffer
			if err := run(context.Background(), args, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			if stdout.Len() != 0 {
				t.Fatalf("failure emitted report: %s", stdout.String())
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 0 {
				t.Fatalf("failure left event output or temporary files: %v", files)
			}
		})
	}
}

func TestRunCancellationAndOutputProtection(t *testing.T) {
	manifestPath, databasePath, _ := cliFixture(t, 1, false)
	base := []string{"-manifest", manifestPath, "-database", databasePath}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, base, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if err := run(context.Background(), append(append([]string{}, base...), "-timeout", "1ns"), io.Discard, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout=%v", err)
	}
	for _, path := range []string{manifestPath, databasePath} {
		before, _ := os.ReadFile(path)
		if err := run(context.Background(), append(append([]string{}, base...), "-events", path), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "input file") {
			t.Fatalf("overwrite input=%v", err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("input overwritten")
		}
	}
	existing := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(existing, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), append(append([]string{}, base...), "-events", existing), io.Discard, io.Discard); err == nil {
		t.Fatal("existing output replaced")
	}
	data, _ := os.ReadFile(existing)
	if string(data) != "original" {
		t.Fatal("existing output changed")
	}
	dir := t.TempDir()
	if err := run(context.Background(), append(append([]string{}, base...), "-events", filepath.Join(dir, "events.jsonl")), failingWriter{}, io.Discard); err == nil {
		t.Fatal("stdout failure accepted")
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatalf("failed report left events: %v", files)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestEventPublicationDoesNotReplaceRacingWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	output, err := newEventOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	defer output.cleanup()
	if _, err := output.file.WriteString("new\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := output.publish(); err == nil {
		t.Fatal("publication overwrote concurrently created output")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "other" {
		t.Fatal("racing writer data changed")
	}
}

func cliClassification(t *testing.T) (string, string) {
	t.Helper()
	s := fourlightstudy.SectorSnapshot{Version: 1, Basis: "published_snapshot", Taxonomy: "fixture", Level: "division", SnapshotAsOf: "2025-12-31", PublishedOn: "2026-01-01", ObservedAt: "2026-11-02T00:00:00Z",
		Sources:     []fourlightstudy.SectorSource{{URL: "https://example.org/frozen.pdf", SHA256: strings.Repeat("a", 64)}},
		Memberships: []fourlightstudy.SectorMembership{{Code: "sz000001", SectorCode: "J66", SectorName: "货币金融服务"}}}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "classification.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	return path, hex.EncodeToString(hash[:])
}

func TestRunClassificationParityAndOutputProtection(t *testing.T) {
	manifestPath, databasePath, manifest := cliFixture(t, 3, false)
	manifest.Version, manifest.WaveModel, manifest.PairingPolicy = 2, strategy.UpwardWaveNoDrawdownModel, fourlightstudy.PairingPolicy
	writeCLIManifest(t, manifestPath, manifest)
	classification, digest := cliClassification(t)
	base := []string{"-manifest", manifestPath, "-database", databasePath}
	var baseline, actual bytes.Buffer
	if err := run(context.Background(), base, &baseline, io.Discard); err != nil {
		t.Fatal(err)
	}
	args := append(append([]string{}, base...), "-classification", classification, "-classification-sha256", digest)
	events := filepath.Join(t.TempDir(), "events.jsonl")
	if err := run(context.Background(), append(append([]string{}, args...), "-events", events), &actual, io.Discard); err != nil {
		t.Fatal(err)
	}
	var before, after output
	if err := json.Unmarshal(baseline.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(actual.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(before.Report)
	a, _ := json.Marshal(after.Report)
	if !bytes.Equal(a, b) || after.ClassificationSHA256 != digest || after.SectorContext == nil {
		t.Fatal("classification changed baseline or lost provenance")
	}
	data, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var event fourlightstudy.Event
		if err := json.Unmarshal(row, &event); err != nil {
			t.Fatal(err)
		}
		if !event.FourLight.AllRed || event.Context == nil {
			t.Fatal("missing all-red event context")
		}
	}
	original, _ := os.ReadFile(classification)
	if err := run(context.Background(), append(append([]string{}, args...), "-events", classification), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "input file") {
		t.Fatalf("classification overwrite: %v", err)
	}
	unchanged, _ := os.ReadFile(classification)
	if !bytes.Equal(original, unchanged) {
		t.Fatal("classification modified")
	}
	failedDir := t.TempDir()
	if err := run(context.Background(), append(append([]string{}, args...), "-events", filepath.Join(failedDir, "failed.jsonl")), failingWriter{}, io.Discard); err == nil {
		t.Fatal("failed report accepted")
	}
	files, _ := os.ReadDir(failedDir)
	if len(files) != 0 {
		t.Fatal("partial events left on failure")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, args, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestClassificationInputFailures(t *testing.T) {
	manifestPath, databasePath, manifest := cliFixture(t, 3, false)
	classification, digest := cliClassification(t)
	base := []string{"-manifest", manifestPath, "-database", databasePath}
	for _, extra := range [][]string{{"-classification", classification}, {"-classification-sha256", digest}, {"-classification", classification, "-classification-sha256", digest}} {
		if err := run(context.Background(), append(append([]string{}, base...), extra...), io.Discard, io.Discard); err == nil {
			t.Fatal("accepted missing paired flag or manifest v1")
		}
	}
	manifest.Version, manifest.WaveModel, manifest.PairingPolicy = 2, strategy.UpwardWaveNoDrawdownModel, fourlightstudy.PairingPolicy
	writeCLIManifest(t, manifestPath, manifest)
	data, _ := os.ReadFile(classification)
	for _, tc := range []struct {
		name string
		data []byte
		hash string
		want string
	}{
		{"mismatch", data, strings.Repeat("b", 64), "mismatch"},
		{"uppercase", data, strings.ToUpper(digest), "lowercase"},
		{"unknown", append(append([]byte{}, data[:len(data)-1]...), []byte(",\"extra\":1}")...), "", "unknown field"},
		{"trailing", append(append([]byte{}, data...), []byte(" {}")...), "", "exactly one"},
		{"invalid", []byte("{}"), "", "version"},
		{"oversize", bytes.Repeat([]byte(" "), (8<<20)+1), "", "exceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input.json")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			hash := tc.hash
			if hash == "" {
				h := sha256.Sum256(tc.data)
				hash = hex.EncodeToString(h[:])
			}
			dir := t.TempDir()
			args := append(append([]string{}, base...), "-classification", path, "-classification-sha256", hash, "-events", filepath.Join(dir, "events.jsonl"))
			var stdout bytes.Buffer
			if err := run(context.Background(), args, &stdout, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s got %v", tc.want, err)
			}
			files, _ := os.ReadDir(dir)
			if stdout.Len() != 0 || len(files) != 0 {
				t.Fatal("failure published results")
			}
		})
	}
}
