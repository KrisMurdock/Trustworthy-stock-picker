package strategy

import (
	"fmt"
	"math"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	PhaseVersion    = 1
	PhaseMethod     = "daily_phase_rule_v1"
	PhaseAdvance    = "advance"
	PhaseRange      = "range"
	PhaseDecline    = "decline"
	PhaseUnknown    = "unknown"
	phaseMinBars    = 60
	phaseLimitation = "规则化日线阶段分类，不是趋势确认，也不构成买卖建议"
)

// PhaseStep is one auditable link in the phase reasoning chain. Score is the
// directional vote contributed by the step: +1 bullish, -1 bearish, 0 neutral.
type PhaseStep struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Value   string `json:"value"`
	Verdict string `json:"verdict"`
	Score   int    `json:"score"`
}

// PhaseModelEvidence is auxiliary short-horizon evidence. Tree and JEV models
// predict upside ranking, not a three-class trend; they therefore never change
// the rule phase and only make a small, explicitly labelled confidence
// adjustment when their direction is available.
type PhaseModelEvidence struct {
	Name   string  `json:"name"`
	Status string  `json:"status"`
	Score  float64 `json:"score"`
	Weight float64 `json:"weight"`
	Detail string  `json:"detail"`
}

// PhaseEvidence classifies the current daily stage as advance, range or
// decline. It is a deterministic state description, not a forecast or a
// trading signal. The reasoning chain is preserved so a reader can inspect
// every input instead of seeing only the final label.
type PhaseEvidence struct {
	Version             int                  `json:"schema_version"`
	Method              string               `json:"method"`
	AsOf                string               `json:"as_of,omitempty"`
	Phase               string               `json:"phase"`
	Label               string               `json:"label"`
	Confidence          string               `json:"confidence"`
	ConfidenceScore     float64              `json:"confidence_score"`
	Score               int                  `json:"score"`
	Strength            int                  `json:"strength"`
	RulePhase           string               `json:"rule_phase,omitempty"`
	RuleScore           int                  `json:"rule_score,omitempty"`
	BaseConfidenceScore float64              `json:"base_confidence_score,omitempty"`
	Models              []PhaseModelEvidence `json:"models,omitempty"`
	ModelAgreement      string               `json:"model_agreement,omitempty"`
	DecisionStage       string               `json:"decision_stage,omitempty"`
	MA20DistancePct     float64              `json:"ma20_distance_pct"`
	MA20SlopePct        float64              `json:"ma20_slope_5_pct"`
	RegressionSlopePct  float64              `json:"regression_slope_20d_pct_per_day"`
	RegressionR2        float64              `json:"regression_r2_20d"`
	ATRMultiple         float64              `json:"regression_slope_atr_multiple"`
	RangePosition       float64              `json:"range_position_20d"`
	RangeLow            float64              `json:"range_low_20d,omitempty"`
	RangeHigh           float64              `json:"range_high_20d,omitempty"`
	Invalidation        float64              `json:"invalidation_price,omitempty"`
	Steps               []PhaseStep          `json:"steps"`
	Evidence            string               `json:"evidence"`
	Limitation          string               `json:"limitation"`
}

// AnalyzePhase builds a deterministic daily phase classification from price
// position, MA20 slope, moving-average alignment, 20-bar regression quality,
// Donchian position and the existing Chan/wave evidence. All inputs are
// completed bars. Insufficient or invalid data returns PhaseUnknown instead of
// guessing.
func AnalyzePhase(bars []market.Bar, features Features, wave *WaveEvidence) PhaseEvidence {
	evidence := PhaseEvidence{
		Version: PhaseVersion, Method: PhaseMethod,
		Phase: PhaseUnknown, Label: phaseLabel(PhaseUnknown), Confidence: "unknown",
		Steps: []PhaseStep{}, Limitation: phaseLimitation,
	}
	if len(bars) < phaseMinBars {
		evidence.Steps = append(evidence.Steps, PhaseStep{
			Key: "data", Label: "数据完整性",
			Value:   fmt.Sprintf("%d 根日线，少于 %d 根", len(bars), phaseMinBars),
			Verdict: "不足",
		})
		evidence.Evidence = fmt.Sprintf("日线样本 %d 根，少于 %d 根，无法判定阶段", len(bars), phaseMinBars)
		return evidence
	}
	for _, bar := range bars[len(bars)-phaseMinBars:] {
		if !validWaveBar(bar) {
			evidence.Steps = append(evidence.Steps, PhaseStep{
				Key: "data", Label: "数据完整性", Value: "最近日线存在无效 OHLC 或成交量", Verdict: "无效",
			})
			evidence.Evidence = "最近日线存在无效 OHLC 或成交量，无法判定阶段"
			return evidence
		}
	}

	last := bars[len(bars)-1]
	evidence.AsOf = last.Date
	ma5, ma10, ma20 := features.Metrics.MA5, features.Metrics.MA10, features.Metrics.MA20
	if ma20 <= 0 {
		ma5 = phaseSMA(bars, 5)
		ma10 = phaseSMA(bars, 10)
		ma20 = phaseSMA(bars, 20)
	}
	if ma5 <= 0 || ma10 <= 0 || ma20 <= 0 {
		evidence.Evidence = "均线价格无效，无法判定阶段"
		return evidence
	}
	ma20Slope := features.MA20Slope
	if features.Metrics.MA20 <= 0 {
		ma20Slope = waveMA20Slope(bars)
	}
	atrPct := features.ATR14Pct
	if atrPct <= 0 {
		atrPct = phaseATR14Pct(bars)
	}
	slopePct, r2 := phaseLogRegression(lastCloses(bars, 20))
	slopeATR := 0.0
	if atrPct > 0 {
		slopeATR = math.Abs(slopePct*20) / atrPct
	}
	rangePosition := waveRangePosition(bars)
	low20, high20 := phaseRange(bars, 20)
	distPct := (last.Close/ma20 - 1) * 100
	strength := phaseStrength(r2, slopeATR)

	steps := make([]PhaseStep, 0, 7)
	total := 0
	add := func(key, label, value, verdict string, score int) {
		steps = append(steps, PhaseStep{Key: key, Label: label, Value: value, Verdict: verdict, Score: score})
		total += score
	}
	add("data", "数据完整性", fmt.Sprintf("%d 根日线，满足至少 %d 根", len(bars), phaseMinBars), "充足", 0)

	priceScore, priceVerdict := phaseScore(distPct, 0.5, "偏多", "偏空")
	add("price_vs_ma20", "价格位置", fmt.Sprintf("收盘 %.2f，MA20 %.2f（%+.2f%%）", last.Close, ma20, distPct), priceVerdict, priceScore)

	slopeScore, slopeVerdict := phaseScore(ma20Slope, 0.2, "向上", "向下")
	add("ma20_slope", "中期方向", fmt.Sprintf("MA20 五日斜率 %+.2f%%", ma20Slope), slopeVerdict, slopeScore)

	alignmentScore, alignmentVerdict := 0, "交叉"
	switch {
	case ma5 > ma10 && ma10 > ma20:
		alignmentScore, alignmentVerdict = 1, "多头排列"
	case ma5 < ma10 && ma10 < ma20:
		alignmentScore, alignmentVerdict = -1, "空头排列"
	}
	add("ma_alignment", "均线排列", fmt.Sprintf("MA5 %.2f / MA10 %.2f / MA20 %.2f", ma5, ma10, ma20), alignmentVerdict, alignmentScore)

	regressionScore := 0
	regressionVerdict := "中性"
	if slopePct > 0.05 && strength > 0 {
		regressionScore, regressionVerdict = 1, "向上且质量可用"
	} else if slopePct < -0.05 && strength > 0 {
		regressionScore, regressionVerdict = -1, "向下且质量可用"
	} else if strength == 0 {
		regressionVerdict = "趋势质量弱"
	}
	add("regression", "趋势质量", fmt.Sprintf("20 日回归斜率 %+.2f%%/日，R² %.2f，约 %.1f ATR", slopePct, r2, slopeATR), regressionVerdict, regressionScore)

	rangeScore, rangeVerdict := 0, "中部"
	if rangePosition >= 0.7 {
		rangeScore, rangeVerdict = 1, "靠上"
	} else if rangePosition <= 0.3 {
		rangeScore, rangeVerdict = -1, "靠下"
	}
	add("range_position", "区间位置", fmt.Sprintf("收盘位于 20 日区间 %.0f%%（%.2f~%.2f）", rangePosition*100, low20, high20), rangeVerdict, rangeScore)

	structureScore, structureVerdict, structureValue := 0, "未知", "无缠论/波浪证据"
	if wave != nil && wave.Status == waveReady {
		structureValue = fmt.Sprintf("最后一笔%s；波浪阶段%s", waveDirectionLabel(wave.LastStroke), wave.Label)
		structureVerdict = "中性"
		switch wave.Stage {
		case waveStageAdvance:
			structureScore, structureVerdict = 1, "偏多"
		case waveStageWeak:
			structureScore, structureVerdict = -1, "偏空"
		case waveStagePullback:
			structureVerdict = "上升中的调整"
		}
	}
	add("structure", "缠论/波浪结构", structureValue, structureVerdict, structureScore)

	upCore := last.Close > ma20 && ma20Slope > 0.2 && slopePct > 0.05
	downCore := last.Close < ma20 && ma20Slope < -0.2 && slopePct < -0.05
	phase := PhaseRange
	switch {
	case upCore && total >= 3 && strength >= 1:
		phase = PhaseAdvance
	case downCore && total <= -3 && strength >= 1:
		phase = PhaseDecline
	}

	evidence.Phase = phase
	evidence.Label = phaseLabel(phase)
	evidence.BaseConfidenceScore = phaseBaseConfidence(phase, total, strength)
	evidence.ConfidenceScore = evidence.BaseConfidenceScore
	evidence.Confidence = phaseConfidenceFromScore(evidence.ConfidenceScore)
	evidence.Score = total
	evidence.Strength = strength
	evidence.RulePhase = phase
	evidence.RuleScore = total
	evidence.DecisionStage = "rule"
	evidence.MA20DistancePct = distPct
	evidence.MA20SlopePct = ma20Slope
	evidence.RegressionSlopePct = slopePct
	evidence.RegressionR2 = r2
	evidence.ATRMultiple = slopeATR
	evidence.RangePosition = rangePosition
	evidence.RangeLow = low20
	evidence.RangeHigh = high20
	evidence.Invalidation = phaseInvalidation(phase, last.Close, low20, high20, wave)
	evidence.Steps = steps
	evidence.Evidence = fmt.Sprintf(
		"收盘 %.2f，MA20 %.2f（%+.2f%%）；MA20 五日斜率 %+.2f%%；MA5/10/20 为 %.2f/%.2f/%.2f；20 日回归斜率 %+.2f%%/日（R² %.2f，约 %.1f ATR）；收盘位于 20 日区间 %.0f%%；%s；综合得分 %+d，判定%s。",
		last.Close, ma20, distPct, ma20Slope, ma5, ma10, ma20, slopePct, r2, slopeATR, rangePosition*100,
		structureValue, total, evidence.Label,
	)
	return evidence
}

func phaseScore(value, threshold float64, positive, negative string) (int, string) {
	switch {
	case value > threshold:
		return 1, positive
	case value < -threshold:
		return -1, negative
	default:
		return 0, "中性"
	}
}

func phaseStrength(r2, slopeATR float64) int {
	strength := 0
	if r2 >= 0.2 || slopeATR >= 0.15 {
		strength = 1
	}
	if r2 >= 0.45 && slopeATR >= 0.3 {
		strength = 2
	}
	return strength
}

func phaseBaseConfidence(phase string, score, strength int) float64 {
	switch phase {
	case PhaseAdvance, PhaseDecline:
		return math.Min(94, 48+float64(absInt(score))*6+float64(strength)*5)
	case PhaseRange:
		// A range is a statement that directional evidence is weak. Its
		// confidence therefore rewards neutrality and consistency of the
		// range evidence, but is deliberately capped below the high band.
		return math.Min(72, 52+float64(3-absInt(score))*4+float64(2-strength)*3)
	default:
		return 0
	}
}

func phaseConfidenceFromScore(score float64) string {
	switch {
	case score >= 75:
		return "high"
	case score >= 60:
		return "medium"
	case score > 0:
		return "low"
	default:
		return "unknown"
	}
}

func phaseInvalidation(phase string, closePrice, low20, high20 float64, wave *WaveEvidence) float64 {
	switch phase {
	case PhaseAdvance:
		if wave != nil && wave.Invalidation > 0 && wave.Invalidation < closePrice {
			return wave.Invalidation
		}
		return low20
	case PhaseDecline:
		if wave != nil && wave.Resistance > 0 && wave.Resistance > closePrice {
			return wave.Resistance
		}
		return high20
	default:
		return 0
	}
}

func phaseLabel(phase string) string {
	switch phase {
	case PhaseAdvance:
		return "上涨"
	case PhaseDecline:
		return "下跌"
	case PhaseRange:
		return "震荡"
	default:
		return "未知"
	}
}

// PhaseLabel returns the user-facing Chinese label for a phase.
func PhaseLabel(phase string) string { return phaseLabel(phase) }

// PhaseConfidenceFromScore converts a 0-100 confidence score into the
// high/medium/low vocabulary used by persisted reports.
func PhaseConfidenceFromScore(score float64) string { return phaseConfidenceFromScore(score) }

func phaseSMA(bars []market.Bar, n int) float64 {
	if len(bars) < n || n <= 0 {
		return 0
	}
	sum := 0.0
	for _, bar := range bars[len(bars)-n:] {
		sum += bar.Close
	}
	return sum / float64(n)
}

func phaseATR14Pct(bars []market.Bar) float64 {
	if len(bars) < 15 {
		return 0
	}
	closePrice := bars[len(bars)-1].Close
	if closePrice <= 0 {
		return 0
	}
	sum := 0.0
	for i := len(bars) - 14; i < len(bars); i++ {
		previous := bars[i-1].Close
		trueRange := math.Max(bars[i].High-bars[i].Low, math.Max(math.Abs(bars[i].High-previous), math.Abs(bars[i].Low-previous)))
		sum += trueRange
	}
	return sum / 14 / closePrice * 100
}

func phaseRange(bars []market.Bar, n int) (low, high float64) {
	if len(bars) < n || n <= 0 {
		return 0, 0
	}
	low, high = math.Inf(1), math.Inf(-1)
	for _, bar := range bars[len(bars)-n:] {
		low = math.Min(low, bar.Low)
		high = math.Max(high, bar.High)
	}
	return low, high
}

func lastCloses(bars []market.Bar, n int) []float64 {
	if len(bars) < n || n <= 0 {
		return nil
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = bars[len(bars)-n+i].Close
	}
	return out
}

// phaseLogRegression returns the daily log-return slope in percent and the
// regression R². Regressing log prices keeps the slope invariant to the price
// scale: the same percentage trend must produce the same phase evidence for a
// 10 yuan and a 1000 yuan stock.
func phaseLogRegression(values []float64) (slopePct, r2 float64) {
	n := len(values)
	if n < 2 {
		return 0, 0
	}
	logs := make([]float64, n)
	for i, value := range values {
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, 0
		}
		logs[i] = math.Log(value)
	}
	meanX := float64(n-1) / 2
	meanY := 0.0
	for _, value := range logs {
		meanY += value
	}
	meanY /= float64(n)
	var numerator, denominator, totalVariance, residual float64
	for i, value := range logs {
		dx := float64(i) - meanX
		numerator += dx * (value - meanY)
		denominator += dx * dx
	}
	if denominator == 0 {
		return 0, 0
	}
	slope := numerator / denominator
	intercept := meanY - slope*meanX
	for i, value := range logs {
		predicted := intercept + slope*float64(i)
		residual += (value - predicted) * (value - predicted)
		totalVariance += (value - meanY) * (value - meanY)
	}
	slopePct = (math.Exp(slope) - 1) * 100
	if totalVariance <= 0 {
		return slopePct, 0
	}
	r2 = 1 - residual/totalVariance
	return slopePct, math.Max(0, math.Min(1, r2))
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
