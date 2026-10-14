package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/strategy"
	"sync"
	"testing"
	"time"
)

func dashboardFixture(t *testing.T) (*MarketDB, []config.Stock, market.History, selection.Report) {
	t.Helper()
	db, err := InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stocks := []config.Stock{{Code: "sz000001", Name: "测试银行", Sector: "银行"}, {Code: "sh600519", Name: "测试消费", Sector: "消费"}, {Code: "sz000002", Name: "测试缺失"}}
	history := dbHistory(stocks[0].Code, "qfq", "qfqday")
	history.Name = stocks[0].Name
	history.Bars = nil
	date := time.Date(2026, 8, 1, 0, 0, 0, 0, market.Shanghai)
	for ; date.Format("2006-01-02") <= history.AsOf; date = date.AddDate(0, 0, 1) {
		if date.Weekday() == time.Saturday || date.Weekday() == time.Sunday {
			continue
		}
		price := 10 + float64(len(history.Bars))/10
		history.Bars = append(history.Bars, market.Bar{Date: date.Format("2006-01-02"), Open: price, Close: price + .05, High: price + .2, Low: price - .2, Volume: 200})
	}
	report := selection.Report{Version: 1, ID: "dashboard-v1", AsOf: history.AsOf, Target: "2026-10-14", GeneratedAt: history.FetchedAt,
		Adjust: "qfq", PoolSize: len(stocks), Eligible: 1, Selected: []selection.Candidate{{Code: stocks[0].Code, Name: stocks[0].Name, Close: history.Bars[len(history.Bars)-1].Close}},
		Excluded: []selection.Excluded{{Code: stocks[1].Code, Reason: "missing_data"}, {Code: stocks[2].Code, Reason: "http_403"}}}
	return db, stocks, history, report
}

func publishDashboard(t *testing.T, db *MarketDB, stocks []config.Stock, history market.History, report selection.Report, id string) {
	t.Helper()
	err := db.CommitSnapshot(context.Background(), SyncRun{ID: id, AsOf: report.AsOf, StartedAt: report.GeneratedAt.Add(-time.Minute), FinishedAt: report.GeneratedAt, CoveragePct: 100.0 / 3},
		[]SyncItem{{Stock: stocks[0], Adjusted: &history, Status: "ok"}, {Stock: stocks[2], Status: "isolated", Reason: "http_403"}}, report)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDashboardEmptyPreservesEntirePoolWithoutCreatingDatabase(t *testing.T) {
	root := t.TempDir()
	db, err := OpenMarketView(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stocks := []config.Stock{{Code: "sz000001", Name: "测试"}, {Code: "sh600519", Name: "缺失"}}
	got, err := db.LoadDashboard(context.Background(), stocks, "qfq")
	if err != nil || got.Report != nil || got.LatestSync != nil || got.GeneratedAt != nil || got.AsOf != "" || len(got.Stocks) != 2 || got.Summary.Missing != 2 || got.Dates == nil {
		t.Fatalf("unexpected empty dashboard: %+v, %v", got, err)
	}
	for i, row := range got.Stocks {
		if row.Code != stocks[i].Code || row.Name != stocks[i].Name || row.Status != "missing" || row.Bar != nil || row.Metrics != nil || row.ChangePct != nil {
			t.Fatalf("missing stock omitted or fabricated: %+v", row)
		}
	}
	if _, err := os.Stat(filepath.Join(root, marketDBName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dashboard created a database: %v", err)
	}
}

func TestDashboardPublishedPoolAndFailedSync(t *testing.T) {
	db, stocks, history, report := dashboardFixture(t)
	publishDashboard(t, db, stocks, history, report, "initial")
	err := db.RecordFailedRun(context.Background(), SyncRun{ID: "failed-later", AsOf: "2026-10-14", StartedAt: history.FetchedAt.Add(time.Hour), FinishedAt: history.FetchedAt.Add(2 * time.Hour), ErrorKind: "sync"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadDashboard(context.Background(), stocks, "hfq")
	if err != nil {
		t.Fatal(err)
	}
	if got.AsOf != report.AsOf || got.Adjust != report.Adjust || got.Summary.PoolSize != 3 || got.Summary.Available != 1 || got.Summary.Missing != 2 || got.Summary.Eligible != 1 || got.Summary.Selected != 1 || math.Abs(got.Summary.CoveragePct-100.0/3) > .001 {
		t.Fatalf("snapshot summary not preserved: %+v", got)
	}
	if got.LatestSync == nil || got.LatestSync.AsOf != "2026-10-14" || got.LatestSync.Status != "failed" || got.LatestSync.ErrorKind != "sync" {
		t.Fatalf("failed sync missing: %+v", got.LatestSync)
	}
	if got.Stocks[0].Status != "selected" || got.Stocks[0].Metrics == nil || got.Stocks[0].ChangePct == nil || got.Stocks[0].Bar.Close != report.Selected[0].Close || got.Stocks[0].Bar.Volume != 200 {
		t.Fatalf("published market values missing: %+v", got.Stocks[0])
	}
	if got.Stocks[1].Status != "missing" || got.Stocks[2].Status != "missing" || got.Stocks[2].Reason != "http_403" {
		t.Fatalf("incomplete stocks were hidden: %+v", got.Stocks)
	}
}

func TestDashboardReportVersionsAndHistoricalRanking(t *testing.T) {
	db, stocks, history, first := dashboardFixture(t)
	publishDashboard(t, db, stocks, history, first, "v1")
	second := first
	second.Version, second.ID, second.AsOf, second.Target = 2, "dashboard-v2", "2026-10-14", "2026-10-15"
	second.GeneratedAt = first.GeneratedAt.Add(24 * time.Hour)
	second.Ranking = []strategy.Candidate{{Code: stocks[0].Code, Name: stocks[0].Name, Rank: 1, Score: 83, Recommended: true}}
	history.AsOf = second.AsOf
	history.Bars[len(history.Bars)-1].Close += .02
	last := history.Bars[len(history.Bars)-1]
	last.Date = second.AsOf
	history.Bars = append(history.Bars, last)
	publishDashboard(t, db, stocks, history, second, "v2")
	got, err := db.LoadDashboard(context.Background(), stocks, "qfq")
	if err != nil || got.Report.Version != 2 || len(got.Report.Ranking) != 1 || !reflect.DeepEqual(got.Dates, []string{second.AsOf, first.AsOf}) {
		t.Fatalf("v2 dashboard report lost ranking/date ordering: %+v %v", got, err)
	}
	historical, err := db.LoadDashboardReport(context.Background(), first.AsOf)
	historicalJSON, _ := json.Marshal(historical)
	firstJSON, _ := json.Marshal(first)
	if err != nil || string(historicalJSON) != string(firstJSON) {
		t.Fatalf("historical report was recomputed from overwritten bars: %+v %v", historical, err)
	}
	if _, err := db.LoadDashboardReport(context.Background(), "2026-01-01"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing historical report: %v", err)
	}
}

func TestDashboardFailureAndCancellation(t *testing.T) {
	db, stocks, history, report := dashboardFixture(t)
	publishDashboard(t, db, stocks, history, report, "initial")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.LoadDashboard(ctx, stocks, "qfq"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, err := db.LoadDashboardReport(ctx, report.AsOf); !errors.Is(err, context.Canceled) {
		t.Fatalf("report cancellation lost: %v", err)
	}
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := db.LoadDashboard(ctx, stocks, "qfq"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("connection wait ignored timeout: %v", err)
	}
	_ = tx.Rollback()
	if _, err := db.db.Exec("UPDATE selection_run SET report_json='invalid private database content'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LoadDashboard(context.Background(), stocks, "qfq"); err == nil || err.Error() != "invalid stored dashboard report" {
		t.Fatalf("corrupt data should produce a generic error: %v", err)
	}
	_ = db.Close()
	if _, err := db.LoadDashboard(context.Background(), stocks, "qfq"); err == nil || err.Error() != "cannot read dashboard snapshot" {
		t.Fatalf("closed database unexpectedly readable: %v", err)
	}
}

func TestDashboardConcurrentPublicationIsAtomic(t *testing.T) {
	db, stocks, history, report := dashboardFixture(t)
	publishDashboard(t, db, stocks, history, report, "initial")
	view, err := OpenMarketView(db.root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for i := 1; i <= 8; i++ {
			history.Bars[len(history.Bars)-1].Close = 15 + float64(i)/100
			report.ID = fmt.Sprintf("atomic-report-%d", i)
			report.Selected[0].Close = history.Bars[len(history.Bars)-1].Close
			err := db.CommitSnapshot(context.Background(), SyncRun{ID: fmt.Sprintf("atomic-run-%d", i), AsOf: report.AsOf, StartedAt: report.GeneratedAt, FinishedAt: report.GeneratedAt, CoveragePct: 100}, []SyncItem{{Stock: stocks[0], Adjusted: &history, Status: "ok"}}, report)
			if err != nil {
				t.Errorf("publish: %v", err)
				return
			}
		}
	}()
	for i := 0; i < 12; i++ {
		got, err := view.LoadDashboard(context.Background(), stocks, "qfq")
		if err != nil {
			t.Errorf("read snapshot: %v", err)
			break
		}
		if got.Stocks[0].Bar.Close != got.Report.Selected[0].Close {
			t.Errorf("mixed publication: bar=%v report=%v", got.Stocks[0].Bar.Close, got.Report.Selected[0].Close)
			break
		}
	}
	workers.Wait()
}
