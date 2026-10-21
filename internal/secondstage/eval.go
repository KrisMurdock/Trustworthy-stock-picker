package secondstage

import (
	"errors"
	"math"
	"sort"
)

type Observation struct {
	TargetDate       string
	Code             string
	PredictedScore   float64
	ActualMaxHighPct float64
	ActualClose3Pct  float64
	Touch1Pct        bool
}

type Metrics struct {
	Days               int     `json:"days"`
	TopNMeanMaxHighPct float64 `json:"top_n_mean_max_high_pct"`
	TopNMeanClose3Pct  float64 `json:"top_n_mean_close3_pct"`
	TopNHit1Pct        float64 `json:"top_n_hit_1pct"`
	TopNNDCG           float64 `json:"top_n_ndcg"`
}

func Evaluate(observations []Observation, topN int) (Metrics, error) {
	if topN <= 0 {
		return Metrics{}, errors.New("second-stage evaluation requires positive top_n")
	}
	byDate := map[string][]Observation{}
	for _, observation := range observations {
		if observation.TargetDate == "" || !finite(observation.PredictedScore) || !finite(observation.ActualMaxHighPct) || !finite(observation.ActualClose3Pct) {
			return Metrics{}, errors.New("second-stage observation contains invalid values")
		}
		byDate[observation.TargetDate] = append(byDate[observation.TargetDate], observation)
	}
	if len(byDate) == 0 {
		return Metrics{}, errors.New("second-stage evaluation has no observations")
	}
	dates := make([]string, 0, len(byDate))
	for date := range byDate {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	var maxHigh, close3, hit, ndcg float64
	for _, date := range dates {
		rows := append([]Observation(nil), byDate[date]...)
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].PredictedScore != rows[j].PredictedScore {
				return rows[i].PredictedScore > rows[j].PredictedScore
			}
			return rows[i].Code < rows[j].Code
		})
		limit := min(topN, len(rows))
		dayMaxHigh, dayClose3, dayHit := 0.0, 0.0, 0.0
		for _, row := range rows[:limit] {
			dayMaxHigh += row.ActualMaxHighPct
			dayClose3 += row.ActualClose3Pct
			if row.Touch1Pct {
				dayHit++
			}
		}
		maxHigh += dayMaxHigh / float64(limit)
		close3 += dayClose3 / float64(limit)
		hit += dayHit / float64(limit)
		ndcg += dailyNDCG(rows, limit)
	}
	return Metrics{
		Days:               len(dates),
		TopNMeanMaxHighPct: maxHigh / float64(len(dates)),
		TopNMeanClose3Pct:  close3 / float64(len(dates)),
		TopNHit1Pct:        hit / float64(len(dates)),
		TopNNDCG:           ndcg / float64(len(dates)),
	}, nil
}

func dailyNDCG(rows []Observation, limit int) float64 {
	dcg := 0.0
	for index := 0; index < limit && index < len(rows); index++ {
		dcg += math.Max(0, rows[index].ActualMaxHighPct) / math.Log2(float64(index+2))
	}
	relevance := make([]float64, 0, len(rows))
	for _, row := range rows {
		relevance = append(relevance, math.Max(0, row.ActualMaxHighPct))
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(relevance)))
	ideal := 0.0
	for index := 0; index < limit && index < len(relevance); index++ {
		ideal += relevance[index] / math.Log2(float64(index+2))
	}
	if ideal == 0 {
		return 0
	}
	return dcg / ideal
}
