package bottomstudy

import (
	"context"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"math"
	"sort"
)

const SpeedEngine = "no_stop_holding_speed_v1"

var speedConfirmations = []string{"none", "price_reclaim", "momentum_turn", "price_and_momentum"}

type SpeedPoint struct {
	Day     int     `json:"day"`
	Targets int     `json:"targets"`
	Rate    float64 `json:"rate"`
}

type SpeedStats struct {
	ComparisonStats
	Curve         []SpeedPoint `json:"cumulative_first_target"`
	Day50         int          `json:"day_to_50pct"` // Zero: not reached.
	Day80         int          `json:"day_to_80pct"`
	PenalizedDays float64      `json:"mean_target_days_failure_21"`
	SuccessDays   float64      `json:"mean_target_days_success_only"`
}

type SpeedArm struct {
	Training SpeedStats `json:"training"`
	Later    SpeedStats `json:"later_period"`
	Trades   []Trade    `json:"trades"`
}

type SpeedPair struct {
	ID           string      `json:"id"`
	Policy       TradePolicy `json:"policy"`
	Confirmation string      `json:"entry_confirmation"`
	Stock        SpeedArm    `json:"stock_all"`
	Combined     SpeedArm    `json:"stock_sector_market"`
}

type SpeedSelection struct {
	Confirmation string `json:"entry_confirmation"`
	SelectionAudit
}

type SpeedWinner struct {
	Target float64 `json:"target_pct"`
	Arm    string  `json:"arm"`
	ID     string  `json:"policy_id"`
	Day80  int     `json:"training_day_to_80pct"`
}

type SpeedBatchControl struct {
	WinnerID string     `json:"winner_policy_id"`
	Arm      string     `json:"arm"`
	PolicyID string     `json:"policy_id"`
	Training SpeedStats `json:"common_entry_training"`
	Later    SpeedStats `json:"common_entry_later_period"`
}

type SpeedReport struct {
	Version      int                 `json:"schema_version"`
	Engine       string              `json:"engine"`
	SnapshotID   string              `json:"stock_snapshot_id"`
	Basis        string              `json:"classification_basis"`
	Options      GridOptions         `json:"options"`
	TopN         int                 `json:"daily_signal_limit"`
	Omissions    map[string]int      `json:"omissions"`
	Selections   []SpeedSelection    `json:"selections"`
	Pairs        []SpeedPair         `json:"pairs"`
	Winners      []SpeedWinner       `json:"training_winners"`
	BatchControl []SpeedBatchControl `json:"common_entry_batch_controls"`
	Limitations  []string            `json:"limitations"`
}

// Compare batch counts on positions that actually entered in all three
// policies. The full unmatched cohorts remain in Pairs, including unknowns.
func speedBatchControls(pairs []SpeedPair, winners []SpeedWinner) []SpeedBatchControl {
	out := []SpeedBatchControl{}
	byID := map[string]SpeedPair{}
	for _, p := range pairs {
		byID[p.ID] = p
	}
	for _, w := range winners {
		winner := byID[w.ID]
		group := []SpeedPair{}
		for _, p := range pairs {
			if p.Policy.Rule == winner.Policy.Rule && p.Policy.Target == winner.Policy.Target && p.Confirmation == winner.Confirmation {
				group = append(group, p)
			}
		}
		counts := map[string]int{}
		for _, p := range group {
			arm := p.Stock
			if w.Arm == "stock_sector_market" {
				arm = p.Combined
			}
			for _, t := range arm.Trades {
				if t.EntryDate != "" {
					counts[t.ID]++
				}
			}
		}
		for _, p := range group {
			arm := p.Stock
			if w.Arm == "stock_sector_market" {
				arm = p.Combined
			}
			matched := []Trade{}
			for _, t := range arm.Trades {
				if counts[t.ID] == len(group) {
					matched = append(matched, t)
				}
			}
			out = append(out, SpeedBatchControl{w.ID, w.Arm, p.ID, summarizeSpeed(matched, "training", nil), summarizeSpeed(matched, "later_period", nil)})
		}
	}
	return out
}

// Every point comes from the same 20-day path. An early loss or unresolved
// observation is never mistaken for quickly reaching the profit target.
func summarizeSpeed(trades []Trade, split string, decisions []SelectionDecision) SpeedStats {
	s := SpeedStats{ComparisonStats: summarizeComparison(trades, split, decisions, false), Curve: make([]SpeedPoint, 20)}
	for i := range s.Curve {
		s.Curve[i].Day = i + 1
	}
	for _, t := range trades {
		if t.Split != split || t.EntryDate == "" && t.Status == "entry_skipped" {
			continue
		}
		wait := 21
		if t.EntryDate != "" && resolvedTrade(t) && t.TargetHit && t.TargetDay >= 1 && t.TargetDay <= 20 {
			wait = t.TargetDay
			s.SuccessDays += float64(wait)
			for i := wait - 1; i < len(s.Curve); i++ {
				s.Curve[i].Targets++
			}
		}
		s.PenalizedDays += float64(wait)
	}
	if s.Samples > 0 {
		s.PenalizedDays /= float64(s.Samples)
		for i := range s.Curve {
			p := &s.Curve[i]
			p.Rate = float64(p.Targets) / float64(s.Samples)
			if s.Day50 == 0 && p.Targets*2 >= s.Samples {
				s.Day50 = p.Day
			}
			if s.Day80 == 0 && p.Targets*5 >= s.Samples*4 {
				s.Day80 = p.Day
			}
		}
	}
	if s.Targets > 0 {
		s.SuccessDays /= float64(s.Targets)
	}
	return s
}

func speedConfirmed(f ContextFeature, confirmation string) bool {
	switch confirmation {
	case "none":
		return true
	case "price_reclaim":
		return f.Close >= f.MA5
	case "momentum_turn":
		return f.RSIRising && f.MACDRising
	case "price_and_momentum":
		return f.Close >= f.MA5 && f.RSIRising && f.MACDRising
	default:
		return false
	}
}

// Filter the initial signal before ranking. Do not use the next day's prices
// or retry a rejected episode at a retrospectively favorable later signal.
func selectSpeedSignals(opps []opportunity, features map[string]map[string]ContextFeature, lookup contextLookup, arm string, topN int, confirmation string) []SelectionDecision {
	eligible := make([]opportunity, 0, len(opps))
	positions := []int{}
	out := make([]SelectionDecision, len(opps))
	for i, o := range opps {
		f, ok := features[o.code][o.bars[0].Date]
		if ok && speedConfirmed(f, confirmation) {
			eligible, positions = append(eligible, o), append(positions, i)
		} else {
			reason := "entry_not_confirmed"
			if !ok {
				reason = "missing_stock_features"
			}
			out[i] = SelectionDecision{ID: o.id, Code: o.code, Date: o.bars[0].Date, Split: o.split, StockScore: f.TrendScore, Context: lookup.at(o.code, o.bars[0].Date), Reason: reason}
		}
	}
	for j, d := range selectSignals(eligible, features, lookup, arm, topN) {
		out[positions[j]] = d
	}
	return out
}

func faster(a, b SpeedStats, aid, bid string) bool {
	ad, bd := a.Day80, b.Day80
	if ad == 0 {
		ad = 21
	}
	if bd == 0 {
		bd = 21
	}
	if ad != bd {
		return ad < bd
	}
	if math.Abs(a.PenalizedDays-b.PenalizedDays) > 1e-12 {
		return a.PenalizedDays < b.PenalizedDays
	}
	if math.Abs(a.Curve[0].Rate-b.Curve[0].Rate) > 1e-12 {
		return a.Curve[0].Rate > b.Curve[0].Rate
	}
	if math.Abs(a.Rate-b.Rate) > 1e-12 {
		return a.Rate > b.Rate
	}
	if a.Samples != b.Samples {
		return a.Samples > b.Samples
	}
	return aid < bid
}

// OptimizeSpeed changes the selection objective and initial confirmations;
// the existing execution model and two context arms remain identical.
func OptimizeSpeed(ctx context.Context, snapshot experiment.Snapshot, background MarketContext, options GridOptions) (SpeedReport, error) {
	r := SpeedReport{Version: 1, Engine: SpeedEngine, SnapshotID: snapshot.ID, Basis: background.Basis, Options: options, TopN: snapshot.TopN, Selections: []SpeedSelection{}, Pairs: []SpeedPair{}, Winners: []SpeedWinner{}, Limitations: []string{
		"Fixed no stop and 20 sellable-day deadline; every cumulative hit curve uses a single complete trade path, including all its adds and T+1 restrictions.",
		"First sellable day is the trading day after entry. Targets are net of costs and use executable intraday highs, not closing returns or same-day unsellable highs.",
		"Only initial signals are filtered by confirmation, before daily top-N ranking; adds retain the previous bottom-event and sector rules.",
		"Choose on training only: at least 30 entries, 10 dates and 10 stocks; earliest day reaching 80%, then minimum mean wait with failures/unknowns charged 21 days, then next-day rate, final rate, samples, ID. Zero day-to-80 means not reached.",
		"This is a finite grid, not a global optimum. Later history has already been explored and is not an unseen holdout.",
		"Current-only sector membership introduces retrospective classification bias. Missing initial context excludes B; needed missing add context remains unresolved.",
		"Per-stock trades are not account portfolio returns. Same-date/sector samples are dependent; different batch budgets may exclude different stocks due to minimum lot sizes.",
		"Daily bars cannot establish real intraday execution or guarantee profits. No stop removes a loss constraint; the 20-day observation deadline remains and unknown outcomes are retained in hit-rate denominators.",
	}}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if snapshot.TopN < 1 || snapshot.TopN > 100 {
		return r, errors.New("speed study requires daily top_n from 1 to 100")
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
	arms := []string{"stock_all", "stock_sector_market"}
	for _, rule := range searchRules {
		for _, confirmation := range speedConfirmations {
			decisions := map[string][]SelectionDecision{}
			opps := map[string][]opportunity{}
			for _, arm := range arms {
				decisions[arm] = selectSpeedSignals(data[rule], features, lookup, arm, r.TopN, confirmation)
				opps[arm] = selectedOpportunities(data[rule], decisions[arm], lookup, arm == "stock_sector_market")
				r.Selections = append(r.Selections, SpeedSelection{confirmation, SelectionAudit{rule, arm, decisions[arm]}})
			}
			for _, batches := range []int{1, 2, 3} {
				for _, target := range []float64{2.5, 3} {
					if err := ctx.Err(); err != nil {
						return r, err
					}
					p := TradePolicy{Rule: rule, Batches: batches, Horizon: 20, Target: target, Stop: 0}
					pair := SpeedPair{ID: trialID(p) + "/" + confirmation, Policy: p, Confirmation: confirmation}
					for i, arm := range arms {
						_, trades := evaluateGridPolicy(snapshot.Parameters, p, opps[arm], nil)
						x := SpeedArm{summarizeSpeed(trades, "training", decisions[arm]), summarizeSpeed(trades, "later_period", decisions[arm]), trades}
						if i == 0 {
							pair.Stock = x
						} else {
							pair.Combined = x
						}
					}
					r.Pairs = append(r.Pairs, pair)
				}
			}
		}
	}
	for _, target := range []float64{2.5, 3} {
		for _, arm := range arms {
			var best SpeedStats
			id := ""
			for _, p := range r.Pairs {
				s := p.Stock.Training
				if arm == "stock_sector_market" {
					s = p.Combined.Training
				}
				if p.Policy.Target == target && enough(s.TradeStats) && (id == "" || faster(s, best, p.ID, id)) {
					best, id = s, p.ID
				}
			}
			if id != "" {
				r.Winners = append(r.Winners, SpeedWinner{target, arm, id, best.Day80})
			}
		}
	}
	r.BatchControl = speedBatchControls(r.Pairs, r.Winners)
	return r, ctx.Err()
}
