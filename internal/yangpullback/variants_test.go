package yangpullback

import (
	"math"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestVariantFixedRuleMatchesFixedHold(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-05", Open: 9.8, Close: 10, High: 10.1, Low: 9.7, Volume: 1000},
		{Date: "2026-01-06", Open: 10, Close: 10.2, High: 10.3, Low: 9.9, Volume: 1000},
		{Date: "2026-01-07", Open: 10.2, Close: 10.5, High: 10.6, Low: 10.1, Volume: 1000},
		{Date: "2026-01-08", Open: 10.5, Close: 11, High: 11.1, Low: 10.4, Volume: 1000},
		{Date: "2026-01-09", Open: 11, Close: 11.2, High: 11.3, Low: 10.9, Volume: 1000},
	}
	pair := testPair("sz000001", bars)
	signal := Signal{Code: "sz000001", Date: "2026-01-05"}
	strategy := zeroCostStrategy()
	want := fixedHoldTrade(pair, signal, strategy, testCalendar(), 2)
	got := variantTrade(pair, signal, strategy, testCalendar(), ExitRule{Name: "fixed_2d", Horizon: 2})
	if got.Resolved != want.Resolved || got.EntrySkipped != want.EntrySkipped || got.Status != want.Status {
		t.Fatalf("status mismatch: got %+v want %+v", got, want)
	}
	if got.EntryDate != want.EntryDate || got.ExitDate != want.ExitDate || got.HoldingDays != want.HoldingDays {
		t.Fatalf("execution dates mismatch: got %+v want %+v", got, want)
	}
	if math.Abs(got.NetPct-want.NetPct) > 1e-12 {
		t.Fatalf("net mismatch: got %.12f want %.12f", got.NetPct, want.NetPct)
	}
	if got.PriceTouch != want.PriceTouch || math.Abs(got.MaxHighPct-want.MaxHighPct) > 1e-12 {
		t.Fatalf("touch mismatch: got %+v want %+v", got, want)
	}
}

func TestVariantStopLossExitsAtStopPrice(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-05", Open: 9.8, Close: 10, High: 10.1, Low: 9.7, Volume: 1000},
		{Date: "2026-01-06", Open: 10, Close: 10.1, High: 10.2, Low: 9.9, Volume: 1000},
		{Date: "2026-01-07", Open: 10, Close: 9.6, High: 10.2, Low: 9.5, Volume: 1000},
	}
	trade := variantTrade(testPair("sz000001", bars), Signal{Code: "sz000001", Date: "2026-01-05"}, zeroCostStrategy(), testCalendar(), ExitRule{Name: "sl3_5d", Horizon: 5, StopLoss: -3})
	if !trade.Resolved || trade.Status != "stop" || trade.HoldingDays != 1 {
		t.Fatalf("stop trade = %+v", trade)
	}
	if trade.NetPct > -2.85 || trade.NetPct < -3.0 {
		t.Fatalf("stop net = %.4f, want about -3", trade.NetPct)
	}
	if trade.ExitDate != "2026-01-07" {
		t.Fatalf("stop exit date = %s", trade.ExitDate)
	}
}

func TestVariantTakeProfitExitsAtTargetPrice(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-05", Open: 9.8, Close: 10, High: 10.1, Low: 9.7, Volume: 1000},
		{Date: "2026-01-06", Open: 10, Close: 10.09, High: 10.1, Low: 9.9, Volume: 1000},
		{Date: "2026-01-07", Open: 10, Close: 10.4, High: 10.5, Low: 9.9, Volume: 1000},
	}
	trade := variantTrade(testPair("sz000001", bars), Signal{Code: "sz000001", Date: "2026-01-05"}, zeroCostStrategy(), testCalendar(), ExitRule{Name: "tp3_5d", Horizon: 5, TakeProfit: 3})
	if !trade.Resolved || trade.Status != "target" || trade.HoldingDays != 1 {
		t.Fatalf("target trade = %+v", trade)
	}
	if math.Abs(trade.NetPct-3) > 1e-9 {
		t.Fatalf("target net = %.12f, want 3", trade.NetPct)
	}
}

func TestVariantStopWinsWhenTargetAndStopShareADay(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-05", Open: 9.8, Close: 10, High: 10.1, Low: 9.7, Volume: 1000},
		{Date: "2026-01-06", Open: 10, Close: 10.0, High: 10.1, Low: 9.9, Volume: 1000},
		{Date: "2026-01-07", Open: 10, Close: 10.0, High: 10.5, Low: 9.5, Volume: 1000},
	}
	trade := variantTrade(testPair("sz000001", bars), Signal{Code: "sz000001", Date: "2026-01-05"}, zeroCostStrategy(), testCalendar(), ExitRule{Name: "tp3_sl3_5d", Horizon: 5, TakeProfit: 3, StopLoss: -3})
	if trade.Status != "stop" || !trade.Ambiguous || trade.TargetFirst {
		t.Fatalf("ambiguous trade = %+v", trade)
	}
}

func TestVariantEntryGapCapSkipsChasing(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-05", Open: 9.8, Close: 10, High: 10.1, Low: 9.7, Volume: 1000},
		{Date: "2026-01-06", Open: 11, Close: 11.1, High: 11.2, Low: 10.9, Volume: 1000},
	}
	trade := variantTrade(testPair("sz000001", bars), Signal{Code: "sz000001", Date: "2026-01-05"}, zeroCostStrategy(), testCalendar(), ExitRule{Name: "gap_cap3_5d", Horizon: 5, EntryGapCap: 3})
	if !trade.EntrySkipped || trade.Resolved || trade.Status != "entry_gap_skipped" {
		t.Fatalf("gap cap trade = %+v", trade)
	}
}

func TestVariantTrailingExitsOnCloseDrawdown(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-05", Open: 9.8, Close: 10, High: 10.1, Low: 9.7, Volume: 1000},
		{Date: "2026-01-06", Open: 10, Close: 10.4, High: 10.5, Low: 9.9, Volume: 1000},
		{Date: "2026-01-07", Open: 10.4, Close: 10.0, High: 10.5, Low: 9.9, Volume: 1000},
	}
	trade := variantTrade(testPair("sz000001", bars), Signal{Code: "sz000001", Date: "2026-01-05"}, zeroCostStrategy(), testCalendar(), ExitRule{Name: "trail3_10d", Horizon: 10, Trail: 3})
	if !trade.Resolved || trade.Status != "trail" || trade.HoldingDays != 1 {
		t.Fatalf("trailing trade = %+v", trade)
	}
	if trade.ExitDate != "2026-01-07" || math.Abs(trade.NetPct) > 1e-9 {
		t.Fatalf("trailing exit = %+v", trade)
	}
}

func TestVariantRejectsInvalidRule(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-05", Open: 9.8, Close: 10, High: 10.1, Low: 9.7, Volume: 1000},
		{Date: "2026-01-06", Open: 10, Close: 10.2, High: 10.3, Low: 9.9, Volume: 1000},
	}
	trade := variantTrade(testPair("sz000001", bars), Signal{Code: "sz000001", Date: "2026-01-05"}, zeroCostStrategy(), testCalendar(), ExitRule{Name: "bad", Horizon: 0})
	if trade.Status != "invalid_rule" {
		t.Fatalf("invalid rule trade = %+v", trade)
	}
}

func zeroCostStrategy() config.Strategy {
	s := config.DefaultStrategy()
	s.CommissionRate = 0
	s.MinCommission = 0
	s.TransferRate = 0
	s.SellStampRate = 0
	s.SlippageRate = 0
	s.TradeCash = 20000
	return s
}
