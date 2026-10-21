package secondstage

import (
	"math"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
)

func TestJEVGroupStatsUseGroupRelativePercentile(t *testing.T) {
	candidates := make([]firststage.Candidate, 16)
	scores := map[string]JEVScore{}
	for index := range candidates {
		code := "sh6000" + string(rune('A'+index))
		candidates[index] = firststage.Candidate{Rank: index + 1, Code: code}
		scores[code] = JEVScore{Adjacent15: float64(index%15) / 10, Full15Top12: float64(index%15) / 20}
	}
	stats := buildJEVGroupStats(candidates, scores)
	first := stats[0]
	if first.Max != 1.4 || math.Abs(first.Percentile[candidates[0].Code]-0) > 1e-9 || math.Abs(first.Percentile[candidates[5].Code]-5.0/14.0) > 1e-9 || math.Abs(first.Percentile[candidates[14].Code]-1) > 1e-9 {
		t.Fatalf("unexpected first group stats: %+v", first)
	}
	second := stats[1]
	if second.Max != 0 || len(second.Percentile) != 1 || second.Percentile[candidates[15].Code] != 0.5 {
		t.Fatalf("unexpected second group stats: %+v", second)
	}
	if len(first.Top12Percentile) != 15 || len(second.Top12Percentile) != 0 {
		t.Fatalf("full-15 feature leaked outside first group: %+v %+v", first.Top12Percentile, second.Top12Percentile)
	}
}

func TestFeatureVectorMatchesFeatureContract(t *testing.T) {
	candidate := firststage.Candidate{
		Rank:       1,
		Code:       "sh600001",
		BlendScore: 0.8,
		BaseRank:   0.9,
		ModelRank:  0.7,
		BaseScore:  80,
		ModelScore: 0.6,
		Features:   firststage.FeatureSnapshot{Return5: 2},
	}
	scores := map[string]JEVScore{"sh600001": {Adjacent15: 0.4, Full15Top12: 0.3}}
	groups := buildJEVGroupStats([]firststage.Candidate{candidate}, scores)
	vector := featureVector(candidate, scores[candidate.Code], true, groups[0], 0, 1.5)
	if len(vector) != len(FeatureNames) {
		t.Fatalf("feature vector length %d != contract %d", len(vector), len(FeatureNames))
	}
	if vector[1] != 0 || vector[6] != 0.5 || vector[7] != 1 || vector[9] != 0.5 || vector[10] != 1 {
		t.Fatalf("unexpected JEV features: %+v", vector)
	}
}
