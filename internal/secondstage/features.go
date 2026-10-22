package secondstage

import (
	"math"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
)

const JEVGroupSize = 15

var FeatureNames = []string{
	"first_stage_blend",
	"first_stage_rank",
	"first_stage_base_rank",
	"first_stage_model_rank",
	"first_stage_base_score",
	"jev_adjacent15",
	"jev_adjacent15_pct",
	"jev_adjacent15_rel",
	"jev_full15_top12",
	"jev_full15_top12_pct",
	"jev_present",
	"jev_group_max",
	"return1",
	"return5",
	"return10",
	"relative_return5",
	"ma20_slope",
	"ma20_distance",
	"atr14_pct",
	"rsi14",
	"volume_ratio",
	"close_position",
	"upper_wick",
	"high20_distance",
	"low20_distance",
	"touch_rate20",
	"pool_return5",
}

// FeatureNamesV2 keeps the frozen v1 prefix and appends teacher-interaction
// features. The order must match the offline trainer byte for byte.
var FeatureNamesV2 = append(append([]string(nil), FeatureNames...),
	"rank_jump_adjacent15",
	"geo_blend_jev",
	"harmonic_blend_jev",
	"return_to_atr_ratio",
)

type jevGroupStats struct {
	Max             float64
	Percentile      map[string]float64
	Top12Percentile map[string]float64
}

func buildJEVGroupStats(candidates []firststage.Candidate, scores map[string]JEVScore) map[int]jevGroupStats {
	groups := map[int][]firststage.Candidate{}
	for index, candidate := range candidates {
		group := index / JEVGroupSize
		if candidate.Rank > 0 {
			group = (candidate.Rank - 1) / JEVGroupSize
		}
		groups[group] = append(groups[group], candidate)
	}
	stats := make(map[int]jevGroupStats, len(groups))
	for group, members := range groups {
		values := make([]float64, 0, len(members))
		for _, candidate := range members {
			if score, ok := scores[candidate.Code]; ok {
				values = append(values, score.Adjacent15)
			}
		}
		item := jevGroupStats{Percentile: map[string]float64{}}
		item.Max = maxScore(values)
		for code, percentile := range scorePercentiles(members, scores, func(score JEVScore) float64 { return score.Adjacent15 }) {
			item.Percentile[code] = percentile
		}
		if group == 0 {
			item.Top12Percentile = scorePercentiles(members, scores, func(score JEVScore) float64 { return score.Full15Top12 })
		}
		stats[group] = item
	}
	return stats
}

func maxScore(values []float64) float64 {
	maximum := 0.0
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}

func scorePercentiles(members []firststage.Candidate, scores map[string]JEVScore, value func(JEVScore) float64) map[string]float64 {
	values := make(map[string]float64, len(members))
	for _, candidate := range members {
		if score, ok := scores[candidate.Code]; ok {
			values[candidate.Code] = value(score)
		}
	}
	return percentilesByCode(values)
}

// percentilesByCode ranks higher scores higher and assigns the midpoint
// percentile to tied rows. The tie handling matches the offline evaluation so
// production does not silently break equal first-stage or model scores.
func percentilesByCode(scores map[string]float64) map[string]float64 {
	type row struct {
		code  string
		score float64
	}
	rows := make([]row, 0, len(scores))
	for code, score := range scores {
		rows = append(rows, row{code: code, score: score})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].score != rows[j].score {
			return rows[i].score < rows[j].score
		}
		return rows[i].code < rows[j].code
	})
	out := make(map[string]float64, len(rows))
	denominator := len(rows) - 1
	if denominator < 1 {
		denominator = 1
	}
	for index := 0; index < len(rows); {
		end := index
		for end+1 < len(rows) && rows[end+1].score == rows[index].score {
			end++
		}
		percentile := 0.5
		if len(rows) > 1 {
			percentile = (float64(index) + float64(end)) / 2 / float64(denominator)
		}
		for tied := index; tied <= end; tied++ {
			out[rows[tied].code] = percentile
		}
		index = end + 1
	}
	return out
}

func featureVector(candidate firststage.Candidate, jev JEVScore, jevPresent bool, group jevGroupStats, rankPosition, poolReturn5 float64, featureVersion int) []float64 {
	present := 0.0
	if jevPresent {
		present = 1
	}
	percentile := group.Percentile[candidate.Code]
	top12Percentile := group.Top12Percentile[candidate.Code]
	relative := 0.0
	if group.Max > 0 {
		relative = jev.Adjacent15 / group.Max
	}
	f := candidate.Features
	vector := []float64{
		candidate.BlendScore,
		rankPosition,
		candidate.BaseRank,
		candidate.ModelRank,
		candidate.BaseScore,
		jev.Adjacent15,
		percentile,
		relative,
		jev.Full15Top12,
		top12Percentile,
		present,
		group.Max,
		f.Return1,
		f.Return5,
		f.Return10,
		f.RelativeReturn5,
		f.MA20Slope,
		f.MA20Distance,
		f.ATR14Pct,
		f.RSI14,
		f.VolumeRatio,
		f.ClosePosition,
		f.UpperWick,
		f.High20Distance,
		f.Low20Distance,
		f.TouchRate20,
		poolReturn5,
	}
	if featureVersion != FeatureVersionV2 {
		return vector
	}
	blend := clamp01(candidate.BlendScore)
	rankJump := 0.0
	geo := 0.0
	harmonic := 0.0
	if jevPresent {
		teacher := clamp01(percentile)
		rankJump = teacher - (1 - rankPosition)
		geo = math.Sqrt(blend * teacher)
		harmonic = harmonicMean(blend, teacher)
	}
	return append(vector,
		rankJump,
		geo,
		harmonic,
		f.Return5/math.Max(f.ATR14Pct, 0.5),
	)
}

func featureNamesForVersion(version int) ([]string, bool) {
	switch version {
	case FeatureVersionV1:
		return FeatureNames, true
	case FeatureVersionV2:
		return FeatureNamesV2, true
	default:
		return nil, false
	}
}

func clamp01(value float64) float64 {
	return math.Max(0, math.Min(1, value))
}

func harmonicMean(left, right float64) float64 {
	if left <= 0 || right <= 0 {
		return 0
	}
	return 2 / (1/left + 1/right)
}
