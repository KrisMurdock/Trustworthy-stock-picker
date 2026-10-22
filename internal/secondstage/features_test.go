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

func TestJEVGroupStatsAverageTiedPercentiles(t *testing.T) {
	candidates := []firststage.Candidate{
		{Rank: 1, Code: "sh600001"},
		{Rank: 2, Code: "sh600002"},
		{Rank: 3, Code: "sh600003"},
	}
	stats := buildJEVGroupStats(candidates, map[string]JEVScore{
		"sh600001": {Adjacent15: 0.2},
		"sh600002": {Adjacent15: 0.8},
		"sh600003": {Adjacent15: 0.8},
	})
	if stats[0].Percentile["sh600001"] != 0 || stats[0].Percentile["sh600002"] != 0.75 || stats[0].Percentile["sh600003"] != 0.75 {
		t.Fatalf("tied JEV percentiles were not averaged: %+v", stats[0].Percentile)
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
	vector := featureVector(candidate, scores[candidate.Code], true, groups[0], 0, 1.5, FeatureVersion)
	if len(vector) != len(FeatureNames) {
		t.Fatalf("feature vector length %d != contract %d", len(vector), len(FeatureNames))
	}
	if vector[1] != 0 || vector[6] != 0.5 || vector[7] != 1 || vector[9] != 0.5 || vector[10] != 1 {
		t.Fatalf("unexpected JEV features: %+v", vector)
	}
}

func TestFeatureVectorV2AddsTeacherInteractions(t *testing.T) {
	candidate := firststage.Candidate{
		Rank:       1,
		Code:       "sh600001",
		BlendScore: 0.8,
		Features:   firststage.FeatureSnapshot{Return5: 2, ATR14Pct: 4},
	}
	scores := map[string]JEVScore{"sh600001": {Adjacent15: 0.4}}
	groups := buildJEVGroupStats([]firststage.Candidate{candidate}, scores)
	vector := featureVector(candidate, scores[candidate.Code], true, groups[0], 0.25, 1.5, FeatureVersionV2)
	if len(vector) != len(FeatureNamesV2) {
		t.Fatalf("v2 feature vector length %d != contract %d", len(vector), len(FeatureNamesV2))
	}
	if math.Abs(vector[27]+0.25) > 1e-12 || math.Abs(vector[28]-math.Sqrt(0.4)) > 1e-12 || math.Abs(vector[29]-8.0/13.0) > 1e-12 || vector[30] != 0.5 {
		t.Fatalf("unexpected v2 interactions: %+v", vector[27:])
	}

	missing := featureVector(candidate, JEVScore{}, false, groups[0], 0.25, 1.5, FeatureVersionV2)
	if missing[27] != 0 || missing[28] != 0 || missing[29] != 0 || missing[30] != 0.5 {
		t.Fatalf("missing JEV interactions were not neutral: %+v", missing[27:])
	}
}
