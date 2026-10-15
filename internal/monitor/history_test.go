package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/storage"
)

func TestHistoryStartEnforcesCalendarDecade(t *testing.T) {
	for _, tc := range []struct {
		end  string
		days int
		want string
	}{
		{"2026-10-13", 90, "2016-10-13"},
		{"2026-10-13", 3650, "2016-10-13"},
		{"2024-03-01", 365, "2014-03-01"},
		{"2024-02-29", 3650, "2014-02-28"},
		{"2026-10-13", 7300, "2006-10-18"},
	} {
		end, _ := time.Parse("2006-01-02", tc.end)
		if got := historyStart(end, tc.days); got != tc.want {
			t.Errorf("start(%s,%d)=%s, want %s", tc.end, tc.days, got, tc.want)
		}
	}
}

func TestOldCalendarDoesNotHideTruncatedHistory(t *testing.T) {
	m, source, _ := fixture(t)
	stock := config.Stock{Code: "sz000001", Name: "测试"}
	h, err := source.Fetch(context.Background(), stock, "2026-01-01", "2026-10-13", "none")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateHistory(m.Calendar, stock, h, "2016-10-13", "2026-10-13"); err == nil {
		t.Fatal("current-year calendar hid nine missing years")
	}
	stock.ListedFrom = "2026-01-01"
	if err := validateHistory(m.Calendar, stock, h, "2016-10-13", "2026-10-13"); err != nil {
		t.Fatalf("declared IPO rejected: %v", err)
	}
}

func TestTenYearHTTPBackfillSQLiteAndIncrementalRecovery(t *testing.T) {
	m, fake, _ := fixture(t)
	m.Config.Stocks[0].ListedFrom = ""
	m.Config.Daily.Adjust = "none" // Exercise raw full/partial fetches without running research.
	m.Config.Notify.Channels = nil
	stock := m.Config.Stocks[0]
	root := t.TempDir()
	db, err := storage.InitMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	m.Market = db
	t.Cleanup(func() { m.Market.Close() })
	ctx := context.Background()
	// Simulate the pre-upgrade database, which contains only a short window.
	old, err := fake.Fetch(ctx, stock, "2026-07-15", "2026-10-12", "none")
	if err != nil {
		t.Fatal(err)
	}
	report := selection.Report{Version: 1, ID: "old-report", AsOf: old.AsOf, Target: "2026-10-13", GeneratedAt: m.Now(), Adjust: "none"}
	err = db.CommitSnapshot(ctx, storage.SyncRun{ID: "old-run", AsOf: old.AsOf, StartedAt: m.Now(), FinishedAt: m.Now(), CoveragePct: 100}, []storage.SyncItem{{Stock: stock, Adjusted: &old, Status: "ok"}}, report)
	if err != nil {
		t.Fatal(err)
	}
	fullRequests, incrementalRequests := 0, 0
	failPage := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Query().Get("param"), ",")
		count, _ := strconv.Atoi(parts[4])
		if count == 640 {
			fullRequests++
		} else {
			incrementalRequests++
		}
		if failPage && parts[3] < "2024-01-01" {
			w.WriteHeader(503)
			return
		}
		last, _ := time.Parse("2006-01-02", parts[3])
		var rows [][]any
		for day := time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC); !day.After(last); day = day.AddDate(0, 0, 1) {
			if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
				rows = append(rows, []any{day.Format("2006-01-02"), 10, 10, 11, 9, 100})
			}
		}
		if len(rows) > count {
			rows = rows[len(rows)-count:]
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{stock.Code: map[string]any{"day": rows}}})
	}))
	defer srv.Close()
	client := market.NewClient(2 * time.Second)
	client.Endpoint = srv.URL
	defer client.Close()
	m.Source = client
	if _, err := m.Sync(ctx, "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	if fullRequests < 4 || incrementalRequests != 0 {
		t.Fatalf("short old window was not backfilled: full=%d increment=%d", fullRequests, incrementalRequests)
	}
	assertStored := func(asof, first string) {
		t.Helper()
		histories, err := m.Market.LoadHistories(ctx, []config.Stock{stock}, asof, "none")
		if err != nil || len(histories) != 1 {
			t.Fatalf("read history: %v", err)
		}
		h := histories[0]
		if len(h.Bars) < 2500 || h.Bars[0].Date != first || h.Bars[len(h.Bars)-1].Date != asof {
			t.Fatalf("persisted history truncated: first=%s last=%s count=%d", h.Bars[0].Date, h.Bars[len(h.Bars)-1].Date, len(h.Bars))
		}
		chart, err := m.Market.LoadDashboardHistory(ctx, stock, "none")
		if err != nil || len(chart.Bars) != len(h.Bars) || chart.Bars[0].Date != first || chart.AsOf != asof {
			t.Fatalf("dashboard lost long history: bars=%d as_of=%s err=%v", len(chart.Bars), chart.AsOf, err)
		}
	}
	assertStored("2026-10-13", "2016-10-13")
	// Reopen the actual database, then use a five-bar update without losing history.
	if err := m.Market.Close(); err != nil {
		t.Fatal(err)
	}
	m.Market, err = storage.OpenMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	before := fullRequests
	m.Now = func() time.Time { return time.Date(2026, 10, 14, 19, 0, 0, 0, market.Shanghai) }
	if _, err := m.Sync(ctx, "2026-10-14"); err != nil {
		t.Fatal(err)
	}
	if fullRequests != before || incrementalRequests != 1 {
		t.Fatalf("complete history did not use increment: full=%d increment=%d", fullRequests, incrementalRequests)
	}
	assertStored("2026-10-14", "2016-10-14")
	// A long outage cannot be repaired by the latest five sessions alone.
	m.Now = func() time.Time { return time.Date(2026, 11, 16, 19, 0, 0, 0, market.Shanghai) }
	if _, err := m.Sync(ctx, "2026-11-16"); err != nil {
		t.Fatal(err)
	}
	if fullRequests <= before || incrementalRequests != 2 {
		t.Fatal("incremental gap did not trigger full backfill")
	}
	assertStored("2026-11-16", "2016-11-16")
	// A later failed historical page must leave the published snapshot intact.
	m.Now = func() time.Time { return time.Date(2026, 12, 16, 19, 0, 0, 0, market.Shanghai) }
	failPage = true
	if _, err := m.Sync(ctx, "2026-12-16"); err == nil {
		t.Fatal("partial paginated failure accepted")
	}
	// The failed stock is now recorded as isolated for the new run. Its old
	// history remains available by date, but must not appear current on the UI.
	retained, err := m.Market.LoadHistories(ctx, []config.Stock{stock}, "2026-11-16", "none")
	if err != nil || len(retained) != 1 || len(retained[0].Bars) < 2500 || retained[0].Bars[0].Date != "2016-11-16" || retained[0].Bars[len(retained[0].Bars)-1].Date != "2026-11-16" {
		t.Fatalf("failed run discarded historical data: %v", err)
	}
	view, err := m.Market.LoadDashboard(ctx, m.Config.Stocks, "none")
	if err != nil || view.Summary.Available != 0 || view.LatestSync.ErrorKind != "coverage" {
		t.Fatalf("failed stock presented as current: %+v %v", view, err)
	}
	if _, err := m.Market.LoadReport(ctx, "2026-12-16"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("failed run published: %v", err)
	}
}

func TestRawArchiveIsIndependentOfAnalysisAndStrategy(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "legacy"
		if enabled {
			name = "strategy"
		}
		t.Run(name, func(t *testing.T) {
			m, source, _ := fixture(t)
			m.Config.Daily.Adjust = "qfq"
			m.Config.Strategy.Enabled = enabled
			m.Config.Stocks[0].ListedFrom = "2021-01-04"
			m.Config.Daily.HistoryDays = 365
			db, err := storage.InitMarket(t.TempDir(), false)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			m.Market = db
			report, err := m.Sync(context.Background(), "2026-10-13")
			if err != nil {
				t.Fatal(err)
			}
			if source.calls != 2 {
				t.Fatalf("raw archive omitted: requests=%d", source.calls)
			}
			adjusted, raw, err := db.LoadHistoriesPair(context.Background(), m.Config.Stocks, "2026-10-13", "qfq", true)
			if err != nil {
				t.Fatal(err)
			}
			if len(adjusted) != 1 || len(raw) != 1 || adjusted[0].Bars[0].Date != "2025-10-13" || raw[0].Bars[0].Date != "2021-01-04" || len(raw[0].Bars) < 1000 {
				t.Fatal("archive did not preserve full post-IPO history independently of analysis")
			}
			preview, err := m.Preview("2026-10-13")
			if err != nil || preview.Eligible != report.Eligible || preview.Version != report.Version {
				t.Fatalf("long archive broke preview: %v", err)
			}
			snapshot, err := db.LoadDashboard(context.Background(), m.Config.Stocks, "qfq")
			if err != nil || snapshot.Summary.Available != 1 || snapshot.Summary.CoveragePct != 100 {
				t.Fatalf("long archive broke visible snapshot: %v", err)
			}
		})
	}
}
