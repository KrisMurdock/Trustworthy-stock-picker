package strategy

import (
	"strings"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/indicators"
)

func TestOpportunityFormulaIsReproducible(t *testing.T) {
	f := Features{
		Metrics:       indicators.Metrics{Score: 80},
		MA20Slope:     1,
		Relative5:     1,
		ClosePosition: .8,
		VolumeRatio:   1.5,
		ATR14Pct:      2,
		UpperWick:     .1,
	}
	value, formula := opportunityFormula(f)
	if value != 95 {
		t.Fatalf("opportunity = %.1f, want 95", value)
	}
	for _, want := range []string{"日线80×0.25", "MA20↑", "强于池", "收盘强", "量能适中", "波动适中", "= 95.0"} {
		if !strings.Contains(formula, want) {
			t.Errorf("formula missing %q: %s", want, formula)
		}
	}
}

func TestRankScoreBreakdownExplainsEvidenceBlend(t *testing.T) {
	s := config.DefaultStrategy()
	f := Features{
		Metrics:       indicators.Metrics{Score: 80, MA5: 10, MA10: 9, MA20: 8, RSI14: 60},
		MA20Slope:     1,
		Relative5:     1,
		ClosePosition: .8,
		VolumeRatio:   1.5,
		ATR14Pct:      2,
		Return5:       5,
	}
	stats := []Stats{
		{Rule: "baseline", Target: 3, Horizon: 5, HitRate: .5, MeanNet: .5},
		{Rule: "momentum", Target: 3, Horizon: 5, Samples: 100, Dates: 60, Resolved: 100, MeanNet: 1, HitRate: .7, WorstNet: -1, LowerBound: .6},
	}
	candidate := rank("sz000001", "测试", f, stats, s)
	if candidate.Evidence == nil || candidate.Evidence.Rule != "momentum" {
		t.Fatalf("expected momentum evidence, got %+v", candidate.Evidence)
	}
	if candidate.ScoreBreakdown == nil || candidate.ScoreBreakdown.Final != candidate.Score {
		t.Fatalf("score breakdown does not match final score: %+v score=%.1f", candidate.ScoreBreakdown, candidate.Score)
	}
	if !strings.Contains(candidate.ScoreBreakdown.Formula, "证据融合") {
		t.Fatalf("formula does not explain evidence blend: %s", candidate.ScoreBreakdown.Formula)
	}
}
