package strategy

import "testing"

func TestHighTouchSucceedsDespiteCloseBelowTarget(t *testing.T) {
	s, cal, signal, future := tradingFixture()
	s.SlippageRate = 0
	p, _ := NewPlan(s, "sz000001", 10)
	future[1].High = p.Target30 // A touch is not a proven limit-order fill.
	out, err := Simulate(s, cal, "sz000001", signal, future, 1)
	if err != nil || out.Status != "timeout" || out.NetPct >= 0 || out.HighTouch == nil || out.HighTouch.Day25 != 1 || out.HighTouch.Day30 != 1 {
		t.Fatalf("daily-high opportunity was confused with close/exit profit: %+v %v", out, err)
	}
	s.SlippageRate = .01
	out, err = Simulate(s, cal, "sz000001", signal, future, 1)
	if err != nil || out.HighTouch == nil || out.HighTouch.Day30 != 0 {
		t.Fatal("costs and slippage were omitted from high-price opportunity")
	}
}

func TestHighTouchWindowAndAmbiguousExit(t *testing.T) {
	s, cal, signal, future := tradingFixture()
	out, err := Simulate(s, cal, "sz000001", signal, future, 1)
	if err != nil || out.HighTouch == nil || out.HighTouch.Day25 != 0 {
		t.Fatal("unsellable buy-day high counted as opportunity")
	}
	future[1].Low = 9.5
	future[2].High = 11
	out, err = Simulate(s, cal, "sz000001", signal, future, 5)
	if err != nil || out.Status != "stop" || out.HighTouch.Day25 != 0 || out.HighTouch.ObservedDays != 1 {
		t.Fatal("high after exit counted in holding window")
	}
	future[1].High = 11
	out, err = Simulate(s, cal, "sz000001", signal, future, 5)
	if err != nil || out.Status != "stop" || out.HighTouch.Day30 != 1 || !out.HighTouch.Day25Unknown || !out.HighTouch.Day30Unknown || !out.HighTouch.Ambiguous || out.NetPct >= 0 {
		t.Fatal("ambiguous daily-high touch hid conservative stop execution")
	}
	future[1].Open, future[1].Low = 9.5, 9.4
	out, err = Simulate(s, cal, "sz000001", signal, future, 5)
	if err != nil || out.Status != "stop_gap" || out.HighTouch.Day25 != 0 || !BreachesLossLimit(out.NetPct) {
		t.Fatal("rebound after known opening exit counted as held opportunity")
	}
}

func TestHighTouchRatesAndSpeedDoNotUseFinalProfit(t *testing.T) {
	s := Stats{}
	for _, out := range []Outcome{
		{Status: "timeout", EntryDate: "2026-01-05", NetPct: -1, HighTouch: &HighTouch{Version: 1, ObservedDays: 3, Day25: 2, Day30: 2}},
		{Status: "stop", EntryDate: "2026-01-05", NetPct: -2.9, HighTouch: &HighTouch{Version: 1, ObservedDays: 1, Day25: 1, Day25Unknown: true, Ambiguous: true}},
		{Status: "timeout", EntryDate: "2026-01-05", NetPct: -1}, // Legacy: unknown, not a miss.
		{Status: "corporate_action", EntryDate: "2026-01-05", HighTouch: &HighTouch{Version: 1, ObservedDays: 1, Day25: 1, Day30: 1}},
	} {
		add(&s, Sample{Date: "2026-01-02"}, out)
	}
	s = finalize(s)
	if s.HighTouch25Rate != .25 || s.HighTouch30Rate != .25 || s.HighTouch25Days != 2 || s.HighTouch30Days != 2 || s.HighTouchUnknown != 2 || s.HighTouchAmbiguous != 1 || s.Profit25 != 0 {
		t.Fatalf("invalid high-touch statistics: %+v", s)
	}
}
