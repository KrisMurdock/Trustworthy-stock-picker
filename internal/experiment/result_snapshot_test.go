package experiment

import (
	"reflect"
	"stock-watcher/internal/config"
	"stock-watcher/internal/strategy"
	"testing"
)

func resultFixture() Record {
	c := config.Defaults()
	return Record{Version: 1, ID: "0123456789abcdef0123456789abcdef", Status: "completed", Engine: Engine, Data: DataSummary{ID: "data", Parameters: c.Strategy}, Spec: Spec{Title: "测试", Change: "固定规则"}, Result: &Results{Development: strategy.DevelopmentResult{Research: strategy.Research{Parameters: c.Strategy, Development: strategy.Evaluation{From: "2026-04-01", Through: "2026-06-30", TopRanked: strategy.Stats{Samples: 5, Resolved: 4, Unresolved: 1, HighTouchSamples: 4, HighTouch25: 2, HighTouch25Days: 1.5, HighTouch30: 1, HighTouch30Days: 2, MeanDays: 3, WorstNet: -8.21, WorstMAE: -12, MeanNet: -.2}}}}}}
}

func TestResultSnapshotFreezesOriginalMetrics(t *testing.T) {
	r := resultFixture()
	s := SnapshotResult(r)
	if err := s.Validate(r); err != nil {
		t.Fatal(err)
	}
	x := s.Rows[0]
	if *x.TargetDays != 1.5 || *x.HoldingDays != 3 || *x.MaxLoss != 8.21 || *x.MaxAdverse != 12 || *x.Rate != .4 || *x.Targets != 2 || x.Unresolved != 1 || *x.Stop <= 0 {
		t.Fatal("wrong metric scope", x)
	}
	r.Review = ReviewStats(strategy.Stats{WorstNet: -90, MeanDays: 90, HighTouch25Days: 90})
	if !reflect.DeepEqual(s, SnapshotResult(r)) {
		t.Fatal("current review changed original results")
	}
	s.Rows[0].MaxLoss = Value(99.0)
	if s.Validate(r) == nil {
		t.Fatal("tampered snapshot accepted")
	}
}

func TestResultSnapshotDistinguishesZeroUnknownAndIncomplete(t *testing.T) {
	r := resultFixture()
	x := &r.Result.Development.Research.Development.TopRanked
	x.HighTouch25, x.HighTouch25Days, x.WorstNet = 0, 0, 1
	s := SnapshotResult(r)
	if s.Rows[0].TargetDays != nil || *s.Rows[0].Targets != 0 || *s.Rows[0].Rate != 0 || *s.Rows[0].MaxLoss != 0 {
		t.Fatal("no target or no realized loss misrepresented")
	}
	x.HighTouchSamples = 0
	if s = SnapshotResult(r); s.Rows[0].Targets != nil || s.Rows[0].Rate != nil {
		t.Fatal("missing touch evidence claimed zero success")
	}
	*x = strategy.Stats{}
	s = SnapshotResult(r)
	if s.Rows[0].MaxLoss != nil || s.Rows[0].HoldingDays != nil || s.Rows[0].MeanNet != nil || s.Rows[0].TargetDays != nil {
		t.Fatal("empty data displayed zero metrics")
	}
	for _, status := range []string{"running", "failed", "canceled", "interrupted"} {
		r.Status = status
		if SnapshotResult(r) != nil {
			t.Fatal("incomplete experiment acquired results")
		}
	}
}
