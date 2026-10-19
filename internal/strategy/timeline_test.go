package strategy

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"stock-watcher/internal/config"
)

func TestStatsTimelineMatchesStatsFor(t *testing.T) {
	p := config.DefaultStrategy()
	p.MaxHoldingDays = 5

	outcome := func(entry, status string, net float64, days int) Outcome {
		return Outcome{Status: status, EntryDate: entry, NetPct: net, HoldingDays: days}
	}
	samples := []Sample{
		{
			Code: "sz000001", Date: "2026-01-02", LabelEnd: "2026-01-09",
			LabelEnds: map[string]string{
				key(2.5, 1): "2026-01-05", key(2.5, 3): "2026-01-07", key(2.5, 5): "2026-01-08",
				key(3, 1): "2026-01-05", key(3, 3): "2026-01-07", key(3, 5): "2026-01-09",
			},
			Rules: []string{"baseline", "breakout"},
			Outcomes: map[string]Outcome{
				key(2.5, 1): outcome("2026-01-05", "target", 2.5, 1),
				key(2.5, 3): outcome("2026-01-05", "timeout", 1, 3),
				key(2.5, 5): outcome("2026-01-05", "timeout", 2, 5),
				key(3, 1):   outcome("2026-01-05", "target", 3, 1),
				key(3, 3):   outcome("2026-01-05", "timeout", 1, 3),
				key(3, 5):   outcome("2026-01-05", "timeout", 2, 5),
			},
		},
		{
			Code: "sz000002", Date: "2026-01-05", LabelEnd: "2026-01-12",
			LabelEnds: map[string]string{
				key(2.5, 1): "2026-01-06", key(2.5, 3): "2026-01-08", key(2.5, 5): "2026-01-09",
				key(3, 1): "2026-01-06", key(3, 3): "2026-01-08", key(3, 5): "2026-01-12",
			},
			Rules: []string{"baseline", "pullback"},
			Outcomes: map[string]Outcome{
				key(2.5, 1): outcome("2026-01-06", "stop", -1, 1),
				key(2.5, 3): outcome("2026-01-06", "timeout", 1, 3),
				key(2.5, 5): outcome("2026-01-06", "target", 2.5, 4),
				key(3, 1):   outcome("2026-01-06", "stop", -1, 1),
				key(3, 3):   outcome("2026-01-06", "timeout", 1, 3),
				key(3, 5):   outcome("2026-01-06", "target", 3, 4),
			},
		},
		{
			Code: "sz000003", Date: "2026-01-06", LabelEnd: "2026-01-13",
			LabelEnds: map[string]string{
				key(3, 1): "2026-01-07", key(3, 3): "2026-01-09", key(3, 5): "2026-01-13",
			},
			Rules: []string{"baseline", "momentum"},
			Outcomes: map[string]Outcome{
				key(3, 1): outcome("", "entry_path_unknown", 0, 0),
				key(3, 3): outcome("2026-01-07", "timeout", 0.5, 3),
				key(3, 5): outcome("2026-01-07", "timeout", 1.5, 5),
			},
		},
	}
	dates := []string{"2026-01-10", "2026-01-03", "2026-01-05", "2026-01-08", "2026-01-03", "2026-01-09", "2026-01-06", "2026-01-13", "2026-01-14"}
	timeline := statsTimeline(samples, dates, p)
	for _, date := range dates {
		want, err := json.Marshal(statsFor(samples, date, p))
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(timeline[date])
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("timeline mismatch at %s\n got: %s\nwant: %s", date, got, want)
		}
	}
}

func BenchmarkStatsTimeline(b *testing.B) {
	p := config.DefaultStrategy()
	p.MaxHoldingDays = 5
	samples, dates := timelineBenchmarkFixture()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = statsTimeline(samples, dates, p)
	}
}

func BenchmarkStatsForAllDates(b *testing.B) {
	p := config.DefaultStrategy()
	p.MaxHoldingDays = 5
	samples, dates := timelineBenchmarkFixture()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, date := range dates {
			_ = statsFor(samples, date, p)
		}
	}
}

func timelineBenchmarkFixture() ([]Sample, []string) {
	samples := []Sample{}
	dates := []string{}
	start := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	for day := 0; day < 120; day++ {
		date := start.AddDate(0, 0, day)
		stamp := date.Format("2006-01-02")
		dates = append(dates, stamp)
		for code := 0; code < 20; code++ {
			outcomes := map[string]Outcome{}
			labelEnds := map[string]string{}
			for _, target := range []float64{2.5, 3} {
				for _, horizon := range []int{1, 3, 5} {
					end := date.AddDate(0, 0, horizon+1).Format("2006-01-02")
					labelEnds[key(target, horizon)] = end
					status, net := "timeout", float64((code+day+horizon)%7-2)
					if net >= target {
						status = "target"
					}
					outcomes[key(target, horizon)] = Outcome{Status: status, EntryDate: date.AddDate(0, 0, 1).Format("2006-01-02"), NetPct: net, HoldingDays: horizon}
				}
			}
			samples = append(samples, Sample{
				Code: fmt.Sprintf("sz%06d", code+1), Date: stamp,
				LabelEnd: date.AddDate(0, 0, 6).Format("2006-01-02"), LabelEnds: labelEnds,
				Rules: []string{"baseline", "breakout"}, Outcomes: outcomes,
			})
		}
	}
	return samples, dates
}
