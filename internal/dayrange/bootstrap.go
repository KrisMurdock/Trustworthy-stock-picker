package dayrange

import (
	"context"
	"math"
	"math/rand"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
)

const bootstrapResamples = 10000
const bootstrapSeed int64 = 1949

type dailyValue struct{ value, baseline float64 }

func summarize(rows []valueRow, kind string) dailyValue {
	out := dailyValue{}
	squared := kind == "rmse" || kind == "skill_rmse"
	for _, r := range rows {
		v, b := r.value, r.baseline
		if squared {
			v, b = v*v, b*b
		}
		out.value += v / float64(len(rows))
		out.baseline += b / float64(len(rows))
	}
	if squared {
		out.value = math.Sqrt(out.value)
		out.baseline = math.Sqrt(out.baseline)
	}
	return out
}
func summarizeDays(rows []valueRow, kind string) []dailyValue {
	groups := map[string][]valueRow{}
	for _, r := range rows {
		groups[r.date] = append(groups[r.date], r)
	}
	days := make([]string, 0, len(groups))
	for date := range groups {
		days = append(days, date)
	}
	sort.Strings(days)
	out := make([]dailyValue, 0, len(days))
	for _, day := range days {
		out = append(out, summarize(groups[day], kind))
	}
	return out
}

// day_equal RMSE is the mean of daily RMSEs. Skill compares aggregate errors on
// the same paired rows; it is not the mean of per-day ratios (which may divide
// by zero on perfect-baseline days).
func aggregate(values []dailyValue, kind string) (float64, bool) {
	v, b := 0.0, 0.0
	for _, x := range values {
		v += x.value / float64(len(values))
		b += x.baseline / float64(len(values))
	}
	if kind == "skill_mae" || kind == "skill_rmse" {
		if b == 0 {
			return 0, false
		}
		return 1 - v/b, true
	}
	return v, true
}
func bootstrap(ctx context.Context, days []dailyValue, kind string) (*modeleval.ConfidenceInterval, error) {
	ci := &modeleval.ConfidenceInterval{Method: "paired_day_block_bootstrap", Confidence: .95, Resamples: bootstrapResamples, Seed: bootstrapSeed, Applicability: modeleval.ApplicabilityInsufficientData, Reason: "fewer than 10 resolved trading days"}
	if len(days) < 10 {
		return ci, nil
	}
	rng := rand.New(rand.NewSource(bootstrapSeed))
	values := make([]float64, bootstrapResamples)
	draws := make([]dailyValue, len(days))
	for i := range values {
		if i%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for j := range draws {
			draws[j] = days[rng.Intn(len(days))]
		}
		v, ok := aggregate(draws, kind)
		if !ok {
			ci.Reason = "zero baseline error in a bootstrap draw"
			return ci, nil
		}
		values[i] = v
	}
	sort.Float64s(values)
	ci.Applicability = modeleval.ApplicabilityOK
	ci.Reason = ""
	ci.Lower = modeleval.Float(values[int(math.Ceil(.025*float64(len(values))))-1])
	ci.Upper = modeleval.Float(values[int(math.Ceil(.975*float64(len(values))))-1])
	return ci, nil
}
