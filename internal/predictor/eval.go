package predictor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const EvaluationSchemaVersion = 1
const unanimityRule = "all window close predictions share a direction"

var ErrEvaluationUnavailable = errors.New("predictor evaluation data unavailable")

// WindowSpec is one fixed-length rolling training window. Weight is populated
// by EvaluateRollingWindows with the normalized E1 weight.
type WindowSpec struct {
	Days   int     `json:"days"`
	Weight float64 `json:"weight"`
}

type EvaluationOptions struct {
	AsOf            string
	Windows         []WindowSpec
	TradingDays     []string
	MarketStates    map[string]string
	MinTrainingDays int
	HalfLifeDays    int
	TrainOptions    TrainOptions
}

type EvaluationDay struct {
	Date           string
	Samples        []Sample
	Codes          []string
	ReportRevision string
	ReportSource   string
}

type DayProvider func(date string) (EvaluationDay, error)

type EvaluationReport struct {
	SchemaVersion  int              `json:"schema_version"`
	Status         string           `json:"status"`
	AsOf           string           `json:"as_of"`
	FeatureVersion int              `json:"feature_version"`
	ValidationDays int              `json:"validation_days"`
	ValidationFrom string           `json:"validation_from,omitempty"`
	ValidationTo   string           `json:"validation_to,omitempty"`
	DataFrom       string           `json:"data_from,omitempty"`
	DataTo         string           `json:"data_to,omitempty"`
	Windows        []WindowResult   `json:"windows"`
	Strategies     []StrategyResult `json:"strategies"`
	Regimes        []RegimeResult   `json:"regimes"`
	Universes      []UniverseResult `json:"universes,omitempty"`
	Skipped        []string         `json:"skipped,omitempty"`
	Notes          []string         `json:"notes,omitempty"`
}

type WindowResult struct {
	Days       int          `json:"days"`
	Weight     float64      `json:"weight"`
	Available  bool         `json:"available"`
	Metrics    ModelMetrics `json:"metrics"`
	Comparison *Comparison  `json:"comparison,omitempty"`
}

type StrategyResult struct {
	Name       string        `json:"name"`
	Available  bool          `json:"available"`
	Metrics    ModelMetrics  `json:"metrics"`
	Comparison *Comparison   `json:"comparison,omitempty"`
	Filter     *FilterResult `json:"filter,omitempty"`
}

type Comparison struct {
	Baseline          string        `json:"baseline"`
	MAEImprovementPct TargetMetrics `json:"mae_improvement_pct"`
	DayWinRate        TargetMetrics `json:"day_win_rate"`
}

type FilterResult struct {
	Rule                string  `json:"rule"`
	AgreementRate       float64 `json:"agreement_rate"`
	Retention           float64 `json:"retention"`
	CloseDispersionMean float64 `json:"close_dispersion_mean"`
	CloseDispersionP90  float64 `json:"close_dispersion_p90"`
}

type RegimeResult struct {
	Regime     string           `json:"regime"`
	Strategies []StrategyResult `json:"strategies"`
}

type UniverseResult struct {
	Date           string   `json:"date"`
	Codes          []string `json:"codes"`
	ReportRevision string   `json:"report_revision,omitempty"`
	ReportSource   string   `json:"report_source,omitempty"`
	MarketState    string   `json:"market_state"`
}

// EvaluateRollingWindows evaluates fixed-length rolling windows on a common
// set of validation days. It never trains on the validation date or later.
func EvaluateRollingWindows(ctx context.Context, dates []string, provider DayProvider, opts EvaluationOptions) (EvaluationReport, error) {
	report := EvaluationReport{
		SchemaVersion:  EvaluationSchemaVersion,
		Status:         "insufficient_history",
		AsOf:           opts.AsOf,
		FeatureVersion: FeatureVersion,
		Windows:        []WindowResult{},
		Strategies:     []StrategyResult{},
		Regimes:        []RegimeResult{},
		Universes:      []UniverseResult{},
		Skipped:        []string{},
		Notes:          []string{},
	}
	if provider == nil {
		return report, errors.New("predictor evaluation requires a day provider")
	}
	if opts.MinTrainingDays <= 0 {
		opts.MinTrainingDays = 5
	}
	if opts.HalfLifeDays <= 0 {
		opts.HalfLifeDays = 60
	}
	windows, weights, err := normalizeWindows(opts.Windows, opts.HalfLifeDays)
	if err != nil {
		return report, err
	}
	report.Windows = make([]WindowResult, 0, len(windows))
	for i, spec := range windows {
		report.Windows = append(report.Windows, WindowResult{Days: spec.Days, Weight: weights[i]})
	}
	tradingDays := normalizeDays(opts.TradingDays)
	if len(tradingDays) == 0 {
		return report, errors.New("predictor evaluation requires trading days")
	}
	dates = normalizeDays(dates)
	if len(dates) == 0 {
		report.Notes = append(report.Notes, "no validation date was supplied")
		return report, nil
	}

	all := map[string]*metricAccumulator{}
	byRegime := map[string]map[string]*metricAccumulator{}
	byRegimeFilter := map[string]*filterAccumulator{}
	commonDates := []string{}
	totalFilterSamples := 0
	retainedFilterSamples := 0
	dispersions := []float64{}
	dataFrom, dataTo := "", ""

	for _, date := range dates {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		day, err := provider(date)
		if errors.Is(err, ErrEvaluationUnavailable) {
			report.Skipped = append(report.Skipped, date+":data_unavailable")
			continue
		}
		if err != nil {
			return report, err
		}
		if day.Date != "" && day.Date != date {
			return report, fmt.Errorf("predictor evaluation day date mismatch: got %s want %s", day.Date, date)
		}
		validation := samplesOnDate(day.Samples, date)
		if len(validation) == 0 {
			report.Skipped = append(report.Skipped, date+":no_validation_samples")
			continue
		}
		before := tradingDaysBefore(tradingDays, date)
		if len(before) < opts.MinTrainingDays {
			report.Skipped = append(report.Skipped, date+":insufficient_training_days")
			continue
		}
		b0Train := samplesOnDates(day.Samples, before)
		if len(b0Train) == 0 {
			report.Skipped = append(report.Skipped, date+":no_b0_training_samples")
			continue
		}
		windowTrain := make(map[int][]Sample, len(windows))
		windowsReady := true
		for _, spec := range windows {
			windowDays := lastTradingDays(before, spec.Days)
			if len(windowDays) != spec.Days {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s:window_%d_insufficient", date, spec.Days))
				windowsReady = false
				break
			}
			if missingSampleDays(day.Samples, windowDays) {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s:window_%d_missing_session", date, spec.Days))
				windowsReady = false
				break
			}
			train := samplesOnDates(day.Samples, windowDays)
			if len(train) == 0 {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s:window_%d_no_samples", date, spec.Days))
				windowsReady = false
				break
			}
			windowTrain[spec.Days] = train
		}
		if !windowsReady {
			continue
		}

		b0Model, err := trainEvaluationModel(b0Train, date, opts.TrainOptions)
		if err != nil {
			return report, fmt.Errorf("train B0 for %s: %w", date, err)
		}
		b0Preds, err := predictEvaluationSamples(b0Model, validation)
		if err != nil {
			return report, fmt.Errorf("predict B0 for %s: %w", date, err)
		}

		windowPreds := make(map[int][]map[Target]float64, len(windows))
		for _, spec := range windows {
			model, err := trainEvaluationModel(windowTrain[spec.Days], date, opts.TrainOptions)
			if err != nil {
				return report, fmt.Errorf("train window %d for %s: %w", spec.Days, date, err)
			}
			preds, err := predictEvaluationSamples(model, validation)
			if err != nil {
				return report, fmt.Errorf("predict window %d for %s: %w", spec.Days, date, err)
			}
			windowPreds[spec.Days] = preds
		}

		commonDates = append(commonDates, date)
		state := opts.MarketStates[date]
		if state == "" {
			state = MarketUnknown
		}
		codes := append([]string(nil), day.Codes...)
		sort.Strings(codes)
		report.Universes = append(report.Universes, UniverseResult{
			Date: date, Codes: codes, ReportRevision: day.ReportRevision,
			ReportSource: day.ReportSource, MarketState: state,
		})
		updateDataRange(&dataFrom, &dataTo, day.Samples)

		for i, sample := range validation {
			scoreSample(all, byRegime, state, "B0", sample, b0Preds[i])
			scoreSample(all, byRegime, state, "N0-last", sample, naiveLastPrediction(sample))
			scoreSample(all, byRegime, state, "N0-range", sample, naiveRangePrediction(sample))
			for _, spec := range windows {
				scoreSample(all, byRegime, state, fmt.Sprintf("window-%d", spec.Days), sample, windowPreds[spec.Days][i])
			}
			e0, e1 := combineWindowPredictions(windowPreds, windows, weights, i)
			scoreSample(all, byRegime, state, "E0", sample, e0)
			scoreSample(all, byRegime, state, "E1", sample, e1)

			dispersion := windowCloseDispersion(windowPreds, windows, i)
			dispersions = append(dispersions, dispersion)
			totalFilterSamples++
			regimeFilter := byRegimeFilter[state]
			if regimeFilter == nil {
				regimeFilter = &filterAccumulator{}
				byRegimeFilter[state] = regimeFilter
			}
			regimeFilter.total++
			regimeFilter.dispersions = append(regimeFilter.dispersions, dispersion)
			if unanimousCloseDirection(windowPreds, windows, i) {
				retainedFilterSamples++
				regimeFilter.retained++
				scoreSample(all, byRegime, state, "E1-filtered", sample, e1)
				scoreSample(all, byRegime, state, "B0-filtered", sample, b0Preds[i])
			}
		}
	}

	if len(commonDates) == 0 {
		report.Notes = append(report.Notes, "no common validation day had every requested window and B0")
		report.Skipped = dedupeStrings(report.Skipped)
		return report, nil
	}
	report.Status = "ok"
	report.ValidationDays = len(commonDates)
	report.ValidationFrom = commonDates[0]
	report.ValidationTo = commonDates[len(commonDates)-1]
	report.DataFrom = dataFrom
	report.DataTo = dataTo
	report.Skipped = dedupeStrings(report.Skipped)

	filter := &FilterResult{
		Rule:                unanimityRule,
		AgreementRate:       ratio(retainedFilterSamples, totalFilterSamples),
		Retention:           ratio(retainedFilterSamples, totalFilterSamples),
		CloseDispersionMean: mean(dispersions),
		CloseDispersionP90:  percentile90(dispersions),
	}
	for i, spec := range windows {
		name := fmt.Sprintf("window-%d", spec.Days)
		report.Windows[i].Available = all[name] != nil && all[name].samples > 0
		report.Windows[i].Metrics = metricsOf(all[name])
		report.Windows[i].Comparison = compareTo(all[name], all["B0"], "B0")
	}
	report.Strategies = strategyResults(all, []string{"B0", "N0-last", "N0-range", "E0", "E1", "E1-filtered", "B0-filtered"}, filter)
	report.Regimes = regimeResults(byRegime, byRegimeFilter)
	if retainedFilterSamples == 0 {
		report.Notes = append(report.Notes, "the unanimity filter retained no validation samples")
	}
	return report, nil
}

func normalizeWindows(in []WindowSpec, halfLife int) ([]WindowSpec, []float64, error) {
	if len(in) == 0 {
		return nil, nil, errors.New("predictor evaluation requires at least one window")
	}
	out := append([]WindowSpec(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Days < out[j].Days })
	unique := out[:0]
	for _, spec := range out {
		if spec.Days <= 0 {
			return nil, nil, errors.New("predictor evaluation windows must be positive")
		}
		if len(unique) > 0 && unique[len(unique)-1].Days == spec.Days {
			continue
		}
		unique = append(unique, spec)
	}
	weights := make([]float64, len(unique))
	sum := 0.0
	for i, spec := range unique {
		weights[i] = math.Pow(2, -float64(spec.Days)/float64(halfLife))
		sum += weights[i]
	}
	if sum <= 0 {
		return nil, nil, errors.New("predictor evaluation has invalid window weights")
	}
	for i := range unique {
		weights[i] /= sum
	}
	return unique, weights, nil
}

func trainEvaluationModel(samples []Sample, date string, opts TrainOptions) (*Model, error) {
	day, err := time.ParseInLocation("2006-01-02", date, market.Shanghai)
	if err != nil {
		return nil, err
	}
	info := ModelInfo{
		SchemaVersion: ArtifactSchemaVersion, CreatedAt: time.Date(day.Year(), day.Month(), day.Day(), 15, 0, 0, 0, market.Shanghai).UTC(),
		ValidFor: date, FeatureVersion: FeatureVersion, FeatureNames: FeatureNames(),
	}
	artifact, err := Train(samples, info, opts)
	if err != nil {
		return nil, err
	}
	return &Model{Artifact: *artifact}, nil
}

func predictEvaluationSamples(model *Model, samples []Sample) ([]map[Target]float64, error) {
	out := make([]map[Target]float64, len(samples))
	for i, sample := range samples {
		predicted, err := model.Predict(sample.Features)
		if err != nil {
			return nil, err
		}
		out[i] = predicted
	}
	return out, nil
}

func combineWindowPredictions(preds map[int][]map[Target]float64, windows []WindowSpec, weights []float64, index int) (map[Target]float64, map[Target]float64) {
	equal := map[Target]float64{}
	exponential := map[Target]float64{}
	for _, target := range Targets {
		for i, spec := range windows {
			predicted := preds[spec.Days][index]
			equal[target] += predicted[target] / float64(len(windows))
			exponential[target] += predicted[target] * weights[i]
		}
	}
	return equal, exponential
}

func windowCloseDispersion(preds map[int][]map[Target]float64, windows []WindowSpec, index int) float64 {
	low, high := math.Inf(1), math.Inf(-1)
	for _, spec := range windows {
		value := preds[spec.Days][index][TargetClose]
		low = math.Min(low, value)
		high = math.Max(high, value)
	}
	if math.IsInf(low, 0) || math.IsInf(high, 0) {
		return 0
	}
	return high - low
}

func unanimousCloseDirection(preds map[int][]map[Target]float64, windows []WindowSpec, index int) bool {
	positive, negative := false, false
	for _, spec := range windows {
		switch value := preds[spec.Days][index][TargetClose]; {
		case value > 0:
			positive = true
		case value < 0:
			negative = true
		}
	}
	return !(positive && negative)
}

func naiveLastPrediction(sample Sample) map[Target]float64 {
	high, low := math.NaN(), math.NaN()
	if sample.InputClose > 0 {
		high = sample.InputHigh/sample.InputClose - 1
		low = sample.InputLow/sample.InputClose - 1
	}
	return map[Target]float64{TargetHigh: high, TargetLow: low, TargetClose: 0}
}

func naiveRangePrediction(sample Sample) map[Target]float64 {
	return map[Target]float64{TargetHigh: sample.BaselineRange / 2, TargetLow: -sample.BaselineRange / 2, TargetClose: 0}
}

func samplesOnDate(samples []Sample, date string) []Sample {
	out := make([]Sample, 0, 64)
	for _, sample := range samples {
		if sample.Date == date {
			out = append(out, sample)
		}
	}
	return out
}

func samplesOnDates(samples []Sample, dates []string) []Sample {
	allowed := make(map[string]bool, len(dates))
	for _, date := range dates {
		allowed[date] = true
	}
	out := make([]Sample, 0, len(samples))
	for _, sample := range samples {
		if allowed[sample.Date] {
			out = append(out, sample)
		}
	}
	return out
}

func missingSampleDays(samples []Sample, dates []string) bool {
	seen := map[string]bool{}
	for _, sample := range samples {
		seen[sample.Date] = true
	}
	for _, date := range dates {
		if !seen[date] {
			return true
		}
	}
	return false
}

func tradingDaysBefore(tradingDays []string, date string) []string {
	index := sort.SearchStrings(tradingDays, date)
	if index < len(tradingDays) && tradingDays[index] == date {
		return tradingDays[:index]
	}
	if index == 0 {
		return nil
	}
	return tradingDays[:index]
}

func lastTradingDays(days []string, count int) []string {
	if count <= 0 || len(days) < count {
		return nil
	}
	return days[len(days)-count:]
}

func updateDataRange(from, to *string, samples []Sample) {
	for _, sample := range samples {
		if sample.Date == "" {
			continue
		}
		if *from == "" || sample.Date < *from {
			*from = sample.Date
		}
		if *to == "" || sample.Date > *to {
			*to = sample.Date
		}
	}
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

type metricAccumulator struct {
	samples       int
	days          map[string]struct{}
	mae           TargetMetrics
	baselineLast  TargetMetrics
	baselineRange TargetMetrics
	directionHits int
	coverageHits  int
	dayAbs        map[string]TargetMetrics
	daySamples    map[string]int
}

func newMetricAccumulator() *metricAccumulator {
	return &metricAccumulator{
		days:       map[string]struct{}{},
		dayAbs:     map[string]TargetMetrics{},
		daySamples: map[string]int{},
	}
}

func (a *metricAccumulator) add(sample Sample, predicted map[Target]float64) {
	if a == nil || sample.InputClose <= 0 {
		return
	}
	actual := TargetMetrics{High: sample.Labels[TargetHigh], Low: sample.Labels[TargetLow], Close: sample.Labels[TargetClose]}
	if !finite(actual.High) || !finite(actual.Low) || !finite(actual.Close) || !finite(predicted[TargetHigh]) || !finite(predicted[TargetLow]) || !finite(predicted[TargetClose]) {
		return
	}
	last := TargetMetrics{High: sample.InputHigh/sample.InputClose - 1, Low: sample.InputLow/sample.InputClose - 1, Close: 0}
	rangeBaseline := TargetMetrics{High: sample.BaselineRange / 2, Low: -sample.BaselineRange / 2, Close: 0}
	a.samples++
	a.days[sample.Date] = struct{}{}
	a.daySamples[sample.Date]++
	dayAbs := a.dayAbs[sample.Date]
	for _, target := range Targets {
		abs := math.Abs(predicted[target] - actual.value(target))
		a.mae.accumulate(target, abs)
		a.baselineLast.accumulate(target, math.Abs(last.value(target)-actual.value(target)))
		a.baselineRange.accumulate(target, math.Abs(rangeBaseline.value(target)-actual.value(target)))
		dayAbs.accumulate(target, abs)
	}
	a.dayAbs[sample.Date] = dayAbs
	if sameDirection(predicted[TargetClose], actual.Close) {
		a.directionHits++
	}
	if predicted[TargetHigh] >= actual.High && predicted[TargetLow] <= actual.Low {
		a.coverageHits++
	}
}

func (a *metricAccumulator) metrics() ModelMetrics {
	if a == nil || a.samples == 0 {
		return ModelMetrics{}
	}
	return ModelMetrics{
		Samples: a.samples, Days: len(a.days), MAE: a.mae.divided(a.samples),
		BaselineLastMAE: a.baselineLast.divided(a.samples), BaselineRangeMAE: a.baselineRange.divided(a.samples),
		DirectionAccuracy: float64(a.directionHits) / float64(a.samples),
		Coverage:          float64(a.coverageHits) / float64(a.samples),
	}
}

func (a *metricAccumulator) dayMAE(day string, target Target) (float64, bool) {
	if a == nil || a.daySamples[day] == 0 {
		return 0, false
	}
	return a.dayAbs[day].value(target) / float64(a.daySamples[day]), true
}

func scoreSample(all map[string]*metricAccumulator, regimes map[string]map[string]*metricAccumulator, regime, name string, sample Sample, predicted map[Target]float64) {
	if all[name] == nil {
		all[name] = newMetricAccumulator()
	}
	all[name].add(sample, predicted)
	if regimes[regime] == nil {
		regimes[regime] = map[string]*metricAccumulator{}
	}
	if regimes[regime][name] == nil {
		regimes[regime][name] = newMetricAccumulator()
	}
	regimes[regime][name].add(sample, predicted)
}

func metricsOf(acc *metricAccumulator) ModelMetrics {
	if acc == nil {
		return ModelMetrics{}
	}
	return acc.metrics()
}

func compareTo(strategy, baseline *metricAccumulator, baselineName string) *Comparison {
	if strategy == nil || baseline == nil || strategy.samples == 0 || baseline.samples == 0 {
		return nil
	}
	sm, bm := strategy.metrics().MAE, baseline.metrics().MAE
	comparison := &Comparison{Baseline: baselineName}
	for _, target := range Targets {
		base := bm.value(target)
		if base > 0 {
			comparison.MAEImprovementPct.set(target, (base-sm.value(target))/base*100)
		}
		wins, total := 0, 0
		for day := range baseline.days {
			b, ok := baseline.dayMAE(day, target)
			if !ok {
				continue
			}
			s, ok := strategy.dayMAE(day, target)
			if !ok {
				continue
			}
			total++
			if s < b {
				wins++
			}
		}
		if total > 0 {
			comparison.DayWinRate.set(target, float64(wins)/float64(total))
		}
	}
	return comparison
}

func strategyResults(all map[string]*metricAccumulator, order []string, filter *FilterResult) []StrategyResult {
	out := make([]StrategyResult, 0, len(order))
	for _, name := range order {
		acc := all[name]
		if acc == nil || acc.samples == 0 {
			out = append(out, StrategyResult{Name: name, Available: false, Metrics: ModelMetrics{}})
			continue
		}
		result := StrategyResult{Name: name, Available: true, Metrics: acc.metrics()}
		switch name {
		case "E0", "E1":
			result.Comparison = compareTo(acc, all["B0"], "B0")
		case "E1-filtered":
			result.Comparison = compareTo(acc, all["B0-filtered"], "B0-filtered")
		}
		if name == "E1" || name == "E1-filtered" {
			copied := *filter
			if name == "E1" {
				copied.Retention = 1
			}
			result.Filter = &copied
		}
		out = append(out, result)
	}
	return out
}

func regimeResults(byRegime map[string]map[string]*metricAccumulator, filters map[string]*filterAccumulator) []RegimeResult {
	order := []string{MarketUp, MarketRange, MarketDown, MarketUnknown}
	out := []RegimeResult{}
	for _, regime := range order {
		accs := byRegime[regime]
		if len(accs) == 0 {
			continue
		}
		result := RegimeResult{Regime: regime, Strategies: []StrategyResult{}}
		filter := FilterResult{Rule: unanimityRule}
		if accumulator := filters[regime]; accumulator != nil {
			filter = accumulator.result()
		}
		for _, name := range []string{"B0", "N0-last", "N0-range", "E0", "E1", "E1-filtered", "B0-filtered"} {
			acc := accs[name]
			if acc == nil || acc.samples == 0 {
				continue
			}
			strategy := StrategyResult{Name: name, Available: true, Metrics: acc.metrics()}
			switch name {
			case "E0", "E1":
				strategy.Comparison = compareTo(acc, accs["B0"], "B0")
			case "E1-filtered":
				strategy.Comparison = compareTo(acc, accs["B0-filtered"], "B0-filtered")
			}
			if name == "E1" || name == "E1-filtered" {
				copied := filter
				if name == "E1" {
					copied.Retention = 1
				}
				strategy.Filter = &copied
			}
			result.Strategies = append(result.Strategies, strategy)
		}
		if len(result.Strategies) > 0 {
			out = append(out, result)
		}
	}
	return out
}

func (m *TargetMetrics) set(target Target, value float64) {
	switch target {
	case TargetHigh:
		m.High = value
	case TargetLow:
		m.Low = value
	case TargetClose:
		m.Close = value
	}
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func sameDirection(left, right float64) bool {
	return (left > 0 && right > 0) || (left < 0 && right < 0) || (left == 0 && right == 0)
}

func ratio(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total)
}

type filterAccumulator struct {
	total       int
	retained    int
	dispersions []float64
}

func (f *filterAccumulator) result() FilterResult {
	if f == nil {
		return FilterResult{Rule: unanimityRule}
	}
	return FilterResult{
		Rule:                unanimityRule,
		AgreementRate:       ratio(f.retained, f.total),
		Retention:           ratio(f.retained, f.total),
		CloseDispersionMean: mean(f.dispersions),
		CloseDispersionP90:  percentile90(f.dispersions),
	}
}
