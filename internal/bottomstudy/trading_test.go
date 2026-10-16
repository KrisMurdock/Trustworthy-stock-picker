package bottomstudy

import (
	"math"
	"reflect"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"stock-watcher/internal/strategy"
	"testing"
)

func tradeFixture() (config.Strategy, TradePolicy, opportunity) {
	s := config.DefaultStrategy()
	s.CommissionRate, s.MinCommission, s.TransferRate, s.SellStampRate, s.SlippageRate = 0, 0, 0, 0, 0
	b := fixture(25).Bars
	for i := range b {
		b[i].Open, b[i].Close, b[i].High, b[i].Low = 10, 10, 10.1, 9.9
	}
	o := opportunity{id: "sh600036/2026-01-01", code: "sh600036", name: "样本", split: "training", bars: b, events: make([]bool, len(b)), invalid: map[int]string{}}
	o.events[0] = true
	return s, TradePolicy{Rule: "any", Batches: 1, Horizon: 5, Target: 3}, o
}

func TestFeesAndLotsMatchExistingSingleEntryPlan(t *testing.T) {
	s := config.DefaultStrategy()
	for _, code := range []string{"sh600036", "sh688001"} {
		var p position
		b := market.Bar{Date: "2026-01-05", Open: 12.34, High: 13, Low: 12, Close: 12.5}
		f, ok := p.buy(s, code, b, 1, s.TradeCash)
		if !ok {
			t.Fatal("buy failed")
		}
		plan, err := strategy.NewPlan(s, code, f.Price)
		if err != nil || f.Shares != plan.Shares || math.Abs(f.Cash-plan.BuyCost) > 1e-9 || p.price(s, 3, 12.34) != plan.Target30 {
			t.Fatalf("fee/lot/target drift: %+v %+v %v", f, plan, err)
		}
	}
}

func TestT1DisallowsBuyDayTargetAndLatchesStop(t *testing.T) {
	s, p, o := tradeFixture()
	o.bars[1].High = 11
	a := simulateTrade(s, p, o)
	if a.TargetHit || !a.TargetBlockedT1 || a.Status != "timeout" {
		t.Fatal(a)
	}
	p.Stop = 3
	o.bars[1].Low = 9.5
	o.bars[2].Open, o.bars[2].Close, o.bars[2].Low, o.bars[2].High = 9.4, 10.5, 9.3, 11
	a = simulateTrade(s, p, o)
	if a.TargetHit || !a.StopTriggered || !a.BlockedByT1 || a.ExitDate != o.bars[2].Date || a.NetPct > -5.9 || !a.StopExceeded {
		t.Fatalf("T+1 stop must exit at next available open, not the recovered high: %+v", a)
	}
	o.bars[2].Open, o.bars[2].Low = 10.5, 10.4
	a = simulateTrade(s, p, o)
	if !a.StopTriggered || !a.TargetHit || a.TargetDay != 1 || a.NetPct < 3 {
		t.Fatal("a delayed stop filled above the profit target is still a net success", a)
	}
}

func TestStopBeforeAddAndPartialT1Liquidation(t *testing.T) {
	s, p, o := tradeFixture()
	p.Batches, p.Stop = 3, 3
	o.events[1] = true
	o.bars[2].Open, o.bars[2].Low = 9.5, 9.4
	a := simulateTrade(s, p, o)
	if a.BatchesFilled != 1 || a.Status != "stop_gap" || a.HoldingDays != 1 {
		t.Fatal(a)
	}
	// Opening price is safe, but the post-add low triggers a stop. The old lot
	// sells now; today's added lot must wait. No further addition is allowed.
	o.bars[2].Open, o.bars[2].High, o.bars[2].Low, o.bars[2].Close = 10, 10.1, 9.5, 9.8
	o.events[2] = true
	a = simulateTrade(s, p, o)
	if a.BatchesFilled != 2 || !a.BlockedByT1 || a.ExitDate != o.bars[3].Date || len(a.Fills) != 4 {
		t.Fatalf("partial stop: %+v", a)
	}
	if a.Fills[2].Date != o.bars[2].Date || a.Fills[3].Date != o.bars[3].Date || a.Fills[2].Shares != a.Fills[0].Shares || a.Fills[3].Shares != a.Fills[1].Shares {
		t.Fatal("sold locked shares or lost inventory", a.Fills)
	}
}

func TestAmbiguousDailyBarStopsFirstAndLossBuffer(t *testing.T) {
	s, p, o := tradeFixture()
	p.Stop = 3
	o.bars[2].High, o.bars[2].Low = 10.5, 9.5
	a := simulateTrade(s, p, o)
	if a.Status != "stop" || a.TargetHit || !a.Ambiguous || a.NetPct <= -3 || a.StopExceeded {
		t.Fatal(a)
	}
	p.Stop = 0
	b := simulateTrade(s, p, o)
	if !b.TargetHit || b.TargetDay != 1 || b.NetPct < 3-1e-9 {
		t.Fatal(b)
	}
}

func TestBudgetDeadlineAndNoForcedAddition(t *testing.T) {
	s, p, o := tradeFixture()
	p.Batches, p.Horizon = 5, 3
	o.events[1], o.events[2], o.events[3] = true, true, true
	a := simulateTrade(s, p, o)
	if a.BatchesFilled != 3 || a.ExitDate != o.bars[4].Date || a.Invested > s.TradeCash || a.HoldingDays != 3 {
		t.Fatal(a)
	}
	_, p, o = tradeFixture()
	p.Batches = 5
	a = simulateTrade(s, p, o)
	if a.BatchesFilled != 1 || a.Invested > s.TradeCash/5 {
		t.Fatal(a)
	}
	// No minimum order: no delayed entry at some favorable later date.
	s.TradeCash = 1000
	a = simulateTrade(s, p, o)
	if a.EntryDate != "" || len(a.Fills) != 0 {
		t.Fatal(a)
	}
}

func TestMissingCorporateAndLockedExitStayUnknown(t *testing.T) {
	s, p, o := tradeFixture()
	for _, reason := range []string{"corporate_action", "missing_session"} {
		o.invalid[2] = reason
		a := simulateTrade(s, p, o)
		stats := tradeStats([]Trade{a}, "training", nil)
		if a.Status != reason || stats.Samples != 1 || stats.Unresolved != 1 || stats.Targets != 0 {
			t.Fatal(a, stats)
		}
	}
	delete(o.invalid, 2)
	p.Horizon = 1
	o.bars[2].Open, o.bars[2].Close, o.bars[2].High, o.bars[2].Low = 9, 9, 9, 9
	a := simulateTrade(s, p, o)
	if resolvedTrade(a) || a.ExitDate != "" {
		t.Fatal("fabricated locked-limit exit", a)
	}
}

func TestWideStopAndMatchedStatistics(t *testing.T) {
	s, p, o := tradeFixture()
	o.bars[2].Low = 9
	o.bars[3].High = 10.5
	a := simulateTrade(s, p, o)
	p.Stop = 20
	b := simulateTrade(s, p, o)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("nonbinding stop changed path", a, b)
	}
	p.Stop = 3
	if simulateTrade(s, p, o).TargetHit {
		t.Fatal("3% and 20% experimental stops must be distinct")
	}
	stats := tradeStats([]Trade{b}, "training", map[string]Trade{a.ID: a})
	if stats.Retention != 1 || stats.RetainedTargets != 1 || stats.LostTargets != 0 || stats.Targets != 1 {
		t.Fatal(stats)
	}
}

func TestSearchRankingCannotUseLaterPeriod(t *testing.T) {
	a := Trial{ID: "a", Train: TradeStats{Rate: .8, MeanDays: 3}, Validation: TradeStats{Rate: 0}}
	b := Trial{ID: "b", Train: TradeStats{Rate: .7, MeanDays: 1}, Validation: TradeStats{Rate: 1}}
	if !better(a, b) || better(b, a) {
		t.Fatal("selected on later-period outcome")
	}
	b.Train.Rate = .8
	if !better(b, a) {
		t.Fatal("equal success must prefer shorter actual holding")
	}
}

func TestConfluenceUsesDistinctFamiliesAndPastOnly(t *testing.T) {
	s := make([]Signal, 6)
	s[1].Events = []string{"rsi_persistence", "rsi_divergence"}
	if gridEvent(s, 1, "recent_confluence") {
		t.Fatal("same family counted twice")
	}
	s[3].Events = []string{"bollinger_reentry"}
	if !gridEvent(s, 3, "recent_confluence") || gridEvent(s, 1, "recent_confluence") {
		t.Fatal("wrong lookback or future leakage")
	}
	s[4].Events = []string{"macd_contraction"}
	if !gridEvent(s, 4, "macd_and_bollinger") || gridEvent(s, 5, "macd_and_bollinger") {
		t.Fatal("state without new event must not add a batch")
	}
}
