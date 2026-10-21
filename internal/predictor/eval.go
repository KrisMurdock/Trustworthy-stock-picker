package predictor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
)

const EvaluationSchemaVersion = 2
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
	BaselineOnly    bool
	CalibrationGrid bool
	TradingDays     []string
	MarketStates    map[string]string
	MinTrainingDays int
	MaxTrainingDays int
	HalfLifeDays    int
	TrainOptions    TrainOptions
	PredictionHook  func(PredictionRecord) error
}

type PredictionRecord struct {
	Date          string        `json:"date"`
	Code          string        `json:"code"`
	InputEnd      time.Time     `json:"input_end"`
	BaselineRange float64       `json:"baseline_range"`
	Actual        TargetMetrics `json:"actual"`
	Predicted     TargetMetrics `json:"predicted"`
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
	SchemaVersion  int                      `json:"schema_version"`
	Mode           string                   `json:"mode"`
	Universe       string                   `json:"universe,omitempty"`
	Status         string                   `json:"status"`
	AsOf           string                   `json:"as_of"`
	FeatureVersion int                      `json:"feature_version"`
	ValidationDays int                      `json:"validation_days"`
	ValidationFrom string                   `json:"validation_from,omitempty"`
	ValidationTo   string                   `json:"validation_to,omitempty"`
	DataFrom       string                   `json:"data_from,omitempty"`
	DataTo         string                   `json:"data_to,omitempty"`
	Metrics        []modeleval.MetricRecord `json:"metrics"`
	Days           []DayResult              `json:"days,omitempty"`
	Windows        []WindowResult           `json:"windows"`
	Strategies     []StrategyResult         `json:"strategies"`
	Regimes        []RegimeResult           `json:"regimes"`
	Universes      []UniverseResult         `json:"universes,omitempty"`
	Skipped        []string                 `json:"skipped,omitempty"`
	Notes          []string                 `json:"notes,omitempty"`
}

type WindowResult struct {
	Days      int     `json:"days"`
	Weight    float64 `json:"weight"`
	Available bool    `json:"available"`
}

type DayResult struct {
	Date              string `json:"date"`
	TrainingDays      int    `json:"training_days"`
	TrainingSamples   int    `json:"training_samples"`
	ValidationSamples int    `json:"validation_samples"`
}

type StrategyResult struct {
	Name      string        `json:"name"`
	Available bool          `json:"available"`
	Filter    *FilterResult `json:"filter,omitempty"`
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

// PredictionVariant applies a deterministic post-processing rule to the raw
// B0 prediction. It is used only by predictor-eval to diagnose calibration
// without retraining; production inference never applies an unvalidated
// variant.
type PredictionVariant struct {
	Name            string
	HighScale       float64
	LowScale        float64
	CloseScale      float64
	HighRangeWeight float64
	LowRangeWeight  float64
	EnforceOrder    bool
	RangeRatioMin   float64
	RangeRatioMax   float64
}

var calibrationVariants = []PredictionVariant{
	{Name: "B0-shrink75", HighScale: 0.75, LowScale: 0.75, CloseScale: 0.75},
	{Name: "B0-shrink50", HighScale: 0.5, LowScale: 0.5, CloseScale: 0.5},
	{Name: "B0-shrink25", HighScale: 0.25, LowScale: 0.25, CloseScale: 0.25},
	{Name: "B0-close75", HighScale: 1, LowScale: 1, CloseScale: 0.75},
	{Name: "B0-close50", HighScale: 1, LowScale: 1, CloseScale: 0.5},
	{Name: "B0-close25", HighScale: 1, LowScale: 1, CloseScale: 0.25},
	{Name: "B0-range10", HighScale: 0.9, LowScale: 0.9, CloseScale: 1, HighRangeWeight: 0.1, LowRangeWeight: 0.1},
	{Name: "B0-range15", HighScale: 0.85, LowScale: 0.85, CloseScale: 1, HighRangeWeight: 0.15, LowRangeWeight: 0.15},
	{Name: "B0-range20", HighScale: 0.8, LowScale: 0.8, CloseScale: 1, HighRangeWeight: 0.2, LowRangeWeight: 0.2},
	{Name: "B0-range25", HighScale: 0.75, LowScale: 0.75, CloseScale: 1, HighRangeWeight: 0.25, LowRangeWeight: 0.25},
	{Name: "B0-range30", HighScale: 0.7, LowScale: 0.7, CloseScale: 1, HighRangeWeight: 0.3, LowRangeWeight: 0.3},
	{Name: "B0-range35", HighScale: 0.65, LowScale: 0.65, CloseScale: 1, HighRangeWeight: 0.35, LowRangeWeight: 0.35},
	{Name: "B0-range40", HighScale: 0.6, LowScale: 0.6, CloseScale: 1, HighRangeWeight: 0.4, LowRangeWeight: 0.4},
	{Name: "B0-range50", HighScale: 0.5, LowScale: 0.5, CloseScale: 1, HighRangeWeight: 0.5, LowRangeWeight: 0.5},
	{Name: "B0-range60", HighScale: 0.4, LowScale: 0.4, CloseScale: 1, HighRangeWeight: 0.6, LowRangeWeight: 0.6},
	{Name: "B0-hi15-lo35", HighScale: 0.85, LowScale: 0.65, CloseScale: 1, HighRangeWeight: 0.15, LowRangeWeight: 0.35},
	{Name: "B0-hi20-lo40", HighScale: 0.8, LowScale: 0.6, CloseScale: 1, HighRangeWeight: 0.2, LowRangeWeight: 0.4},
	{Name: "B0-hi25-lo35", HighScale: 0.75, LowScale: 0.65, CloseScale: 1, HighRangeWeight: 0.25, LowRangeWeight: 0.35},
	{Name: "B0-hi30-lo50", HighScale: 0.7, LowScale: 0.5, CloseScale: 1, HighRangeWeight: 0.3, LowRangeWeight: 0.5},
	{Name: "B0-range25-close50", HighScale: 0.75, LowScale: 0.75, CloseScale: 0.5, HighRangeWeight: 0.25, LowRangeWeight: 0.25},
	{Name: "B0-range25-close25", HighScale: 0.75, LowScale: 0.75, CloseScale: 0.25, HighRangeWeight: 0.25, LowRangeWeight: 0.25},
	{Name: "B0-range30-close50", HighScale: 0.7, LowScale: 0.7, CloseScale: 0.5, HighRangeWeight: 0.3, LowRangeWeight: 0.3},
	{Name: "B0-range40-close50", HighScale: 0.6, LowScale: 0.6, CloseScale: 0.5, HighRangeWeight: 0.4, LowRangeWeight: 0.4},
	{Name: "B0-range25-wide125-close25", HighScale: 0.75, LowScale: 0.75, CloseScale: 0.25, HighRangeWeight: 0.25, LowRangeWeight: 0.25, RangeRatioMin: 1.25},
	{Name: "B0-range25-wide150-close25", HighScale: 0.75, LowScale: 0.75, CloseScale: 0.25, HighRangeWeight: 0.25, LowRangeWeight: 0.25, RangeRatioMin: 1.5},
	{Name: "B0-range25-wide200-close25", HighScale: 0.75, LowScale: 0.75, CloseScale: 0.25, HighRangeWeight: 0.25, LowRangeWeight: 0.25, RangeRatioMin: 2},
	{Name: "B0-range25-narrow100-close25", HighScale: 0.75, LowScale: 0.75, CloseScale: 0.25, HighRangeWeight: 0.25, LowRangeWeight: 0.25, RangeRatioMax: 1},
	{Name: "B0-range50-wide150-close25", HighScale: 0.5, LowScale: 0.5, CloseScale: 0.25, HighRangeWeight: 0.5, LowRangeWeight: 0.5, RangeRatioMin: 1.5},
	{Name: "B0-order", HighScale: 1, LowScale: 1, CloseScale: 1, EnforceOrder: true},
}

// EvaluateRollingWindows evaluates fixed-length rolling windows on a common
// set of validation days. It never trains on the validation date or later.
func EvaluateRollingWindows(ctx context.Context, dates []string, provider DayProvider, opts EvaluationOptions) (EvaluationReport, error) {
	report := EvaluationReport{
		SchemaVersion:  EvaluationSchemaVersion,
		Mode:           "rolling",
		Status:         "insufficient_history",
		AsOf:           opts.AsOf,
		FeatureVersion: FeatureVersion,
		Metrics:        []modeleval.MetricRecord{},
		Days:           []DayResult{},
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
	if opts.BaselineOnly {
		report.Mode = "baseline"
	}
	var windows []WindowSpec
	var weights []float64
	if !opts.BaselineOnly {
		var err error
		windows, weights, err = normalizeWindows(opts.Windows, opts.HalfLifeDays)
		if err != nil {
			return report, err
		}
		report.Windows = make([]WindowResult, 0, len(windows))
		for i, spec := range windows {
			report.Windows = append(report.Windows, WindowResult{Days: spec.Days, Weight: weights[i]})
		}
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
	byDay := map[string]map[string]*metricAccumulator{}
	byRegime := map[string]map[string]*metricAccumulator{}
	byRegimeFilter := map[string]*filterAccumulator{}
	commonDates := []string{}
	totalFilterSamples := 0
	retainedFilterSamples := 0
	dispersions := []float64{}
	dataFrom, dataTo := "", ""
	variants := []PredictionVariant{}
	if opts.CalibrationGrid {
		variants = calibrationVariants
	}

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
		b0Days := before
		if opts.MaxTrainingDays > 0 && len(b0Days) > opts.MaxTrainingDays {
			b0Days = lastTradingDays(b0Days, opts.MaxTrainingDays)
		}
		b0Train := samplesOnDates(day.Samples, b0Days)
		if len(b0Train) == 0 {
			report.Skipped = append(report.Skipped, date+":no_b0_training_samples")
			continue
		}
		windowTrain := make(map[int][]Sample, len(windows))
		if !opts.BaselineOnly {
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
		if !opts.BaselineOnly {
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
		}

		commonDates = append(commonDates, date)
		report.Days = append(report.Days, DayResult{
			Date: date, TrainingDays: len(before), TrainingSamples: len(b0Train), ValidationSamples: len(validation),
		})
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
			if opts.PredictionHook != nil {
				actual := TargetMetrics{High: sample.Labels[TargetHigh], Low: sample.Labels[TargetLow], Close: sample.Labels[TargetClose]}
				predicted := TargetMetrics{High: b0Preds[i][TargetHigh], Low: b0Preds[i][TargetLow], Close: b0Preds[i][TargetClose]}
				if err := opts.PredictionHook(PredictionRecord{Date: sample.Date, Code: sample.Code, InputEnd: sample.InputEnd, BaselineRange: sample.BaselineRange, Actual: actual, Predicted: predicted}); err != nil {
					return report, fmt.Errorf("record prediction for %s: %w", date, err)
				}
			}
			scoreSample(all, byRegime, state, "B0", sample, b0Preds[i])
			for _, variant := range variants {
				transformed := applyPredictionVariant(sample, b0Preds[i], variant)
				scoreSample(all, byRegime, state, variant.Name, sample, transformed)
				dayAcc := byDayAccumulators(byDay, date)
				scoreDaySample(dayAcc, variant.Name, sample, transformed)
			}
			scoreSample(all, byRegime, state, "N0-last", sample, naiveLastPrediction(sample))
			scoreSample(all, byRegime, state, "N0-range", sample, naiveRangePrediction(sample))
			dayAcc := byDayAccumulators(byDay, date)
			scoreDaySample(dayAcc, "B0", sample, b0Preds[i])
			scoreDaySample(dayAcc, "N0-last", sample, naiveLastPrediction(sample))
			scoreDaySample(dayAcc, "N0-range", sample, naiveRangePrediction(sample))
			if opts.BaselineOnly {
				continue
			}
			for _, spec := range windows {
				scoreSample(all, byRegime, state, fmt.Sprintf("window-%d", spec.Days), sample, windowPreds[spec.Days][i])
				scoreDaySample(dayAcc, fmt.Sprintf("window-%d", spec.Days), sample, windowPreds[spec.Days][i])
			}
			e0, e1 := combineWindowPredictions(windowPreds, windows, weights, i)
			scoreSample(all, byRegime, state, "E0", sample, e0)
			scoreSample(all, byRegime, state, "E1", sample, e1)
			scoreDaySample(dayAcc, "E0", sample, e0)
			scoreDaySample(dayAcc, "E1", sample, e1)

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
				scoreDaySample(dayAcc, "E1-filtered", sample, e1)
				scoreDaySample(dayAcc, "B0-filtered", sample, b0Preds[i])
			}
		}
	}

	if len(commonDates) == 0 {
		if opts.BaselineOnly {
			report.Notes = append(report.Notes, "no validation day had a trainable B0 model")
		} else {
			report.Notes = append(report.Notes, "no common validation day had every requested window and B0")
		}
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
		report.Windows[i].Available = all[name] != nil && all[name].sampleCount() > 0
	}
	strategyOrder := []string{"B0", "N0-last", "N0-range"}
	if opts.CalibrationGrid {
		for _, variant := range variants {
			strategyOrder = append(strategyOrder, variant.Name)
		}
	}
	if !opts.BaselineOnly {
		strategyOrder = append(strategyOrder, "E0", "E1", "E1-filtered", "B0-filtered")
	}
	report.Strategies = strategyResults(all, strategyOrder, filter)
	report.Regimes = regimeResults(byRegime, byRegimeFilter)
	report.Metrics = rollingMetricRecords(all, byRegime, windows, strategyOrder)
	report.Metrics = sortUniqueRecords(append(report.Metrics, dailyMetricRecords(byDay, commonDates, strategyOrder)...))
	if !opts.BaselineOnly && retainedFilterSamples == 0 {
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

func applyPredictionVariant(sample Sample, predicted map[Target]float64, variant PredictionVariant) map[Target]float64 {
	out := map[Target]float64{
		TargetHigh:  predicted[TargetHigh] * variant.HighScale,
		TargetLow:   predicted[TargetLow] * variant.LowScale,
		TargetClose: predicted[TargetClose] * variant.CloseScale,
	}
	if variant.RangeRatioMin > 0 || variant.RangeRatioMax > 0 {
		ratio := 0.0
		if sample.BaselineRange > 0 {
			ratio = (predicted[TargetHigh] - predicted[TargetLow]) / sample.BaselineRange
		}
		inside := (variant.RangeRatioMin <= 0 || ratio >= variant.RangeRatioMin) &&
			(variant.RangeRatioMax <= 0 || ratio <= variant.RangeRatioMax)
		if !inside {
			out[TargetHigh] = predicted[TargetHigh]
			out[TargetLow] = predicted[TargetLow]
		}
	}
	if variant.HighRangeWeight > 0 {
		rangeHigh := sample.BaselineRange / 2
		out[TargetHigh] = (1-variant.HighRangeWeight)*predicted[TargetHigh] + variant.HighRangeWeight*rangeHigh
	}
	if variant.LowRangeWeight > 0 {
		rangeLow := -sample.BaselineRange / 2
		out[TargetLow] = (1-variant.LowRangeWeight)*predicted[TargetLow] + variant.LowRangeWeight*rangeLow
	}
	if variant.EnforceOrder {
		out[TargetHigh] = math.Max(out[TargetHigh], out[TargetClose])
		out[TargetLow] = math.Min(out[TargetLow], out[TargetClose])
	}
	return out
}

func byDayAccumulators(byDay map[string]map[string]*metricAccumulator, date string) map[string]*metricAccumulator {
	if byDay[date] == nil {
		byDay[date] = map[string]*metricAccumulator{}
	}
	return byDay[date]
}

func scoreDaySample(all map[string]*metricAccumulator, name string, sample Sample, predicted map[Target]float64) {
	if all[name] == nil {
		all[name] = newMetricAccumulator()
	}
	all[name].add(sample, predicted)
}

func strategyResults(all map[string]*metricAccumulator, order []string, filter *FilterResult) []StrategyResult {
	out := make([]StrategyResult, 0, len(order))
	for _, name := range order {
		acc := all[name]
		if acc == nil || acc.sampleCount() == 0 {
			out = append(out, StrategyResult{Name: name, Available: false})
			continue
		}
		result := StrategyResult{Name: name, Available: true}
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
			if acc == nil || acc.sampleCount() == 0 {
				continue
			}
			strategy := StrategyResult{Name: name, Available: true}
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

func rollingMetricRecords(all map[string]*metricAccumulator, byRegime map[string]map[string]*metricAccumulator, windows []WindowSpec, strategyOrder []string) []modeleval.MetricRecord {
	records := []modeleval.MetricRecord{}
	candidate := func(sample metricSample) TargetMetrics { return sample.Predicted }
	for _, spec := range windows {
		if accumulator := all[fmt.Sprintf("window-%d", spec.Days)]; accumulator != nil {
			records = append(records, accumulator.records(fmt.Sprintf("window:%d", spec.Days), nil, candidate, true)...)
		}
	}
	for _, name := range strategyOrder {
		if accumulator := all[name]; accumulator != nil {
			records = append(records, accumulator.records("strategy:"+name, nil, candidate, true)...)
		}
	}
	for _, regime := range []string{MarketUp, MarketRange, MarketDown, MarketUnknown} {
		accumulators := byRegime[regime]
		if len(accumulators) == 0 {
			continue
		}
		regimeSlice := []modeleval.SliceDimension{{Name: "regime", Value: regime}}
		for _, name := range strategyOrder {
			if accumulator := accumulators[name]; accumulator != nil {
				records = append(records, accumulator.records("strategy:"+name, regimeSlice, candidate, false)...)
			}
		}
	}
	if accumulator := all["N0-last"]; accumulator != nil {
		records = append(records, accumulator.records(modeleval.BaselineSubject(modeleval.BaselineLastPrice), nil,
			func(sample metricSample) TargetMetrics { return sample.Last }, false)...)
	}
	if accumulator := all["N0-range"]; accumulator != nil {
		records = append(records, accumulator.records(modeleval.BaselineSubject(modeleval.BaselineRange), nil,
			func(sample metricSample) TargetMetrics { return sample.Range }, false)...)
	}
	if accumulator := all["B0"]; accumulator != nil {
		records = append(records, accumulator.majorityRecords(modeleval.BaselineSubject(modeleval.BaselineMajority), nil)...)
	}
	return sortUniqueRecords(records)
}

func dailyMetricRecords(byDay map[string]map[string]*metricAccumulator, dates []string, strategyOrder []string) []modeleval.MetricRecord {
	records := []modeleval.MetricRecord{}
	candidate := func(sample metricSample) TargetMetrics { return sample.Predicted }
	for _, date := range dates {
		accumulators := byDay[date]
		if len(accumulators) == 0 {
			continue
		}
		slice := []modeleval.SliceDimension{{Name: "date", Value: date}}
		for _, name := range strategyOrder {
			if accumulator := accumulators[name]; accumulator != nil {
				records = append(records, accumulator.records("strategy:"+name, slice, candidate, false)...)
			}
		}
	}
	return records
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
