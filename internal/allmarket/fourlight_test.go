package allmarket

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func TestAnalyzeFourLightStockLifecycle(t *testing.T) {
	asOf, previous := "2026-10-13", "2026-10-12"
	stock := config.Stock{Code: "sz000001", Name: "测试股票"}
	computed := time.Date(2026, 10, 13, 18, 40, 0, 0, time.UTC)
	tests := []struct {
		name      string
		bars      []market.Bar
		prev      string
		state     string
		lifecycle string
		data      string
		reason    string
	}{
		{"first", fourLightBars(asOf, 80, true, false), previous, "all_red", "first_all_red", "ready", ""},
		{"continuing", fourLightBars(asOf, 80, true, true), previous, "all_red", "continuing_all_red", "ready", ""},
		{"exit", fourLightExitBars(asOf, 80), previous, "non_all_red", "exit_all_red", "ready", ""},
		{"non-all-red", fourLightBars(asOf, 80, false, false), previous, "non_all_red", "non_all_red", "ready", ""},
		{"previous missing", withoutPreviousBar(fourLightBars(asOf, 80, true, false)), previous, "all_red", "unknown", "partial", "上一交易日"},
		{"stale bar", fourLightBars("2026-10-12", 80, true, false), previous, "unknown", "unknown", "unknown", "未覆盖信号日"},
		{"insufficient", fourLightBars(asOf, 59, true, false), previous, "unknown", "unknown", "unknown", "不足"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			record := analyzeFourLightStock(stock, storage.AllMarketBarWindow{Code: stock.Code, Adjust: "qfq", Bars: tc.bars, Source: "tencent", DataField: "qfqday"}, nil, asOf, tc.prev, "four_light_approx_v1", "run-1", computed)
			if record.State != tc.state || record.Lifecycle != tc.lifecycle || record.DataStatus != tc.data {
				t.Fatalf("record = %+v", record)
			}
			if tc.reason != "" && !contains(record.Reason, tc.reason) {
				t.Fatalf("reason %q does not contain %q", record.Reason, tc.reason)
			}
		})
	}
}

func TestFourLightRunnerCoversActiveUniverseAndRecordsUnknown(t *testing.T) {
	asOf, previous := "2026-10-13", "2026-10-12"
	stocks := []config.Stock{
		{Code: "sz000001", Name: "首次"},
		{Code: "sh600519", Name: "持续"},
		{Code: "sz000002", Name: "退出"},
		{Code: "sz000004", Name: "缺失"},
		{Code: "sz000005", Name: "上一日缺失"},
	}
	store := &fakeFourLightStore{stocks: stocks, windows: map[string]storage.AllMarketBarWindow{
		"sz000001": {Code: "sz000001", Adjust: "qfq", Bars: fourLightBars(asOf, 80, true, false), Source: "tencent", DataField: "qfqday"},
		"sh600519": {Code: "sh600519", Adjust: "qfq", Bars: fourLightBars(asOf, 80, true, true), Source: "tencent", DataField: "qfqday"},
		"sz000002": {Code: "sz000002", Adjust: "qfq", Bars: fourLightExitBars(asOf, 80), Source: "tencent", DataField: "qfqday"},
		"sz000005": {Code: "sz000005", Adjust: "qfq", Bars: withoutPreviousBar(fourLightBars(asOf, 80, true, false)), Source: "tencent", DataField: "qfqday"},
	}, missing: map[string]error{"sz000004": fs.ErrNotExist}}
	c := config.Defaults()
	c.Calendar.ValidFrom, c.Calendar.ValidThrough = "2020-01-01", "2099-12-31"
	c.Calendar.Holidays = nil
	runner := NewFourLight(c, store, nil)
	runner.Now = func() time.Time { return time.Date(2026, 10, 13, 18, 40, 0, 0, market.Shanghai) }
	result, err := runner.Run(context.Background(), FourLightOptions{AsOf: asOf})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 5 || result.AllRed != 3 || result.FirstAllRed != 1 || result.ContinuingAllRed != 1 || result.ExitAllRed != 1 || result.StateUnknown != 1 || result.TransitionUnknown != 2 {
		t.Fatalf("result = %+v", result)
	}
	if store.started.ID == "" || store.completed.ID != store.started.ID || store.failed.ID != "" || len(store.records) != 5 {
		t.Fatalf("store calls = %+v", store)
	}
	missing := store.records[3]
	if missing.Code != "sz000004" || missing.State != "unknown" || missing.Lifecycle != "unknown" || missing.DataStatus != "unknown" {
		t.Fatalf("missing stock record = %+v", missing)
	}
	if previous != missing.PreviousTradeDate {
		t.Fatalf("previous trading date = %q", missing.PreviousTradeDate)
	}
	partial := store.records[4]
	if partial.Code != "sz000005" || partial.State != "all_red" || partial.Lifecycle != "unknown" || partial.DataStatus != "partial" {
		t.Fatalf("partial stock record = %+v", partial)
	}
}

func TestFourLightRunnerStorageFailureDoesNotCompleteRun(t *testing.T) {
	store := &fakeFourLightStore{stocks: []config.Stock{{Code: "sz000001", Name: "测试"}}, windows: map[string]storage.AllMarketBarWindow{}, missing: map[string]error{"sz000001": errors.New("disk failed")}}
	c := config.Defaults()
	c.Calendar.ValidFrom, c.Calendar.ValidThrough = "2020-01-01", "2099-12-31"
	c.Calendar.Holidays = nil
	runner := NewFourLight(c, store, nil)
	runner.Now = func() time.Time { return time.Date(2026, 10, 13, 18, 40, 0, 0, market.Shanghai) }
	result, err := runner.Run(context.Background(), FourLightOptions{AsOf: "2026-10-13"})
	if err == nil || result.Status != "failed" || store.completed.ID != "" || store.failed.ID == "" {
		t.Fatalf("storage failure was treated as a signal: result=%+v store=%+v err=%v", result, store, err)
	}
}

type fakeFourLightStore struct {
	stocks    []config.Stock
	windows   map[string]storage.AllMarketBarWindow
	missing   map[string]error
	started   storage.FourLightRun
	completed storage.FourLightRun
	failed    storage.FourLightRun
	records   []storage.FourLightRecord
}

func (f *fakeFourLightStore) ActiveStocks(context.Context) ([]config.Stock, error) {
	return append([]config.Stock(nil), f.stocks...), nil
}

func (f *fakeFourLightStore) LoadBarsThrough(_ context.Context, code, _, _ string, _ int) (storage.AllMarketBarWindow, error) {
	if err := f.missing[code]; err != nil {
		return storage.AllMarketBarWindow{}, err
	}
	window, ok := f.windows[code]
	if !ok {
		return storage.AllMarketBarWindow{}, fs.ErrNotExist
	}
	return window, nil
}

func (f *fakeFourLightStore) StartFourLightRun(_ context.Context, run storage.FourLightRun) error {
	f.started = run
	return nil
}

func (f *fakeFourLightStore) CompleteFourLightRun(_ context.Context, run storage.FourLightRun, records []storage.FourLightRecord) error {
	f.completed = run
	f.records = append([]storage.FourLightRecord(nil), records...)
	return nil
}

func (f *fakeFourLightStore) FailFourLightRun(_ context.Context, run storage.FourLightRun) error {
	f.failed = run
	return nil
}

func fourLightBars(end string, count int, currentRed, previousRed bool) []market.Bar {
	t, _ := time.ParseInLocation("2006-01-02", end, market.Shanghai)
	start := t.AddDate(0, 0, -(count - 1))
	bars := make([]market.Bar, 0, count)
	for index := 0; index < count; index++ {
		close := 10 + float64(index)*0.1
		volume := 1000.0
		if previousRed && index == count-2 {
			volume = 2000
		}
		if currentRed && index == count-1 {
			volume = 2000
		}
		day := start.AddDate(0, 0, index).Format("2006-01-02")
		bars = append(bars, market.Bar{Date: day, Open: close - 0.05, High: close + 0.1, Low: close - 0.1, Close: close, Volume: volume})
	}
	return bars
}

func fourLightExitBars(end string, count int) []market.Bar {
	bars := fourLightBars(end, count, true, true)
	previousClose := bars[len(bars)-2].Close
	bars[len(bars)-1].Open = previousClose * 0.6
	bars[len(bars)-1].High = previousClose * 0.62
	bars[len(bars)-1].Low = previousClose * 0.55
	bars[len(bars)-1].Close = previousClose * 0.6
	bars[len(bars)-1].Volume = 2000
	return bars
}

func withoutPreviousBar(bars []market.Bar) []market.Bar {
	return append(bars[:len(bars)-2], bars[len(bars)-1])
}

func contains(value, target string) bool {
	for index := 0; index+len(target) <= len(value); index++ {
		if value[index:index+len(target)] == target {
			return true
		}
	}
	return false
}
