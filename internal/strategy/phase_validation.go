package strategy

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	PhaseValidationVersion    = 2
	PhaseValidationMethod     = "walk_forward_rule_phase_v2"
	phaseValidationHorizon    = 5
	phaseValidationLookback   = 60
	phaseValidationStep       = 10
	phaseValidationMovePct    = 1.0
	phaseValidationMinSamples = 20
	phaseValidationMinStocks  = 3
	phaseValidationMinDates   = 5
	phaseValidationLimitation = "规则阶段的方向合理性检查；特征使用当前前复权快照，公司行为窗口未重建；使用 5 根已保存日线标签、重叠样本，非独立留出集；不代表收益、成功率或交易信号"
)

type PhaseClassValidation struct {
	Phase     string  `json:"phase"`
	Label     string  `json:"label"`
	Predicted int     `json:"predicted"`
	Support   int     `json:"support"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
}

type PhaseConfusion struct {
	Predicted string `json:"predicted"`
	Actual    string `json:"actual"`
	Count     int    `json:"count"`
}

// PhaseValidation is a walk-forward sanity check for the rule phase. Features
// use only bars visible on each signal date. Direction labels use the
// unadjusted close when available and fall back to the adjusted snapshot only
// with an explicit label_source marker. This is not an untouched holdout and
// must not be presented as a trading return or success-rate estimate.
type PhaseValidation struct {
	Version          int                    `json:"schema_version"`
	Method           string                 `json:"method"`
	Status           string                 `json:"status"`
	AsOf             string                 `json:"as_of"`
	HorizonDays      int                    `json:"horizon_days"`
	From             string                 `json:"from,omitempty"`
	To               string                 `json:"to,omitempty"`
	Stocks           int                    `json:"stocks"`
	Dates            int                    `json:"signal_dates"`
	Evaluated        int                    `json:"evaluated"`
	Predicted        int                    `json:"predicted"`
	Coverage         float64                `json:"coverage"`
	Accuracy         float64                `json:"accuracy"`
	BalancedAccuracy float64                `json:"balanced_accuracy"`
	BaselineAccuracy float64                `json:"majority_baseline_accuracy"`
	LabelSource      string                 `json:"label_source,omitempty"`
	MinSamples       int                    `json:"min_samples"`
	MinStocks        int                    `json:"min_stocks"`
	MinDates         int                    `json:"min_signal_dates"`
	Classes          []PhaseClassValidation `json:"classes"`
	Confusion        []PhaseConfusion       `json:"confusion,omitempty"`
	Limitation       string                 `json:"limitation"`
}

// EvaluatePhaseValidation samples the most recent visible window per stock.
// Histories supply adjusted features; raw supplies unadjusted direction labels
// when available. Both are truncated at asof before any feature or label is
// computed.
func EvaluatePhaseValidation(ctx context.Context, histories, raw []market.History, asof string) PhaseValidation {
	report := PhaseValidation{
		Version: PhaseValidationVersion, Method: PhaseValidationMethod,
		Status: "unavailable", AsOf: asof, HorizonDays: phaseValidationHorizon,
		MinSamples: phaseValidationMinSamples, MinStocks: phaseValidationMinStocks, MinDates: phaseValidationMinDates,
		Classes: []PhaseClassValidation{}, Limitation: phaseValidationLimitation,
	}
	if _, err := time.Parse("2006-01-02", asof); err != nil {
		report.Limitation = "as_of 日期无效，阶段验证暂不可用；" + phaseValidationLimitation
		return report
	}
	rawByCode := make(map[string]map[string]float64, len(raw))
	for _, history := range raw {
		if history.Adjust != "none" {
			continue
		}
		bars, ok := phaseBarsThrough(history, phaseHistoryCutoff(history, asof))
		if !ok {
			continue
		}
		closes := make(map[string]float64, len(bars))
		for _, bar := range bars {
			closes[bar.Date] = bar.Close
		}
		rawByCode[history.Code] = closes
	}

	confusion := map[string]map[string]int{}
	for _, phase := range []string{PhaseAdvance, PhaseRange, PhaseDecline} {
		confusion[phase] = map[string]int{}
	}
	seen := map[string]bool{}
	signalDates := map[string]bool{}
	rawLabels, adjustedLabels := 0, 0
	for _, history := range histories {
		if err := ctx.Err(); err != nil {
			return report
		}
		if history.Code == "" || seen[history.Code] {
			continue
		}
		seen[history.Code] = true
		bars, ok := phaseBarsThrough(history, phaseHistoryCutoff(history, asof))
		if !ok || len(bars) < phaseMinBars+phaseValidationHorizon {
			continue
		}
		last := len(bars) - phaseValidationHorizon - 1
		first := maxInt(phaseMinBars-1, last-phaseValidationLookback)
		if first > last {
			continue
		}
		report.Stocks++
		rawCloses := rawByCode[history.Code]
		for i := first; i <= last; i += phaseValidationStep {
			if err := ctx.Err(); err != nil {
				return report
			}
			visible := bars[:i+1]
			features, err := Extract(visible)
			if err != nil {
				continue
			}
			wave := AnalyzeWave(visible)
			phase := AnalyzePhase(visible, features, &wave)
			report.Evaluated++
			if phase.Phase == PhaseUnknown {
				continue
			}
			labelClose, labelOK := phaseDirectionClose(rawCloses, bars[i].Date, bars[i+phaseValidationHorizon].Date, bars[i].Close, bars[i+phaseValidationHorizon].Close)
			if !labelOK {
				continue
			}
			forward := (labelClose.end/labelClose.start - 1) * 100
			if labelClose.raw {
				rawLabels++
			} else {
				adjustedLabels++
			}
			actual := PhaseRange
			switch {
			case forward > phaseValidationMovePct:
				actual = PhaseAdvance
			case forward < -phaseValidationMovePct:
				actual = PhaseDecline
			}
			confusion[phase.Phase][actual]++
			report.Predicted++
			signalDates[bars[i].Date] = true
			if report.From == "" || bars[i].Date < report.From {
				report.From = bars[i].Date
			}
			if report.To == "" || bars[i].Date > report.To {
				report.To = bars[i].Date
			}
		}
	}
	report.LabelSource = phaseLabelSource(rawLabels, adjustedLabels)
	report.Dates = len(signalDates)
	if report.Evaluated == 0 || report.Predicted == 0 {
		report.Limitation = "没有足够的已完成日线样本，阶段验证暂不可用；" + phaseValidationLimitation
		return report
	}
	report.Coverage = float64(report.Predicted) / float64(report.Evaluated)

	actualCounts := map[string]int{}
	predictedCounts := map[string]int{}
	correct := 0
	for predicted, row := range confusion {
		for actual, count := range row {
			actualCounts[actual] += count
			predictedCounts[predicted] += count
			report.Confusion = append(report.Confusion, PhaseConfusion{Predicted: predicted, Actual: actual, Count: count})
			if predicted == actual {
				correct += count
			}
		}
	}
	sort.Slice(report.Confusion, func(i, j int) bool {
		if report.Confusion[i].Predicted == report.Confusion[j].Predicted {
			return report.Confusion[i].Actual < report.Confusion[j].Actual
		}
		return report.Confusion[i].Predicted < report.Confusion[j].Predicted
	})
	report.Accuracy = float64(correct) / float64(report.Predicted)
	recallSum := 0.0
	recallClasses := 0
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
		report.Classes = append(report.Classes, PhaseClassValidation{
			Phase: phase, Label: phaseLabel(phase), Predicted: predictedCounts[phase],
			Support: actualCounts[phase], Precision: precision, Recall: recall,
		})
	}
	if recallClasses > 0 {
		report.BalancedAccuracy = recallSum / float64(recallClasses)
	}
	for _, count := range actualCounts {
		if float64(count)/float64(report.Predicted) > report.BaselineAccuracy {
			report.BaselineAccuracy = float64(count) / float64(report.Predicted)
		}
	}
	if report.Stocks < phaseValidationMinStocks || report.Predicted < phaseValidationMinSamples || report.Dates < phaseValidationMinDates {
		report.Status = "insufficient"
		return report
	}
	report.Status = "ok"
	return report
}

type phaseLabelClose struct {
	start float64
	end   float64
	raw   bool
}

func phaseDirectionClose(rawCloses map[string]float64, startDate, endDate string, adjustedStart, adjustedEnd float64) (phaseLabelClose, bool) {
	if rawCloses != nil {
		start, startOK := rawCloses[startDate]
		end, endOK := rawCloses[endDate]
		if startOK && endOK && start > 0 && end > 0 {
			return phaseLabelClose{start: start, end: end, raw: true}, true
		}
	}
	if adjustedStart <= 0 || adjustedEnd <= 0 {
		return phaseLabelClose{}, false
	}
	return phaseLabelClose{start: adjustedStart, end: adjustedEnd}, true
}

func phaseLabelSource(rawLabels, adjustedLabels int) string {
	switch {
	case rawLabels > 0 && adjustedLabels == 0:
		return "unadjusted_close"
	case rawLabels > 0:
		return "mixed"
	case adjustedLabels > 0:
		return "adjusted_close_fallback"
	default:
		return ""
	}
}

// phaseBarsThrough validates ordering and truncates every history at asof.
// Invalid or duplicated dates reject the stock instead of silently including
// a future or ambiguous bar.
func phaseBarsThrough(history market.History, asof string) ([]market.Bar, bool) {
	cutoff, err := time.Parse("2006-01-02", asof)
	if err != nil {
		return nil, false
	}
	bars := make([]market.Bar, 0, len(history.Bars))
	previous := ""
	for _, bar := range history.Bars {
		if bar.Date > asof {
			break
		}
		date, parseErr := time.Parse("2006-01-02", bar.Date)
		if parseErr != nil || bar.Date <= previous || date.After(cutoff) || date.Weekday() == time.Saturday || date.Weekday() == time.Sunday || !validWaveBar(bar) {
			return nil, false
		}
		previous = bar.Date
		bars = append(bars, bar)
	}
	return bars, len(bars) > 0
}

func phaseHistoryCutoff(history market.History, asof string) string {
	if history.AsOf == "" {
		return asof
	}
	if _, err := time.Parse("2006-01-02", history.AsOf); err != nil {
		return asof
	}
	if history.AsOf < asof {
		return history.AsOf
	}
	return asof
}

func (v PhaseValidation) Summary() string {
	if v.Status != "ok" {
		return "阶段验证暂不可用"
	}
	return fmt.Sprintf("规则阶段样本 %d/%d 个信号日，覆盖 %.1f%%，准确率 %.1f%%，基线 %.1f%%，平衡准确率 %.1f%%",
		v.Predicted, v.Dates, v.Coverage*100, v.Accuracy*100, v.BaselineAccuracy*100, v.BalancedAccuracy*100)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
