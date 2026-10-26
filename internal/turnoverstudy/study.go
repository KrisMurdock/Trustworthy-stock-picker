// Package turnoverstudy runs a controlled, point-in-time comparison between the
// existing first-stage feature proxy and the same proxy plus turnover/amount
// features. It is research-only and never changes production recommendations.
package turnoverstudy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/turnover"
)

const ReportSchemaVersion = 1

var BaselineFeatureNames = []string{
	"atr14_pct",
	"low20_distance",
	"return5",
	"rsi14",
	"touch_rate20",
	"base_score",
	"volume_ratio",
	"high20_distance",
}

const (
	FeatureSetFull           = "full"
	FeatureSetTurnover       = "turnover"
	FeatureSetAmount         = "amount"
	FeatureSetAmountLevel    = "amount-level"
	FeatureSetAmountDynamics = "amount-dynamics"
	FeatureSetAmountSize     = "amount-size"
)

func CandidateFeatureNames() []string {
	return CandidateFeatureNamesFor(FeatureSetFull)
}

func CandidateFeatureNamesFor(featureSet string) []string {
	out := append([]string(nil), BaselineFeatureNames...)
	return append(out, AdditionalFeatureNames(featureSet)...)
}

func AdditionalFeatureNames(featureSet string) []string {
	switch featureSet {
	case FeatureSetTurnover:
		return append([]string(nil), turnover.TurnoverFeatureNames...)
	case FeatureSetAmount:
		return append([]string(nil), turnover.AmountFeatureNames...)
	case FeatureSetAmountLevel:
		return append([]string(nil), turnover.AmountLevelFeatureNames...)
	case FeatureSetAmountDynamics:
		return append([]string(nil), turnover.AmountDynamicsFeatureNames...)
	case FeatureSetAmountSize:
		out := append([]string(nil), turnover.AmountFeatureNames...)
		return append(out, turnover.FloatMcapFeatureNames...)
	default:
		return append([]string(nil), turnover.FeatureNames...)
	}
}

func normalizeFeatureSet(featureSet string) (string, error) {
	switch featureSet {
	case "", FeatureSetFull:
		return FeatureSetFull, nil
	case FeatureSetTurnover, FeatureSetAmount, FeatureSetAmountLevel, FeatureSetAmountDynamics, FeatureSetAmountSize:
		return featureSet, nil
	default:
		return "", fmt.Errorf("unknown feature set %q", featureSet)
	}
}

type Options struct {
	From          string
	Through       string
	TopN          int
	Horizon       int
	TradeCash     float64
	MinTrainDays  int
	RetrainEvery  int
	Ridge         float64
	Bootstrap     int
	BootstrapSeed int64
	FeatureSet    string
	// PlaceboSeed, when non-zero, permutes each date's turnover feature block
	// across stocks before fitting. It preserves the block's joint distribution
	// while destroying its link to the actual stock, providing a capacity-only
	// control for the incremental-feature test.
	PlaceboSeed int64
}

type Data struct {
	Stocks      []config.Stock
	Adjusted    map[string][]market.Bar
	Raw         map[string][]market.Bar
	Metrics     map[string][]market.DailyMetric
	Calendar    *calendar.Calendar
	TradingDays []string
}

type Aggregate struct {
	EvaluationDates      int            `json:"evaluation_dates"`
	Slots                int            `json:"slots"`
	Executed             int            `json:"executed"`
	Skipped              int            `json:"skipped"`
	Unresolved           int            `json:"unresolved"`
	MeanExecutedNetPct   float64        `json:"mean_executed_net_pct"`
	MeanSlotNetPct       float64        `json:"mean_slot_net_pct"`
	WinRatePct           float64        `json:"win_rate_pct"`
	TargetRatePct        float64        `json:"target_rate_pct"`
	StopRatePct          float64        `json:"stop_rate_pct"`
	WorstNetPct          float64        `json:"worst_net_pct"`
	BestNetPct           float64        `json:"best_net_pct"`
	MeanMfePct           float64        `json:"mean_mfe_pct"`
	MeanMaePct           float64        `json:"mean_mae_pct"`
	CohortCount          int            `json:"cohort_count"`
	CohortCompoundedPct  float64        `json:"cohort_compounded_pct"`
	CohortMaxDrawdownPct float64        `json:"cohort_max_drawdown_pct"`
	OutcomeStatuses      map[string]int `json:"outcome_statuses"`
}

type Bootstrap struct {
	Method       string  `json:"method"`
	Resamples    int     `json:"resamples"`
	Seed         int64   `json:"seed"`
	BlockLength  int     `json:"block_length"`
	MeanNetDiff  float64 `json:"mean_slot_net_diff_pct"`
	Lower95Pct   float64 `json:"lower_95_pct"`
	Upper95Pct   float64 `json:"upper_95_pct"`
	PositiveRate float64 `json:"positive_rate"`
}

type Report struct {
	SchemaVersion int                 `json:"schema_version"`
	ModelID       string              `json:"model_id"`
	FeatureSet    string              `json:"feature_set"`
	Placebo       bool                `json:"placebo"`
	PlaceboSeed   int64               `json:"placebo_seed,omitempty"`
	From          string              `json:"from"`
	Through       string              `json:"through"`
	TradeCash     float64             `json:"trade_cash"`
	FeatureNames  []string            `json:"feature_names"`
	FeatureGroups map[string][]string `json:"feature_groups"`
	Data          DataCoverage        `json:"data"`
	DateRange     DateCoverage        `json:"date_range"`
	Baseline      Aggregate           `json:"baseline"`
	Candidate     Aggregate           `json:"candidate"`
	Increment     Increment           `json:"increment"`
	Bootstrap     Bootstrap           `json:"bootstrap"`
	Coefficients  []Coefficient       `json:"coefficients"`
	Notes         []string            `json:"notes"`
}

type DataCoverage struct {
	Stocks              int            `json:"stocks"`
	MetricStocks        int            `json:"metric_stocks"`
	MetricRows          int            `json:"metric_rows"`
	MetricFirstDate     string         `json:"metric_first_date"`
	MetricLastDate      string         `json:"metric_last_date"`
	AdjustedFirstDate   string         `json:"adjusted_first_date"`
	AdjustedLastDate    string         `json:"adjusted_last_date"`
	FeatureStocks       int            `json:"feature_stocks"`
	FeatureRows         int            `json:"feature_rows"`
	TurnoverFeatureRows int            `json:"turnover_feature_rows"`
	ResolvedLabels      int            `json:"resolved_labels"`
	PositiveLabels      int            `json:"positive_labels"`
	LabelCoveragePct    float64        `json:"label_coverage_pct"`
	PositiveRatePct     float64        `json:"positive_rate_pct"`
	OutcomeStatuses     map[string]int `json:"outcome_statuses"`
}

type DateCoverage struct {
	FirstSignalDate    string `json:"first_signal_date"`
	LastSignalDate     string `json:"last_signal_date"`
	FirstEvalDate      string `json:"first_eval_date"`
	LastEvalDate       string `json:"last_eval_date"`
	SignalDates        int    `json:"signal_dates"`
	MaturedSignalDates int    `json:"matured_signal_dates"`
	EvalDates          int    `json:"eval_dates"`
	TrainingDates      int    `json:"training_dates"`
}

type Increment struct {
	MeanExecutedNetPct  float64 `json:"mean_executed_net_pct"`
	MeanSlotNetPct      float64 `json:"mean_slot_net_pct"`
	WinRatePct          float64 `json:"win_rate_pct"`
	TargetRatePct       float64 `json:"target_rate_pct"`
	StopRatePct         float64 `json:"stop_rate_pct"`
	CohortCompoundedPct float64 `json:"cohort_compounded_pct"`
}

type Coefficient struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

type rawRow struct {
	date, code    string
	values        []float64
	outcome       strategy.Outcome
	label         bool
	labelKnown    bool
	trendScore    int
	ma20Slope     float64
	ma20Distance  float64
	closePosition float64
	upperWick     float64
	atr14         float64
}

func Run(ctx context.Context, data Data, opts Options) (Report, error) {
	opts = normalizeOptions(opts)
	featureSet, err := normalizeFeatureSet(opts.FeatureSet)
	if err != nil {
		return Report{}, err
	}
	opts.FeatureSet = featureSet
	candidateNames := CandidateFeatureNamesFor(featureSet)
	if opts.TradeCash < 1000 || opts.TradeCash > 1e8 {
		return Report{}, errors.New("turnover study trade cash must be between 1000 and 100000000")
	}
	modelID := "turnover_study_v1"
	if opts.PlaceboSeed != 0 {
		modelID = "turnover_study_placebo_v1"
	}
	report := Report{SchemaVersion: ReportSchemaVersion, ModelID: modelID, FeatureSet: featureSet, Placebo: opts.PlaceboSeed != 0, PlaceboSeed: opts.PlaceboSeed, FeatureNames: candidateNames,
		FeatureGroups: map[string][]string{
			"baseline":        append([]string(nil), BaselineFeatureNames...),
			"turnover":        append([]string(nil), turnover.TurnoverFeatureNames...),
			"amount":          append([]string(nil), turnover.AmountFeatureNames...),
			"amount_level":    append([]string(nil), turnover.AmountLevelFeatureNames...),
			"amount_dynamics": append([]string(nil), turnover.AmountDynamicsFeatureNames...),
			"float_mcap":      append([]string(nil), turnover.FloatMcapFeatureNames...),
			"candidate":       append([]string(nil), candidateNames...),
		},
		Data: DataCoverage{Stocks: len(data.Stocks)}, Notes: []string{
			"Baseline is the first-stage feature proxy: ATR, low20 distance, return5, RSI14, touch_rate20, base_score, volume_ratio, high20 distance; base_score includes the upper-wick penalty and 0-100 clamp used by production.",
			"Turnover features are point-in-time Sohu turn/amount observations; no future observation is used. Float market value is derived from amount / turnover rate as a size control.",
			"Labels are next-session-open entries with the repository's 3% target, 3% stop, fees and T+1 daily simulation.",
			"Trade cash is the per-position budget; a minimum board lot that cannot be funded is recorded as cash_insufficient rather than silently resized or dropped from the ranking.",
			"Walk-forward training uses only labels whose five-session horizon closed strictly before each evaluation date.",
			"The baseline is a refitted proxy of the first-stage feature set, not the frozen production logistic artifact; this isolates the feature increment but is not a production replay.",
			"The study covers the saved qfq window only. A short single-regime sample cannot establish durable edge.",
			"Results are research statistics, not a promise of future returns or a production recommendation.",
		}}
	if opts.PlaceboSeed != 0 {
		report.Notes = append(report.Notes, "Placebo control: the turnover feature block was permuted across stocks within each date; the candidate model therefore has the same extra capacity but no stock-specific liquidity signal.")
	}
	if featureSet != FeatureSetFull {
		report.Notes = append(report.Notes, "Ablation mode: the candidate adds only the selected feature group; baseline and trading simulation are unchanged.")
	}
	if err := validateData(data); err != nil {
		return report, err
	}
	metricRows, metricFirst, metricLast := 0, "", ""
	for _, metrics := range data.Metrics {
		metricRows += len(metrics)
		for _, metric := range metrics {
			if metricFirst == "" || metric.Date < metricFirst {
				metricFirst = metric.Date
			}
			if metric.Date > metricLast {
				metricLast = metric.Date
			}
		}
	}
	adjustedFirst, adjustedLast := "", ""
	for _, bars := range data.Adjusted {
		if len(bars) == 0 {
			continue
		}
		if adjustedFirst == "" || bars[0].Date < adjustedFirst {
			adjustedFirst = bars[0].Date
		}
		if last := bars[len(bars)-1].Date; last > adjustedLast {
			adjustedLast = last
		}
	}
	dateIndex := make(map[string]int, len(data.TradingDays))
	for i, day := range data.TradingDays {
		dateIndex[day] = i
	}
	raw, coverage, first, last, err := buildRows(ctx, data, opts, dateIndex)
	if err != nil {
		return report, err
	}
	report.Data = coverage
	report.Data.MetricRows = metricRows
	report.Data.MetricFirstDate = metricFirst
	report.Data.MetricLastDate = metricLast
	report.Data.AdjustedFirstDate = adjustedFirst
	report.Data.AdjustedLastDate = adjustedLast
	report.From, report.Through = first, last
	report.TradeCash = opts.TradeCash
	if len(raw) == 0 {
		return report, errors.New("no eligible turnover-study rows")
	}
	byDate := map[string][]rawRow{}
	for _, row := range raw {
		byDate[row.date] = append(byDate[row.date], row)
	}
	dates := make([]string, 0, len(byDate))
	for date := range byDate {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	if opts.PlaceboSeed != 0 {
		rng := rand.New(rand.NewSource(opts.PlaceboSeed))
		for _, date := range dates {
			shuffleTurnoverBlock(byDate[date], len(BaselineFeatureNames), rng)
		}
	}
	for _, date := range dates {
		standardize(byDate[date])
	}
	report.DateRange.SignalDates = len(dates)
	if len(dates) > 0 {
		report.DateRange.FirstSignalDate, report.DateRange.LastSignalDate = dates[0], dates[len(dates)-1]
	}
	// A label is usable only after the full horizon has elapsed.
	labeledDates := make([]string, 0, len(dates))
	for _, date := range dates {
		idx, ok := dateIndex[date]
		if ok && idx+opts.Horizon+1 < len(data.TradingDays) {
			labeledDates = append(labeledDates, date)
		}
	}
	sort.Strings(labeledDates)
	report.DateRange.MaturedSignalDates = len(labeledDates)
	baseline, candidate, bootstrap, coefficients, evalDates, trainDates, err := walkForward(ctx, data, byDate, dates, dateIndex, opts)
	if err != nil {
		return report, err
	}
	report.Baseline = baseline
	report.Candidate = candidate
	report.Bootstrap = bootstrap
	report.Coefficients = coefficients
	report.DateRange.EvalDates = len(evalDates)
	report.DateRange.TrainingDates = trainDates
	if len(evalDates) > 0 {
		report.DateRange.FirstEvalDate, report.DateRange.LastEvalDate = evalDates[0], evalDates[len(evalDates)-1]
	}
	report.Increment = Increment{
		MeanExecutedNetPct:  candidate.MeanExecutedNetPct - baseline.MeanExecutedNetPct,
		MeanSlotNetPct:      candidate.MeanSlotNetPct - baseline.MeanSlotNetPct,
		WinRatePct:          candidate.WinRatePct - baseline.WinRatePct,
		TargetRatePct:       candidate.TargetRatePct - baseline.TargetRatePct,
		StopRatePct:         candidate.StopRatePct - baseline.StopRatePct,
		CohortCompoundedPct: candidate.CohortCompoundedPct - baseline.CohortCompoundedPct,
	}
	return report, nil
}

func shuffleTurnoverBlock(rows []rawRow, offset int, rng *rand.Rand) {
	if len(rows) < 2 || offset <= 0 {
		return
	}
	block := make([][]float64, len(rows))
	for i := range rows {
		if offset >= len(rows[i].values) {
			return
		}
		block[i] = append([]float64(nil), rows[i].values[offset:]...)
	}
	order := rng.Perm(len(rows))
	for i := range rows {
		copy(rows[i].values[offset:], block[order[i]])
	}
}

func normalizeOptions(opts Options) Options {
	if opts.FeatureSet == "" {
		opts.FeatureSet = FeatureSetFull
	}
	if opts.TopN <= 0 {
		opts.TopN = 5
	}
	if opts.Horizon <= 0 {
		opts.Horizon = 5
	}
	if opts.TradeCash <= 0 {
		opts.TradeCash = config.DefaultStrategy().TradeCash
	}
	if opts.MinTrainDays <= 0 {
		opts.MinTrainDays = 40
	}
	if opts.RetrainEvery <= 0 {
		opts.RetrainEvery = 5
	}
	if opts.Ridge <= 0 {
		opts.Ridge = 1.0
	}
	if opts.Bootstrap <= 0 {
		opts.Bootstrap = 2000
	}
	if opts.BootstrapSeed == 0 {
		opts.BootstrapSeed = 20261026
	}
	return opts
}

func validateData(data Data) error {
	if data.Calendar == nil || len(data.TradingDays) == 0 || len(data.Adjusted) == 0 || len(data.Raw) == 0 || len(data.Metrics) == 0 {
		return errors.New("turnover study data is incomplete")
	}
	if len(data.Stocks) == 0 {
		return errors.New("turnover study stock pool is empty")
	}
	return nil
}

func buildRows(ctx context.Context, data Data, opts Options, dateIndex map[string]int) ([]rawRow, DataCoverage, string, string, error) {
	coverage := DataCoverage{Stocks: len(data.Stocks), MetricStocks: len(data.Metrics), OutcomeStatuses: map[string]int{}}
	out := []rawRow{}
	featureStocks := map[string]bool{}
	first, last := "", ""
	featureIndexes := additionalFeatureIndexes(opts.FeatureSet)
	for _, day := range data.TradingDays {
		if opts.From != "" && day < opts.From || opts.Through != "" && day > opts.Through {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, coverage, "", "", err
		}
		var dayRows []rawRow
		simConfig := config.DefaultStrategy()
		simConfig.TradeCash = opts.TradeCash
		for _, stock := range data.Stocks {
			adjusted := data.Adjusted[stock.Code]
			rawBars := data.Raw[stock.Code]
			idx := barIndex(adjusted, day)
			rawIdx := barIndex(rawBars, day)
			if idx < 0 || rawIdx < 0 || idx < 64 {
				continue
			}
			// Keep training and evaluation strictly inside the frozen horizon.
			// A near-end signal without entry plus five exit sessions is not a label.
			if dateIndex[day]+opts.Horizon+1 >= len(data.TradingDays) {
				continue
			}
			metrics := data.Metrics[stock.Code]
			if len(metrics) == 0 {
				continue
			}
			tf, err := turnover.Extract(metrics, day)
			if err != nil {
				continue
			}
			features, err := strategy.Extract(adjusted[:idx+1])
			if err != nil {
				continue
			}
			low, ok := priorLow20(adjusted[:idx+1])
			if !ok {
				continue
			}
			lowDistance := (adjusted[idx].Close/low - 1) * 100
			touch := touchRate20(adjusted[:idx+1])
			outcome, err := strategy.Simulate(simConfig, data.Calendar, stock.Code, rawBars[rawIdx], rawBars[rawIdx+1:], opts.Horizon)
			if err != nil {
				return nil, coverage, "", "", err
			}
			values := []float64{
				features.ATR14Pct, lowDistance, features.Return5, features.Metrics.RSI14, touch, 0,
				features.VolumeRatio, features.High20Distance,
			}
			turnoverValues := tf.Values()
			for _, index := range featureIndexes {
				values = append(values, turnoverValues[index])
			}
			row := rawRow{date: day, code: stock.Code, values: values, outcome: outcome,
				trendScore: features.Metrics.Score, ma20Slope: features.MA20Slope,
				ma20Distance: features.MA20Distance, closePosition: features.ClosePosition,
				upperWick: features.UpperWick, atr14: features.ATR14Pct}
			row.labelKnown = strategy.ResolvedOutcome(outcome)
			row.label = row.labelKnown && outcome.NetPct > 0
			coverage.OutcomeStatuses[outcome.Status]++
			dayRows = append(dayRows, row)
		}
		if len(dayRows) == 0 {
			continue
		}
		poolReturn5 := 0.0
		for _, row := range dayRows {
			poolReturn5 += row.values[2]
		}
		poolReturn5 /= float64(len(dayRows))
		for i := range dayRows {
			row := &dayRows[i]
			row.values[5] = baseScore(row.trendScore, row.values[2], poolReturn5, row.ma20Slope, row.ma20Distance, row.closePosition, row.values[6], row.atr14, row.upperWick)
		}
		for _, row := range dayRows {
			out = append(out, row)
		}
		if first == "" || day < first {
			first = day
		}
		if day > last {
			last = day
		}
		coverage.FeatureRows += len(dayRows)
		if len(dayRows[0].values) == len(CandidateFeatureNamesFor(opts.FeatureSet)) {
			coverage.TurnoverFeatureRows += len(dayRows)
		}
	}
	for _, row := range out {
		featureStocks[row.code] = true
	}
	coverage.FeatureStocks = len(featureStocks)
	for _, row := range out {
		if row.labelKnown {
			coverage.ResolvedLabels++
		}
		if row.label {
			coverage.PositiveLabels++
		}
	}
	if coverage.FeatureRows > 0 {
		coverage.LabelCoveragePct = float64(coverage.ResolvedLabels) / float64(coverage.FeatureRows) * 100
	}
	if coverage.ResolvedLabels > 0 {
		coverage.PositiveRatePct = float64(coverage.PositiveLabels) / float64(coverage.ResolvedLabels) * 100
	}
	return out, coverage, first, last, nil
}

func additionalFeatureIndexes(featureSet string) []int {
	indexes := make(map[string]int, len(turnover.FeatureNames))
	for i, name := range turnover.FeatureNames {
		indexes[name] = i
	}
	names := AdditionalFeatureNames(featureSet)
	out := make([]int, 0, len(names))
	for _, name := range names {
		out = append(out, indexes[name])
	}
	return out
}

func baseScore(trendScore int, return5, poolReturn5, ma20Slope, ma20Distance, closePosition, volumeRatio, atr14, upperWick float64) float64 {
	value := float64(trendScore)*0.25 + 25
	if ma20Slope > 0 {
		value += 10
	}
	if return5-poolReturn5 > 0 {
		value += 10
	}
	if closePosition >= 0.65 {
		value += 10
	}
	if volumeRatio >= 1 && volumeRatio <= 2.5 {
		value += 10
	}
	if atr14 >= 1 && atr14 <= 4 {
		value += 10
	}
	if ma20Distance > 10 || return5 > 12 {
		value -= 20
	}
	if upperWick > 0.5 {
		value -= 10
	}
	return math.Max(0, math.Min(100, value))
}

func standardize(rows []rawRow) {
	for column := range rows[0].values {
		mean, deviation := 0.0, 0.0
		for _, row := range rows {
			mean += row.values[column]
		}
		mean /= float64(len(rows))
		for _, row := range rows {
			delta := row.values[column] - mean
			deviation += delta * delta
		}
		deviation = math.Sqrt(deviation / float64(len(rows)))
		for i := range rows {
			if deviation <= 1e-12 {
				rows[i].values[column] = 0
				continue
			}
			value := (rows[i].values[column] - mean) / deviation
			rows[i].values[column] = math.Max(-3, math.Min(3, value))
		}
	}
}

type aggregateAccumulator struct {
	slots, executed, skipped, unresolved int
	netSum, mfeSum, maeSum               float64
	wins, targets, stops                 int
	worst, best                          float64
	daily                                []float64
	statuses                             map[string]int
}

func (a *aggregateAccumulator) add(out strategy.Outcome) {
	a.slots++
	if a.statuses == nil {
		a.statuses = map[string]int{}
	}
	a.statuses[out.Status]++
	if out.EntryDate == "" {
		a.skipped++
		a.daily = append(a.daily, 0)
		return
	}
	if !strategy.ResolvedOutcome(out) {
		a.unresolved++
		a.daily = append(a.daily, 0)
		return
	}
	a.executed++
	a.netSum += out.NetPct
	a.mfeSum += out.MFE
	a.maeSum += out.MAE
	if out.NetPct > 0 {
		a.wins++
	}
	if out.NetPct >= 3 {
		a.targets++
	}
	if out.NetPct <= -3 {
		a.stops++
	}
	if a.executed == 1 || out.NetPct < a.worst {
		a.worst = out.NetPct
	}
	if a.executed == 1 || out.NetPct > a.best {
		a.best = out.NetPct
	}
	a.daily = append(a.daily, out.NetPct)
}

func (a aggregateAccumulator) finish() Aggregate {
	out := Aggregate{Slots: a.slots, Executed: a.executed, Skipped: a.skipped, Unresolved: a.unresolved, OutcomeStatuses: a.statuses}
	if a.executed > 0 {
		out.MeanExecutedNetPct = a.netSum / float64(a.executed)
		out.WinRatePct = float64(a.wins) / float64(a.executed) * 100
		out.TargetRatePct = float64(a.targets) / float64(a.executed) * 100
		out.StopRatePct = float64(a.stops) / float64(a.executed) * 100
		out.WorstNetPct = a.worst
		out.BestNetPct = a.best
		out.MeanMfePct = a.mfeSum / float64(a.executed)
		out.MeanMaePct = a.maeSum / float64(a.executed)
	}
	for _, value := range a.daily {
		out.MeanSlotNetPct += value
	}
	if len(a.daily) > 0 {
		out.MeanSlotNetPct /= float64(len(a.daily))
	}
	return out
}

func walkForward(ctx context.Context, data Data, byDate map[string][]rawRow, dates []string, dateIndex map[string]int, opts Options) (Aggregate, Aggregate, Bootstrap, []Coefficient, []string, int, error) {
	if len(dates) < opts.MinTrainDays+opts.Horizon+1 {
		return Aggregate{}, Aggregate{}, Bootstrap{}, nil, nil, 0, errors.New("not enough dates for walk-forward validation")
	}
	candidateNames := CandidateFeatureNamesFor(opts.FeatureSet)
	candidateFeatures := len(candidateNames)
	baseAgg := aggregateAccumulator{worst: math.Inf(1), best: math.Inf(-1)}
	candAgg := aggregateAccumulator{worst: math.Inf(1), best: math.Inf(-1)}
	baseDaily, candDaily := []float64{}, []float64{}
	evalDates := []string{}
	var lastBetaBase, lastBetaCand []float64
	retrainAt := -1
	trainingDates := 0
	for i, date := range dates {
		if err := ctx.Err(); err != nil {
			return Aggregate{}, Aggregate{}, Bootstrap{}, nil, nil, 0, err
		}
		idx, ok := dateIndex[date]
		if !ok || idx+opts.Horizon+1 >= len(data.TradingDays) {
			continue
		}
		train := []rawRow{}
		for _, prior := range dates {
			priorIdx := dateIndex[prior]
			// The exit bar of a training label must close strictly before
			// the evaluation signal close, otherwise the label leaks the
			// evaluation day's own outcome.
			if priorIdx+opts.Horizon+1 >= idx {
				continue
			}
			for _, row := range byDate[prior] {
				if strategy.ResolvedOutcome(row.outcome) {
					train = append(train, row)
				}
			}
		}
		trainDates := map[string]bool{}
		for _, row := range train {
			trainDates[row.date] = true
		}
		if len(trainDates) < opts.MinTrainDays {
			continue
		}
		if retrainAt < 0 || i-retrainAt >= opts.RetrainEvery {
			lastBetaBase = fitLogistic(train, len(BaselineFeatureNames), opts.Ridge)
			lastBetaCand = fitLogistic(train, candidateFeatures, opts.Ridge)
			retrainAt = i
			trainingDates = len(trainDates)
		}
		if lastBetaBase == nil || lastBetaCand == nil {
			continue
		}
		rows := byDate[date]
		baseRank := rankRows(rows, lastBetaBase, len(BaselineFeatureNames))
		candRank := rankRows(rows, lastBetaCand, candidateFeatures)
		basePick, candPick := baseRank[:min(opts.TopN, len(baseRank))], candRank[:min(opts.TopN, len(candRank))]
		for _, row := range basePick {
			baseAgg.add(row.outcome)
		}
		for _, row := range candPick {
			candAgg.add(row.outcome)
		}
		dailyBase, dailyCand := 0.0, 0.0
		for _, row := range basePick {
			dailyBase += row.outcome.NetPct / float64(len(basePick))
		}
		for _, row := range candPick {
			dailyCand += row.outcome.NetPct / float64(len(candPick))
		}
		baseDaily = append(baseDaily, dailyBase)
		candDaily = append(candDaily, dailyCand)
		evalDates = append(evalDates, date)
	}
	if len(evalDates) == 0 {
		return Aggregate{}, Aggregate{}, Bootstrap{}, nil, nil, 0, errors.New("no walk-forward evaluation dates survived the training warmup")
	}
	baseAgg.daily = baseDaily
	candAgg.daily = candDaily
	base := baseAgg.finish()
	cand := candAgg.finish()
	base.EvaluationDates = len(evalDates)
	cand.EvaluationDates = len(evalDates)
	base.CohortCount, base.CohortCompoundedPct, base.CohortMaxDrawdownPct = cohorts(baseDaily, opts.Horizon)
	cand.CohortCount, cand.CohortCompoundedPct, cand.CohortMaxDrawdownPct = cohorts(candDaily, opts.Horizon)
	bootstrap := bootstrapDaily(baseDaily, candDaily, opts.Bootstrap, opts.BootstrapSeed, opts.Horizon)
	coefficients := make([]Coefficient, 0, len(lastBetaCand)-1)
	for i, name := range candidateNames {
		coefficients = append(coefficients, Coefficient{Name: name, Value: lastBetaCand[i+1]})
	}
	return base, cand, bootstrap, coefficients, evalDates, trainingDates, nil
}

func cohorts(daily []float64, step int) (int, float64, float64) {
	if step < 1 {
		step = 1
	}
	equity := 1.0
	peak := 1.0
	maxDD := 0.0
	count := 0
	for i := 0; i < len(daily); i += step {
		end := min(i+step, len(daily))
		returns := daily[i:end]
		mean := 0.0
		for _, value := range returns {
			mean += value
		}
		mean /= float64(len(returns))
		equity *= 1 + mean/100
		if equity > peak {
			peak = equity
		}
		drawdown := (equity/peak - 1) * 100
		if drawdown < maxDD {
			maxDD = drawdown
		}
		count++
	}
	return count, (equity - 1) * 100, maxDD
}

func fitLogistic(rows []rawRow, features int, ridge float64) []float64 {
	if len(rows) == 0 || features <= 0 {
		return nil
	}
	weights := make([]float64, len(rows))
	positive, negative := 0, 0
	for _, row := range rows {
		if row.label {
			positive++
		} else {
			negative++
		}
	}
	if positive == 0 || negative == 0 {
		return nil
	}
	for i, row := range rows {
		if row.label {
			weights[i] = 0.5 / float64(positive)
		} else {
			weights[i] = 0.5 / float64(negative)
		}
	}
	beta := make([]float64, features+1)
	for iteration := 0; iteration < 40; iteration++ {
		gradient := make([]float64, len(beta))
		hessian := make([][]float64, len(beta))
		for i := range hessian {
			hessian[i] = make([]float64, len(beta))
		}
		for i, row := range rows {
			score := beta[0]
			for j := 0; j < features; j++ {
				score += beta[j+1] * row.values[j]
			}
			p := sigmoid(score)
			gradient[0] += weights[i] * (boolFloat(row.label) - p)
			hessian[0][0] += weights[i] * p * (1 - p)
			for j := 0; j < features; j++ {
				value := row.values[j]
				gradient[j+1] += weights[i] * (boolFloat(row.label) - p) * value
				hessian[j+1][0] += weights[i] * p * (1 - p) * value
				hessian[0][j+1] = hessian[j+1][0]
				for k := 0; k <= j; k++ {
					hessian[j+1][k+1] += weights[i] * p * (1 - p) * value * row.values[k]
				}
			}
		}
		for j := 1; j < len(beta); j++ {
			gradient[j] -= ridge * beta[j]
			hessian[j][j] += ridge
		}
		for j := 1; j < len(beta); j++ {
			for k := 0; k < j; k++ {
				hessian[k][j] = hessian[j][k]
			}
		}
		step := solve(hessian, gradient)
		if step == nil {
			return beta
		}
		maxStep := 0.0
		for j := range beta {
			beta[j] += step[j]
			maxStep = math.Max(maxStep, math.Abs(step[j]))
		}
		if maxStep < 1e-6 {
			break
		}
	}
	return beta
}

func rankRows(rows []rawRow, beta []float64, features int) []rawRow {
	out := append([]rawRow(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool {
		left, right := score(out[i], beta, features), score(out[j], beta, features)
		if left == right {
			return out[i].code < out[j].code
		}
		return left > right
	})
	return out
}

func score(row rawRow, beta []float64, features int) float64 {
	value := beta[0]
	for i := 0; i < features; i++ {
		value += beta[i+1] * row.values[i]
	}
	return value
}

func solve(matrix [][]float64, vector []float64) []float64 {
	n := len(vector)
	a := make([][]float64, n)
	for i := range a {
		a[i] = append(append([]float64(nil), matrix[i]...), vector[i])
	}
	for i := 0; i < n; i++ {
		pivot := i
		for j := i + 1; j < n; j++ {
			if math.Abs(a[j][i]) > math.Abs(a[pivot][i]) {
				pivot = j
			}
		}
		if math.Abs(a[pivot][i]) < 1e-12 {
			return nil
		}
		a[i], a[pivot] = a[pivot], a[i]
		divisor := a[i][i]
		for j := i; j <= n; j++ {
			a[i][j] /= divisor
		}
		for j := 0; j < n; j++ {
			if j == i {
				continue
			}
			factor := a[j][i]
			if factor == 0 {
				continue
			}
			for k := i; k <= n; k++ {
				a[j][k] -= factor * a[i][k]
			}
		}
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = a[i][n]
	}
	return out
}

func bootstrapDaily(base, candidate []float64, resamples int, seed int64, blockLength int) Bootstrap {
	if blockLength < 1 {
		blockLength = 1
	}
	out := Bootstrap{Method: "date_cluster_circular_block_bootstrap", Resamples: resamples, Seed: seed, BlockLength: blockLength}
	if len(base) == 0 || len(candidate) == 0 {
		return out
	}
	n := min(len(base), len(candidate))
	differences := make([]float64, n)
	for i := 0; i < n; i++ {
		differences[i] = candidate[i] - base[i]
	}
	out.MeanNetDiff = mean(differences)
	random := rand.New(rand.NewSource(seed))
	draws := make([]float64, resamples)
	for i := range draws {
		sum := 0.0
		for filled := 0; filled < n; {
			start := random.Intn(n)
			for offset := 0; offset < blockLength && filled < n; offset++ {
				sum += differences[(start+offset)%n]
				filled++
			}
		}
		draws[i] = sum / float64(n)
	}
	sort.Float64s(draws)
	out.Lower95Pct = quantile(draws, .025)
	out.Upper95Pct = quantile(draws, .975)
	positive := 0
	for _, value := range draws {
		if value > 0 {
			positive++
		}
	}
	out.PositiveRate = float64(positive) / float64(len(draws)) * 100
	return out
}

func quantile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(p * float64(len(sorted)-1))
	return sorted[index]
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func sigmoid(value float64) float64 {
	if value >= 0 {
		return 1 / (1 + math.Exp(-value))
	}
	exp := math.Exp(value)
	return exp / (1 + exp)
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func barIndex(bars []market.Bar, date string) int {
	index := sort.Search(len(bars), func(i int) bool { return bars[i].Date >= date })
	if index >= len(bars) || bars[index].Date != date {
		return -1
	}
	return index
}

func priorLow20(bars []market.Bar) (float64, bool) {
	if len(bars) < 21 {
		return 0, false
	}
	low := math.Inf(1)
	for _, bar := range bars[len(bars)-21 : len(bars)-1] {
		if bar.Low <= 0 || math.IsNaN(bar.Low) || math.IsInf(bar.Low, 0) {
			return 0, false
		}
		low = math.Min(low, bar.Low)
	}
	return low, low > 0
}

func touchRate20(bars []market.Bar) float64 {
	if len(bars) < 4 {
		return 0
	}
	lastAnchor := len(bars) - 4
	start := max(0, lastAnchor-19)
	hits, count := 0, 0
	for anchor := start; anchor <= lastAnchor; anchor++ {
		close := bars[anchor].Close
		if close <= 0 {
			continue
		}
		high := math.Max(bars[anchor+1].High, math.Max(bars[anchor+2].High, bars[anchor+3].High))
		count++
		if high/close-1 >= .01 {
			hits++
		}
	}
	if count == 0 {
		return 0
	}
	return float64(hits) / float64(count)
}
