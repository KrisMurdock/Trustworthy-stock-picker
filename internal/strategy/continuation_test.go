package strategy

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

func TestContinuationStatsUseOnlyMatureUpDayLabels(t *testing.T) {
	s := config.DefaultStrategy()
	samples := []Sample{}
	start, _ := time.Parse("2006-01-02", "2026-01-01")
	for i := 0; i < 30; i++ {
		day := start.AddDate(0, 0, i)
		samples = append(samples, Sample{
			Date:     day.Format("2006-01-02"),
			Features: Features{Return1: 1},
			Rules:    []string{"baseline"},
			NextDay:  &NextDay{Date: day.AddDate(0, 0, 1).Format("2006-01-02"), Higher: i%2 == 0},
		})
	}
	for i := 0; i < 5; i++ {
		day := start.AddDate(0, 0, 40+i)
		samples = append(samples, Sample{
			Date:     day.Format("2006-01-02"),
			Features: Features{Return1: -1},
			Rules:    []string{"baseline"},
			NextDay:  &NextDay{Date: day.AddDate(0, 0, 1).Format("2006-01-02"), Higher: true},
		})
	}
	for i := 0; i < 5; i++ {
		day := start.AddDate(0, 0, 50+i)
		samples = append(samples, Sample{
			Date:     day.Format("2006-01-02"),
			Features: Features{Return1: 1},
			Rules:    []string{"baseline"},
			NextDay:  &NextDay{Date: "2026-03-01", Higher: true},
		})
	}

	stats := find(statsFor(samples, "2026-02-15", s), "baseline", 3, s.MaxHoldingDays)
	continuation := stats.Continuation
	if continuation == nil {
		t.Fatal("continuation stats were not attached")
	}
	if continuation.Samples != 30 || continuation.Hits != 15 || continuation.Dates != 30 || continuation.Status != "ready" {
		t.Fatalf("unexpected continuation evidence: %+v", continuation)
	}
	if math.Abs(continuation.Rate-0.5) > 1e-9 || continuation.Score != continuation.LowerBound*100 {
		t.Fatalf("unexpected continuation rate or score: %+v", continuation)
	}
	if continuation.LowerBound <= 0 || continuation.LowerBound >= continuation.Rate {
		t.Fatalf("lower bound should be conservative and positive: %+v", continuation)
	}
}

func TestRankAddsContinuationWithoutChangingOpportunityScore(t *testing.T) {
	s := config.DefaultStrategy()
	s.MinRankScore = 0
	f := Features{Return1: 1, ClosePosition: 0.9, VolumeRatio: 1.4, MA20Distance: 2, MA20Slope: 1, ATR14Pct: 2}
	f.Metrics.Score = 100
	continuation := newContinuationStats()
	continuation.Samples, continuation.Dates, continuation.Hits = 100, 60, 60
	finalizeContinuation(continuation)
	stats := []Stats{
		{Rule: "baseline", Target: 3, Horizon: 5, HitRate: 0.2, Continuation: newContinuationStats()},
		{Rule: "volume_recovery", Target: 3, Horizon: 5, Samples: 100, Dates: 60, MeanNet: 1, HitRate: 0.7, LowerBound: 0.57, WorstNet: -2, Continuation: continuation},
	}

	candidate := rank("sz000001", "测试", f, stats, s)
	base, _ := opportunityFormula(f)
	want := base*0.5 + 0.57*50
	if math.Abs(candidate.Score-want) > 1e-9 {
		t.Fatalf("continuation changed opportunity score: got %.6f want %.6f", candidate.Score, want)
	}
	if candidate.Continuation == nil || candidate.Continuation.Status != "ready" || candidate.Continuation.Rule != "volume_recovery" {
		t.Fatalf("continuation score not attached to the evidence rule: %+v", candidate.Continuation)
	}

	f.Return1 = 0
	downDay := rank("sz000001", "测试", f, stats, s)
	if downDay.Continuation == nil || downDay.Continuation.Status != "not_applicable" || downDay.Continuation.Score != 0 {
		t.Fatalf("down day should not receive a continuation score: %+v", downDay.Continuation)
	}
}

func TestAnalyzeBuildsContinuationDiagnostics(t *testing.T) {
	c, histories, raw := researchFixture()
	result, err := Analyze(context.Background(), c, histories, raw, histories[0].AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Ranking) != 1 || result.Ranking[0].Continuation == nil {
		t.Fatalf("continuation diagnostics missing from ranking: %+v", result.Ranking)
	}
	if result.Ranking[0].Continuation.Label != ContinuationLabel {
		t.Fatalf("unexpected continuation label: %+v", result.Ranking[0].Continuation)
	}
}

func TestContinuationTimelineMatchesStatsFor(t *testing.T) {
	s := config.DefaultStrategy()
	start, _ := time.Parse("2006-01-02", "2026-01-01")
	samples := []Sample{}
	for i := 0; i < 30; i++ {
		day := start.AddDate(0, 0, i)
		samples = append(samples, Sample{
			Date:     day.Format("2006-01-02"),
			Features: Features{Return1: 1},
			Rules:    []string{"baseline"},
			NextDay:  &NextDay{Date: day.AddDate(0, 0, 1).Format("2006-01-02"), Higher: i%3 != 0},
		})
	}
	cutoff := "2026-02-15"
	direct := find(statsFor(samples, cutoff, s), "baseline", 3, s.MaxHoldingDays).Continuation
	timeline := find(statsTimeline(samples, []string{cutoff}, s)[cutoff], "baseline", 3, s.MaxHoldingDays).Continuation
	if direct == nil || timeline == nil || *direct != *timeline {
		t.Fatalf("timeline continuation diverged from direct stats: direct=%+v timeline=%+v", direct, timeline)
	}
}
