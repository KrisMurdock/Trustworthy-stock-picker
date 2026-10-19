package strategy

import (
	"math"
	"reflect"
	"stock-watcher/internal/indicators"
	"testing"
)

func conditionalSelectionFixture() []conditionalSignal {
	signalIndicators := &SignalIndicators{Version: 1}
	return []conditionalSignal{
		// Excluded by guard because MA20Distance is above its 5% bound. Its high
		// five-day return deliberately skews the full-pool cross-sectional mean.
		{code: "sz000001", features: Features{Return5: 9, MA20Distance: 6, MA20Slope: 1, ClosePosition: .8, VolumeRatio: 1.4, ATR14Pct: 2, SignalIndicators: signalIndicators}},
		{code: "sz000002", features: Features{Return5: 1, MA20Slope: 1, ClosePosition: .8, VolumeRatio: 1.4, ATR14Pct: 2, SignalIndicators: signalIndicators}},
		{code: "sz000003", features: Features{Return5: 4, MA20Slope: 1, ClosePosition: .5, VolumeRatio: 1.4, ATR14Pct: 2, Metrics: indicators.Metrics{Score: 4}, SignalIndicators: signalIndicators}},
	}
}

func TestConditionalSelectionFiltersBeforeCrossSectionalScoring(t *testing.T) {
	group := conditionalSelectionFixture()
	full := scoreConditionalSignals(group)
	if len(full) != 3 || full[0].signal.code != "sz000001" || full[1].signal.code != "sz000002" || full[2].signal.code != "sz000003" {
		t.Fatalf("unexpected full-pool order: %+v", full)
	}
	if math.Abs(full[0].candidate.Features.Relative5-(9-14.0/3)) > 1e-10 || math.Abs(full[1].candidate.Features.Relative5-(1-14.0/3)) > 1e-10 {
		t.Fatalf("full-pool mean did not include every candidate: %+v", full)
	}

	selected, filtered, eligible := selectConditionalSignals(group, "guard", 2)
	if eligible != 2 || len(selected) != 2 || len(filtered) != 1 {
		t.Fatalf("unexpected eligible selection: selected=%d filtered=%d eligible=%d", len(selected), len(filtered), eligible)
	}
	if selected[0].signal.code != "sz000003" || selected[0].rank != 1 || math.Abs(selected[0].candidate.Features.Relative5-1.5) > 1e-10 {
		t.Fatalf("eligible pool was not rescored independently: %+v", selected[0])
	}
	if selected[1].signal.code != "sz000002" || selected[1].rank != 2 || math.Abs(selected[1].candidate.Features.Relative5+1.5) > 1e-10 {
		t.Fatalf("eligible pool rank was not retained: %+v", selected[1])
	}
	if filtered[0].signal.code != "sz000001" || filtered[0].rank != 1 || math.Abs(filtered[0].candidate.Features.Relative5-(9-14.0/3)) > 1e-10 {
		t.Fatalf("filtered original top N lost its audit evidence: %+v", filtered[0])
	}

	// Only the eligible pool supplies Top N. An eligible runner-up must not be
	// added when the highest-scoring full-pool candidate is filtered out.
	one, oneFiltered, oneEligible := selectConditionalSignals(group, "guard", 1)
	if oneEligible != 2 || len(one) != 1 || one[0].signal.code != "sz000003" || len(oneFiltered) != 1 || oneFiltered[0].signal.code != "sz000001" {
		t.Fatalf("Top N was backfilled or fetched from the wrong pool: selected=%+v filtered=%+v", one, oneFiltered)
	}
}

func TestEmptyConditionalRuleKeepsFullUniverseScoring(t *testing.T) {
	group := conditionalSelectionFixture()
	full := scoreConditionalSignals(group)
	selected, filtered, eligible := selectConditionalSignals(group, "", 2)
	if eligible != len(group) || len(filtered) != 0 {
		t.Fatalf("empty rule filtered candidates: eligible=%d filtered=%d", eligible, len(filtered))
	}
	if !reflect.DeepEqual(selected, full[:2]) {
		t.Fatalf("empty rule changed scoring or ranking:\nselected=%+v\nfull=%+v", selected, full[:2])
	}
}
