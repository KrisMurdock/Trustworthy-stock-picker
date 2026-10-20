package strategy

import (
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

type statsKey struct {
	rule    string
	target  float64
	horizon int
}

type maturityEvent struct {
	end    string
	sample Sample
}

// statsTimeline answers the walk-forward cutoff for every evaluation date in
// one pass. Each rule/target/horizon accumulates samples exactly when its own
// label matures, preserving the per-cutoff semantics of statsFor.
func statsTimeline(samples []Sample, dates []string, s config.Strategy) map[string][]Stats {
	hs := horizons(s)
	keys := make([]statsKey, 0, len(ruleNames)*2*len(hs))
	for _, rule := range ruleNames {
		for _, target := range []float64{2.5, 3} {
			for _, horizon := range hs {
				keys = append(keys, statsKey{rule: rule, target: target, horizon: horizon})
			}
		}
	}
	events := make(map[statsKey][]maturityEvent, len(keys))
	continuationEvents := make(map[string][]maturityEvent, len(ruleNames))
	for _, sample := range samples {
		for _, rule := range sample.Rules {
			if sample.NextDay != nil && sample.Features.Return1 > 0 {
				continuationEvents[rule] = append(continuationEvents[rule], maturityEvent{end: sample.NextDay.Date, sample: sample})
			}
			for _, target := range []float64{2.5, 3} {
				for _, horizon := range hs {
					end := labelEnd(sample, target, horizon)
					if end == "" {
						continue
					}
					k := statsKey{rule: rule, target: target, horizon: horizon}
					events[k] = append(events[k], maturityEvent{end: end, sample: sample})
				}
			}
		}
	}
	for k := range events {
		list := events[k]
		sort.SliceStable(list, func(i, j int) bool { return list[i].end < list[j].end })
		events[k] = list
	}
	for rule := range continuationEvents {
		list := continuationEvents[rule]
		sort.SliceStable(list, func(i, j int) bool { return list[i].end < list[j].end })
		continuationEvents[rule] = list
	}
	ordered := append([]string{}, dates...)
	sort.Strings(ordered)
	unique := ordered[:0]
	for _, date := range ordered {
		if len(unique) == 0 || unique[len(unique)-1] != date {
			unique = append(unique, date)
		}
	}
	accumulators := make(map[statsKey]Stats, len(keys))
	positions := make(map[statsKey]int, len(keys))
	continuationAccumulators := make(map[string]ContinuationStats, len(ruleNames))
	continuationDates := make(map[string]map[string]bool, len(ruleNames))
	continuationPositions := make(map[string]int, len(ruleNames))
	out := make(map[string][]Stats, len(unique))
	for _, date := range unique {
		continuationByRule := make(map[string]*ContinuationStats, len(ruleNames))
		for _, rule := range ruleNames {
			acc, ok := continuationAccumulators[rule]
			if !ok {
				acc = *newContinuationStats()
			}
			list := continuationEvents[rule]
			pos := continuationPositions[rule]
			for pos < len(list) && list[pos].end < date {
				sample := list[pos].sample
				acc.Samples++
				if sample.NextDay.Higher {
					acc.Hits++
				}
				if continuationDates[rule] == nil {
					continuationDates[rule] = map[string]bool{}
				}
				continuationDates[rule][sample.Date] = true
				pos++
			}
			acc.Dates = len(continuationDates[rule])
			finalizeContinuation(&acc)
			copy := acc
			continuationByRule[rule] = &copy
			continuationAccumulators[rule] = acc
			continuationPositions[rule] = pos
		}
		stats := make([]Stats, 0, len(keys))
		for _, k := range keys {
			acc := accumulators[k]
			if acc.Rule == "" {
				acc = Stats{Rule: k.rule, Target: k.target, Horizon: k.horizon}
			}
			list := events[k]
			pos := positions[k]
			for pos < len(list) && list[pos].end < date {
				add(&acc, list[pos].sample, list[pos].sample.Outcomes[key(k.target, k.horizon)])
				pos++
			}
			accumulators[k] = acc
			positions[k] = pos
			snapshot := acc
			snapshot.Dates = len(acc.DateSet)
			snapshot.DateSet = nil
			if continuation := continuationByRule[k.rule]; continuation != nil {
				copy := *continuation
				snapshot.Continuation = &copy
			}
			stats = append(stats, finalize(snapshot))
		}
		out[date] = stats
	}
	return out
}
