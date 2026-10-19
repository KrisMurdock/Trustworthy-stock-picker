package strategy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"stock-watcher/internal/calendar"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"strconv"
	"strings"
	"time"
)

type Dataset struct {
	Adjusted market.History
	Raw      market.History
}
type Sample struct {
	Code     string
	Date     string
	LabelEnd string
	Features Features
	Rules    []string
	Outcomes map[string]Outcome
}
type Stats struct {
	Rule                 string          `json:"rule"`
	Target               float64         `json:"target_net_pct"`
	Horizon              int             `json:"holding_days"`
	Samples              int             `json:"samples"`
	Dates                int             `json:"distinct_signal_dates"`
	Resolved             int             `json:"resolved"`
	Targets              int             `json:"targets"`
	Stops                int             `json:"stops"`
	Timeouts             int             `json:"timeouts"`
	Unresolved           int             `json:"unresolved"`
	HitRate              float64         `json:"target_rate"`
	LowerBound           float64         `json:"target_rate_lower_bound"`
	MeanNet              float64         `json:"mean_resolved_net_pct"`
	MeanDays             float64         `json:"mean_resolved_holding_days"`
	WorstNet             float64         `json:"worst_resolved_net_pct"`
	MeanMAE              float64         `json:"mean_max_adverse_net_pct"`
	WorstMAE             float64         `json:"worst_max_adverse_net_pct"`
	ProfitRate           float64         `json:"resolved_profit_rate"`
	Ambiguous            int             `json:"ambiguous_trades"`
	SkippedEntries       int             `json:"skipped_entries"`
	UnknownEntries       int             `json:"unknown_entry_paths"`
	LossLimitPct         float64         `json:"net_loss_limit_pct,omitempty"`
	LossBreaches         int             `json:"net_loss_limit_breaches"`
	Profit25             int             `json:"net_profit_2_5_count"`
	Profit30             int             `json:"net_profit_3_count"`
	Profit25Rate         float64         `json:"net_profit_2_5_rate"`
	Profit30Rate         float64         `json:"net_profit_3_rate"`
	HighTouchSamples     int             `json:"high_touch_samples"`
	HighTouchUnknown     int             `json:"high_touch_unknown"`
	HighTouch25          int             `json:"high_touch_2_5_count"`
	HighTouch30          int             `json:"high_touch_3_count"`
	HighTouch25Rate      float64         `json:"high_touch_2_5_rate"`
	HighTouch30Rate      float64         `json:"high_touch_3_rate"`
	HighTouch25Days      float64         `json:"mean_first_high_touch_2_5_days"`
	HighTouch30Days      float64         `json:"mean_first_high_touch_3_days"`
	HighTouchAmbiguous   int             `json:"high_touch_order_unknown"`
	NextDay25            int             `json:"next_day_2_5_count"`
	NextDay30            int             `json:"next_day_3_count"`
	NextDay25Rate        float64         `json:"next_day_2_5_rate"`
	NextDay30Rate        float64         `json:"next_day_3_rate"`
	EntryDayKnown        int             `json:"entry_day_known"`
	EntryDayPositive     int             `json:"entry_day_positive"`
	EntryDayNonPositive  int             `json:"entry_day_nonpositive"`
	EntryDayUnknown      int             `json:"entry_day_unknown"`
	EntryDayPositiveRate float64         `json:"entry_day_positive_rate"`
	Joint25              int             `json:"joint_2_5_count"`
	Joint30              int             `json:"joint_3_count"`
	Joint25Rate          float64         `json:"joint_2_5_rate"`
	Joint30Rate          float64         `json:"joint_3_rate"`
	Touch25DaySum        int             `json:"-"`
	Touch30DaySum        int             `json:"-"`
	MAESum               float64         `json:"-"`
	Profits              int             `json:"-"`
	NetSum               float64         `json:"-"`
	DaySum               int             `json:"-"`
	DateSet              map[string]bool `json:"-"`
}
type Evaluation struct {
	Method       string `json:"method"`
	From         string `json:"from,omitempty"`
	Through      string `json:"through,omitempty"`
	SignalDates  int    `json:"signal_dates"`
	TopRanked    Stats  `json:"top_ranked"`
	Recommended  Stats  `json:"recommended"`
	PoolBaseline Stats  `json:"pool_baseline"`
	LegacyTrend  Stats  `json:"legacy_trend_baseline"`
}
type Research struct {
	Version          int             `json:"schema_version"`
	Strategy         string          `json:"strategy"`
	AsOf             string          `json:"as_of"`
	Parameters       config.Strategy `json:"parameters"`
	TrainingThrough  string          `json:"training_label_through"`
	Rules            []Stats         `json:"rule_statistics"`
	Development      Evaluation      `json:"development"`
	Holdout          Evaluation      `json:"holdout"`
	ValidationPassed bool            `json:"holdout_validation_passed"`
	SampleCount      int             `json:"sample_count"`
	Skipped          map[string]int  `json:"skipped_samples"`
	Limitations      []string        `json:"limitations"`
}
type Candidate struct {
	Code                      string                  `json:"code"`
	Name                      string                  `json:"name"`
	Features                  Features                `json:"features"`
	Score                     float64                 `json:"opportunity_score"`
	ScoreBreakdown            *ScoreBreakdown         `json:"score_breakdown,omitempty"`
	Rank                      int                     `json:"rank"`
	ObservationRank           int                     `json:"observation_rank,omitempty"`
	EligibleRank              int                     `json:"eligible_rank,omitempty"`
	ObservationScore          float64                 `json:"observation_score,omitempty"`
	EligibleScore             float64                 `json:"eligible_score,omitempty"`
	ObservationScoreBreakdown *ScoreBreakdown         `json:"observation_score_breakdown,omitempty"`
	EligibleScoreBreakdown    *ScoreBreakdown         `json:"eligible_score_breakdown,omitempty"`
	Recommended               bool                    `json:"recommended"`
	EligibleRecommended       bool                    `json:"eligible_recommended"`
	Rules                     []string                `json:"matched_rules"`
	Evidence                  *Stats                  `json:"historical_evidence,omitempty"`
	Reasons                   []string                `json:"reasons"`
	Risks                     []string                `json:"risks"`
	Plan                      *Plan                   `json:"reference_plan,omitempty"`
	Chan                      *ChanEvidence           `json:"chan,omitempty"`
	StructureGuard            *StructureGuardEvidence `json:"structure_guard,omitempty"`
}

// ScoreBreakdown records the arithmetic behind a candidate's opportunity
// score. It is additive report data so older persisted reports stay readable.
type ScoreBreakdown struct {
	Version     int     `json:"schema_version"`
	Formula     string  `json:"formula"`
	Opportunity float64 `json:"opportunity_score"`
	Final       float64 `json:"final_score"`
}
type Result struct {
	Ranking     []Candidate
	Excluded    map[string]string
	Research    Research
	GuardPassed int
}

func key(target float64, horizon int) string {
	return strconv.FormatFloat(target, 'f', 1, 64) + "/" + strconv.Itoa(horizon)
}
func horizons(s config.Strategy) []int {
	out := []int{1}
	for _, h := range []int{3, 5, s.MaxHoldingDays} {
		if h > s.MaxHoldingDays {
			continue
		}
		found := false
		for _, v := range out {
			if v == h {
				found = true
			}
		}
		if !found {
			out = append(out, h)
		}
	}
	sort.Ints(out)
	return out
}
func add(s *Stats, sample Sample, out Outcome) {
	if out.EntryDate == "" {
		s.SkippedEntries++
		if out.Status == "entry_path_unknown" {
			s.UnknownEntries++
		}
		return
	}
	s.Samples++
	if s.DateSet == nil {
		s.DateSet = map[string]bool{}
	}
	s.DateSet[sample.Date] = true
	positive := false
	if e := out.EntryDay; e != nil && e.Version == 1 && out.Status != "corporate_action" {
		s.EntryDayKnown++
		positive = e.CloseNetPct > 0
		if positive {
			s.EntryDayPositive++
		} else {
			s.EntryDayNonPositive++
		}
	}
	if !ResolvedOutcome(out) {
		s.Unresolved++
		return
	}
	s.Resolved++
	if h := out.HighTouch; h != nil && h.Version == 1 && h.ObservedDays > 0 {
		s.HighTouchSamples++
		if h.Day25 > 0 && !h.Day25Unknown {
			s.HighTouch25++
			s.Touch25DaySum += h.Day25
			if h.Day25 == 1 {
				s.NextDay25++
				if positive {
					s.Joint25++
				}
			}
		}
		if h.Day30 > 0 && !h.Day30Unknown {
			s.HighTouch30++
			s.Touch30DaySum += h.Day30
			if h.Day30 == 1 {
				s.NextDay30++
				if positive {
					s.Joint30++
				}
			}
		}
		if h.Ambiguous {
			s.HighTouchAmbiguous++
		}
	}
	s.NetSum += out.NetPct
	s.DaySum += out.HoldingDays
	s.WorstNet = math.Min(s.WorstNet, out.NetPct)
	if BreachesLossLimit(out.NetPct) {
		s.LossBreaches++
	}
	if out.NetPct >= 2.5 {
		s.Profit25++
	}
	if out.NetPct >= 3 {
		s.Profit30++
	}
	s.MAESum += out.MAE
	s.WorstMAE = math.Min(s.WorstMAE, out.MAE)
	if out.NetPct > 0 {
		s.Profits++
	}
	if out.Ambiguous {
		s.Ambiguous++
	}
	switch out.Status {
	case "target":
		s.Targets++
	case "stop", "stop_gap":
		s.Stops++
	case "timeout":
		s.Timeouts++
	}
}
func finalize(s Stats) Stats {
	s.LossLimitPct = MaxTradeLossPct
	s.HighTouchUnknown = s.Samples - s.HighTouchSamples
	s.EntryDayUnknown = s.Samples - s.EntryDayKnown
	if s.HighTouch25 > 0 {
		s.HighTouch25Days = float64(s.Touch25DaySum) / float64(s.HighTouch25)
	}
	if s.HighTouch30 > 0 {
		s.HighTouch30Days = float64(s.Touch30DaySum) / float64(s.HighTouch30)
	}
	s.Dates = len(s.DateSet)
	if s.Samples > 0 {
		s.EntryDayPositiveRate = float64(s.EntryDayPositive) / float64(s.Samples)
		s.Joint25Rate = float64(s.Joint25) / float64(s.Samples)
		s.Joint30Rate = float64(s.Joint30) / float64(s.Samples)
		s.NextDay25Rate = float64(s.NextDay25) / float64(s.Samples)
		s.NextDay30Rate = float64(s.NextDay30) / float64(s.Samples)
		s.HighTouch25Rate = float64(s.HighTouch25) / float64(s.Samples)
		s.HighTouch30Rate = float64(s.HighTouch30) / float64(s.Samples)
		s.Profit25Rate = float64(s.Profit25) / float64(s.Samples)
		s.Profit30Rate = float64(s.Profit30) / float64(s.Samples)
		n, p, z := float64(s.Samples), float64(s.Targets)/float64(s.Samples), 1.96
		s.HitRate = p
		// Cross-stock and overlapping observations are correlated. Cap effective n at dates.
		n = math.Min(n, float64(max(1, s.Dates)))
		s.LowerBound = (p + z*z/(2*n) - z*math.Sqrt(p*(1-p)/n+z*z/(4*n*n))) / (1 + z*z/n)
	}
	if s.Resolved > 0 {
		s.MeanNet = s.NetSum / float64(s.Resolved)
		s.MeanDays = float64(s.DaySum) / float64(s.Resolved)
		s.MeanMAE = s.MAESum / float64(s.Resolved)
		s.ProfitRate = float64(s.Profits) / float64(s.Resolved)
	}
	return s
}
func statsFor(samples []Sample, cutoff string, s config.Strategy) []Stats {
	stats := []Stats{}
	for _, rule := range ruleNames {
		for _, target := range []float64{2.5, 3} {
			for _, h := range horizons(s) {
				x := Stats{Rule: rule, Target: target, Horizon: h}
				for _, sample := range samples {
					if sample.LabelEnd >= cutoff {
						continue
					}
					for _, matched := range sample.Rules {
						if matched == rule {
							add(&x, sample, sample.Outcomes[key(target, h)])
							break
						}
					}
				}
				stats = append(stats, finalize(x))
			}
		}
	}
	return stats
}
func find(stats []Stats, rule string, target float64, h int) Stats {
	for _, s := range stats {
		if s.Rule == rule && s.Target == target && s.Horizon == h {
			return s
		}
	}
	return Stats{Rule: rule, Target: target, Horizon: h}
}
func rank(code, name string, f Features, stats []Stats, s config.Strategy) Candidate {
	base, formula := opportunityFormula(f)
	c := Candidate{Code: code, Name: name, Features: f, Score: base, ScoreBreakdown: &ScoreBreakdown{Version: 1, Formula: formula, Opportunity: base, Final: base}, Rules: Matches(f), Reasons: []string{}, Risks: []string{}}
	baseline := find(stats, "baseline", s.TargetNetPct, s.MaxHoldingDays)
	for _, rule := range c.Rules {
		if rule == "baseline" {
			continue
		}
		x := find(stats, rule, s.TargetNetPct, s.MaxHoldingDays)
		if x.LossBreaches > 0 || BreachesLossLimit(x.WorstNet) {
			c.Risks = append(c.Risks, "historical_net_loss_limit_breached")
			continue
		}
		if len(EvidenceFailures(x, baseline, s)) > 0 {
			continue
		}
		if c.Evidence == nil || x.LowerBound > c.Evidence.LowerBound || (x.LowerBound == c.Evidence.LowerBound && x.Rule < c.Evidence.Rule) {
			copy := x
			c.Evidence = &copy
		}
	}
	if c.Evidence != nil {
		c.Score = base*0.5 + c.Evidence.LowerBound*50
		c.ScoreBreakdown.Formula += fmt.Sprintf("；证据融合：%.1f×0.5 + 下界%.1f%%×50 = %.1f", base, c.Evidence.LowerBound*100, c.Score)
		c.ScoreBreakdown.Final = c.Score
		c.Reasons = append(c.Reasons, "historical_rule_"+c.Evidence.Rule)
	} else {
		c.Score = base * 0.5
		c.ScoreBreakdown.Formula += fmt.Sprintf("；无合格历史证据：%.1f×0.5 = %.1f", base, c.Score)
		c.ScoreBreakdown.Final = c.Score
		c.Risks = append(c.Risks, "insufficient_or_unprofitable_evidence")
	}
	if f.MA20Slope > 0 {
		c.Reasons = append(c.Reasons, "rising_ma20")
	}
	if f.Relative5 > 0 {
		c.Reasons = append(c.Reasons, "stronger_than_pool")
	}
	if f.ClosePosition >= 0.65 {
		c.Reasons = append(c.Reasons, "strong_close")
	}
	if f.Return5 > 12 || f.MA20Distance > 10 {
		c.Risks = append(c.Risks, "extended_price")
	}
	if f.ATR14Pct > 4 {
		c.Risks = append(c.Risks, "large_daily_range")
	}
	if f.UpperWick > 0.5 {
		c.Risks = append(c.Risks, "upper_wick")
	}
	c.Recommended = c.Evidence != nil && c.Score >= s.MinRankScore && f.ATR14Pct <= 4 && f.MA20Distance <= 10 && f.Return5 <= 12
	return c
}
func sortRank(candidates []Candidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Code < candidates[j].Code
		}
		return candidates[i].Score > candidates[j].Score
	})
	for i := range candidates {
		candidates[i].Rank = i + 1
	}
}
func special(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	return strings.HasPrefix(name, "ST") || strings.HasPrefix(name, "*ST") || strings.HasPrefix(name, "SST") || strings.HasPrefix(name, "S*ST") || strings.Contains(name, "退")
}

func datasets(c config.Config, histories, raw []market.History, asof string) ([]Dataset, error) {
	if err := c.Strategy.Validate(); err != nil {
		return nil, err
	}
	if c.Daily.Adjust != "qfq" {
		return nil, errors.New("short-horizon strategy requires qfq features and raw execution data")
	}
	byCode := map[string]market.History{}
	for _, h := range raw {
		if err := h.Validate(); err != nil {
			return nil, err
		}
		if _, ok := byCode[h.Code]; ok {
			return nil, errors.New("duplicate raw history")
		}
		byCode[h.Code] = h
	}
	adjusted := map[string]market.History{}
	for _, h := range histories {
		if err := h.Validate(); err != nil {
			return nil, err
		}
		if _, ok := adjusted[h.Code]; ok {
			return nil, errors.New("duplicate adjusted history")
		}
		adjusted[h.Code] = h
	}
	data := []Dataset{}
	for _, stock := range c.Stocks {
		h, ok := adjusted[stock.Code]
		r, exists := byCode[stock.Code]
		if !ok || !exists {
			continue
		}
		if h.AsOf != asof || r.AsOf != asof || h.Adjust != "qfq" || r.Adjust != "none" {
			continue
		}
		if len(h.Bars) != len(r.Bars) {
			continue
		}
		aligned := true
		for i := range h.Bars {
			if h.Bars[i].Date != r.Bars[i].Date {
				aligned = false
				break
			}
		}
		if !aligned {
			continue
		}
		data = append(data, Dataset{h, r})
	}
	return data, nil
}

// Analyze never evaluates a historical signal using labels ending on or after that signal.
// The latest holdout block freezes its evidence at the block boundary.
func Analyze(ctx context.Context, c config.Config, histories, raw []market.History, asof string) (Result, error) {
	return analyze(ctx, c, histories, raw, asof, nil)
}

func analyze(ctx context.Context, c config.Config, histories, raw []market.History, asof string, experiment *developmentOptions) (Result, error) {
	result := Result{Ranking: []Candidate{}, Excluded: map[string]string{}}
	data, err := datasets(c, histories, raw, asof)
	if err != nil {
		return result, err
	}
	if len(data) == 0 {
		return result, errors.New("incomplete or mismatched adjusted/raw pool")
	}
	present := map[string]bool{}
	for _, dataset := range data {
		present[dataset.Adjusted.Code] = true
	}
	for _, stock := range c.Stocks {
		if !present[stock.Code] {
			result.Excluded[stock.Code] = "missing_or_mismatched_data"
		}
	}
	if _, err := time.Parse("2006-01-02", asof); err != nil {
		return result, errors.New("invalid research as-of")
	}
	cal := calendar.New(c)
	s := c.Strategy
	research := Research{Version: 1, Strategy: "short_horizon_v1", AsOf: asof, Parameters: s, Skipped: map[string]int{}, Limitations: []string{
		"Daily OHLC simulations are fill assumptions, not broker executions; target requires one tick through; stop wins ambiguous bars.",
		"Current leader pool is used retrospectively: survivorship and stock-selection bias; historical ST/IPO status is unavailable.",
		"Adjusted history is a current snapshot; only scale-invariant features are used. Corporate-action windows remain unresolved in statistics.",
		"Minimum commission, cash and slippage need broker confirmation; aggregate observations overlap, not a funded portfolio or calibrated prediction.",
		"The date-capped Wilson bound is a ranking heuristic, not a calibrated confidence interval. MAE/MFE are conservative whole-bar bounds, including the exit bar.",
	}}
	samples := []Sample{}
	latest := []Candidate{}
	means := map[string]float64{}
	counts := map[string]int{}
	for _, d := range data {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		h, r := d.Adjusted, d.Raw
		if special(h.Name) {
			result.Excluded[h.Code] = "special_treatment"
			continue
		}
		if h.Bars[len(h.Bars)-1].Date != asof {
			result.Excluded[h.Code] = "stale_or_suspended"
			continue
		}
		if len(h.Bars) < 60 {
			result.Excluded[h.Code] = "insufficient_history"
			continue
		}
		if r.Bars[len(r.Bars)-1].Volume <= 0 {
			result.Excluded[h.Code] = "no_trading_volume"
			continue
		}
		for i := 59; i < len(h.Bars); i++ {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			f, err := Extract(h.Bars[:i+1])
			if err != nil {
				return result, err
			}
			means[h.Bars[i].Date] += f.Return5
			counts[h.Bars[i].Date]++
			if i == len(h.Bars)-1 {
				latest = append(latest, Candidate{Code: h.Code, Name: h.Name, Features: f})
				continue
			}
			if i+s.MaxHoldingDays+1 >= len(h.Bars) {
				continue
			}
			date := h.Bars[i].Date
			ok, err := cal.Trading(date)
			if err != nil || !ok {
				research.Skipped["outside_calendar"]++
				continue
			}
			future := r.Bars[i+1 : i+s.MaxHoldingDays+2]
			if experiment != nil && experiment.boundary != "" && future[len(future)-1].Date >= experiment.boundary {
				continue // Never calculate outcomes touching the reserved final interval.
			}
			factor := h.Bars[i].Close / r.Bars[i].Close
			corporate := false
			for j := i + 1; j <= i+s.MaxHoldingDays+1; j++ {
				if math.Abs(h.Bars[j].Close/r.Bars[j].Close/factor-1) > 0.002 {
					corporate = true
					break
				}
			}
			if corporate {
				research.Skipped["corporate_action"]++
			}
			sample := Sample{Code: h.Code, Date: date, LabelEnd: future[len(future)-1].Date, Features: f, Outcomes: map[string]Outcome{}}
			for _, target := range []float64{2.5, 3} {
				for _, days := range horizons(s) {
					parameters := s
					parameters.TargetNetPct = target
					out, err := Simulate(parameters, cal, h.Code, r.Bars[i], future, days)
					if err != nil {
						research.Skipped["outside_calendar"]++
						continue
					}
					if corporate && out.EntryDate != "" {
						out.Status = "corporate_action"
					}
					sample.Outcomes[key(target, days)] = out
				}
			}
			samples = append(samples, sample)
		}
	}
	// Relative strength uses the cross-section available at that date only.
	for i := range samples {
		samples[i].Features.Relative5 = samples[i].Features.Return5 - means[samples[i].Date]/float64(counts[samples[i].Date])
		samples[i].Rules = Matches(samples[i].Features)
	}
	if experiment != nil {
		dates := []string{}
		seen := map[string]bool{}
		for _, sample := range samples {
			if !seen[sample.Date] {
				dates = append(dates, sample.Date)
				seen[sample.Date] = true
			}
		}
		sort.Strings(dates)
		research.Development = evaluateFiltered(ctx, samples, dates, s, "", c.Selection.TopN, experiment)
		research.SampleCount = len(samples)
		if experiment.rangeFilter != nil {
			research.Limitations = append(research.Limitations, "Explicit signal-date backtest; no independent holdout is declared. Inspected dates cannot be reused as unseen validation data.")
		} else {
			research.Limitations = append(research.Limitations, "Development experiment only; final holdout outcomes have not been evaluated.")
		}
		result.Research = research
		return result, ctx.Err()
	}
	mean := 0.0
	for _, x := range latest {
		mean += x.Features.Return5
	}
	if len(latest) > 0 {
		mean /= float64(len(latest))
	}
	stats := statsFor(samples, asof, s)
	for _, x := range latest {
		x.Features.Relative5 = x.Features.Return5 - mean
		candidate := rank(x.Code, x.Name, x.Features, stats, s)
		for _, d := range data {
			if d.Raw.Code == x.Code {
				p, err := NewPlan(s, x.Code, d.Raw.Bars[len(d.Raw.Bars)-1].Close*(1+s.SlippageRate))
				if err != nil {
					candidate.Recommended = false
					candidate.Risks = append(candidate.Risks, "cash_insufficient")
				} else {
					p.ReferenceOnly = true
					candidate.Plan = &p
				}
				break
			}
		}
		result.Ranking = append(result.Ranking, candidate)
	}
	sortRank(result.Ranking)
	research.Rules = stats
	research.SampleCount = len(samples)
	for _, sample := range samples {
		if sample.LabelEnd < asof && sample.LabelEnd > research.TrainingThrough {
			research.TrainingThrough = sample.LabelEnd
		}
	}
	dateSet := map[string]bool{}
	for _, sample := range samples {
		dateSet[sample.Date] = true
	}
	dates := []string{}
	for date := range dateSet {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	boundary := ""
	if len(dates) > s.MinTrainingDays+s.HoldoutDays {
		boundary = dates[len(dates)-s.HoldoutDays]
	}
	if boundary != "" {
		research.Development = evaluate(ctx, samples, dates[:len(dates)-s.HoldoutDays], s, "", c.Selection.TopN)
		research.Holdout = evaluate(ctx, samples, dates[len(dates)-s.HoldoutDays:], s, boundary, c.Selection.TopN)
	} else {
		research.Development = evaluate(ctx, samples, dates, s, "", c.Selection.TopN)
		research.Limitations = append(research.Limitations, "Insufficient signal dates for the configured untouched holdout; no out-of-sample claim.")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	hold := research.Holdout
	research.ValidationPassed = len(HoldoutFailures(hold)) == 0
	if !research.ValidationPassed {
		for i := range result.Ranking {
			result.Ranking[i].Recommended = false
			result.Ranking[i].Risks = append(result.Ranking[i].Risks, "holdout_validation_not_passed")
		}
	}
	result.Research = research
	return result, nil
}

func evaluate(ctx context.Context, samples []Sample, dates []string, s config.Strategy, frozen string, topN int) Evaluation {
	return evaluateFiltered(ctx, samples, dates, s, frozen, topN, nil)
}

func evaluateFiltered(ctx context.Context, samples []Sample, dates []string, s config.Strategy, frozen string, topN int, experiment *developmentOptions) Evaluation {
	e := Evaluation{Method: "purged_walk_forward", TopRanked: Stats{Rule: "top_ranked", Target: s.TargetNetPct, Horizon: s.MaxHoldingDays}, Recommended: Stats{Rule: "recommended", Target: s.TargetNetPct, Horizon: s.MaxHoldingDays}, PoolBaseline: Stats{Rule: "pool_baseline", Target: s.TargetNetPct, Horizon: s.MaxHoldingDays}, LegacyTrend: Stats{Rule: "legacy_trend", Target: s.TargetNetPct, Horizon: s.MaxHoldingDays}}
	var frozenStats []Stats
	if frozen != "" {
		e.Method = "frozen_holdout"
		frozenStats = statsFor(samples, frozen, s)
	}
	for _, date := range dates {
		if experiment != nil && !experiment.rangeFilter.Includes(date) {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		stats := frozenStats
		if frozen == "" {
			stats = statsFor(samples, date, s)
		}
		base := find(stats, "baseline", s.TargetNetPct, s.MaxHoldingDays)
		if base.Dates < s.MinTrainingDays {
			continue
		}
		if e.From == "" {
			e.From = date
		}
		e.Through = date
		e.SignalDates++
		ranked := []Candidate{}
		byCode := map[string]Sample{}
		for _, sample := range samples {
			if sample.Date != date {
				continue
			}
			byCode[sample.Code] = sample
			ranked = append(ranked, rank(sample.Code, "", sample.Features, stats, s))
		}
		sortRank(ranked)
		recommended, picked := 0, 0
		for i, x := range ranked {
			sample := byCode[x.Code]
			out := sample.Outcomes[key(s.TargetNetPct, s.MaxHoldingDays)]
			add(&e.PoolBaseline, sample, out)
			allowed := experiment == nil || experiment.maxUpperWick == nil || x.Features.UpperWick <= *experiment.maxUpperWick
			allowed = allowed && (experiment == nil || !experiment.requireEvidence || x.Recommended)
			allowed = allowed && (experiment == nil || matchesNextDay(experiment.nextDayRule, x.Features))
			selected := allowed && picked < topN
			if selected {
				add(&e.TopRanked, sample, out)
				picked++
			}
			if experiment != nil && (selected || i < topN) {
				experiment.trades = append(experiment.trades, ExperimentTrade{ID: sample.Date + "/" + x.Code, Code: x.Code, Date: sample.Date, Rank: i + 1, Selected: selected, Features: sample.Features, Outcome: out})
			}
			if allowed && x.Recommended && recommended < topN {
				add(&e.Recommended, sample, out)
				recommended++
			}
			if sample.Features.Metrics.Score >= 70 && sample.Features.Metrics.MA5 > sample.Features.Metrics.MA10 && sample.Features.Metrics.MA10 > sample.Features.Metrics.MA20 && sample.Features.VolumeRatio >= 1.2 && sample.Features.Metrics.DIF > sample.Features.Metrics.DEA && sample.Features.Metrics.RSI14 >= 50 && sample.Features.Metrics.RSI14 <= 80 && sample.Features.Return1 >= -3 && sample.Features.Return1 <= 7 {
				add(&e.LegacyTrend, sample, out)
			}
		}
	}
	e.TopRanked = finalize(e.TopRanked)
	e.Recommended = finalize(e.Recommended)
	e.PoolBaseline = finalize(e.PoolBaseline)
	e.LegacyTrend = finalize(e.LegacyTrend)
	return e
}
