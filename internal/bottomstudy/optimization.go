package bottomstudy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"stock-watcher/internal/calendar"
	"stock-watcher/internal/config"
	"stock-watcher/internal/experiment"
	"stock-watcher/internal/market"
	"strings"
	"time"
)

const OptimizationEngine = "bottom_trade_grid_v1"

type GridOptions struct {
	Options
	TrainThrough string `json:"train_through"`
}

type TradeStats struct {
	Opportunities   int     `json:"opportunities"`
	Entries         int     `json:"entries"`
	Skipped         int     `json:"skipped_entries"`
	UnknownEntries  int     `json:"unknown_entries"`
	Samples         int     `json:"samples"`
	Resolved        int     `json:"resolved"`
	Unresolved      int     `json:"unresolved"`
	Dates           int     `json:"signal_dates"`
	Stocks          int     `json:"stocks"`
	Targets         int     `json:"targets"`
	Rate            float64 `json:"target_rate"`
	Positive        int     `json:"positive_net_trades"`
	ProfitRate      float64 `json:"positive_net_rate"`
	EntryPositive   int     `json:"entry_day_positive"`
	NextDay         int     `json:"next_day_targets"`
	JointNextDay    int     `json:"entry_positive_and_next_day_targets"`
	Stops           int     `json:"stops"`
	StopExceeded    int     `json:"stop_exceeded"`
	Ambiguous       int     `json:"ambiguous_trades"`
	StopBlockedT1   int     `json:"stop_blocked_by_t1"`
	TargetBlockedT1 int     `json:"target_blocked_by_t1"`
	MeanNet         float64 `json:"mean_resolved_net_pct"`
	MeanNetCash     float64 `json:"mean_resolved_profit_cash"`
	WorstNet        float64 `json:"worst_resolved_net_pct"`
	P05Net          float64 `json:"p05_resolved_net_pct"`
	TailMeanNet     float64 `json:"worst_5pct_mean_net_pct"`
	MeanDays        float64 `json:"mean_resolved_holding_days"`
	MeanBatches     float64 `json:"mean_batches_filled"`
	MeanInvested    float64 `json:"mean_invested"`
	RetainedTargets int     `json:"retained_baseline_targets"`
	LostTargets     int     `json:"lost_baseline_targets"`
	Retention       float64 `json:"baseline_target_retention"`
}

type Trial struct {
	ID         string      `json:"id"`
	Policy     TradePolicy `json:"policy"`
	Train      TradeStats  `json:"training"`
	Validation TradeStats  `json:"later_period"`
}

type Comparison struct {
	Target     float64 `json:"target_pct"`
	Batches    int     `json:"batches"`
	Baseline   string  `json:"baseline_trial"`
	Closest    string  `json:"closest_stop_trial,omitempty"`
	Compromise string  `json:"smallest_near_baseline_trial,omitempty"`
}

type Evidence struct {
	TrialID string  `json:"trial_id"`
	Trades  []Trade `json:"trades"`
}

type BatchControl struct {
	ReferenceTrial string     `json:"reference_trial"`
	Trial          Trial      `json:"trial"`
	MatchedTrain   TradeStats `json:"common_entry_training"`
	MatchedLater   TradeStats `json:"common_entry_later_period"`
}

type GridReport struct {
	Version      int             `json:"schema_version"`
	Engine       string          `json:"engine"`
	Options      GridOptions     `json:"options"`
	SnapshotID   string          `json:"snapshot_id"`
	Fees         config.Strategy `json:"frozen_cost_parameters"`
	Stocks       int             `json:"stocks"`
	Omissions    map[string]int  `json:"omissions"`
	Trials       []Trial         `json:"trials"`
	ByBatches    []Comparison    `json:"best_by_batches"`
	Best         []Comparison    `json:"best_without_stop"`
	Evidence     []Evidence      `json:"evidence"`
	BatchControl []BatchControl  `json:"batch_controls"`
	Limitations  []string        `json:"limitations"`
}

var searchRules = []string{"rsi_divergence", "macd_divergence", "rsi_persistence", "macd_contraction", "bollinger_reentry", "any", "macd_or_bollinger", "rsi_or_bollinger", "recent_confluence", "macd_and_bollinger"}
var stopGrid = []float64{3, 4, 5, 6, 7, 8, 10, 12, 15, 20}

func gridEvent(signals []Signal, i int, rule string) bool {
	s := signals[i]
	if len(s.Events) == 0 {
		return false
	}
	switch rule {
	case "macd_or_bollinger", "rsi_or_bollinger":
		prefix := "macd_"
		if rule == "rsi_or_bollinger" {
			prefix = "rsi_"
		}
		for _, event := range s.Events {
			if strings.HasPrefix(event, prefix) || event == "bollinger_reentry" {
				return true
			}
		}
		return false
	case "recent_confluence", "macd_and_bollinger":
		families := map[string]bool{}
		for j := max(0, i-2); j <= i; j++ {
			for _, event := range signals[j].Events {
				families[strings.SplitN(event, "_", 2)[0]] = true
			}
		}
		if rule == "macd_and_bollinger" {
			return families["macd"] && families["bollinger"]
		}
		return len(families) >= 2
	default:
		return has(s.Events, rule)
	}
}

func prepareGrid(ctx context.Context, snapshot experiment.Snapshot, o GridOptions) (map[string][]opportunity, map[string]int, error) {
	data := map[string][]opportunity{}
	omitted := map[string]int{}
	for _, date := range []string{o.From, o.Through, o.TrainThrough} {
		if _, err := time.Parse(time.DateOnly, date); err != nil {
			return nil, nil, errors.New("grid requires ISO from, through and train-through dates")
		}
	}
	if o.From >= o.TrainThrough || o.TrainThrough >= o.Through || o.Through > snapshot.AsOf {
		return nil, nil, errors.New("grid dates must satisfy from < train-through < through <= snapshot as-of")
	}
	if err := experiment.ValidateSnapshot(snapshot); err != nil {
		return nil, nil, err
	}
	if err := snapshot.Parameters.Validate(); err != nil {
		return nil, nil, err
	}
	c := config.Defaults()
	c.Calendar.ValidFrom, c.Calendar.ValidThrough, c.Calendar.Holidays = snapshot.Calendar.From, snapshot.Calendar.Through, snapshot.Calendar.Holidays
	cal := calendar.New(c)
	if _, err := cal.TradingDays(o.From, o.Through); err != nil {
		return nil, nil, err
	}
	raw := map[string]market.History{}
	for _, h := range snapshot.Raw {
		if h.Adjust != "none" || h.Validate() != nil || raw[h.Code].Code != "" {
			return nil, nil, errors.New("invalid or duplicate raw history")
		}
		raw[h.Code] = h
	}
	seen := map[string]bool{}
	for _, h := range snapshot.Adjusted {
		r, ok := raw[h.Code]
		if !ok || seen[h.Code] || h.Adjust != "qfq" || h.DataField != "qfqday" || h.Validate() != nil || h.AsOf != snapshot.AsOf || r.AsOf != snapshot.AsOf || len(h.Bars) != len(r.Bars) {
			return nil, nil, errors.New("grid requires aligned qfq/raw pairs")
		}
		seen[h.Code] = true
		if strings.Contains(strings.ToUpper(h.Name), "ST") || strings.Contains(h.Name, "退") {
			omitted["special_stocks"]++
			continue
		}
		end := sort.Search(len(h.Bars), func(i int) bool { return h.Bars[i].Date > o.Through })
		a, b := h.Bars[:end], r.Bars[:end]
		for i := range a {
			if a[i].Date != b[i].Date {
				return nil, nil, errors.New("qfq/raw date mismatch")
			}
		}
		signals, err := detect(ctx, a)
		if err != nil {
			return nil, nil, err
		}
		for _, rule := range searchRules {
			events := make([]bool, len(a))
			for i := range a {
				events[i] = gridEvent(signals, i, rule)
			}
			for start := warmup - 1; start < len(a); start++ {
				if !events[start] || a[start].Date < o.From {
					continue
				}
				if start+21 >= len(a) {
					omitted[rule+":insufficient_future"]++
					continue
				}
				split := "training"
				if a[start].Date > o.TrainThrough {
					split = "later_period"
				} else if a[start+21].Date > o.TrainThrough {
					omitted[rule+":crosses_training_boundary"]++
					continue
				}
				invalid := map[int]string{}
				factor := a[start].Close / b[start].Close
				for i := 1; i <= 21; i++ {
					j := start + i
					expected, err := cal.Next(b[j-1].Date)
					if err != nil || b[j].Date != expected {
						invalid[i] = "missing_session"
					} else if math.Abs(a[j].Close/b[j].Close/factor-1) > .002 {
						invalid[i] = "corporate_action"
					}
				}
				data[rule] = append(data[rule], opportunity{h.Code + "/" + a[start].Date, h.Code, h.Name, split, b[start : start+22], events[start : start+22], invalid})
				start += 20 // Next start may be the previous deadline's close.
			}
		}
	}
	if len(seen) == 0 {
		return nil, nil, errors.New("grid has no paired histories")
	}
	return data, omitted, nil
}

func trialID(p TradePolicy) string {
	return fmt.Sprintf("%s/b%d/h%d/t%.1f/s%g", p.Rule, p.Batches, p.Horizon, p.Target, p.Stop)
}

func evaluateGridPolicy(s config.Strategy, p TradePolicy, opportunities []opportunity, baseline map[string]Trade) (Trial, []Trade) {
	t := Trial{ID: trialID(p), Policy: p}
	trades := make([]Trade, 0, len(opportunities))
	for _, o := range opportunities {
		trades = append(trades, simulateTrade(s, p, o))
	}
	t.Train = tradeStats(trades, "training", baseline)
	t.Validation = tradeStats(trades, "later_period", baseline)
	return t, trades
}

func enough(s TradeStats) bool { return s.Entries >= 30 && s.Dates >= 10 && s.Stocks >= 10 }

// Compare ONLY training metrics. The later-period results never select a policy.
func better(a, b Trial) bool {
	if math.Abs(a.Train.Rate-b.Train.Rate) > 1e-12 {
		return a.Train.Rate > b.Train.Rate
	}
	if math.Abs(a.Train.MeanDays-b.Train.MeanDays) > 1e-12 {
		return a.Train.MeanDays < b.Train.MeanDays
	}
	if math.Abs(a.Train.MeanNet-b.Train.MeanNet) > 1e-12 {
		return a.Train.MeanNet > b.Train.MeanNet
	}
	if a.Train.Entries != b.Train.Entries {
		return a.Train.Entries > b.Train.Entries
	}
	return a.ID < b.ID
}

func Optimize(ctx context.Context, snapshot experiment.Snapshot, options GridOptions) (GridReport, error) {
	r := GridReport{Version: 1, Engine: OptimizationEngine, Options: options, SnapshotID: snapshot.ID, Fees: snapshot.Parameters, Stocks: len(snapshot.Adjusted), Trials: []Trial{}, Best: []Comparison{}, ByBatches: []Comparison{}, Evidence: []Evidence{}, Limitations: []string{
		"Finite preregistered grid, not a global optimum or future success guarantee.",
		"Later-period check reuses previously explored history; it is not an unseen holdout.",
		"Independent per-stock trades, not an account equity or simultaneous capital allocation backtest.",
		"Daily-bar target/stop ambiguity uses stop-first; T+1, gaps and locked limits can exceed the nominal stop.",
		"Every rule has its own non-overlapping 21-bar opportunities; all horizons/stops for that rule share those opportunities.",
		"Target opportunities are cost-aware simulated fills, not a guarantee of real market execution.",
	}}
	data, omitted, err := prepareGrid(ctx, snapshot, options)
	if err != nil {
		return r, err
	}
	r.Omissions = omitted
	winners := map[string]Trial{}
	for _, rule := range searchRules {
		for batches := 1; batches <= 5; batches++ {
			for _, days := range []int{1, 3, 5, 10, 20} {
				for _, target := range []float64{2.5, 3} {
					if err := ctx.Err(); err != nil {
						return r, err
					}
					p := TradePolicy{rule, batches, days, target, 0}
					t, _ := evaluateGridPolicy(snapshot.Parameters, p, data[rule], nil)
					r.Trials = append(r.Trials, t)
					key := fmt.Sprintf("%.1f/%d", target, batches)
					old, found := winners[key]
					if enough(t.Train) && (!found || better(t, old)) {
						winners[key] = t
					}
				}
			}
		}
	}
	best := map[float64]Trial{}
	comparisons := map[string]Comparison{}
	evidence := map[string][]Trade{}
	for _, target := range []float64{2.5, 3} {
		for batches := 1; batches <= 5; batches++ {
			base, ok := winners[fmt.Sprintf("%.1f/%d", target, batches)]
			if !ok {
				continue
			}
			_, bt := evaluateGridPolicy(snapshot.Parameters, base.Policy, data[base.Policy.Rule], nil)
			baseline := map[string]Trade{}
			for _, t := range bt {
				baseline[t.ID] = t
			}
			comparison := Comparison{Target: target, Batches: batches, Baseline: base.ID}
			evidence[base.ID] = bt
			closestRate := -1.0
			for _, stop := range stopGrid {
				if err := ctx.Err(); err != nil {
					return r, err
				}
				p := base.Policy
				p.Stop = stop
				t, trades := evaluateGridPolicy(snapshot.Parameters, p, data[p.Rule], baseline)
				r.Trials = append(r.Trials, t)
				evidence[t.ID] = trades
				if t.Train.Rate > closestRate+1e-12 {
					comparison.Closest, closestRate = t.ID, t.Train.Rate
				}
				if comparison.Compromise == "" && base.Train.Rate-t.Train.Rate <= .02+1e-12 && t.Train.Retention >= .95-1e-12 {
					comparison.Compromise = t.ID
				}
			}
			r.ByBatches = append(r.ByBatches, comparison)
			comparisons[base.ID] = comparison
			old, found := best[target]
			if !found || better(base, old) {
				best[target] = base
			}
		}
	}
	for _, target := range []float64{2.5, 3} {
		if base, ok := best[target]; ok {
			c := comparisons[base.ID]
			r.Best = append(r.Best, c)
			seen := map[string]bool{}
			for _, id := range []string{c.Baseline, c.Closest, c.Compromise} {
				if id != "" && !seen[id] {
					r.Evidence = append(r.Evidence, Evidence{id, evidence[id]})
					seen[id] = true
				}
			}
			// Hold signal rule, horizon and stop fixed to isolate batch count.
			// Also report the intersection of fundable first entries so that
			// excluding expensive stocks cannot masquerade as a batching gain.
			for _, reference := range r.Trials {
				if !seen[reference.ID] {
					continue
				}
				var controls []Trial
				var paths [][]Trade
				entryCount := map[string]int{}
				for n := 1; n <= 5; n++ {
					p := reference.Policy
					p.Batches = n
					t, trades := evaluateGridPolicy(snapshot.Parameters, p, data[p.Rule], nil)
					controls, paths = append(controls, t), append(paths, trades)
					for _, t := range trades {
						if t.EntryDate != "" {
							entryCount[t.ID]++
						}
					}
				}
				for i, t := range controls {
					matched := []Trade{}
					for _, tr := range paths[i] {
						if entryCount[tr.ID] == 5 {
							matched = append(matched, tr)
						}
					}
					r.BatchControl = append(r.BatchControl, BatchControl{reference.ID, t, tradeStats(matched, "training", nil), tradeStats(matched, "later_period", nil)})
				}
			}
		}
	}
	return r, ctx.Err()
}
