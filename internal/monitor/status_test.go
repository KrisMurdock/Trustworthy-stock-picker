package monitor

import (
	"context"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"stock-watcher/internal/storage"
	"strings"
	"testing"
	"time"
)

type statusFakeSource struct {
	*fakeSource
	suspended map[string]map[string]bool
	missing   map[string]map[string]bool
	unknown   map[string]map[string]bool
	statusErr error
	source    string
	sparse    bool
}

func (f *statusFakeSource) Fetch(ctx context.Context, stock config.Stock, start, end, adjust string) (market.History, error) {
	h, err := f.fakeSource.Fetch(ctx, stock, start, end, adjust)
	if err != nil {
		return h, err
	}
	kept := h.Bars[:0]
	for _, bar := range h.Bars {
		skip := f.suspended[stock.Code][bar.Date] || f.missing[stock.Code][bar.Date] || f.unknown[stock.Code][bar.Date]
		if !skip {
			kept = append(kept, bar)
		}
	}
	h.Bars = kept
	return h, nil
}

func (f *statusFakeSource) FetchStatus(ctx context.Context, stock config.Stock, start, end string) (market.StatusWindow, error) {
	if f.statusErr != nil {
		return market.StatusWindow{}, f.statusErr
	}
	now := time.Now().In(market.Shanghai)
	source := f.source
	if source == "" {
		source = "baostock"
	}
	w := market.StatusWindow{Code: stock.Code, Start: start, End: end, Source: source, FetchedAt: now, Full: !f.sparse}
	for _, day := range weekdays(start, end) {
		if f.unknown[stock.Code][day] {
			continue
		}
		state := market.SessionTraded
		if f.suspended[stock.Code][day] {
			state = market.SessionSuspended
		} else if f.sparse {
			continue
		}
		w.Statuses = append(w.Statuses, market.SessionStatus{Code: stock.Code, Date: day, State: state, Source: source, FetchedAt: now})
	}
	return w, nil
}

func (f *statusFakeSource) FetchTradingDays(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func weekdays(start, end string) []string {
	first, _ := time.Parse("2006-01-02", start)
	last, _ := time.Parse("2006-01-02", end)
	days := []string{}
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			days = append(days, day.Format("2006-01-02"))
		}
	}
	return days
}

func statusFixture(t *testing.T, stocks int) (*Monitor, *statusFakeSource, *storage.MarketDB) {
	t.Helper()
	c := config.Defaults()
	c.RequireCIFS = false
	c.Stocks = []config.Stock{}
	codes := []string{"sz000001", "sz000002", "sz000003"}
	for i := 0; i < stocks; i++ {
		c.Stocks = append(c.Stocks, config.Stock{Code: codes[i], Name: codes[i], ListedFrom: "2026-08-01", Sector: "测试"})
	}
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Selection.Enabled = true
	c.Daily.HistoryDays = 90
	c.Daily.Adjust = "none"
	c.Sync.MinCoveragePct = 90
	c.Sync.MinAnalyzablePct = 50
	c.Sync.StatusRefreshDays = 30
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	db, err := storage.InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	base := &fakeSource{}
	fake := &statusFakeSource{
		fakeSource: base,
		suspended:  map[string]map[string]bool{},
		missing:    map[string]map[string]bool{},
		unknown:    map[string]map[string]bool{},
	}
	m := NewWithMarketDB(c, store, db, &combinedStatusSource{statusFakeSource: fake}, nil, nil)
	now, _ := time.ParseInLocation("2006-01-02 15:04", "2026-10-13 19:00", market.Shanghai)
	m.Now = func() time.Time { return now }
	return m, fake, db
}

// combinedStatusSource implements price, status and calendar interfaces without
// hiding the price fake's call counters.
type combinedStatusSource struct {
	*statusFakeSource
}

func (f *combinedStatusSource) FetchTradingDays(ctx context.Context, start, end string) ([]string, error) {
	return weekdays(start, end), nil
}

func TestStatusBackfillAllowsSuspendedAsOf(t *testing.T) {
	m, fake, db := statusFixture(t, 2)
	fake.suspended["sz000002"] = map[string]bool{"2026-10-13": true}
	ctx := context.Background()
	if _, err := m.StatusBackfill(ctx, "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	report, err := m.Sync(ctx, "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	if report.Eligible != 1 {
		t.Fatalf("suspended stock entered eligibility: %+v", report)
	}
	view, err := db.LoadDashboard(ctx, m.Config.Stocks, "none")
	if err != nil {
		t.Fatal(err)
	}
	if view.Summary.Complete != 2 || view.Summary.Analyzable != 1 || view.Summary.Suspended != 1 || view.Summary.Gaps != 0 {
		t.Fatalf("summary = %+v", view.Summary)
	}
	var suspended *storage.DashboardStock
	for i := range view.Stocks {
		if view.Stocks[i].Code == "sz000002" {
			suspended = &view.Stocks[i]
		}
	}
	if suspended == nil || suspended.Status != "suspended" || suspended.Reason != "complete_not_analyzable" || suspended.SessionState != "suspended" || !suspended.Complete || suspended.Analyzable {
		t.Fatalf("suspended row = %+v", suspended)
	}
}

func TestSyncRefusesToPublishWithoutStatusBackfill(t *testing.T) {
	m, fake, _ := statusFixture(t, 2)
	_, err := m.Sync(context.Background(), "2026-10-13")
	if err == nil || !strings.Contains(err.Error(), "status backfill incomplete") {
		t.Fatalf("expected status backfill refusal: %v", err)
	}
	if fake.fakeSource.calls != 0 {
		t.Fatalf("downloaded prices before status coverage: %d", fake.fakeSource.calls)
	}
}

func TestStatusRefreshFailureInvalidatesRecentCoverage(t *testing.T) {
	m, fake, _ := statusFixture(t, 2)
	ctx := context.Background()
	if _, err := m.StatusBackfill(ctx, "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Sync(ctx, "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	fake.statusErr = &market.Failure{Kind: "network"}
	audit, err := m.StatusRefresh(ctx, "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	if audit.Complete != 0 || audit.Gaps != 2 {
		t.Fatalf("audit after failed refresh = %+v", audit)
	}
	for _, row := range audit.Stocks {
		if row.Complete || row.Reason != "status_coverage_incomplete" {
			t.Fatalf("stock coverage was not invalidated: %+v", row)
		}
	}
	_, err = m.Sync(ctx, "2026-10-13")
	if err == nil || !strings.Contains(err.Error(), "status backfill incomplete") {
		t.Fatalf("sync accepted invalidated status coverage: %v", err)
	}
}

func TestSparseStatusFallbackDoesNotClaimBackfillCoverage(t *testing.T) {
	m, fake, _ := statusFixture(t, 2)
	fake.source = "tushare"
	fake.sparse = true
	fake.suspended["sz000002"] = map[string]bool{"2026-10-13": true}
	ctx := context.Background()
	audit, err := m.StatusBackfill(ctx, "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	if audit.Complete != 0 || audit.Gaps != 2 {
		t.Fatalf("sparse-only backfill claimed completeness: %+v", audit)
	}
	for _, row := range audit.Stocks {
		if row.Complete {
			t.Fatalf("sparse-only row = %+v", row)
		}
	}
	_, err = m.Sync(ctx, "2026-10-13")
	if err == nil || !strings.Contains(err.Error(), "status backfill incomplete") {
		t.Fatalf("sync accepted sparse status coverage: %v", err)
	}
	if fake.fakeSource.calls != 0 {
		t.Fatalf("downloaded prices after sparse-only status: %d", fake.fakeSource.calls)
	}
}

func TestStatusRefreshListingFailureInvalidatesRecentCoverage(t *testing.T) {
	m, _, db := statusFixture(t, 1)
	ctx := context.Background()
	if _, err := m.StatusBackfill(ctx, "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	stock := m.Config.Stocks[0]
	if ok, err := db.StatusCoverage(ctx, stock.Code, "2026-09-14", "2026-10-13"); err != nil || !ok {
		t.Fatalf("initial recent coverage: %v %v", ok, err)
	}
	m.Config.Stocks[0].ListedFrom = ""
	if err := m.refreshRecentStatuses(ctx, "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.StatusCoverage(ctx, stock.Code, "2026-09-14", "2026-10-13"); err != nil || ok {
		t.Fatalf("listing failure left recent coverage valid: %v %v", ok, err)
	}
	if ok, err := db.StatusCoverage(ctx, stock.Code, "2026-08-03", "2026-09-13"); err != nil || !ok {
		t.Fatalf("listing failure discarded earlier coverage: %v %v", ok, err)
	}
}

func TestUnknownGapFailsCoverageInsteadOfMarkingSuspension(t *testing.T) {
	m, fake, db := statusFixture(t, 2)
	fake.unknown["sz000002"] = map[string]bool{"2026-09-15": true}
	ctx := context.Background()
	if _, err := m.StatusBackfill(ctx, "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	_, err := m.Sync(ctx, "2026-10-13")
	if err == nil || !strings.Contains(err.Error(), "coverage") {
		t.Fatalf("unknown gap did not lower coverage: %v", err)
	}
	run, err := db.LatestRun(ctx, "2026-10-13")
	if err != nil || run.ErrorKind != "coverage" || run.Status != "failed" {
		t.Fatalf("run = %+v, %v", run, err)
	}
}

func TestAnalyzableGateRejectsAllSuspendedPool(t *testing.T) {
	m, fake, _ := statusFixture(t, 2)
	fake.suspended["sz000001"] = map[string]bool{"2026-10-13": true}
	fake.suspended["sz000002"] = map[string]bool{"2026-10-13": true}
	ctx := context.Background()
	if _, err := m.StatusBackfill(ctx, "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	_, err := m.Sync(ctx, "2026-10-13")
	if err == nil || !strings.Contains(err.Error(), "analyzable coverage") {
		t.Fatalf("expected analyzable gate: %v", err)
	}
}

func TestQFQFailureKeepsRawCompleteness(t *testing.T) {
	m, fake, db := statusFixture(t, 2)
	m.Config.Daily.Adjust = "qfq"
	fake.fakeSource.err = &market.Failure{Kind: "network"}
	fake.fakeSource.failAdjust = "qfq"
	fake.fakeSource.failCode = "sz000002"
	ctx := context.Background()
	if _, err := m.StatusBackfill(ctx, "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Sync(ctx, "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	raw, err := db.LoadLatestHistory(ctx, "sz000002", "none", "2026-10-13")
	if err != nil || raw.AsOf != "2026-10-13" {
		t.Fatalf("raw history not retained: %v %+v", err, raw)
	}
	if _, err := db.LoadLatestHistory(ctx, "sz000002", "qfq", "2026-10-13"); err == nil {
		t.Fatal("failed qfq history was published")
	}
	view, err := db.LoadDashboard(ctx, m.Config.Stocks, "qfq")
	if err != nil {
		t.Fatal(err)
	}
	if view.Summary.Complete != 2 || view.Summary.Analyzable != 1 || view.Summary.Suspended != 0 {
		t.Fatalf("summary = %+v", view.Summary)
	}
	var failed *storage.DashboardStock
	for i := range view.Stocks {
		if view.Stocks[i].Code == "sz000002" {
			failed = &view.Stocks[i]
		}
	}
	if failed == nil || failed.Status != "isolated" || failed.Reason != "network" || failed.SessionState == "suspended" {
		t.Fatalf("qfq failure row = %+v", failed)
	}
	audit, err := m.StatusAudit(ctx, "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	if audit.Suspended != 0 {
		t.Fatalf("qfq failure counted as suspension: %+v", audit)
	}
	for _, row := range audit.Stocks {
		if row.Code == "sz000002" && (row.Reason != "adjusted_history_unavailable" || !row.Complete || row.Analyzable) {
			t.Fatalf("qfq audit row = %+v", row)
		}
	}
}
