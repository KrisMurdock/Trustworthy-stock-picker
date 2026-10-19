package strategy

import (
	"context"
	"math"
	"stock-watcher/internal/calendar"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"testing"
	"time"
)

func researchFixture() (config.Config, []market.History, []market.History) {
	c := config.Defaults()
	c.Strategy.Enabled = true
	c.Selection.Enabled = true
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试"}}
	bars := []market.Bar{}
	date, _ := time.Parse("2006-01-02", "2026-01-01")
	for len(bars) < 190 {
		if date.Weekday() != time.Saturday && date.Weekday() != time.Sunday {
			i := float64(len(bars))
			p := 10 + i*0.02 + 0.1*math.Sin(i)
			bars = append(bars, market.Bar{Date: date.Format("2006-01-02"), Open: p, Close: p + 0.02, High: p + 0.15, Low: p - 0.1, Volume: 1000})
		}
		date = date.AddDate(0, 0, 1)
	}
	h := market.History{Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Adjust: "qfq", DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: bars[len(bars)-1].Date, FetchedAt: time.Now(), Bars: bars}
	r := h
	r.Adjust = "none"
	r.DataField = "day"
	r.Bars = append([]market.Bar(nil), bars...)
	return c, []market.History{h}, []market.History{r}
}
func TestPurgingFrozenHoldoutAndNoFutureFeatureInput(t *testing.T) {
	c, h, raw := researchFixture()
	x, err := Analyze(context.Background(), c, h, raw, h[0].AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Ranking) != 1 || x.Ranking[0].Rank != 1 || x.Research.Holdout.Method != "frozen_holdout" || x.Research.Holdout.SignalDates != 40 {
		t.Fatalf("missing whole-pool rank or untouched holdout %+v", x.Research.Holdout)
	}
	if x.Research.TrainingThrough >= h[0].AsOf {
		t.Fatal("unfinished label used as evidence")
	}
	if x.Research.ValidationPassed || x.Ranking[0].Recommended {
		t.Fatal("failed holdout enabled recommendations")
	}
	prior, _ := Extract(h[0].Bars[:100])
	h[0].Bars[150].Close *= 2
	again, _ := Extract(h[0].Bars[:100])
	if prior != again {
		t.Fatal("future close changed past features")
	}
	if !hasFlag(x.Research.Flags, "historical_st_unknown") {
		t.Fatal("historical ST limitation was not exposed")
	}
	samples := []Sample{{Date: "2026-03-01", LabelEnd: "2026-03-10", Rules: []string{"baseline"}, Outcomes: map[string]Outcome{key(3, 5): {Status: "target", EntryDate: "2026-03-02", NetPct: 3, HoldingDays: 2}}}}
	if find(statsFor(samples, "2026-03-10", c.Strategy), "baseline", 3, 5).Samples != 0 {
		t.Fatal("boundary labels were not purged")
	}
	if find(statsFor(samples, "2026-03-11", c.Strategy), "baseline", 3, 5).Targets != 1 {
		t.Fatal("completed label missing")
	}
}

func TestCalendarCoverageRejectsBarOnNonTradingDay(t *testing.T) {
	c, h, raw := researchFixture()
	target := h[0].Bars[100].Date
	tradingDays := []string{}
	for _, bar := range h[0].Bars {
		if bar.Date != target {
			tradingDays = append(tradingDays, bar.Date)
		}
	}
	c.Calendar.TradingDays = tradingDays
	data, err := datasets(c, h, raw, h[0].AsOf)
	if err != nil {
		t.Fatal(err)
	}
	coverage := inspectCalendarCoverage(calendar.New(c), data)
	if coverage.Complete || len(coverage.Uncovered) == 0 || coverage.Uncovered[0] != target {
		t.Fatalf("bar on a non-trading day was ignored: %+v", coverage)
	}
	if err := requireCalendarCoverage(calendar.New(c), data); err == nil {
		t.Fatal("experiment accepted a calendar that omitted a saved bar date")
	}
}
func TestResearchMissingRawCancellationAndCorporateActions(t *testing.T) {
	c, h, raw := researchFixture()
	if _, err := Analyze(context.Background(), c, h, nil, h[0].AsOf); err == nil {
		t.Fatal("adjusted prices used as execution prices")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Analyze(ctx, c, h, raw, h[0].AsOf); err != context.Canceled {
		t.Fatal("analysis ignored cancellation")
	}
	for i := 120; i < len(raw[0].Bars); i++ {
		raw[0].Bars[i].Open *= 0.5
		raw[0].Bars[i].High *= 0.5
		raw[0].Bars[i].Low *= 0.5
		raw[0].Bars[i].Close *= 0.5
	}
	x, err := Analyze(context.Background(), c, h, raw, h[0].AsOf)
	if err != nil || x.Research.Skipped["corporate_action"] == 0 {
		t.Fatal("corporate action simulated as profit/loss")
	}
}

func TestShortHorizonsUseTheirOwnMaturityWindow(t *testing.T) {
	c, h, raw := researchFixture()
	x, err := Analyze(context.Background(), c, h, raw, h[0].AsOf)
	if err != nil {
		t.Fatal(err)
	}
	short := find(x.Research.Rules, "baseline", 2.5, 1)
	long := find(x.Research.Rules, "baseline", 2.5, 5)
	if short.Samples <= long.Samples {
		t.Fatalf("short labels were still gated by the longest horizon: short=%d long=%d", short.Samples, long.Samples)
	}
}

func TestMissingHorizonLabelIsNotCountedAsSkippedEntry(t *testing.T) {
	p := config.DefaultStrategy()
	p.MaxHoldingDays = 5
	samples := []Sample{{
		Code: "sz000001", Date: "2026-01-05",
		LabelEnds: map[string]string{key(3, 1): "2026-01-06"},
		Rules:     []string{"baseline"},
		Outcomes: map[string]Outcome{
			key(3, 1): {Status: "timeout", EntryDate: "2026-01-06", NetPct: 1, HoldingDays: 1},
		},
	}}
	short := find(statsFor(samples, "2026-02-01", p), "baseline", 3, 1)
	long := find(statsFor(samples, "2026-02-01", p), "baseline", 3, 5)
	if short.Samples != 1 || long.Samples != 0 || long.SkippedEntries != 0 {
		t.Fatalf("missing long label polluted statistics: short=%+v long=%+v", short, long)
	}
}

func TestEvidenceGatesAndDeterministicTies(t *testing.T) {
	s := config.DefaultStrategy()
	f := Features{ClosePosition: 0.9, Return1: 1, VolumeRatio: 1.4, MA20Distance: 2, MA20Slope: 1, ATR14Pct: 2}
	if rank("sz000001", "", f, nil, s).Recommended {
		t.Fatal("missing evidence generated recommendation")
	}
	stats := []Stats{{Rule: "baseline", Target: 3, Horizon: 5, HitRate: 0.2}, {Rule: "volume_recovery", Target: 3, Horizon: 5, Samples: 100, Dates: 60, MeanNet: 1, HitRate: 0.7, LowerBound: 0.57}}
	s.MinRankScore = 0
	if !rank("sz000001", "", f, stats, s).Recommended {
		t.Fatal("qualified rule cannot recommend")
	}
	f.ATR14Pct = 8
	if rank("sz000001", "", f, stats, s).Recommended {
		t.Fatal("risk gate not enforced")
	}
	ranked := []Candidate{{Code: "sz000002", Score: 50}, {Code: "sz000001", Score: 50}}
	sortRank(ranked)
	if ranked[0].Code != "sz000001" || ranked[1].Rank != 2 {
		t.Fatal("unstable cross-sectional tie")
	}
}

func TestFrozenEvaluationDoesNotLearnLaterHoldoutOutcomes(t *testing.T) {
	s := config.DefaultStrategy()
	s.MinEvidence, s.MinTrainingDays, s.MinRankScore = 10, 20, 0
	strong := Features{ClosePosition: 0.9, Return1: 1, VolumeRatio: 1.4, MA20Distance: 2, MA20Slope: 1, ATR14Pct: 2}
	strong.Metrics.Score = 100
	samples := []Sample{}
	date, _ := time.Parse("2006-01-02", "2026-01-05")
	for i := 0; i < 40; i++ {
		stamp := date.Format("2006-01-02")
		for _, code := range []string{"sz000001", "sz000002"} {
			f, out := strong, Outcome{Status: "target", EntryDate: stamp, NetPct: 3, HoldingDays: 1}
			if i >= 15 {
				out.Status, out.NetPct = "timeout", -0.2
			}
			if code == "sz000002" {
				f = Features{}
				out.Status, out.NetPct = "stop", -3
			}
			samples = append(samples, Sample{Code: code, Date: stamp, LabelEnd: date.AddDate(0, 0, 8).Format("2006-01-02"), Features: f, Rules: Matches(f), Outcomes: map[string]Outcome{key(3, 5): out}})
		}
		date = date.AddDate(0, 0, 1)
	}
	boundary := "2026-03-10"
	dates := []string{boundary, "2026-03-20"}
	for _, stamp := range dates {
		signal, _ := time.Parse("2006-01-02", stamp)
		samples = append(samples, Sample{Code: "sz000001", Date: stamp, LabelEnd: signal.AddDate(0, 0, 8).Format("2006-01-02"), Features: strong, Rules: Matches(strong), Outcomes: map[string]Outcome{key(3, 5): {Status: "stop", EntryDate: stamp, NetPct: -99, HoldingDays: 1}}})
	}
	before := evaluate(context.Background(), samples, dates, s, boundary, 1)
	if before.Recommended.Samples != 2 {
		t.Fatalf("holdout training changed after a later loss: %+v", before.Recommended)
	}
	policy := &developmentOptions{requireEvidence: true}
	strict := evaluateFiltered(context.Background(), samples, dates, s, boundary, 1, policy)
	if strict.TopRanked.Samples != 2 || strict.TopRanked.LossBreaches != 2 {
		t.Fatal("risk-qualified policy used future losses to remove historically qualified entries")
	}
	// Without frozen evidence this extreme first holdout loss would disable the rule.
	samples[len(samples)-2].Outcomes[key(3, 5)] = Outcome{Status: "target", EntryDate: boundary, NetPct: 3, HoldingDays: 1}
	after := evaluate(context.Background(), samples, dates, s, boundary, 1)
	if after.Recommended.Samples != before.Recommended.Samples || after.Recommended.Targets != before.Recommended.Targets+1 {
		t.Fatal("changing a test label changed held-out selection rather than just its result")
	}
}

func TestDevelopmentPurgesSamplesCrossingHoldoutBoundary(t *testing.T) {
	samples := []Sample{
		{Code: "a", Date: "2026-01-05", LabelEnds: map[string]string{key(3, 5): "2026-01-12"}},
		{Code: "b", Date: "2026-01-05", LabelEnds: map[string]string{key(3, 5): "2026-01-13"}},
		{Code: "c", Date: "2026-01-06", LabelEnds: map[string]string{key(3, 5): "2026-01-12"}},
	}
	dates := evaluationDates(samples, 3, 5, "2026-01-13", "", "")
	if len(dates) != 1 || dates[0] != "2026-01-06" {
		t.Fatalf("purged signal dates leaked into development: %+v", dates)
	}
}
