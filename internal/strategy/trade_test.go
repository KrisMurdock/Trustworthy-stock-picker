package strategy

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"math"
	"testing"
)

func tradingFixture() (config.Strategy, *calendar.Calendar, market.Bar, []market.Bar) {
	c := config.Defaults()
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	signal := market.Bar{Date: "2026-10-12", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000}
	future := []market.Bar{
		{Date: "2026-10-13", Open: 10, High: 11, Low: 9.9, Close: 10, Volume: 1000},
		{Date: "2026-10-14", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000},
		{Date: "2026-10-15", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000},
		{Date: "2026-10-16", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000},
		{Date: "2026-10-19", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000},
		{Date: "2026-10-20", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000},
	}
	return c.Strategy, calendar.New(c), signal, future
}
func TestNetTargetsIncludeMinimumCommissionTaxAndTick(t *testing.T) {
	s := config.DefaultStrategy()
	p, err := NewPlan(s, "sz000001", 10)
	if err != nil {
		t.Fatal(err)
	}
	if p.Shares != 1900 || math.Abs(p.BuyCost-19005.19) > 1e-8 {
		t.Fatalf("bad inclusive budget %+v", p)
	}
	if p.Target25 <= 10.25 || p.Target30 <= 10.3 || netPct(s, p, p.Target25) < 2.5 || netPct(s, p, p.Target30) < 3 {
		t.Fatalf("targets do not include costs %+v", p)
	}
	if netPct(s, p, p.Target30-0.01) >= 3 {
		t.Fatal("target was not the minimum qualifying tick")
	}
	if BreachesLossLimit(netPct(s, p, tickDown(p.StopPrice*(1-s.SlippageRate)))) {
		t.Fatal("modeled stop breached the inclusive-cost loss constraint")
	}
	s.TradeCash = 10000
	if _, err := NewPlan(s, "sh688981", 100); err == nil {
		t.Fatal("STAR minimum 200 shares ignored")
	}
}
func TestBuyDayCannotTakeProfitAndDeadline(t *testing.T) {
	s, cal, signal, future := tradingFixture()
	out, err := Simulate(s, cal, "sz000001", signal, future, 5)
	if err != nil || out.Status != "timeout" || out.ExitDate != "2026-10-20" || out.HoldingDays != 5 || out.NetPct >= 0 {
		t.Fatalf("buy day target or deadline %+v %v", out, err)
	}
	first, err := Simulate(s, cal, "sz000001", signal, future, 1)
	if err != nil || first.ExitDate != "2026-10-14" {
		t.Fatal("one day horizon was not first sellable day")
	}
}
func TestStopWinsAmbiguousBarsAndGapsExceedStop(t *testing.T) {
	s, cal, signal, future := tradingFixture()
	future[1].High = 10.8
	future[1].Low = 9.5
	out, err := Simulate(s, cal, "sz000001", signal, future, 5)
	if err != nil || out.Status != "stop" || !out.Ambiguous || BreachesLossLimit(out.NetPct) {
		t.Fatalf("ambiguous order wrong %+v %v", out, err)
	}
	future[1].Open = 9.5
	future[1].High = 9.7
	future[1].Low = 9.3
	future[1].Close = 9.5
	out, err = Simulate(s, cal, "sz000001", signal, future, 5)
	if err != nil || out.Status != "stop_gap" || out.NetPct > -5 {
		t.Fatalf("gap filled at fictitious stop %+v %v", out, err)
	}
}

func TestStopTriggerReservesCostsSlippageAndTick(t *testing.T) {
	for _, fill := range []float64{1, 3.17, 10, 47.05, 100, 823.19} {
		for _, slip := range []float64{0, .0005, .01} {
			s := config.DefaultStrategy()
			s.TradeCash = 200000
			s.SlippageRate = slip
			for _, goal := range []float64{-2, -3, -10} {
				s.StopNetPct = goal
				p, err := NewPlan(s, "sz000001", fill)
				// At a 1-yuan price, two price ticks plus slippage leave
				// no room for a stricter 2% stop after fees.
				if fill == 1 && slip > 0 && goal == -2 {
					if err != ErrStopBuffer {
						t.Fatal("accepted an impossible below-entry stop", err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("fill=%f slip=%f goal=%f: %v", fill, slip, goal, err)
				}
				net := netPct(s, p, tickDown(p.StopPrice*(1-slip)))
				if BreachesLossLimit(net) || net <= math.Max(goal, -3)+1e-9 || p.StopNetPct != net || p.StopPrice >= p.EntryPrice {
					t.Fatalf("unsafe modeled stop: %+v net=%f", p, net)
				}
			}
		}
	}
	s := config.DefaultStrategy()
	s.CommissionRate, s.TransferRate, s.SellStampRate = .01, .01, .01
	if _, err := NewPlan(s, "sz000001", 10); err != ErrStopBuffer {
		t.Fatal("accepted fees that already consume the loss budget", err)
	}
}
func TestTargetTouchDoesNotProveFillAndLockedLimitCensors(t *testing.T) {
	s, cal, signal, future := tradingFixture()
	p, _ := NewPlan(s, "sz000001", 10.01)
	future[1].High = p.TakeProfit
	out, err := Simulate(s, cal, "sz000001", signal, future, 1)
	if err != nil || out.Status == "target" {
		t.Fatal("mere high touch filled limit")
	}
	future[1].High = p.TakeProfit + 0.01
	out, err = Simulate(s, cal, "sz000001", signal, future, 1)
	if err != nil || out.Status != "target" || out.NetPct < 3 {
		t.Fatalf("through-price target missing %+v %v", out, err)
	}
	future[1] = market.Bar{Date: "2026-10-14", Open: 9, High: 9, Low: 9, Close: 9, Volume: 100}
	out, err = Simulate(s, cal, "sz000001", signal, future, 1)
	if err != nil || out.Status != "unresolved" || out.ExitDate != "" {
		t.Fatal("locked limit down sold artificially")
	}
	future[1].Date = "2026-10-15"
	out, err = Simulate(s, cal, "sz000001", signal, future, 1)
	if err != nil || out.Status != "missing_exit_session" {
		t.Fatal("missing session counted as a trading day")
	}
}

func TestSimulateUsesExitTradingDayPriceLimit(t *testing.T) {
	c := config.Defaults()
	c.Calendar.ValidFrom = "2020-08-21"
	c.Calendar.ValidThrough = "2020-08-25"
	cal := calendar.New(c)
	signal := market.Bar{Date: "2020-08-21", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000}
	future := []market.Bar{
		{Date: "2020-08-24", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000},
		{Date: "2020-08-25", Open: 9, High: 9, Low: 9, Close: 9, Volume: 1000},
	}
	out, err := Simulate(c.Strategy, cal, "sz300001", signal, future, 1)
	if err != nil || out.Status != "stop_gap" {
		t.Fatalf("exit day used the wrong price-limit version: %+v %v", out, err)
	}
}
