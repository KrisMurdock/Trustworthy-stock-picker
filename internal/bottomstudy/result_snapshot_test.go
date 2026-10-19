package bottomstudy

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"testing"
)

func TestSpeedSnapshotPreservesUnfavorableControlAndUnknownMetrics(t *testing.T) {
	stats := SpeedStats{ComparisonStats: ComparisonStats{TradeStats: TradeStats{Samples: 20, Entries: 20, Resolved: 19, Unresolved: 1, Targets: 15, Rate: .75, MeanDays: 7, WorstNet: -8.2}}, SuccessDays: 4.5, PenalizedDays: 8}
	p := SpeedPair{ID: "chosen", Policy: TradePolicy{Rule: "macd_contraction", Target: 3, Horizon: 20, Batches: 2}, Confirmation: "none", Stock: SpeedArm{Training: stats, Later: stats}, Combined: SpeedArm{Training: stats, Later: stats}}
	r := SpeedReport{Winners: []SpeedWinner{{ID: "chosen"}}, Pairs: []SpeedPair{p, {ID: "not chosen"}}}
	record := experiment.Record{ID: "run", Engine: SpeedEngine, Data: experiment.DataSummary{ID: "data"}}
	s := SpeedResultSnapshot(r, record)
	if len(s.Rows) != 4 {
		t.Fatal("lost matched A/B or splits")
	}
	if err := s.Validate(record); err != nil {
		t.Fatal(err)
	}
	for _, x := range s.Rows {
		if *x.TargetDays != 4.5 || *x.MaxLoss != 8.2 || x.MaxAdverse != nil || *x.Stop != 0 || x.Samples != 20 || x.Unresolved != 1 || *x.PenalizedDays != 8 {
			t.Fatal("speed metrics mistranslated", x)
		}
	}
}
