package strategy

import (
	"fmt"
	"math"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	PhaseLabelComparisonVersion    = 5
	PhaseLabelComparisonMethod     = "slope_ols_atr_v5"
	phaseLabelPurgeDays            = 20
	phaseLabelMaxHorizon           = 20
	phaseLabelLookback             = 500
	phaseLabelStep                 = 3
	phaseLabelHoldoutDates         = 30
	phaseLabelMinClassShare        = 0.10
	phaseLabelBaselineHorizon      = 5
	phaseLabelBaselineMovePct      = 1.0
	phaseLabelComparisonLimitation = "标签口径对比；使用最近 500 根日线、3 日信号步长构造重叠未来窗口，股票共享信号日；每次报告固定最后 30 个信号日为留出并 purge 20 个交易日，只在开发集选择参数、留出集仅评估一次，跨报告留出会滚动；开发与留出都要求三类标签各至少占 10%；特征使用当前复权快照；不代表收益、成功率或交易信号"
)

var (
	phaseLabelHorizons    = []int{5, 10, 20}
	phaseLabelATRMultiple = []float64{0.5, 1.0, 1.5, 2.0}
	phaseLabelR2Minimum   = []float64{0.4, 0.5, 0.6, 0.7}
)

type PhaseLabelPoint struct {
	Date  string  `json:"date"`
	Close float64 `json:"close"`
}

type phaseValidationSample struct {
	Code       string
	Date       string
	Phase      string
	ATRPct     float64
	StartClose float64
	Forward    []PhaseLabelPoint
	Raw        bool
}

type phaseSlopeStats struct {
	SlopePct    float64
	ExpectedPct float64
	TStat       float64
	R2          float64
	Valid       bool
}

type PhaseLabelSpec struct {
	Horizon     int     `json:"horizon"`
	MovePct     float64 `json:"move_pct,omitempty"`
	ATRMultiple float64 `json:"atr_multiple,omitempty"`
	TCritical   float64 `json:"t_critical,omitempty"`
	R2Minimum   float64 `json:"r2_minimum,omitempty"`
}

type PhaseLabelCount struct {
	Phase string  `json:"phase"`
	Label string  `json:"label"`
	Count int     `json:"count"`
	Share float64 `json:"share"`
}

type PhaseLabelMetrics struct {
	Status           string                 `json:"status"`
	From             string                 `json:"from,omitempty"`
	To               string                 `json:"to,omitempty"`
	Evaluated        int                    `json:"evaluated"`
	Samples          int                    `json:"samples"`
	Dates            int                    `json:"signal_dates"`
	Stocks           int                    `json:"stocks"`
	Coverage         float64                `json:"coverage"`
	Accuracy         float64                `json:"accuracy"`
	BalancedAccuracy float64                `json:"balanced_accuracy"`
	MacroF1          float64                `json:"macro_f1"`
	BaselineAccuracy float64                `json:"majority_baseline_accuracy"`
	ClassCoverage    string                 `json:"class_coverage,omitempty"`
	Distribution     []PhaseLabelCount      `json:"label_distribution"`
	Classes          []PhaseClassValidation `json:"classes"`
	Confusion        []PhaseConfusion       `json:"confusion,omitempty"`
}

type PhaseLabelEvaluation struct {
	Label       string            `json:"label"`
	Spec        PhaseLabelSpec    `json:"spec"`
	Development PhaseLabelMetrics `json:"development"`
	Holdout     PhaseLabelMetrics `json:"holdout"`
	All         PhaseLabelMetrics `json:"all"`
}

// PhaseLabelComparison records whether a volatility-normalized log-price
// slope label is a better three-class target than the fixed-time-horizon
// baseline. Parameter selection uses only the development split; the holdout
// split is evaluated once with the selected specification.
type PhaseLabelComparison struct {
	Version            int                  `json:"schema_version"`
	Method             string               `json:"method"`
	Spec               PhaseLabelSpec       `json:"spec"`
	DevelopmentFrom    string               `json:"development_from,omitempty"`
	DevelopmentThrough string               `json:"development_through,omitempty"`
	HoldoutFrom        string               `json:"holdout_from,omitempty"`
	HoldoutThrough     string               `json:"holdout_through,omitempty"`
	PurgeTradingDays   int                  `json:"purge_trading_days"`
	LookbackBars       int                  `json:"lookback_bars"`
	SignalStep         int                  `json:"signal_step"`
	Candidates         int                  `json:"candidates"`
	LabelSource        string               `json:"label_source,omitempty"`
	Baseline           PhaseLabelEvaluation `json:"baseline"`
	Slope              PhaseLabelEvaluation `json:"slope"`
	BaselineVerdict    string               `json:"baseline_verdict"`
	SlopeVerdict       string               `json:"slope_verdict"`
	Verdict            string               `json:"verdict"`
	Limitation         string               `json:"limitation"`
}

// phaseValidationSampleAt builds a comparison sample with a consistent price
// source for the whole forward window. Raw closes are preferred; if any raw
// close in the window is missing, the adjusted snapshot is used for all points.
func phaseValidationSampleAt(code string, bars []market.Bar, index int, rawCloses map[string]float64, atrPct float64, phase string) (phaseValidationSample, bool) {
	if code == "" || index < 0 || index+phaseLabelMaxHorizon >= len(bars) || atrPct <= 0 || phase == "" || phase == PhaseUnknown {
		return phaseValidationSample{}, false
	}
	points := make([]PhaseLabelPoint, phaseLabelMaxHorizon+1)
	useRaw := rawCloses != nil
	if useRaw {
		for offset := 0; offset <= phaseLabelMaxHorizon; offset++ {
			closePrice, ok := rawCloses[bars[index+offset].Date]
			if !ok || closePrice <= 0 || math.IsNaN(closePrice) || math.IsInf(closePrice, 0) {
				useRaw = false
				break
			}
		}
	}
	for offset := 0; offset <= phaseLabelMaxHorizon; offset++ {
		bar := bars[index+offset]
		closePrice := bar.Close
		if useRaw {
			closePrice = rawCloses[bar.Date]
		}
		if closePrice <= 0 || math.IsNaN(closePrice) || math.IsInf(closePrice, 0) {
			return phaseValidationSample{}, false
		}
		points[offset] = PhaseLabelPoint{Date: bar.Date, Close: closePrice}
	}
	return phaseValidationSample{
		Code: code, Date: bars[index].Date, Phase: phase, ATRPct: atrPct,
		StartClose: points[0].Close, Forward: points, Raw: useRaw,
	}, true
}

// ComparePhaseLabels evaluates the current fixed-time-horizon label against a
// multi-horizon OLS slope candidate. The candidate grid is selected on the
// development split only. The last 30 signal dates form the holdout split, and
// development samples whose 20-day label path reaches the holdout boundary are
// purged.
func ComparePhaseLabels(samples []phaseValidationSample) *PhaseLabelComparison {
	comparison := &PhaseLabelComparison{
		Version: PhaseLabelComparisonVersion, Method: PhaseLabelComparisonMethod,
		PurgeTradingDays: phaseLabelPurgeDays, LookbackBars: phaseLabelLookback, SignalStep: phaseLabelStep,
		Candidates:      len(phaseLabelHorizons) * len(phaseLabelATRMultiple) * len(phaseLabelR2Minimum),
		BaselineVerdict: "unavailable", SlopeVerdict: "unavailable", Verdict: "unavailable",
		Limitation: phaseLabelComparisonLimitation,
	}
	baselineSpec := PhaseLabelSpec{Horizon: phaseLabelBaselineHorizon, MovePct: phaseLabelBaselineMovePct}
	comparison.Baseline = PhaseLabelEvaluation{Label: "fixed_time_horizon", Spec: baselineSpec}
	comparison.Spec = phaseLabelDefaultSpec()

	filtered := make([]phaseValidationSample, 0, len(samples))
	for _, sample := range samples {
		if len(sample.Forward) < phaseLabelMaxHorizon+1 || sample.ATRPct <= 0 || sample.Phase == "" || sample.Phase == PhaseUnknown {
			continue
		}
		filtered = append(filtered, sample)
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Date == filtered[j].Date {
			return filtered[i].Code < filtered[j].Code
		}
		return filtered[i].Date < filtered[j].Date
	})
	if len(filtered) == 0 {
		comparison.Baseline.Development = phaseLabelUnavailableMetrics()
		comparison.Baseline.Holdout = phaseLabelUnavailableMetrics()
		comparison.Baseline.All = phaseLabelUnavailableMetrics()
		comparison.Slope = PhaseLabelEvaluation{Label: "slope_log_atr", Spec: comparison.Spec, Development: phaseLabelUnavailableMetrics(), Holdout: phaseLabelUnavailableMetrics(), All: phaseLabelUnavailableMetrics()}
		return comparison
	}

	rawSamples, adjustedSamples := 0, 0
	for _, sample := range filtered {
		if sample.Raw {
			rawSamples++
		} else {
			adjustedSamples++
		}
	}
	comparison.LabelSource = phaseLabelSource(rawSamples, adjustedSamples)

	development, holdout, developmentFrom, developmentThrough, holdoutFrom, holdoutThrough := splitPhaseLabelSamples(filtered)
	comparison.DevelopmentFrom = developmentFrom
	comparison.DevelopmentThrough = developmentThrough
	comparison.HoldoutFrom = holdoutFrom
	comparison.HoldoutThrough = holdoutThrough

	baselineLabeler := func(sample phaseValidationSample) (string, bool) {
		return phaseFixedTimeHorizonLabel(sample, baselineSpec.Horizon, baselineSpec.MovePct)
	}
	comparison.Baseline.Development = phaseLabelMetrics(development, baselineLabeler)
	comparison.Baseline.Holdout = phaseLabelMetrics(holdout, baselineLabeler)
	comparison.Baseline.All = phaseLabelMetrics(filtered, baselineLabeler)

	bestSpec := phaseLabelDefaultSpec()
	bestScore := phaseLabelSelectionScore{}
	selected := false
	for _, horizon := range phaseLabelHorizons {
		for _, atrMultiple := range phaseLabelATRMultiple {
			for _, r2Minimum := range phaseLabelR2Minimum {
				spec := PhaseLabelSpec{
					Horizon: horizon, ATRMultiple: atrMultiple,
					TCritical: phaseLabelTCritical(horizon), R2Minimum: r2Minimum,
				}
				labeler := func(sample phaseValidationSample) (string, bool) {
					return phaseSlopeLabel(sample, spec)
				}
				metrics := phaseLabelMetrics(development, labeler)
				score := phaseLabelSelectionScore{
					MacroF1: metrics.MacroF1, BalancedAccuracy: metrics.BalancedAccuracy,
					Accuracy: metrics.Accuracy,
				}
				if !selected || score.betterThan(bestScore) {
					bestSpec, bestScore, selected = spec, score, true
				}
			}
		}
	}
	comparison.Spec = bestSpec
	slopeLabeler := func(sample phaseValidationSample) (string, bool) {
		return phaseSlopeLabel(sample, bestSpec)
	}
	comparison.Slope = PhaseLabelEvaluation{
		Label: "slope_log_atr", Spec: bestSpec,
		Development: phaseLabelMetrics(development, slopeLabeler),
		Holdout:     phaseLabelMetrics(holdout, slopeLabeler),
		All:         phaseLabelMetrics(filtered, slopeLabeler),
	}

	comparison.BaselineVerdict = phaseLabelEvaluationVerdict(comparison.Baseline.Holdout)
	switch {
	case comparison.Baseline.Development.Status != "ok" || comparison.Slope.Development.Status != "ok" ||
		comparison.Baseline.Holdout.Status != "ok" || comparison.Slope.Holdout.Status != "ok":
		comparison.SlopeVerdict = "insufficient"
	case !phaseLabelDistributionUsable(comparison.Baseline.Development) || !phaseLabelDistributionUsable(comparison.Slope.Development) ||
		!phaseLabelDistributionUsable(comparison.Baseline.Holdout) || !phaseLabelDistributionUsable(comparison.Slope.Holdout):
		comparison.SlopeVerdict = "unstable"
	case comparison.Slope.Holdout.MacroF1 > comparison.Baseline.Holdout.MacroF1 &&
		comparison.Slope.Holdout.BalancedAccuracy >= comparison.Baseline.Holdout.BalancedAccuracy:
		comparison.SlopeVerdict = "candidate"
	default:
		comparison.SlopeVerdict = "retain_baseline"
	}
	comparison.Verdict = comparison.SlopeVerdict
	return comparison
}

func phaseLabelDefaultSpec() PhaseLabelSpec {
	return PhaseLabelSpec{
		Horizon: phaseLabelBaselineHorizon, ATRMultiple: 1.0,
		TCritical: phaseLabelTCritical(phaseLabelBaselineHorizon), R2Minimum: 0.5,
	}
}

func phaseLabelTCritical(horizon int) float64 {
	switch horizon {
	case 5:
		return 2.776
	case 10:
		return 2.262
	case 20:
		return 2.093
	default:
		return 2.0
	}
}

func splitPhaseLabelSamples(samples []phaseValidationSample) (development, holdout []phaseValidationSample, developmentFrom, developmentThrough, holdoutFrom, holdoutThrough string) {
	dateSet := map[string]bool{}
	dates := make([]string, 0)
	for _, sample := range samples {
		if !dateSet[sample.Date] {
			dateSet[sample.Date] = true
			dates = append(dates, sample.Date)
		}
	}
	sort.Strings(dates)
	if len(dates) == 0 {
		return nil, nil, "", "", "", ""
	}
	holdoutIndex := len(dates) - phaseLabelHoldoutDates
	if holdoutIndex < 0 {
		holdoutIndex = 0
	}
	holdoutFrom = dates[holdoutIndex]
	holdoutThrough = dates[len(dates)-1]
	for _, sample := range samples {
		switch {
		case sample.Date >= holdoutFrom:
			holdout = append(holdout, sample)
		case len(sample.Forward) > phaseLabelMaxHorizon && sample.Forward[phaseLabelMaxHorizon].Date < holdoutFrom:
			development = append(development, sample)
		}
	}
	for _, sample := range development {
		if developmentFrom == "" || sample.Date < developmentFrom {
			developmentFrom = sample.Date
		}
		if developmentThrough == "" || sample.Date > developmentThrough {
			developmentThrough = sample.Date
		}
	}
	return development, holdout, developmentFrom, developmentThrough, holdoutFrom, holdoutThrough
}

type phaseLabelSelectionScore struct {
	MacroF1          float64
	BalancedAccuracy float64
	Accuracy         float64
}

func (s phaseLabelSelectionScore) betterThan(other phaseLabelSelectionScore) bool {
	const epsilon = 1e-12
	switch {
	case s.MacroF1 > other.MacroF1+epsilon:
		return true
	case math.Abs(s.MacroF1-other.MacroF1) > epsilon:
		return false
	case s.BalancedAccuracy > other.BalancedAccuracy+epsilon:
		return true
	case math.Abs(s.BalancedAccuracy-other.BalancedAccuracy) > epsilon:
		return false
	default:
		return s.Accuracy > other.Accuracy+epsilon
	}
}

func phaseLabelMetrics(samples []phaseValidationSample, labeler func(phaseValidationSample) (string, bool)) PhaseLabelMetrics {
	metrics := PhaseLabelMetrics{Status: "unavailable", Coverage: 1, Distribution: []PhaseLabelCount{}, Classes: []PhaseClassValidation{}, Confusion: []PhaseConfusion{}}
	if len(samples) == 0 {
		metrics.Coverage = 0
		return metrics
	}
	dateSet := map[string]bool{}
	stockSet := map[string]bool{}
	actualCounts := map[string]int{}
	predictedCounts := map[string]int{}
	confusion := map[string]map[string]int{}
	for _, phase := range []string{PhaseAdvance, PhaseRange, PhaseDecline} {
		confusion[phase] = map[string]int{}
	}
	correct := 0
	for _, sample := range samples {
		actual, ok := labeler(sample)
		if !ok || actual == "" {
			continue
		}
		metrics.Evaluated++
		if sample.Date != "" {
			dateSet[sample.Date] = true
		}
		if sample.Code != "" {
			stockSet[sample.Code] = true
		}
		actualCounts[actual]++
		predictedCounts[sample.Phase]++
		if confusion[sample.Phase] == nil {
			confusion[sample.Phase] = map[string]int{}
		}
		confusion[sample.Phase][actual]++
		if sample.Phase == actual {
			correct++
		}
		if metrics.From == "" || sample.Date < metrics.From {
			metrics.From = sample.Date
		}
		if metrics.To == "" || sample.Date > metrics.To {
			metrics.To = sample.Date
		}
	}
	metrics.Samples = metrics.Evaluated
	metrics.Dates = len(dateSet)
	metrics.Stocks = len(stockSet)
	if metrics.Evaluated == 0 {
		metrics.Coverage = 0
		return metrics
	}
	metrics.Coverage = 1
	metrics.Accuracy = float64(correct) / float64(metrics.Evaluated)
	recallSum := 0.0
	recallClasses := 0
	macroF1 := 0.0
	f1Classes := 0
	for _, phase := range []string{PhaseAdvance, PhaseRange, PhaseDecline} {
		correctClass := confusion[phase][phase]
		precision := 0.0
		if predictedCounts[phase] > 0 {
			precision = float64(correctClass) / float64(predictedCounts[phase])
		}
		recall := 0.0
		if actualCounts[phase] > 0 {
			recall = float64(correctClass) / float64(actualCounts[phase])
			recallSum += recall
			recallClasses++
		}
		f1 := phaseF1(precision, recall)
		if actualCounts[phase] > 0 || predictedCounts[phase] > 0 {
			macroF1 += f1
			f1Classes++
		}
		metrics.Classes = append(metrics.Classes, PhaseClassValidation{
			Phase: phase, Label: phaseLabel(phase), Predicted: predictedCounts[phase],
			Support: actualCounts[phase], Precision: precision, Recall: recall, F1: f1,
		})
	}
	if recallClasses > 0 {
		metrics.BalancedAccuracy = recallSum / float64(recallClasses)
	}
	if f1Classes > 0 {
		metrics.MacroF1 = macroF1 / float64(f1Classes)
	}
	for _, count := range actualCounts {
		if float64(count)/float64(metrics.Evaluated) > metrics.BaselineAccuracy {
			metrics.BaselineAccuracy = float64(count) / float64(metrics.Evaluated)
		}
	}
	for _, phase := range []string{PhaseAdvance, PhaseRange, PhaseDecline} {
		metrics.Distribution = append(metrics.Distribution, PhaseLabelCount{
			Phase: phase, Label: phaseLabel(phase), Count: actualCounts[phase],
			Share: float64(actualCounts[phase]) / float64(metrics.Evaluated),
		})
	}
	metrics.ClassCoverage = phaseLabelClassCoverage(metrics.Distribution)
	for predicted, row := range confusion {
		for actual, count := range row {
			if count > 0 {
				metrics.Confusion = append(metrics.Confusion, PhaseConfusion{Predicted: predicted, Actual: actual, Count: count})
			}
		}
	}
	sort.Slice(metrics.Confusion, func(i, j int) bool {
		if metrics.Confusion[i].Predicted == metrics.Confusion[j].Predicted {
			return metrics.Confusion[i].Actual < metrics.Confusion[j].Actual
		}
		return metrics.Confusion[i].Predicted < metrics.Confusion[j].Predicted
	})
	if metrics.Samples < phaseValidationMinSamples || metrics.Dates < phaseValidationMinDates || metrics.Stocks < phaseValidationMinStocks {
		metrics.Status = "insufficient"
	} else {
		metrics.Status = "ok"
	}
	return metrics
}

func phaseLabelUnavailableMetrics() PhaseLabelMetrics {
	return PhaseLabelMetrics{Status: "unavailable", ClassCoverage: "unavailable", Distribution: []PhaseLabelCount{}, Classes: []PhaseClassValidation{}, Confusion: []PhaseConfusion{}}
}

func phaseLabelDistributionUsable(metrics PhaseLabelMetrics) bool {
	return metrics.Status == "ok" && metrics.ClassCoverage == "ok"
}

func phaseLabelClassCoverage(distribution []PhaseLabelCount) string {
	if len(distribution) == 0 {
		return "unavailable"
	}
	total := 0
	for _, row := range distribution {
		total += row.Count
	}
	if total == 0 {
		return "unavailable"
	}
	for _, phase := range []string{PhaseAdvance, PhaseRange, PhaseDecline} {
		count := 0
		for _, row := range distribution {
			if row.Phase == phase {
				count = row.Count
				break
			}
		}
		if count == 0 {
			return "missing_class"
		}
		if float64(count)/float64(total) < phaseLabelMinClassShare {
			return "concentrated"
		}
	}
	return "ok"
}

func phaseF1(precision, recall float64) float64 {
	if precision+recall <= 0 {
		return 0
	}
	return 2 * precision * recall / (precision + recall)
}

func phaseFixedTimeHorizonLabel(sample phaseValidationSample, horizon int, movePct float64) (string, bool) {
	if horizon < 0 || len(sample.Forward) <= horizon || sample.StartClose <= 0 {
		return "", false
	}
	end := sample.Forward[horizon].Close
	if end <= 0 {
		return "", false
	}
	move := (end/sample.StartClose - 1) * 100
	switch {
	case move > movePct:
		return PhaseAdvance, true
	case move < -movePct:
		return PhaseDecline, true
	default:
		return PhaseRange, true
	}
}

func phaseSlopeLabel(sample phaseValidationSample, spec PhaseLabelSpec) (string, bool) {
	stats := phaseSlopeStatsForPoints(sample.Forward, spec.Horizon)
	if !stats.Valid || sample.ATRPct <= 0 {
		return "", false
	}
	threshold := spec.ATRMultiple * sample.ATRPct
	if spec.R2Minimum > 0 && stats.R2 < spec.R2Minimum {
		return PhaseRange, true
	}
	if spec.TCritical > 0 && math.Abs(stats.TStat) < spec.TCritical {
		return PhaseRange, true
	}
	if stats.ExpectedPct >= threshold && stats.SlopePct > 0 {
		return PhaseAdvance, true
	}
	if stats.ExpectedPct <= -threshold && stats.SlopePct < 0 {
		return PhaseDecline, true
	}
	return PhaseRange, true
}

func phaseSlopeStatsForPoints(points []PhaseLabelPoint, horizon int) phaseSlopeStats {
	if horizon < 2 || len(points) < horizon+1 {
		return phaseSlopeStats{}
	}
	n := horizon + 1
	values := make([]float64, n)
	for i := 0; i < n; i++ {
		closePrice := points[i].Close
		if closePrice <= 0 || math.IsNaN(closePrice) || math.IsInf(closePrice, 0) {
			return phaseSlopeStats{}
		}
		values[i] = math.Log(closePrice)
	}
	meanX := float64(n-1) / 2
	meanY := 0.0
	for _, value := range values {
		meanY += value
	}
	meanY /= float64(n)
	var ssXX, ssXY, ssYY float64
	for i, value := range values {
		dx := float64(i) - meanX
		dy := value - meanY
		ssXX += dx * dx
		ssXY += dx * dy
		ssYY += dy * dy
	}
	if ssXX <= 0 {
		return phaseSlopeStats{}
	}
	slope := ssXY / ssXX
	intercept := meanY - slope*meanX
	var rss float64
	for i, value := range values {
		predicted := intercept + slope*float64(i)
		rss += (value - predicted) * (value - predicted)
	}
	r2 := 0.0
	if ssYY > 1e-18 {
		r2 = 1 - rss/ssYY
	} else if math.Abs(slope) < 1e-15 {
		r2 = 0
	}
	r2 = math.Max(0, math.Min(1, r2))
	se := 0.0
	if n > 2 {
		variance := rss / float64(n-2)
		if variance > 0 {
			se = math.Sqrt(variance / ssXX)
		}
	}
	tStat := 0.0
	if se > 1e-15 {
		tStat = slope / se
	} else if math.Abs(slope) > 1e-15 {
		tStat = math.Copysign(1e6, slope)
	}
	return phaseSlopeStats{
		SlopePct:    (math.Exp(slope) - 1) * 100,
		ExpectedPct: (math.Exp(slope*float64(horizon)) - 1) * 100,
		TStat:       tStat, R2: r2, Valid: true,
	}
}

func phaseLabelEvaluationVerdict(metrics PhaseLabelMetrics) string {
	switch metrics.Status {
	case "ok":
		if metrics.ClassCoverage != "ok" {
			return "unstable"
		}
		return PhasePerformanceVerdict(metrics.Accuracy, metrics.BaselineAccuracy, metrics.BalancedAccuracy)
	case "insufficient":
		return "insufficient"
	default:
		return "unavailable"
	}
}

func (c PhaseLabelComparison) Summary() string {
	if c.Verdict == "insufficient" || c.Verdict == "unavailable" {
		return "标签口径对比样本不足"
	}
	return fmt.Sprintf("斜率 H%d/ATR%.1f×/R²%.1f 的留出 macro F1 %.1f%%，FTH %.1f%%；%s",
		c.Spec.Horizon, c.Spec.ATRMultiple, c.Spec.R2Minimum,
		c.Slope.Holdout.MacroF1*100, c.Baseline.Holdout.MacroF1*100, c.Verdict)
}
