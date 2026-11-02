package hkconnect

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type fakeHKUniverse struct {
	universe market.Universe
	err      error
}

type failingHKUniverse struct{}

func (failingHKUniverse) Discover(context.Context) (market.Universe, error) {
	return market.Universe{}, errors.New("primary universe unavailable")
}

func (f fakeHKUniverse) Discover(context.Context) (market.Universe, error) {
	return f.universe, f.err
}

type fakeTradingDays struct {
	days []string
}

func (f fakeTradingDays) TradingDays(_ context.Context, from, through string) ([]string, error) {
	out := []string{}
	for _, day := range f.days {
		if day >= from && day <= through {
			out = append(out, day)
		}
	}
	return out, nil
}

type hkFetchCall struct {
	code   string
	start  string
	end    string
	adjust string
}

type fakeHKHistorySource struct {
	mu       sync.Mutex
	calls    []hkFetchCall
	failures map[string]error
	block    map[string]bool
	version  int
	lastBar  string
}

func hkHistoryBars(version int) []market.Bar {
	closeOn29 := 11.5
	if version > 1 {
		closeOn29 = 12.5
	}
	return []market.Bar{
		{Date: "2026-10-28", Open: 10, High: 11, Low: 9, Close: 10.5, Volume: 1000},
		{Date: "2026-10-29", Open: 11, High: 13, Low: 10, Close: closeOn29, Volume: 1100},
		{Date: "2026-10-30", Open: 12, High: 14, Low: 11, Close: 12.5, Volume: 1200},
		{Date: "2026-11-02", Open: 13, High: 15, Low: 12, Close: 13.5, Volume: 1300},
	}
}

func (f *fakeHKHistorySource) Fetch(ctx context.Context, stock config.Stock, start, end, adjust string) (market.History, error) {
	key := stock.Code + ":" + adjust
	f.mu.Lock()
	f.calls = append(f.calls, hkFetchCall{code: stock.Code, start: start, end: end, adjust: adjust})
	failure := f.failures[key]
	blocked := f.block[key]
	version := f.version
	lastBar := f.lastBar
	f.mu.Unlock()
	if blocked {
		<-ctx.Done()
		return market.History{}, ctx.Err()
	}
	if failure != nil {
		return market.History{}, failure
	}
	bars := []market.Bar{}
	for _, bar := range hkHistoryBars(version) {
		if bar.Date >= start && bar.Date <= end && (lastBar == "" || bar.Date <= lastBar) {
			bars = append(bars, bar)
		}
	}
	if len(bars) == 0 {
		return market.History{}, &market.DataError{Reason: "empty_requested_window"}
	}
	h := market.History{Version: 1, Code: stock.Code, Name: stock.Name, Source: "tencent_hk", Adjust: adjust,
		DataField: "day", VolumeUnit: "provider_raw", AsOf: end, FetchedAt: time.Now().In(market.Shanghai), Bars: bars}
	if adjust == "qfq" {
		h.DataField = "qfqday"
	}
	return h, nil
}

func (f *fakeHKHistorySource) Calls() []hkFetchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]hkFetchCall(nil), f.calls...)
}

func hkTestConfig() config.Config {
	c := config.Defaults()
	c.Daily.HistoryDays = 365
	return c
}

func hkTestUniverse(codes ...string) fakeHKUniverse {
	stocks := make([]config.Stock, 0, len(codes))
	for _, code := range codes {
		stocks = append(stocks, config.Stock{Code: code, Name: "股票" + code})
	}
	return fakeHKUniverse{universe: market.Universe{Version: 1, Market: "hk_connect", Total: len(stocks), Stocks: stocks}}
}

func hkTestTradingDays() fakeTradingDays {
	return fakeTradingDays{days: []string{"2026-10-26", "2026-10-27", "2026-10-28", "2026-10-29", "2026-10-30", "2026-11-02"}}
}

func TestSyncerUsesHongKongAsOfAndIncrementalWindows(t *testing.T) {
	c := hkTestConfig()
	db, err := storage.OpenHKMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := &fakeHKHistorySource{failures: map[string]error{}}
	syncer := New(c, db, source, hkTestUniverse("hk00700"), hkTestTradingDays(), nil)
	syncer.Now = func() time.Time { return time.Date(2026, 10, 30, 18, 50, 0, 0, market.Shanghai) }

	first, err := syncer.Run(context.Background(), Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Market != "hk_connect" || first.AsOf != "2026-10-30" || first.Status != "complete" || first.Succeeded != 1 {
		t.Fatalf("first run: %+v", first)
	}
	if calls := source.Calls(); len(calls) != 2 || calls[0].start != "2016-10-30" || calls[1].start != "2025-10-30" {
		t.Fatalf("full windows: %+v", calls)
	}

	second, err := syncer.Run(context.Background(), Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "complete" || second.Skipped != 1 || len(source.Calls()) != 2 {
		t.Fatalf("resume should skip: result=%+v calls=%+v", second, source.Calls())
	}

	syncer.Now = func() time.Time { return time.Date(2026, 11, 2, 18, 50, 0, 0, market.Shanghai) }
	third, err := syncer.Run(context.Background(), Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if third.AsOf != "2026-11-02" || third.Status != "complete" || third.Succeeded != 1 {
		t.Fatalf("third run: %+v", third)
	}
	calls := source.Calls()
	if len(calls) != 4 || calls[2].start != "2026-09-30" || calls[2].adjust != "none" || calls[3].start != "2025-11-02" || calls[3].adjust != "qfq" {
		t.Fatalf("incremental windows: %+v", calls)
	}
}

func TestSyncerDoesNotSkipWhenStoredBarsStopBeforeAsOf(t *testing.T) {
	db, err := storage.OpenHKMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := &fakeHKHistorySource{failures: map[string]error{}, lastBar: "2026-10-29"}
	syncer := New(hkTestConfig(), db, source, hkTestUniverse("hk00700"), hkTestTradingDays(), nil)
	syncer.Now = func() time.Time { return time.Date(2026, 10, 30, 18, 50, 0, 0, market.Shanghai) }

	if result, err := syncer.Run(context.Background(), Options{Workers: 1}); err != nil || result.Status != "complete" {
		t.Fatalf("first run: %+v %v", result, err)
	}
	if calls := source.Calls(); len(calls) != 2 {
		t.Fatalf("first run calls: %+v", calls)
	}
	for _, adjust := range []string{"none", "qfq"} {
		last, err := db.LastBarDate(context.Background(), "hk00700", adjust)
		if err != nil || last != "2026-10-29" {
			t.Fatalf("stored %s last date: %q %v", adjust, last, err)
		}
		refresh, err := syncer.needsRefresh(context.Background(), "hk00700", adjust, "2026-10-30")
		if err != nil || !refresh {
			t.Fatalf("unexpected freshness for %s: %v %v", adjust, refresh, err)
		}
	}
	if result, err := syncer.Run(context.Background(), Options{Workers: 1}); err != nil || result.Status != "complete" {
		t.Fatalf("second run: %+v %v", result, err)
	}
	if calls := source.Calls(); len(calls) != 4 {
		t.Fatalf("stale successful bars were skipped: %+v", calls)
	}
}

func TestFailoverUniverseUsesFallback(t *testing.T) {
	fallback := hkTestUniverse("hk00700")
	fallback.universe.Source = "sina"
	universe, err := (FailoverUniverse{Sources: []UniverseSource{failingHKUniverse{}, fallback}}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if universe.Source != "sina" || len(universe.Stocks) != 1 {
		t.Fatalf("fallback universe not used: %+v", universe)
	}
}

func TestSyncerRejectsUnfinishedAndNonTradingAsOf(t *testing.T) {
	db, err := storage.OpenHKMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	syncer := New(hkTestConfig(), db, &fakeHKHistorySource{failures: map[string]error{}}, hkTestUniverse("hk00700"), hkTestTradingDays(), nil)
	syncer.Now = func() time.Time { return time.Date(2026, 11, 2, 15, 0, 0, 0, market.Shanghai) }
	if _, err := syncer.Run(context.Background(), Options{AsOf: "2026-11-02", Workers: 1}); err == nil {
		t.Fatal("unfinished Hong Kong session accepted as-of")
	}
	syncer.Now = func() time.Time { return time.Date(2026, 11, 2, 18, 50, 0, 0, market.Shanghai) }
	if _, err := syncer.Run(context.Background(), Options{AsOf: "2026-10-25", Workers: 1}); err == nil {
		t.Fatal("non-trading date accepted as-of")
	}
}

func TestSyncerContinuesAfterOneStockFailure(t *testing.T) {
	db, err := storage.OpenHKMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := &fakeHKHistorySource{failures: map[string]error{"hk09988:qfq": errors.New("provider unavailable")}}
	syncer := New(hkTestConfig(), db, source, hkTestUniverse("hk00700", "hk09988"), hkTestTradingDays(), nil)
	syncer.Now = func() time.Time { return time.Date(2026, 10, 30, 18, 50, 0, 0, market.Shanghai) }
	result, err := syncer.Run(context.Background(), Options{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "partial" || result.Succeeded != 1 || result.Partial != 1 || result.Failed != 0 {
		t.Fatalf("unexpected partial result: %+v", result)
	}
	if status, err := db.SyncStatus(context.Background(), "hk09988", "qfq", "2026-10-30"); err != nil || status.Status != "failed" {
		t.Fatalf("expected failed qfq status: %+v %v", status, err)
	}
	if _, err := db.LastSuccessful(context.Background(), "hk09988", "none"); err != nil {
		t.Fatalf("raw history should have been saved: %v", err)
	}
}

func TestSyncerRefetchesFullRawHistoryAfterOverlapDrift(t *testing.T) {
	db, err := storage.OpenHKMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := &fakeHKHistorySource{failures: map[string]error{}}
	syncer := New(hkTestConfig(), db, source, hkTestUniverse("hk00700"), hkTestTradingDays(), nil)
	syncer.Now = func() time.Time { return time.Date(2026, 10, 30, 18, 50, 0, 0, market.Shanghai) }
	if _, err := syncer.Run(context.Background(), Options{Workers: 1}); err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	source.version = 2
	source.mu.Unlock()
	syncer.Now = func() time.Time { return time.Date(2026, 11, 2, 18, 50, 0, 0, market.Shanghai) }
	if _, err := syncer.Run(context.Background(), Options{Workers: 1}); err != nil {
		t.Fatal(err)
	}
	calls := source.Calls()
	if len(calls) != 5 || calls[2].start != "2026-09-30" || calls[3].start != "2016-11-02" || calls[3].adjust != "none" || calls[4].adjust != "qfq" {
		t.Fatalf("overlap drift did not trigger a full raw refetch: %+v", calls)
	}
}

func TestSyncerCancellationAbortsRun(t *testing.T) {
	db, err := storage.OpenHKMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := &fakeHKHistorySource{failures: map[string]error{}, block: map[string]bool{"hk00700:none": true}}
	syncer := New(hkTestConfig(), db, source, hkTestUniverse("hk00700"), hkTestTradingDays(), nil)
	syncer.Now = func() time.Time { return time.Date(2026, 10, 30, 18, 50, 0, 0, market.Shanghai) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		for len(source.Calls()) == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
		close(done)
	}()
	result, err := syncer.Run(ctx, Options{Workers: 1})
	<-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not returned: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("cancelled run status: %+v", result)
	}
}
