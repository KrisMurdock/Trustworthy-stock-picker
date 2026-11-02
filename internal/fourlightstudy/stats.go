package fourlightstudy

import (
	"context"
	"math"
	"math/rand"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func compareGroups(ctx context.Context, dates []string, daily []GroupStats) ([]Comparison, error) {
	return compareNamedGroups(ctx, dates, daily, groupNames, "baseline")
}

func compareNamedGroups(ctx context.Context, dates []string, daily []GroupStats, names []string, reference string) ([]Comparison, error) {
	byDate := make(map[string]map[string]*strategy.UpwardWaveSummary, len(dates))
	for _, row := range daily {
		if byDate[row.Date] == nil {
			byDate[row.Date] = map[string]*strategy.UpwardWaveSummary{}
		}
		byDate[row.Date][row.Name] = row.Wave
	}
	comparisons := make([]Comparison, 0, 2*(len(names)))
	for _, group := range names {
		if group == reference {
			continue
		}
		for _, days := range []int{5, 10} {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			x := Comparison{Group: group, Days: days, BootstrapBlockDays: bootstrapBlock, BootstrapRepetitions: bootstrapRepetitions, BootstrapSeed: bootstrapSeed}
			if reference != "baseline" {
				x.Reference = reference
			}
			deltas := make([]*float64, len(dates))
			sum := 0.0
			for i, date := range dates {
				base, candidate := comparableWindow(byDate[date][reference], days), comparableWindow(byDate[date][group], days)
				if base == nil || candidate == nil {
					x.UnpairedDates++
					continue
				}
				delta := float64(candidate.Success)/float64(candidate.Known) - float64(base.Success)/float64(base.Known)
				deltas[i] = &delta
				sum += delta
				x.PairedDates++
			}
			if x.PairedDates > 0 {
				mean := sum / float64(x.PairedDates)
				x.MeanDifference = &mean
			}
			var err error
			x.Lower95, x.Upper95, x.BootstrapValidDraws, err = movingBlockInterval(ctx, deltas, bootstrapBlock, bootstrapRepetitions, bootstrapSeed)
			if err != nil {
				return nil, err
			}
			switch {
			case x.PairedDates == 0:
				x.IntervalStatus = "no_paired_dates"
			case x.PairedDates < 2:
				x.IntervalStatus = "insufficient_paired_dates"
			case len(dates) < 2*bootstrapBlock:
				x.IntervalStatus = "insufficient_date_blocks"
			case x.BootstrapValidDraws == 0:
				x.IntervalStatus = "no_valid_bootstrap_draws"
			default:
				x.IntervalStatus = "descriptive_only"
			}
			comparisons = append(comparisons, x)
		}
	}
	return comparisons, nil
}

func comparableWindow(summary *strategy.UpwardWaveSummary, days int) *strategy.UpwardWaveStats {
	if summary == nil {
		return nil
	}
	for i := range summary.Windows {
		window := &summary.Windows[i]
		if window.Days == days && window.Known > 0 {
			return window
		}
	}
	return nil
}

// movingBlockInterval resamples the full trading-date grid, including dates on
// which no group/baseline comparison is possible. Removing those dates before
// forming blocks would silently turn a ten-session block into a longer period.
// This is a descriptive percentile interval, not an independent hypothesis test.
func movingBlockInterval(ctx context.Context, deltas []*float64, block, repetitions int, seed int64) (*float64, *float64, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, 0, err
	}
	if block < 1 || repetitions < 1 || len(deltas) < 2*block {
		return nil, nil, 0, nil
	}
	paired := 0
	for _, value := range deltas {
		if value != nil {
			paired++
		}
	}
	if paired < 2 {
		return nil, nil, 0, nil
	}
	rng := rand.New(rand.NewSource(seed))
	means := make([]float64, 0, repetitions)
	for iteration := 0; iteration < repetitions; iteration++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, err
		}
		sum, count := 0.0, 0
		for sampled := 0; sampled < len(deltas); {
			start := rng.Intn(len(deltas) - block + 1)
			for offset := 0; offset < block && sampled < len(deltas); offset++ {
				if value := deltas[start+offset]; value != nil {
					sum += *value
					count++
				}
				sampled++
			}
		}
		if count > 0 {
			means = append(means, sum/float64(count))
		}
	}
	if len(means) == 0 {
		return nil, nil, 0, nil
	}
	sort.Float64s(means)
	low, high := percentile(means, .025), percentile(means, .975)
	return &low, &high, len(means), nil
}

func percentile(sorted []float64, p float64) float64 {
	index := p * float64(len(sorted)-1)
	lower, upper := int(math.Floor(index)), int(math.Ceil(index))
	return sorted[lower] + (sorted[upper]-sorted[lower])*(index-float64(lower))
}
