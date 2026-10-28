package yangpullback

import (
	"math"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
)

const (
	subgroupMinResolved = 100
	subgroupMinWinRate  = 0.50
	subgroupMinPF       = 1.2
	subgroupStability   = 0.05
)

// Subgroup is one pre-registered single-condition filter. Selection happens on
// the development period only; the holdout column is exploratory because the
// primary evaluation already read it.
type Subgroup struct {
	Name               string          `json:"name"`
	Filter             string          `json:"filter"`
	Development        Stats           `json:"development"`
	DevelopmentControl Stats           `json:"development_control"`
	Holdout            Stats           `json:"holdout"`
	HoldoutControl     Stats           `json:"holdout_control"`
	Bootstrap          BootstrapResult `json:"bootstrap_vs_same_filter_control"`
	Selected           bool            `json:"selected"`
	Confirmed          bool            `json:"confirmed"`
}

type subgroupFeatures struct {
	pullbackDays    int
	anchorBodyPct   float64
	phaseConfidence string
	trend60Pct      float64
	breadthPct      float64
}

type subgroupRule struct {
	name   string
	filter string
	match  func(subgroupFeatures) bool
}

func defaultSubgroupRules() []subgroupRule {
	return []subgroupRule{
		{name: "pullback_2", filter: "pullback_days == 2", match: func(f subgroupFeatures) bool { return f.pullbackDays == 2 }},
		{name: "pullback_5", filter: "pullback_days == 5", match: func(f subgroupFeatures) bool { return f.pullbackDays == 5 }},
		{name: "anchor_body_ge_5", filter: "anchor_body_pct >= 5", match: func(f subgroupFeatures) bool {
			return !math.IsNaN(f.anchorBodyPct) && f.anchorBodyPct >= 5
		}},
		{name: "anchor_body_lt_2", filter: "anchor_body_pct < 2", match: func(f subgroupFeatures) bool {
			return !math.IsNaN(f.anchorBodyPct) && f.anchorBodyPct < 2
		}},
		{name: "phase_high", filter: "phase_confidence == high", match: func(f subgroupFeatures) bool { return f.phaseConfidence == "high" }},
		{name: "trend_60d_ge_20", filter: "60d_return_pct >= 20", match: func(f subgroupFeatures) bool {
			return !math.IsNaN(f.trend60Pct) && f.trend60Pct >= 20
		}},
		{name: "breadth_ge_60", filter: "pool_breadth_pct >= 60", match: func(f subgroupFeatures) bool {
			return !math.IsNaN(f.breadthPct) && f.breadthPct >= 60
		}},
	}
}

func appendSubgroups(result *Evaluation, pairs []pairedHistory, pairsByCode map[string]pairedHistory, patternTrades, controlTrades []Trade, cal *calendar.Calendar, opts EvalOptions, boundary string) {
	breadth := poolBreadth(pairs)
	features := make(map[string]subgroupFeatures, len(patternTrades)+len(controlTrades))
	for _, trade := range patternTrades {
		features[subgroupKey(trade.Signal)] = subgroupFeaturesFor(pairsByCode, trade.Signal, breadth)
	}
	for _, trade := range controlTrades {
		features[subgroupKey(trade.Signal)] = subgroupFeaturesFor(pairsByCode, trade.Signal, breadth)
	}

	for _, rule := range defaultSubgroupRules() {
		keep := func(trade Trade) bool {
			return rule.match(features[subgroupKey(trade.Signal)])
		}
		selected := filterTrades(patternTrades, keep)
		development, holdout, purged := splitAtBoundary(selected, boundary, cal, opts.Horizon)
		controlSelected := filterTrades(controlTrades, keep)
		controlDevelopment, controlHoldout, _ := splitAtBoundary(controlSelected, boundary, cal, opts.Horizon)
		bootstrap := bootstrapComparison(holdout, controlHoldout, opts.Bootstrap, opts.Seed, "hold_only_same_filter", opts.Horizon+2)
		result.Subgroups = append(result.Subgroups, Subgroup{
			Name:               rule.name,
			Filter:             rule.filter,
			Development:        summarizeTrades(development, purged),
			DevelopmentControl: summarizeTrades(controlDevelopment, 0),
			Holdout:            summarizeTrades(holdout, 0),
			HoldoutControl:     summarizeTrades(controlHoldout, 0),
			Bootstrap:          bootstrap,
		})
	}

	selected, confirmed := selectSubgroup(result.Subgroups)
	result.Data["subgroup_count"] = len(result.Subgroups)
	result.Data["subgroup_selected"] = selected
	result.Data["subgroup_confirmed"] = confirmed
	result.Data["subgroup_scope"] = "seven pre-registered single-condition filters; selected on development; holdout already read by the primary evaluation"
	result.Data["subgroup_selection_rule"] = "max development win_rate_resolved among filters with >=100 resolved pattern and control signals, win>=50%, mean>0, median>0, PF>=1.2 and win above the same-filter control; confirmed if holdout repeats all conditions and win is within 5pp of development"
}

func filterTrades(trades []Trade, keep func(Trade) bool) []Trade {
	out := make([]Trade, 0, len(trades))
	for _, trade := range trades {
		if keep(trade) {
			out = append(out, trade)
		}
	}
	return out
}

func subgroupKey(signal Signal) string {
	return signal.Code + "|" + signal.Date
}

func subgroupFeaturesFor(pairs map[string]pairedHistory, signal Signal, breadth map[string]float64) subgroupFeatures {
	features := subgroupFeatures{
		pullbackDays:    signal.PullbackDays,
		anchorBodyPct:   math.NaN(),
		phaseConfidence: signal.PhaseConfidence,
		trend60Pct:      math.NaN(),
		breadthPct:      math.NaN(),
	}
	pair, ok := pairs[signal.Code]
	if !ok {
		return features
	}
	index, ok := pair.indexByDate[signal.Date]
	if !ok {
		return features
	}
	bars := pair.adjusted.Bars
	if index >= 60 && bars[index-60].Close > 0 {
		features.trend60Pct = (bars[index].Close/bars[index-60].Close - 1) * 100
	}
	if anchorIndex, ok := pair.indexByDate[signal.AnchorDate]; ok && anchorIndex < len(bars) {
		anchor := bars[anchorIndex]
		if anchor.Open > 0 {
			features.anchorBodyPct = (anchor.Close/anchor.Open - 1) * 100
		}
	}
	if value, ok := breadth[signal.Date]; ok {
		features.breadthPct = value * 100
	}
	return features
}

// poolBreadth is the share of aligned stocks closing above their 20-day moving
// average on each date. It uses only bars up to that date and requires at least
// 50 stocks so early dates do not produce a one-stock "regime".
func poolBreadth(pairs []pairedHistory) map[string]float64 {
	counts := map[string]int{}
	totals := map[string]int{}
	for _, pair := range pairs {
		sum := 0.0
		for index, bar := range pair.adjusted.Bars {
			sum += bar.Close
			if index >= 20 {
				sum -= pair.adjusted.Bars[index-20].Close
			}
			if index < 19 {
				continue
			}
			ma20 := sum / 20
			totals[bar.Date]++
			if bar.Close > ma20 {
				counts[bar.Date]++
			}
		}
	}
	out := make(map[string]float64, len(totals))
	for date, total := range totals {
		if total >= 50 {
			out[date] = float64(counts[date]) / float64(total)
		}
	}
	return out
}

func selectSubgroup(subgroups []Subgroup) (string, bool) {
	best := -1
	for index := range subgroups {
		development := subgroups[index].Development
		control := subgroups[index].DevelopmentControl
		if development.Resolved < subgroupMinResolved ||
			control.Resolved < subgroupMinResolved ||
			development.WinRateResolved < subgroupMinWinRate ||
			development.MeanNet <= 0 ||
			development.MedianNet <= 0 ||
			development.ProfitFactor == nil ||
			*development.ProfitFactor < subgroupMinPF ||
			development.WinRateResolved <= control.WinRateResolved {
			continue
		}
		if best < 0 || development.WinRateResolved > subgroups[best].Development.WinRateResolved ||
			(development.WinRateResolved == subgroups[best].Development.WinRateResolved && development.MeanNet > subgroups[best].Development.MeanNet) {
			best = index
		}
	}
	if best < 0 {
		return "", false
	}
	subgroups[best].Selected = true
	holdout := subgroups[best].Holdout
	control := subgroups[best].HoldoutControl
	development := subgroups[best].Development
	confirmed := holdout.Resolved >= subgroupMinResolved &&
		control.Resolved >= subgroupMinResolved &&
		holdout.WinRateResolved >= subgroupMinWinRate &&
		holdout.MeanNet > 0 &&
		holdout.MedianNet > 0 &&
		holdout.ProfitFactor != nil &&
		*holdout.ProfitFactor >= subgroupMinPF &&
		holdout.WinRateResolved > control.WinRateResolved &&
		holdout.WinRateResolved >= development.WinRateResolved-subgroupStability
	subgroups[best].Confirmed = confirmed
	return subgroups[best].Name, confirmed
}
