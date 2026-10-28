package yangpullback

import (
	"math"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestSplitTradesPercentAndBoundaryPurge(t *testing.T) {
	dates := testWeekdays(10)
	trades := make([]Trade, 0, len(dates))
	for _, date := range dates {
		trades = append(trades, Trade{
			Signal:   Signal{Code: "sz000001", Date: date},
			ExitDate: date,
			Status:   "fixed_hold",
			Resolved: true,
		})
	}
	trades[1].ExitDate = dates[0]
	trades[1].Status = "exit_locked"
	trades[1].Resolved = false
	trades[2].ExitDate = dates[0]
	trades[2].Status = "corporate_action"
	trades[2].Resolved = false
	trades[3].ExitDate = dates[0]
	trades[3].Status = "missing_exit_session"
	trades[3].Resolved = false
	trades[4].ExitDate = dates[0]

	development, holdout, purged, boundary := splitTrades(trades, .70, testCalendar(), 5)
	if boundary != dates[7] {
		t.Fatalf("boundary = %q", boundary)
	}
	if purged != 6 {
		t.Fatalf("purged = %d", purged)
	}
	if len(development) != 1 || len(holdout) != 3 {
		t.Fatalf("split sizes = development %d, holdout %d", len(development), len(holdout))
	}
	if development[0].Signal.Date != dates[0] {
		t.Fatalf("development leaked a crossing label: %+v", development)
	}
	for _, trade := range development {
		if trade.Signal.Date >= boundary {
			t.Fatalf("development leaked holdout signal: %+v", trade)
		}
	}
	for _, trade := range holdout {
		if trade.Signal.Date < boundary {
			t.Fatalf("holdout leaked development signal: %+v", trade)
		}
	}
}

func TestFixedHoldTradeUsesRawOpenAndCloseWithCosts(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-05", Open: 9.8, Close: 10, High: 10.1, Low: 9.7, Volume: 1000},
		{Date: "2026-01-06", Open: 10, Close: 10.2, High: 10.3, Low: 9.9, Volume: 1000},
		{Date: "2026-01-07", Open: 10.2, Close: 10.5, High: 10.6, Low: 10.1, Volume: 1000},
		{Date: "2026-01-08", Open: 10.5, Close: 11, High: 11.1, Low: 10.4, Volume: 1000},
	}
	pair := testPair("sz000001", bars)
	c := config.Config{}
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	cal := calendar.New(c)
	parameters := config.DefaultStrategy()
	parameters.CommissionRate = 0
	parameters.MinCommission = 0
	parameters.TransferRate = 0
	parameters.SellStampRate = 0
	parameters.SlippageRate = 0
	parameters.TradeCash = 20000

	trade := fixedHoldTrade(pair, Signal{Code: "sz000001", Date: "2026-01-05"}, parameters, cal, 2)
	if !trade.Resolved || trade.EntrySkipped {
		t.Fatalf("trade not resolved: %+v", trade)
	}
	if trade.EntryDate != "2026-01-06" || trade.ExitDate != "2026-01-08" || trade.HoldingDays != 2 {
		t.Fatalf("execution dates = %+v", trade)
	}
	if math.Abs(trade.NetPct-10) > 1e-12 {
		t.Fatalf("net %% = %.12f", trade.NetPct)
	}
}

func TestFixedHoldTradeSkipsUntradableEntry(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-05", Open: 9.8, Close: 10, High: 10.1, Low: 9.7, Volume: 1000},
		{Date: "2026-01-06", Open: 10, Close: 10, High: 10, Low: 10, Volume: 0},
		{Date: "2026-01-07", Open: 10.2, Close: 10.5, High: 10.6, Low: 10.1, Volume: 1000},
		{Date: "2026-01-08", Open: 10.5, Close: 11, High: 11.1, Low: 10.4, Volume: 1000},
	}
	pair := testPair("sz000001", bars)
	c := config.Config{}
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	cal := calendar.New(c)
	trade := fixedHoldTrade(pair, Signal{Code: "sz000001", Date: "2026-01-05"}, config.DefaultStrategy(), cal, 2)
	if !trade.EntrySkipped || trade.Resolved || trade.Status != "entry_skipped" {
		t.Fatalf("entry skip = %+v", trade)
	}
}

func TestFixedHoldTradeLeavesLockedDownExitUnresolved(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-05", Open: 9.8, Close: 10, High: 10.1, Low: 9.7, Volume: 1000},
		{Date: "2026-01-06", Open: 10, Close: 10.2, High: 10.3, Low: 9.9, Volume: 1000},
		{Date: "2026-01-07", Open: 10.2, Close: 10.2, High: 10.3, Low: 10.1, Volume: 1000},
		{Date: "2026-01-08", Open: 9.18, Close: 9.18, High: 9.18, Low: 9.18, Volume: 1000},
	}
	pair := testPair("sz000001", bars)
	cal := testCalendar()
	trade := fixedHoldTrade(pair, Signal{Code: "sz000001", Date: "2026-01-05"}, config.DefaultStrategy(), cal, 2)
	if trade.Resolved || trade.Status != "exit_locked" {
		t.Fatalf("locked-down exit = %+v", trade)
	}
}

func TestFixedHoldTradeMarksPossibleSTLockedEntryUnknown(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-05", Open: 9.8, Close: 10, High: 10.1, Low: 9.7, Volume: 1000},
		{Date: "2026-01-06", Open: 10.5, Close: 10.5, High: 10.5, Low: 10.5, Volume: 1000},
		{Date: "2026-01-07", Open: 10.6, Close: 10.8, High: 10.9, Low: 10.5, Volume: 1000},
		{Date: "2026-01-08", Open: 10.8, Close: 11, High: 11.1, Low: 10.7, Volume: 1000},
	}
	trade := fixedHoldTrade(testPair("sz000001", bars), Signal{Code: "sz000001", Date: "2026-01-05"}, config.DefaultStrategy(), testCalendar(), 2)
	if !trade.EntrySkipped || trade.Resolved || trade.Status != "limit_status_unknown" {
		t.Fatalf("possible ST locked entry = %+v", trade)
	}
}

func TestFixedHoldTradeMarksPossibleSTLockedExitUnknown(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-05", Open: 9.8, Close: 10, High: 10.1, Low: 9.7, Volume: 1000},
		{Date: "2026-01-06", Open: 10, Close: 10.2, High: 10.3, Low: 9.9, Volume: 1000},
		{Date: "2026-01-07", Open: 10.2, Close: 10, High: 10.3, Low: 9.9, Volume: 1000},
		{Date: "2026-01-08", Open: 9.5, Close: 9.5, High: 9.5, Low: 9.5, Volume: 1000},
	}
	trade := fixedHoldTrade(testPair("sz000001", bars), Signal{Code: "sz000001", Date: "2026-01-05"}, config.DefaultStrategy(), testCalendar(), 2)
	if trade.EntrySkipped || trade.Resolved || trade.Status != "limit_status_unknown" {
		t.Fatalf("possible ST locked exit = %+v", trade)
	}
}

func TestFixedHoldTradeRejectsCorporateActionWindow(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-05", Open: 9.8, Close: 10, High: 10.1, Low: 9.7, Volume: 1000},
		{Date: "2026-01-06", Open: 10, Close: 10.2, High: 10.3, Low: 9.9, Volume: 1000},
		{Date: "2026-01-07", Open: 10.2, Close: 10.5, High: 10.6, Low: 10.1, Volume: 1000},
		{Date: "2026-01-08", Open: 10.5, Close: 11, High: 11.1, Low: 10.4, Volume: 1000},
	}
	pair := testPair("sz000001", bars)
	pair.adjusted.Bars[3].Close *= 0.90
	trade := fixedHoldTrade(pair, Signal{Code: "sz000001", Date: "2026-01-05"}, config.DefaultStrategy(), testCalendar(), 2)
	if trade.Resolved || trade.Status != "corporate_action" {
		t.Fatalf("corporate action = %+v", trade)
	}
}

func testCalendar() *calendar.Calendar {
	c := config.Config{}
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	return calendar.New(c)
}

func testPair(code string, bars []market.Bar) pairedHistory {
	indexes := make(map[string]int, len(bars))
	for index, bar := range bars {
		indexes[bar.Date] = index
	}
	return pairedHistory{
		code: code,
		adjusted: market.History{
			Code: code,
			AsOf: bars[len(bars)-1].Date,
			Bars: append([]market.Bar(nil), bars...),
		},
		raw: market.History{
			Code: code,
			AsOf: bars[len(bars)-1].Date,
			Bars: append([]market.Bar(nil), bars...),
		},
		indexByDate: indexes,
	}
}

func TestFixedHoldTradeReportsPathMFEAndMAE(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-05", Open: 9.8, Close: 10, High: 10.1, Low: 9.7, Volume: 1000},
		{Date: "2026-01-06", Open: 10, Close: 10.2, High: 10.3, Low: 9.9, Volume: 1000},
		{Date: "2026-01-07", Open: 10.2, Close: 10.5, High: 10.6, Low: 10.1, Volume: 1000},
		{Date: "2026-01-08", Open: 10.5, Close: 9.8, High: 10.5, Low: 9.7, Volume: 1000},
	}
	pair := testPair("sz000001", bars)
	trade := fixedHoldTrade(pair, Signal{Code: "sz000001", Date: "2026-01-05"}, zeroCostStrategy(), testCalendar(), 2)
	if !trade.Resolved {
		t.Fatalf("trade not resolved: %+v", trade)
	}
	if math.Abs(trade.MFE-6) > 1e-9 {
		t.Fatalf("MFE = %.6f, want 6", trade.MFE)
	}
	if math.Abs(trade.MAE-(-3)) > 1e-9 {
		t.Fatalf("MAE = %.6f, want -3", trade.MAE)
	}
}
