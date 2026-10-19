package strategy

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

// conditionalSignal is one preselected stock/signal-date sample. Cross-sectional
// scoring is intentionally deferred until the signal rule has defined the
// eligible pool.
type conditionalSignal struct {
	code         string
	features     Features
	bar          market.Bar
	future       []market.Bar
	corporate    bool
	chanEvidence *ChanEvidence
}

type conditionalScored struct {
	signal    conditionalSignal
	candidate Candidate
	rank      int
}

func conditionalAllowed(rule string, signal conditionalSignal) bool {
	if rule == ChanSignalRule {
		return signal.chanEvidence != nil && signal.chanEvidence.Status == chanPass
	}
	return matchesSignalRule(rule, signal.features)
}

// scoreConditionalSignals computes relative strength and the opportunity score
// only inside the supplied pool. This is the key difference from scoring the
// full daily universe before filtering: excluded stocks cannot change the
// cross-sectional mean or the final ranking.
func scoreConditionalSignals(group []conditionalSignal) []conditionalScored {
	if len(group) == 0 {
		return nil
	}
	mean := 0.0
	for _, signal := range group {
		mean += signal.features.Return5
	}
	mean /= float64(len(group))
	lookup := make(map[string]conditionalSignal, len(group))
	candidates := make([]Candidate, 0, len(group))
	for _, signal := range group {
		signal.features.Relative5 = signal.features.Return5 - mean
		lookup[signal.code] = signal
		candidates = append(candidates, Candidate{Code: signal.code, Features: signal.features, Score: opportunity(signal.features)})
	}
	sortRank(candidates)
	scored := make([]conditionalScored, 0, len(candidates))
	for i, candidate := range candidates {
		scored = append(scored, conditionalScored{signal: lookup[candidate.Code], candidate: candidate, rank: i + 1})
	}
	return scored
}

// selectConditionalSignals applies eligibility first, then scores/ranks the
// eligible pool. The full-pool ranking is retained only for audit attribution of
// an original Top-N sample rejected by the signal rule.
func selectConditionalSignals(group []conditionalSignal, rule string, topN int) (selected, filteredOriginalTop []conditionalScored, eligible int) {
	selected, filteredOriginalTop, _, eligible = selectConditionalSignalsWithBaseline(group, rule, topN)
	return
}

// selectConditionalSignalsWithBaseline additionally returns the unfiltered
// full-pool Top N so callers can verify whether the signal rule adds value.
func selectConditionalSignalsWithBaseline(group []conditionalSignal, rule string, topN int) (selected, filteredOriginalTop, baseline []conditionalScored, eligible int) {
	if topN < 0 {
		topN = 0
	}
	fullRanking := scoreConditionalSignals(group)
	allowed := make([]conditionalSignal, 0, len(group))
	allowedByCode := make(map[string]bool, len(group))
	for _, signal := range group {
		if conditionalAllowed(rule, signal) {
			allowed = append(allowed, signal)
			allowedByCode[signal.code] = true
		}
	}
	ranked := scoreConditionalSignals(allowed)
	end := min(topN, len(ranked))
	selected = append([]conditionalScored{}, ranked[:end]...)
	baseline = append([]conditionalScored{}, fullRanking[:min(topN, len(fullRanking))]...)
	for _, item := range fullRanking {
		if item.rank <= topN && !allowedByCode[item.signal.code] {
			filteredOriginalTop = append(filteredOriginalTop, item)
		}
	}
	return selected, filteredOriginalTop, baseline, len(allowed)
}
