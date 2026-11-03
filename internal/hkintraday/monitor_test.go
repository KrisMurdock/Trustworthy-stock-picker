package hkintraday

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type testUniverse struct {
	stocks []config.Stock
	err    error
}

func (source testUniverse) Discover(context.Context) (market.Universe, error) {
	return market.Universe{Version: 1, Market: "hk_connect", Source: "fixture", Total: len(source.stocks), Stocks: source.stocks}, source.err
}

type testCalendar struct {
	days []string
	err  error
}

func (source testCalendar) TradingDays(_ context.Context, from, through string) ([]string, error) {
	var result []string
	for _, day := range source.days {
		if day >= from && day <= through {
			result = append(result, day)
		}
	}
	return result, source.err
}

type testMinuteSource struct {
	histories map[string]market.HKMinuteHistory
	errors    map[string]error
}

type trackingMinuteSource struct {
	mu      sync.Mutex
	history market.HKMinuteHistory
	from    string
}

type signalingMinuteSource struct {
	history market.HKMinuteHistory
	called  chan struct{}
}

func (source signalingMinuteSource) FetchMinutes(_ context.Context, _ config.Stock, _, _ string) (market.HKMinuteHistory, error) {
	select {
	case source.called <- struct{}{}:
	default:
	}
	return source.history, nil
}

func (source *trackingMinuteSource) FetchMinutes(_ context.Context, _ config.Stock, from, _ string) (market.HKMinuteHistory, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.from = from
	return source.history, nil
}

func (source testMinuteSource) FetchMinutes(ctx context.Context, stock config.Stock, _, _ string) (market.HKMinuteHistory, error) {
	if ctx.Err() != nil {
		return market.HKMinuteHistory{}, ctx.Err()
	}
	return source.histories[stock.Code], source.errors[stock.Code]
}

func testHistory(code, day string, early bool) market.HKMinuteHistory {
	date, _ := time.ParseInLocation("2006-01-02", day, market.Shanghai)
	history := market.HKMinuteHistory{Version: 1, Code: code, Name: code, Source: "eastmoney_hk", Period: 5, BasePeriod: 5,
		Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: date.Add(16*time.Hour + 20*time.Minute)}
	sessions := [][2]int{{570, 720}, {780, 960}}
	if early {
		sessions = sessions[:1]
	}
	for _, session := range sessions {
		for minute := session[0] + 5; minute <= session[1]; minute += 5 {
			history.Bars = append(history.Bars, market.MinuteBar{
				End:  time.Date(date.Year(), date.Month(), date.Day(), minute/60, minute%60, 0, 0, market.Shanghai),
				Open: 10, High: 12, Low: 9, Close: 11, Volume: 100, Final: true,
			})
		}
	}
	return history
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	c := config.Defaults()
	c.DataDir = t.TempDir()
	c.RequireCIFS = false
	c.HKIntraday.Enabled = true
	c.HKIntraday.Workers = 2
	return c
}

func TestHKScheduleUsesIndependentSessionsAndEarlyCloses(t *testing.T) {
	c := config.DefaultHKIntraday()
	c.Enabled = true
	for _, test := range []struct {
		stamp  string
		active bool
	}{
		{"2026-11-02 09:29", false}, {"2026-11-02 09:35", true},
		{"2026-11-02 12:10", true}, {"2026-11-02 12:30", false},
		{"2026-11-02 13:00", true}, {"2026-11-02 16:15", true},
		{"2026-11-02 17:01", false}, {"2026-11-01 10:00", false},
		{"2026-12-24 12:15", true}, {"2026-12-24 13:10", false},
	} {
		now, _ := time.ParseInLocation("2006-01-02 15:04", test.stamp, market.Shanghai)
		active, err := Active(c, now)
		if err != nil || active != test.active {
			t.Fatalf("%s: active=%t err=%v", test.stamp, active, err)
		}
	}
	now, _ := time.ParseInLocation("2006-01-02", "2027-01-04", market.Shanghai)
	if _, err := Active(c, now); err == nil {
		t.Fatal("expired HK calendar accepted")
	}
}

func TestHKSyncPersistsSuccessGapsAndProviderFailure(t *testing.T) {
	c := testConfig(t)
	stocks := []config.Stock{{Code: "hk00700", Name: "腾讯"}, {Code: "hk00005", Name: "汇丰"}, {Code: "hk01299", Name: "友邦"}}
	complete := testHistory(stocks[0].Code, "2026-11-02", false)
	partial := testHistory(stocks[1].Code, "2026-11-02", false)
	partial.Bars = append(partial.Bars[:20], partial.Bars[21:]...)
	source := testMinuteSource{histories: map[string]market.HKMinuteHistory{stocks[0].Code: complete, stocks[1].Code: partial}, errors: map[string]error{stocks[2].Code: &market.Failure{Status: 403, Kind: "status"}}}
	monitor := New(c, source, testUniverse{stocks: stocks}, testCalendar{days: []string{"2026-11-02"}}, nil)
	monitor.Now = func() time.Time { return complete.FetchedAt }
	result, err := monitor.Sync(context.Background(), Options{From: "2026-11-02", Through: "2026-11-02"})
	if err != nil || result.Status != "partial" || result.Succeeded != 1 || result.Partial != 1 || result.Failed != 1 {
		t.Fatalf("unexpected result: %+v %v", result, err)
	}
	view, err := storage.OpenHKMarketView(c.DataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	for code, expected := range map[string]string{stocks[0].Code: "ok", stocks[1].Code: "partial", stocks[2].Code: "failed"} {
		snapshot, err := view.LoadHKMinutes(context.Background(), code, 5, c.HKIntraday)
		if err != nil || snapshot.Sync.Status != expected {
			t.Fatalf("%s: %+v %v", code, snapshot.Sync, err)
		}
		if expected == "partial" && (snapshot.Sync.MissingBars != 1 || snapshot.Sync.Reason != "incomplete_session") {
			t.Fatalf("missing bar not recorded: %+v", snapshot.Sync)
		}
		if expected == "failed" && snapshot.Sync.Reason != "http_403" {
			t.Fatalf("provider failure not classified: %+v", snapshot.Sync)
		}
	}
}

func TestHKSyncBackfillCannotClaimTruncatedHistory(t *testing.T) {
	c := testConfig(t)
	stock := config.Stock{Code: "hk00700", Name: "腾讯"}
	history := testHistory(stock.Code, "2026-11-02", false)
	monitor := New(c, testMinuteSource{histories: map[string]market.HKMinuteHistory{stock.Code: history}}, testUniverse{stocks: []config.Stock{stock}},
		testCalendar{days: []string{"2026-10-30", "2026-11-02"}}, nil)
	monitor.Now = func() time.Time { return history.FetchedAt }
	result, err := monitor.Sync(context.Background(), Options{From: "2026-10-30", Through: "2026-11-02"})
	if err != nil || result.Status != "partial" || result.Partial != 1 {
		t.Fatalf("truncated backfill accepted: %+v %v", result, err)
	}
	view, err := storage.OpenHKMarketView(c.DataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	state, err := view.HKMinuteState(context.Background(), stock.Code)
	if err != nil || state.MissingBars != 66 || state.RequestedFrom != "2026-10-30" {
		t.Fatalf("missing history not recorded: %+v %v", state, err)
	}
}

func TestHKSyncAutomaticallyRetriesEarlierIncompleteRange(t *testing.T) {
	c := testConfig(t)
	stock := config.Stock{Code: "hk00700", Name: "腾讯"}
	history := testHistory(stock.Code, "2026-11-02", false)
	source := &trackingMinuteSource{history: history}
	monitor := New(c, source, testUniverse{stocks: []config.Stock{stock}}, testCalendar{days: []string{"2026-10-02", "2026-11-02"}}, nil)
	monitor.Now = func() time.Time { return history.FetchedAt }
	result, err := monitor.Sync(context.Background(), Options{From: "2026-10-02", Through: "2026-11-02"})
	if err != nil || result.Status != "partial" {
		t.Fatalf("initial incomplete range: %+v %v", result, err)
	}
	result, err = monitor.Sync(context.Background(), Options{From: "2026-11-02", Through: "2026-11-02"})
	if err != nil || result.Status != "partial" {
		t.Fatalf("narrow success concealed an earlier gap: %+v %v", result, err)
	}
	view, err := storage.OpenHKMarketView(c.DataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	state, err := view.HKMinuteState(context.Background(), stock.Code)
	view.Close()
	if err != nil || state.Status != "partial" || state.Reason != "history_gap_unresolved" || state.UnresolvedFrom != "2026-10-02" || state.UnresolvedThrough != "2026-11-01" {
		t.Fatalf("narrow success persisted an incorrect status: %+v %v", state, err)
	}
	result, err = monitor.Sync(context.Background(), Options{Automatic: true})
	if err != nil || result.Status != "partial" || source.from != "2026-10-02" {
		t.Fatalf("earlier gap was not retried: from=%s result=%+v err=%v", source.from, result, err)
	}
}

func TestHKSyncNarrowRecoveryCannotHideFailedHistory(test *testing.T) {
	configuration := testConfig(test)
	stock := config.Stock{Code: "hk00700", Name: "腾讯"}
	history := testHistory(stock.Code, "2026-11-02", false)
	failed := testMinuteSource{errors: map[string]error{stock.Code: &market.Failure{Status: 403, Kind: "status"}}}
	calendar := testCalendar{days: []string{"2026-10-30", "2026-11-02"}}
	monitor := New(configuration, failed, testUniverse{stocks: []config.Stock{stock}}, calendar, nil)
	monitor.Now = func() time.Time { return history.FetchedAt }
	result, err := monitor.Sync(context.Background(), Options{From: "2026-10-30", Through: "2026-11-02"})
	if err != nil || result.Status != "partial" || result.Failed != 1 {
		test.Fatalf("provider failure not recorded: %+v %v", result, err)
	}
	source := &trackingMinuteSource{history: history}
	monitor.Market = source
	result, err = monitor.Sync(context.Background(), Options{From: "2026-11-02", Through: "2026-11-02"})
	if err != nil || result.Status != "partial" || result.Partial != 1 {
		test.Fatalf("narrow recovery concealed failed history: %+v %v", result, err)
	}
	result, err = monitor.Sync(context.Background(), Options{Automatic: true})
	if err != nil || result.Status != "partial" || source.from > "2026-10-30" {
		test.Fatalf("failed history was not retried: from=%s result=%+v err=%v", source.from, result, err)
	}
}

func TestHKSyncEarlyCloseAndUnconfirmedTradingDay(t *testing.T) {
	c := testConfig(t)
	stock := config.Stock{Code: "hk00700", Name: "腾讯"}
	history := testHistory(stock.Code, "2026-12-24", true)
	monitor := New(c, testMinuteSource{histories: map[string]market.HKMinuteHistory{stock.Code: history}}, testUniverse{stocks: []config.Stock{stock}},
		testCalendar{days: []string{"2026-12-24"}}, nil)
	monitor.Now = func() time.Time { return history.FetchedAt }
	result, err := monitor.Sync(context.Background(), Options{From: "2026-12-24", Through: "2026-12-24"})
	if err != nil || result.Status != "complete" {
		t.Fatalf("early close not complete: %+v %v", result, err)
	}
	monitor.Calendar = testCalendar{days: []string{"2026-12-23"}}
	result, err = monitor.Sync(context.Background(), Options{Automatic: true})
	if err != nil || result.Status != "skipped" || result.Reason != "trading_day_unconfirmed" {
		t.Fatalf("unconfirmed day was not skipped: %+v %v", result, err)
	}
}

func TestHKMinuteLoopRunsAndReleasesWriterOnCancellation(test *testing.T) {
	configuration := testConfig(test)
	stock := config.Stock{Code: "hk00700", Name: "腾讯"}
	history := testHistory(stock.Code, "2026-11-02", false)
	called := make(chan struct{}, 1)
	source := signalingMinuteSource{history: history, called: called}
	monitor := New(configuration, source, testUniverse{stocks: []config.Stock{stock}}, testCalendar{days: []string{"2026-11-02"}}, nil)
	monitor.Now = func() time.Time { return history.FetchedAt }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- monitor.Run(ctx) }()
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		test.Fatal("automatic HK minute loop did not request data")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			test.Fatalf("unexpected loop result: %v", err)
		}
	case <-time.After(5 * time.Second):
		test.Fatal("automatic HK minute loop did not stop")
	}
	database, err := storage.OpenHKMarket(configuration.DataDir, false)
	if err != nil {
		test.Fatalf("HK writer lock was not released: %v", err)
	}
	database.Close()
}

func TestHKSyncRejectsInvalidConfigStorageCodesAndCancellation(t *testing.T) {
	c := testConfig(t)
	stock := config.Stock{Code: "hk00700", Name: "腾讯"}
	monitor := New(c, testMinuteSource{}, testUniverse{stocks: []config.Stock{stock}}, testCalendar{days: []string{"2026-11-02"}}, nil)
	monitor.Now = func() time.Time { return time.Date(2026, 11, 2, 16, 20, 0, 0, market.Shanghai) }
	for _, options := range []Options{
		{From: "2025-12-31", Through: "2026-11-02"},
		{From: "2026-11-02"},
		{From: "2026-11-02", Through: "2026-11-02", Codes: []string{"sz000001"}},
		{From: "2026-11-02", Through: "2026-11-02", Codes: []string{"hk99999"}},
	} {
		if _, err := monitor.Sync(context.Background(), options); err == nil {
			t.Fatal(fmt.Sprintf("invalid options accepted: %+v", options))
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := monitor.Sync(canceled, Options{From: "2026-11-02", Through: "2026-11-02"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	monitor.Config.DataDir += "/missing"
	if _, err := monitor.Sync(context.Background(), Options{From: "2026-11-02", Through: "2026-11-02"}); err == nil {
		t.Fatal("missing data root accepted")
	}
}
