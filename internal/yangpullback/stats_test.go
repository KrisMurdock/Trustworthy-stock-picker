package yangpullback

import (
	"math"
	"testing"
)

func testTrade(code, date string, net float64, resolved, skipped bool, mfe float64) Trade {
	return Trade{
		Signal:       Signal{Code: code, Date: date},
		NetPct:       net,
		Resolved:     resolved,
		EntrySkipped: skipped,
		MFE:          mfe,
	}
}

func TestSummarizeTradesCoreStatistics(t *testing.T) {
	trades := []Trade{
		testTrade("sz000001", "2025-01-02", 5, true, false, 1),
		testTrade("sz000001", "2025-01-03", -2, true, false, -1),
		testTrade("sz000002", "2026-01-02", 0, true, false, 0),
		testTrade("sz000002", "2026-01-03", 0, false, false, 0),
	}
	got := summarizeTrades(trades, 0)
	if got.Signals != 4 || got.Resolved != 3 || got.Wins != 1 {
		t.Fatalf("counts = %+v", got)
	}
	if math.Abs(got.WinRate-0.25) > 1e-12 || math.Abs(got.WinRateResolved-1.0/3.0) > 1e-12 {
		t.Fatalf("win rates = %+v", got)
	}
	if math.Abs(got.MeanNet-1) > 1e-12 || math.Abs(got.MedianNet) > 1e-12 {
		t.Fatalf("mean/median = %+v", got)
	}
	if got.ProfitFactor == nil || math.Abs(*got.ProfitFactor-2.5) > 1e-12 {
		t.Fatalf("profit factor = %v", got.ProfitFactor)
	}
	if math.Abs(got.TouchRate-1.0/3.0) > 1e-12 {
		t.Fatalf("touch rate = %v", got.TouchRate)
	}
	if got.Unresolved != 1 || got.EntrySkipped != 0 || got.Dates != 4 || got.Stocks != 2 {
		t.Fatalf("partition counts = %+v", got)
	}
	if got.MaxStockShare != .5 || got.MaxYearShare != .5 {
		t.Fatalf("concentration = %+v", got)
	}
	if got.ByYear["2025"] != 2 || got.ByYear["2026"] != 2 {
		t.Fatalf("by year = %+v", got.ByYear)
	}
}

func TestSummarizeTradesEmptyAndUnresolved(t *testing.T) {
	empty := summarizeTrades(nil, 3)
	if empty.Signals != 0 || empty.ProfitFactor != nil || empty.ByYear == nil {
		t.Fatalf("empty stats = %+v", empty)
	}
	for _, value := range []float64{empty.WinRate, empty.WinRateResolved, empty.MeanNet, empty.MedianNet, empty.TouchRate, empty.MaxStockShare, empty.MaxYearShare} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			t.Fatalf("empty stats are not finite: %+v", empty)
		}
	}
	if empty.Purged != 3 {
		t.Fatalf("purged = %d", empty.Purged)
	}

	unresolved := summarizeTrades([]Trade{testTrade("sz000001", "2026-02-02", 99, false, false, 2)}, 0)
	if unresolved.Wins != 0 || unresolved.WinRate != 0 || unresolved.Resolved != 0 || unresolved.Unresolved != 1 {
		t.Fatalf("unresolved stats = %+v", unresolved)
	}
	if unresolved.ProfitFactor != nil || unresolved.MeanNet != 0 || unresolved.MedianNet != 0 {
		t.Fatalf("unresolved returns leaked into stats: %+v", unresolved)
	}
}

func TestSummarizeTradesNoLossProfitFactorIsNull(t *testing.T) {
	got := summarizeTrades([]Trade{
		testTrade("sz000001", "2026-03-02", 1, true, false, 1),
		testTrade("sz000002", "2026-03-02", 2, true, false, 1),
	}, 0)
	if got.ProfitFactor != nil {
		t.Fatalf("profit factor should be null without losses: %v", *got.ProfitFactor)
	}
}

func TestSummarizeTradesPriceTouchRate(t *testing.T) {
	trades := []Trade{
		{Signal: Signal{Code: "sz000001", Date: "2026-01-05"}, Resolved: true, PriceTouch: true, NetPct: -1},
		{Signal: Signal{Code: "sz000002", Date: "2026-01-06"}, Resolved: true, PriceTouch: false, NetPct: 1},
	}
	got := summarizeTrades(trades, 0)
	if math.Abs(got.PriceTouchRate-.5) > 1e-12 {
		t.Fatalf("price touch rate = %v", got.PriceTouchRate)
	}
}

func TestBootstrapComparisonDeterministicAndPaired(t *testing.T) {
	pattern := []Trade{
		testTrade("sz000001", "2026-01-05", 1, true, false, 1),
		testTrade("sz000002", "2026-01-06", 1, true, false, 1),
		testTrade("sz000003", "2026-01-07", 1, true, false, 1),
	}
	baseline := []Trade{
		testTrade("sz000001", "2026-01-05", -1, true, false, 0),
		testTrade("sz000002", "2026-01-06", -1, true, false, 0),
		testTrade("sz000003", "2026-01-07", -1, true, false, 0),
	}
	first := bootstrapComparison(pattern, baseline, 250, 17, "hold_only")
	again := bootstrapComparison(pattern, baseline, 250, 17, "hold_only")
	if first != again {
		t.Fatalf("bootstrap is not deterministic:\nfirst=%+v\nagain=%+v", first, again)
	}
	if !first.Available || first.WinRateLower != 1 || first.WinRateUpper != 1 {
		t.Fatalf("win-rate interval = %+v", first)
	}
	if !first.DifferenceAvailable || first.PairedDates != 3 || first.Difference != 1 || first.DifferenceLower != 1 || first.DifferenceUpper != 1 {
		t.Fatalf("difference interval = %+v", first)
	}
}

func TestBootstrapComparisonEmptyBaseline(t *testing.T) {
	got := bootstrapComparison([]Trade{testTrade("sz000001", "2026-01-05", 1, true, false, 1)}, nil, 100, 1, "hold_only")
	if !got.Available || got.DifferenceAvailable || got.DifferenceLower != 0 || got.DifferenceUpper != 0 {
		t.Fatalf("unpaired bootstrap = %+v", got)
	}
}
