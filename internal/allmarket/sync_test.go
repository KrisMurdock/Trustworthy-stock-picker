package allmarket

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

type fakeUniverse struct {
	universe market.Universe
	err      error
}

func (f fakeUniverse) Discover(context.Context) (market.Universe, error) {
	return f.universe, f.err
}

type fetchCall struct {
	code   string
	start  string
	end    string
	adjust string
}

type fakeHistorySource struct {
	mu       sync.Mutex
	calls    []fetchCall
	failures map[string]error
}

func (f *fakeHistorySource) Fetch(_ context.Context, stock config.Stock, start, end, adjust string) (market.History, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fetchCall{code: stock.Code, start: start, end: end, adjust: adjust})
	failure := f.failures[stock.Code+":"+adjust]
	f.mu.Unlock()
	if failure != nil {
		return market.History{}, failure
	}
	all := []market.Bar{
		{Date: "2026-10-28", Open: 10, High: 11, Low: 9, Close: 10.5, Volume: 1000},
		{Date: "2026-10-29", Open: 11, High: 12, Low: 10, Close: 11.5, Volume: 1100},
		{Date: "2026-10-30", Open: 12, High: 13, Low: 11, Close: 12.5, Volume: 1200},
		{Date: "2026-11-02", Open: 13, High: 14, Low: 12, Close: 13.5, Volume: 1300},
	}
	bars := []market.Bar{}
	for _, bar := range all {
		if bar.Date >= start && bar.Date <= end {
			bars = append(bars, bar)
		}
	}
	if len(bars) == 0 {
		return market.History{}, &market.DataError{Reason: "empty_requested_window"}
	}
	h := market.History{Version: 1, Code: stock.Code, Name: stock.Name, Source: "tencent", Adjust: adjust,
		DataField: "day", VolumeUnit: "provider_raw", AsOf: end, FetchedAt: time.Now().In(market.Shanghai), Bars: bars}
	if adjust != "none" {
		h.DataField = adjust + "day"
	}
	return h, nil
}

func (f *fakeHistorySource) Calls() []fetchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fetchCall(nil), f.calls...)
}

func allMarketTestConfig() config.Config {
	c := config.Defaults()
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Daily.HistoryDays = 365
	return c
}

func TestSyncerFullThenSkipAndIncremental(t *testing.T) {
	c := allMarketTestConfig()
	db, err := storage.OpenAllMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := &fakeHistorySource{failures: map[string]error{}}
	universe := fakeUniverse{universe: market.Universe{Version: 1, Total: 1, Stocks: []config.Stock{{Code: "sh600000", Name: "测试股票"}}}}
	syncer := New(c, db, source, universe, nil)
	syncer.Now = func() time.Time { return time.Date(2026, 10, 30, 16, 0, 0, 0, market.Shanghai) }

	first, err := syncer.Run(context.Background(), Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "complete" || first.Succeeded != 1 || first.Skipped != 0 {
		t.Fatalf("first run: %+v", first)
	}
	if calls := source.Calls(); len(calls) != 2 || calls[0].adjust != "none" || calls[1].adjust != "qfq" {
		t.Fatalf("first run calls: %+v", calls)
	}
	if calls := source.Calls(); calls[0].start != "2016-10-30" || calls[1].start != "2025-10-30" {
		t.Fatalf("unexpected full windows: %+v", calls)
	}

	second, err := syncer.Run(context.Background(), Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "complete" || second.Skipped != 1 {
		t.Fatalf("second run: %+v", second)
	}
	if calls := source.Calls(); len(calls) != 2 {
		t.Fatalf("resume should not fetch again: %+v", calls)
	}

	syncer.Now = func() time.Time { return time.Date(2026, 11, 2, 16, 0, 0, 0, market.Shanghai) }
	third, err := syncer.Run(context.Background(), Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if third.Status != "complete" || third.Succeeded != 1 {
		t.Fatalf("third run: %+v", third)
	}
	calls := source.Calls()
	if len(calls) != 4 || calls[2].adjust != "none" || calls[2].start != "2026-09-30" || calls[3].adjust != "qfq" || calls[3].start != "2025-11-02" {
		t.Fatalf("incremental calls: %+v", calls)
	}
	last, err := db.LastBarDate(context.Background(), "sh600000", "none")
	if err != nil || last != "2026-11-02" {
		t.Fatalf("last date after incremental run: %q %v", last, err)
	}
}

func TestSyncerContinuesAfterOneStockFailure(t *testing.T) {
	c := allMarketTestConfig()
	db, err := storage.OpenAllMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := &fakeHistorySource{failures: map[string]error{"sz000002:qfq": errors.New("provider unavailable")}}
	universe := fakeUniverse{universe: market.Universe{Version: 1, Total: 2, Stocks: []config.Stock{
		{Code: "sh600000", Name: "成功股票"},
		{Code: "sz000002", Name: "部分股票"},
	}}}
	syncer := New(c, db, source, universe, nil)
	syncer.Now = func() time.Time { return time.Date(2026, 10, 30, 16, 0, 0, 0, market.Shanghai) }
	result, err := syncer.Run(context.Background(), Options{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "partial" || result.Succeeded != 1 || result.Partial != 1 || result.Failed != 0 {
		t.Fatalf("unexpected partial result: %+v", result)
	}
	if status, err := db.SyncStatus(context.Background(), "sz000002", "qfq", "2026-10-30"); err != nil || status.Status != "failed" {
		t.Fatalf("expected failed qfq status: %+v %v", status, err)
	}
	if _, err := db.LastSuccessful(context.Background(), "sz000002", "none"); err != nil {
		t.Fatalf("raw history should have been saved: %v", err)
	}
}

func TestSyncerResumeWithOneMissingAdjustmentIsSuccess(t *testing.T) {
	c := allMarketTestConfig()
	db, err := storage.OpenAllMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	universe := fakeUniverse{universe: market.Universe{Version: 1, Total: 1, Stocks: []config.Stock{{Code: "sh600000", Name: "测试股票"}}}}
	syncer := New(c, db, &fakeHistorySource{failures: map[string]error{"sh600000:qfq": errors.New("provider unavailable")}}, universe, nil)
	syncer.Now = func() time.Time { return time.Date(2026, 10, 30, 16, 0, 0, 0, market.Shanghai) }
	first, err := syncer.Run(context.Background(), Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "partial" || first.Partial != 1 {
		t.Fatalf("first run should be partial: %+v", first)
	}

	syncer.Market = &fakeHistorySource{failures: map[string]error{}}
	second, err := syncer.Run(context.Background(), Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "complete" || second.Succeeded != 1 || second.Partial != 0 || second.Skipped != 0 {
		t.Fatalf("resumed run should complete the missing adjustment: %+v", second)
	}
	if status, err := db.SyncStatus(context.Background(), "sh600000", "qfq", "2026-10-30"); err != nil || status.Status != "ok" {
		t.Fatalf("qfq status after resume: %+v %v", status, err)
	}
}
