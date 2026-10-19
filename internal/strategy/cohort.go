package strategy

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"math"
	"math/rand"
	"sort"
)

const (
	CohortVersion       = 1
	BootstrapVersion    = 1
	BootstrapIterations = 2000
	BootstrapSeed       = int64(0x5EED5EED)
)

// CohortStats treats each signal day as one observation. Top N is a fixed
// budget: unfilled slots remain cash and are never redistributed.
type CohortStats struct {
	Version            int             `json:"schema_version"`
	SignalDates        int             `json:"signal_dates"`
	PlannedSlots       int             `json:"planned_slots"`
	FilledSlots        int             `json:"filled_slots"`
	IncompleteDates    int             `json:"incomplete_signal_dates"`
	Hits25             int             `json:"net_profit_2_5_hits"`
	Hits30             int             `json:"net_profit_3_hits"`
	Rate25             float64         `json:"net_profit_2_5_rate"`
	Rate30             float64         `json:"net_profit_3_rate"`
	MeanNet            float64         `json:"mean_net_pct"`
	WorstNet           float64         `json:"worst_net_pct"`
	LossBreaches       int             `json:"net_loss_limit_breaches"`
	PairedDates        int             `json:"paired_signal_dates,omitempty"`
	BaselineMeanNet    float64         `json:"baseline_mean_net_pct,omitempty"`
	MeanNetIncrement   float64         `json:"mean_net_increment_pct,omitempty"`
	Bootstrap          BootstrapStats  `json:"bootstrap"`
	Bootstrap25        BootstrapStats  `json:"bootstrap_2_5"`
	Bootstrap30        BootstrapStats  `json:"bootstrap_3"`
	IncrementBootstrap *BootstrapStats `json:"increment_bootstrap,omitempty"`
	days               []cohortDay
}

type BootstrapStats struct {
	Version      int     `json:"schema_version"`
	Iterations   int     `json:"iterations"`
	BlockLength  int     `json:"block_length"`
	Seed         int64   `json:"seed"`
	RateLower    float64 `json:"target_rate_lower"`
	RateUpper    float64 `json:"target_rate_upper"`
	MeanNetLower float64 `json:"mean_net_lower"`
	MeanNetUpper float64 `json:"mean_net_upper"`
}

type cohortDay struct {
	date   string
	net    float64
	rate25 float64
	rate30 float64
}

type cohortObservation struct {
	date       string
	planned    int
	filled     int
	hits25     int
	hits30     int
	net        float64
	incomplete bool
}

func (x *cohortObservation) add(out Outcome) {
	if unknownBacktestOutcome(out) {
		x.incomplete = true
		return
	}
	if out.EntryDate == "" {
		return
	}
	x.filled++
	if ResolvedOutcome(out) {
		x.net += out.NetPct
	}
	if out.HighTouch != nil && out.HighTouch.Version == 1 && out.HighTouch.ObservedDays > 0 {
		if out.HighTouch.Day25 > 0 && !out.HighTouch.Day25Unknown {
			x.hits25++
		}
		if out.HighTouch.Day30 > 0 && !out.HighTouch.Day30Unknown {
			x.hits30++
		}
	}
}

func (x *cohortObservation) finish() cohortObservation {
	if !x.incomplete && x.planned > 0 {
		x.net /= float64(x.planned)
	}
	return *x
}

func summarizeCohorts(obs []cohortObservation, horizon int) CohortStats {
	out := CohortStats{Version: CohortVersion}
	rates25 := []float64{}
	rates30 := []float64{}
	nets := []float64{}
	hasNet := false
	for _, x := range obs {
		if x.incomplete {
			out.IncompleteDates++
			continue
		}
		if x.planned <= 0 {
			continue
		}
		out.SignalDates++
		out.PlannedSlots += x.planned
		out.FilledSlots += x.filled
		out.Hits25 += x.hits25
		out.Hits30 += x.hits30
		if !hasNet || x.net < out.WorstNet {
			out.WorstNet = x.net
			hasNet = true
		}
		rate25 := float64(x.hits25) / float64(x.planned)
		rate30 := float64(x.hits30) / float64(x.planned)
		rates25 = append(rates25, rate25)
		rates30 = append(rates30, rate30)
		nets = append(nets, x.net)
		out.days = append(out.days, cohortDay{date: x.date, net: x.net, rate25: rate25, rate30: rate30})
		if BreachesLossLimit(x.net) {
			out.LossBreaches++
		}
	}
	if len(rates30) == 0 {
		return out
	}
	out.Rate25 = mean(rates25)
	out.Rate30 = mean(rates30)
	out.MeanNet = mean(nets)
	block := horizon + 2
	if block < 1 {
		block = 1
	}
	out.Bootstrap = bootstrapStats(block)
	out.Bootstrap25 = bootstrapStats(block)
	out.Bootstrap30 = bootstrapStats(block)
	out.Bootstrap25.RateLower, out.Bootstrap25.RateUpper = bootstrapBounds(rates25, block, percentile)
	out.Bootstrap30.RateLower, out.Bootstrap30.RateUpper = bootstrapBounds(rates30, block, percentile)
	out.Bootstrap.MeanNetLower, out.Bootstrap.MeanNetUpper = bootstrapBounds(nets, block, percentile)
	return out
}

// WithBaseline attaches a paired, by-signal-date comparison. The increment is
// resampled in the same blocks as the cohort itself, so correlated days remain
// paired and the reported lower bound answers whether the rule adds value.
func (c CohortStats) WithBaseline(baseline CohortStats) CohortStats {
	if len(c.days) == 0 || len(baseline.days) == 0 {
		return c
	}
	base := map[string]float64{}
	for _, day := range baseline.days {
		base[day.date] = day.net
	}
	diffs := []float64{}
	baselineSum := 0.0
	for _, day := range c.days {
		value, ok := base[day.date]
		if !ok {
			continue
		}
		diffs = append(diffs, day.net-value)
		baselineSum += value
	}
	if len(diffs) == 0 {
		return c
	}
	c.BaselineMeanNet = baselineSum / float64(len(diffs))
	c.MeanNetIncrement = mean(diffs)
	c.PairedDates = len(diffs)
	block := c.Bootstrap.BlockLength
	if block < 1 {
		block = 1
	}
	stats := bootstrapStats(block)
	stats.MeanNetLower, stats.MeanNetUpper = bootstrapBounds(diffs, block, percentile)
	c.IncrementBootstrap = &stats
	return c
}

func SummarizeExperimentCohorts(trades []ExperimentTrade, parameters config.Strategy, topN int) CohortStats {
	byDate := map[string][]ExperimentTrade{}
	for _, trade := range trades {
		if trade.Date == "" {
			continue
		}
		if _, ok := byDate[trade.Date]; !ok {
			byDate[trade.Date] = []ExperimentTrade{}
		}
		if trade.Selected {
			byDate[trade.Date] = append(byDate[trade.Date], trade)
		}
	}
	dates := make([]string, 0, len(byDate))
	for date := range byDate {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	obs := make([]cohortObservation, 0, len(dates))
	for _, date := range dates {
		group := byDate[date]
		sort.Slice(group, func(i, j int) bool {
			if group[i].Rank == group[j].Rank {
				return group[i].Code < group[j].Code
			}
			return group[i].Rank < group[j].Rank
		})
		if topN < 0 {
			topN = 0
		}
		x := cohortObservation{date: date, planned: topN}
		incomplete := false
		for i := 0; i < topN; i++ {
			if i >= len(group) {
				continue
			}
			out := group[i].Outcome
			if unknownBacktestOutcome(out) {
				incomplete = true
				break
			}
			if out.EntryDate == "" {
				continue
			}
			x.filled++
			if ResolvedOutcome(out) {
				x.net += out.NetPct
			}
			if out.HighTouch != nil && out.HighTouch.Version == 1 && out.HighTouch.ObservedDays > 0 {
				if out.HighTouch.Day25 > 0 && !out.HighTouch.Day25Unknown {
					x.hits25++
				}
				if out.HighTouch.Day30 > 0 && !out.HighTouch.Day30Unknown {
					x.hits30++
				}
			}
		}
		x.incomplete = incomplete
		obs = append(obs, x.finish())
	}
	return summarizeCohorts(obs, parameters.MaxHoldingDays)
}

func unknownBacktestOutcome(out Outcome) bool {
	switch out.Status {
	case "entry_path_unknown", "missing_entry_session", "missing_exit_session", "unresolved", "corporate_action":
		return true
	}
	return false
}

func bootstrapStats(block int) BootstrapStats {
	return BootstrapStats{Version: BootstrapVersion, Iterations: BootstrapIterations, BlockLength: block, Seed: BootstrapSeed}
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func bootstrapBounds(values []float64, block int, stat func([]float64) float64) (float64, float64) {
	if len(values) == 0 || block < 1 {
		return 0, 0
	}
	rng := rand.New(rand.NewSource(BootstrapSeed))
	samples := make([]float64, 0, BootstrapIterations)
	for i := 0; i < BootstrapIterations; i++ {
		samples = append(samples, stat(resampleBlocks(values, block, rng)))
	}
	sort.Float64s(samples)
	return samples[int(math.Floor(.025*float64(BootstrapIterations)))], samples[int(math.Ceil(.975*float64(BootstrapIterations)))-1]
}

func resampleBlocks(values []float64, block int, rng *rand.Rand) []float64 {
	out := make([]float64, 0, len(values))
	for len(out) < len(values) {
		start := rng.Intn(len(values))
		for i := 0; i < block && len(out) < len(values); i++ {
			out = append(out, values[(start+i)%len(values)])
		}
	}
	return out
}

func percentile(values []float64) float64 {
	return mean(values)
}
