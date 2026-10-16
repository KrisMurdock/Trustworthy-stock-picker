package bottomstudy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"stock-watcher/internal/experiment"
)

const ComparisonEngine = "stock_sector_market_pair_v1"

type SelectionDecision struct {
	ID         string        `json:"id"`
	Code       string        `json:"code"`
	Date       string        `json:"signal_date"`
	Split      string        `json:"split"`
	StockScore float64       `json:"stock_score"`
	RankScore  float64       `json:"rank_score"`
	Context    SignalContext `json:"context"`
	Selected   bool          `json:"selected"`
	Reason     string        `json:"reason"`
}

type SelectionAudit struct {
	Rule      string              `json:"rule"`
	Arm       string              `json:"arm"`
	Decisions []SelectionDecision `json:"decisions"`
}

type GroupStats struct {
	Kind  string     `json:"kind"`
	Name  string     `json:"name"`
	Stats TradeStats `json:"stats"`
}

type ComparisonStats struct {
	TradeStats
	DateEqualRate  float64        `json:"date_equal_target_rate"`
	LargestDatePct float64        `json:"largest_signal_date_sample_share"`
	DecisionCounts map[string]int `json:"decision_counts"`
	Groups         []GroupStats   `json:"groups,omitempty"`
}

type ComparisonArm struct {
	Training ComparisonStats `json:"training"`
	Later    ComparisonStats `json:"later_period"`
}

type ComparisonPair struct {
	ID                string        `json:"id"`
	Policy            TradePolicy   `json:"policy"`
	StockAll          ComparisonArm `json:"stock_all"`
	StockCommon       ComparisonArm `json:"stock_common_context"`
	Combined          ComparisonArm `json:"stock_sector_market"`
	TrainingRateDelta float64       `json:"training_rate_delta_pp"`
	LaterRateDelta    float64       `json:"later_rate_delta_pp"`
}

type ComparisonEvidence struct {
	PolicyID string  `json:"policy_id"`
	Arm      string  `json:"arm"`
	Trades   []Trade `json:"trades"`
}

type ComparisonWinner struct {
	Target   float64 `json:"target_pct"`
	Arm      string  `json:"arm"`
	PolicyID string  `json:"policy_id"`
}

type ComparisonReport struct {
	Version     int                  `json:"schema_version"`
	Engine      string               `json:"engine"`
	SnapshotID  string               `json:"stock_snapshot_id"`
	Basis       string               `json:"classification_basis"`
	ObservedAt  string               `json:"classification_observed_at"`
	Options     GridOptions          `json:"options"`
	TopN        int                  `json:"daily_signal_limit"`
	Weights     [3]float64           `json:"combined_stock_sector_market_weights"`
	Omissions   map[string]int       `json:"omissions"`
	Selections  []SelectionAudit     `json:"selections"`
	Pairs       []ComparisonPair     `json:"pairs"`
	Winners     []ComparisonWinner   `json:"training_no_stop_winners"`
	Evidence    []ComparisonEvidence `json:"evidence"`
	Limitations []string             `json:"limitations"`
}

func selectSignals(opps []opportunity, features map[string]map[string]ContextFeature, lookup contextLookup, arm string, topN int) []SelectionDecision {
	out := make([]SelectionDecision, len(opps))
	byDate := map[string][]int{}
	for i, o := range opps {
		f, ok := features[o.code][o.bars[0].Date]
		d := SelectionDecision{ID: o.id, Code: o.code, Date: o.bars[0].Date, Split: o.split, StockScore: f.TrendScore, RankScore: f.TrendScore, Context: lookup.at(o.code, o.bars[0].Date), Reason: "rank_limit"}
		if !ok {
			d.Reason = "missing_stock_features"
		} else if arm != "stock_all" && !d.Context.Known {
			d.Reason = d.Context.Missing
		} else if arm == "stock_sector_market" && !d.Context.SectorEligible {
			d.Reason = "sector_not_ready"
		} else {
			if arm == "stock_sector_market" {
				d.RankScore = .6*d.StockScore + .3*d.Context.SectorScore + .1*d.Context.MarketScore
			}
			byDate[d.Date] = append(byDate[d.Date], i)
		}
		out[i] = d
	}
	for _, indices := range byDate {
		sort.Slice(indices, func(i, j int) bool {
			a, b := out[indices[i]], out[indices[j]]
			if a.RankScore != b.RankScore {
				return a.RankScore > b.RankScore
			}
			return a.Code < b.Code
		})
		for _, i := range indices[:min(topN, len(indices))] {
			out[i].Selected, out[i].Reason = true, "selected"
		}
	}
	return out
}

func selectedOpportunities(opps []opportunity, decisions []SelectionDecision, lookup contextLookup, combined bool) []opportunity {
	selected := []opportunity{}
	for i, o := range opps {
		if !decisions[i].Selected {
			continue
		}
		if combined {
			// Copy event decisions; A and all policy pairs must retain
			// exactly the original opportunities and individual signal events.
			events := append([]bool(nil), o.events...)
			unknown := map[int]bool{}
			for day := 1; day < len(events)-1; day++ {
				if !events[day] {
					continue
				}
				c := lookup.at(o.code, o.bars[day].Date)
				events[day] = !c.Known || c.SectorEligible
				unknown[day+1] = !c.Known
			}
			o.events, o.unknownAdds = events, unknown
		}
		selected = append(selected, o)
	}
	return selected
}

func summarizeComparison(trades []Trade, split string, decisions []SelectionDecision, detailed bool) ComparisonStats {
	s := ComparisonStats{TradeStats: tradeStats(trades, split, nil), DecisionCounts: map[string]int{}, Groups: []GroupStats{}}
	sectors := map[string]string{}
	for _, d := range decisions {
		sectors[d.ID] = d.Context.SectorName
		if d.Split == split {
			s.DecisionCounts[d.Reason]++
		}
	}
	byDate := map[string][]Trade{}
	groups := map[string][]Trade{}
	for _, t := range trades {
		if t.Split != split {
			continue
		}
		sector := sectors[t.ID]
		if sector == "" {
			sector = "unknown"
		}
		if detailed {
			groups["sector/"+sector] = append(groups["sector/"+sector], t)
			groups["month/"+t.SignalDate[:7]] = append(groups["month/"+t.SignalDate[:7]], t)
		}
		if t.EntryDate != "" || t.Status != "entry_skipped" {
			byDate[t.SignalDate] = append(byDate[t.SignalDate], t)
		}
	}
	// Sorting avoids floating-point aggregation order changing output bytes.
	for _, date := range sortedKeys(byDate) {
		ts := byDate[date]
		d := tradeStats(ts, split, nil)
		s.DateEqualRate += d.Rate / float64(len(byDate))
		if s.Samples > 0 {
			s.LargestDatePct = max(s.LargestDatePct, float64(d.Samples)/float64(s.Samples))
		}
	}
	for _, key := range sortedKeys(groups) {
		kind, name := key[:5], key[6:]
		if key[:6] == "sector" {
			kind, name = "sector", key[7:]
		}
		s.Groups = append(s.Groups, GroupStats{kind, name, tradeStats(groups[key], split, nil)})
	}
	return s
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func CompareContext(ctx context.Context, snapshot experiment.Snapshot, background MarketContext, options GridOptions) (ComparisonReport, error) {
	r := ComparisonReport{Version: 1, Engine: ComparisonEngine, SnapshotID: snapshot.ID, Basis: background.Basis, ObservedAt: background.ObservedAt, Options: options, TopN: snapshot.TopN, Weights: [3]float64{.6, .3, .1}, Selections: []SelectionAudit{}, Pairs: []ComparisonPair{}, Winners: []ComparisonWinner{}, Evidence: []ComparisonEvidence{}, Limitations: []string{
		"Only two strategy arms: individual stock versus individual stock + sector + weak market context.",
		"Current-only classifications are retrospective and can introduce look-ahead/survivorship bias; dated classifications require separately verified historical intervals.",
		"Later history was previously explored; this is not an unseen holdout.",
		"A is rerun with the same daily top-N limit as B; previous unlimited-entry rates are not the paired baseline.",
		"Missing initial context is excluded from B and reported alongside both full A and A with the same context coverage. Missing context for a needed add remains unresolved, without blocking known exits.",
		"Sector components/breadth and historical sector membership changes are not inferred from the small stock pool.",
		"Daily bars cannot guarantee execution; fees, T+1 and gaps can cause losses beyond the stop threshold.",
		"Signals on the same date or sector are dependent; trade counts are not independent evidence or account portfolio returns.",
	}}
	if snapshot.TopN < 1 || snapshot.TopN > 100 {
		return r, errors.New("comparison requires daily top_n from 1 to 100")
	}
	if err := validateContext(background, snapshot, options); err != nil {
		return r, err
	}
	data, omitted, err := prepareGrid(ctx, snapshot, options)
	if err != nil {
		return r, err
	}
	r.Omissions = omitted
	lookup, err := buildContext(ctx, background, options.Through)
	if err != nil {
		return r, err
	}
	features := map[string]map[string]ContextFeature{}
	for _, h := range snapshot.Adjusted {
		end := sort.Search(len(h.Bars), func(i int) bool { return h.Bars[i].Date > options.Through })
		features[h.Code], err = contextFeatures(ctx, h.Bars[:end])
		if err != nil {
			return r, err
		}
	}
	for _, target := range []float64{2.5, 3} {
		rule := "macd_contraction"
		if target == 3 {
			rule = "macd_or_bollinger"
		}
		arms := []string{"stock_all", "stock_common_context", "stock_sector_market"}
		decisions := map[string][]SelectionDecision{}
		opps := map[string][]opportunity{}
		for _, arm := range arms {
			decisions[arm] = selectSignals(data[rule], features, lookup, arm, r.TopN)
			opps[arm] = selectedOpportunities(data[rule], decisions[arm], lookup, arm == "stock_sector_market")
			r.Selections = append(r.Selections, SelectionAudit{rule, arm, decisions[arm]})
		}
		best := map[string]Trial{}
		for _, batches := range []int{2, 3} {
			for _, days := range []int{1, 3, 5, 10, 20} {
				for _, stop := range append([]float64{0}, stopGrid...) {
					if err := ctx.Err(); err != nil {
						return r, err
					}
					p := TradePolicy{rule, batches, days, target, stop}
					pair := ComparisonPair{ID: trialID(p), Policy: p}
					detailed := days == 20 && (stop == 0 || stop == 3 || stop == 12 || stop == 15 || stop == 20)
					for i, arm := range arms {
						t, trades := evaluateGridPolicy(snapshot.Parameters, p, opps[arm], nil)
						stats := ComparisonArm{summarizeComparison(trades, "training", decisions[arm], detailed), summarizeComparison(trades, "later_period", decisions[arm], detailed)}
						switch i {
						case 0:
							pair.StockAll = stats
						case 1:
							pair.StockCommon = stats
						case 2:
							pair.Combined = stats
						}
						if stop == 0 && arm != "stock_all" && enough(t.Train) {
							old, ok := best[arm]
							if !ok || better(t, old) {
								best[arm] = t
							}
						}
						if detailed {
							r.Evidence = append(r.Evidence, ComparisonEvidence{pair.ID, arm, trades})
						}
					}
					pair.TrainingRateDelta = 100 * (pair.Combined.Training.Rate - pair.StockCommon.Training.Rate)
					pair.LaterRateDelta = 100 * (pair.Combined.Later.Rate - pair.StockCommon.Later.Rate)
					r.Pairs = append(r.Pairs, pair)
				}
			}
		}
		for _, arm := range arms[1:] {
			if t, ok := best[arm]; ok {
				r.Winners = append(r.Winners, ComparisonWinner{target, arm, t.ID})
			} else {
				r.Limitations = append(r.Limitations, fmt.Sprintf("No no-stop %g%% %s candidate meets 30 training entries, 10 signal dates and 10 stocks.", target, arm))
			}
		}
	}
	return r, ctx.Err()
}
