package strategy

import (
	"fmt"
	"testing"

	"stock-watcher/internal/config"
)

func TestCohortUsesFixedSlotsAndExcludesUnknownDates(t *testing.T) {
	p := config.DefaultStrategy()
	p.MaxHoldingDays = 5
	trades := []ExperimentTrade{
		{ID: "2026-01-05/a", Code: "sz000001", Date: "2026-01-05", Rank: 1, Selected: true, Outcome: Outcome{Status: "timeout", EntryDate: "2026-01-06", NetPct: 5, HoldingDays: 5}},
		{ID: "2026-01-05/b", Code: "sz000002", Date: "2026-01-05", Rank: 2, Selected: false, Outcome: Outcome{Status: "signal_filtered"}},
		{ID: "2026-01-06/a", Code: "sz000001", Date: "2026-01-06", Rank: 1, Selected: true, Outcome: Outcome{Status: "entry_path_unknown"}},
	}
	got := SummarizeExperimentCohorts(trades, p, 2)
	if got.SignalDates != 1 || got.PlannedSlots != 2 || got.FilledSlots != 1 || got.IncompleteDates != 1 {
		t.Fatalf("unexpected cohort counts: %+v", got)
	}
	if got.MeanNet != 2.5 {
		t.Fatalf("empty slot must stay cash, got mean %v", got.MeanNet)
	}
	if got.Bootstrap.Iterations != BootstrapIterations || got.Bootstrap.Seed != BootstrapSeed || got.Bootstrap.BlockLength != p.MaxHoldingDays+2 {
		t.Fatalf("bootstrap is not reproducible metadata: %+v", got.Bootstrap)
	}
	if again := SummarizeExperimentCohorts(trades, p, 2); again.MeanNet != got.MeanNet || again.Bootstrap.MeanNetLower != got.Bootstrap.MeanNetLower {
		t.Fatal("bootstrap changed across identical runs")
	}
}

func TestCohortBaselineUsesPairedSignalDates(t *testing.T) {
	p := config.DefaultStrategy()
	p.MaxHoldingDays = 1
	base := []ExperimentTrade{
		{Code: "a", Date: "2026-01-05", Rank: 1, Selected: true, Outcome: Outcome{Status: "timeout", EntryDate: "2026-01-06", NetPct: 1}},
		{Code: "a", Date: "2026-01-06", Rank: 1, Selected: true, Outcome: Outcome{Status: "timeout", EntryDate: "2026-01-07", NetPct: 1}},
	}
	better := []ExperimentTrade{
		{Code: "a", Date: "2026-01-05", Rank: 1, Selected: true, Outcome: Outcome{Status: "timeout", EntryDate: "2026-01-06", NetPct: 3}},
		{Code: "a", Date: "2026-01-06", Rank: 1, Selected: true, Outcome: Outcome{Status: "timeout", EntryDate: "2026-01-07", NetPct: 3}},
	}
	baseline := SummarizeExperimentCohorts(base, p, 1)
	got := SummarizeExperimentCohorts(better, p, 1).WithBaseline(baseline)
	if got.MeanNetIncrement != 2 || got.BaselineMeanNet != 1 {
		t.Fatalf("paired increment not computed: %+v", got)
	}
	if got.IncrementBootstrap == nil || got.IncrementBootstrap.MeanNetLower != 2 {
		t.Fatalf("increment bootstrap not aligned: %+v", got.IncrementBootstrap)
	}
	if got.PairedDates != 2 {
		t.Fatalf("paired date coverage was not recorded: %+v", got)
	}
}

func TestCohortGateRejectsSparseBaselineOverlap(t *testing.T) {
	p := config.DefaultStrategy()
	p.MaxHoldingDays = 1
	trades := []ExperimentTrade{}
	for day := 0; day < 30; day++ {
		date := "2026-01-" + fmt.Sprintf("%02d", day+1)
		trades = append(trades, ExperimentTrade{Code: "a", Date: date, Rank: 1, Selected: true, Outcome: Outcome{Status: "timeout", EntryDate: date, NetPct: 1}})
	}
	rule := SummarizeExperimentCohorts(trades, p, 1)
	baseline := SummarizeExperimentCohorts(trades[:2], p, 1)
	got := rule.WithBaseline(baseline)
	if got.PairedDates != 2 {
		t.Fatalf("unexpected paired dates: %d", got.PairedDates)
	}
	found := false
	for _, failure := range EvidenceFailures(Stats{Cohort: got, Samples: 30, Dates: 30, Resolved: 30, MeanNet: 1, HitRate: .5}, Stats{HitRate: .1}, p) {
		found = found || failure.Code == "baseline_signal_dates"
	}
	if !found {
		t.Fatal("sparse paired baseline overlap passed the gate")
	}
}

func TestLabelsMaturePerHorizon(t *testing.T) {
	p := config.DefaultStrategy()
	p.MaxHoldingDays = 5
	samples := []Sample{{
		Date:      "2026-01-02",
		LabelEnd:  "2026-01-09",
		LabelEnds: map[string]string{key(3, 1): "2026-01-05", key(3, 3): "2026-01-07", key(3, 5): "2026-01-09"},
		Rules:     []string{"baseline"},
		Outcomes: map[string]Outcome{
			key(3, 1): {Status: "timeout", EntryDate: "2026-01-05", NetPct: 1},
			key(3, 3): {Status: "timeout", EntryDate: "2026-01-05", NetPct: 1},
			key(3, 5): {Status: "timeout", EntryDate: "2026-01-05", NetPct: 1},
		},
	}}
	stats := statsFor(samples, "2026-01-06", p)
	if find(stats, "baseline", 3, 1).Samples != 1 {
		t.Fatal("mature one-day label was purged with the longest horizon")
	}
	if find(stats, "baseline", 3, 3).Samples != 0 || find(stats, "baseline", 3, 5).Samples != 0 {
		t.Fatal("immature longer-horizon labels leaked into training")
	}
}
