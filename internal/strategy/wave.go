package strategy

import (
	"fmt"
	"math"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/chanlun"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	WaveVersion       = 1
	WaveMethod        = "simplified_swing_v1"
	waveMinBars       = 60
	waveReady         = "ready"
	waveUnknown       = "unknown"
	waveStageAdvance  = "advance"
	waveStagePullback = "pullback"
	waveStageRange    = "range"
	waveStageWeak     = "weak"
	waveStageUnknown  = "unknown"
	waveLimitation    = "仅依据完成笔与回撤的启发式描述，不是严格艾略特浪级计数"
)

// WaveEvidence is a deterministic, deliberately narrow description of the
// latest completed swing. It is not an Elliott Wave count and does not claim
// that an unfinished impulse or correction has been confirmed.
type WaveEvidence struct {
	Version          int     `json:"schema_version"`
	Method           string  `json:"method"`
	Stage            string  `json:"stage"`
	Label            string  `json:"label"`
	Status           string  `json:"status"`
	CompletedStrokes int     `json:"completed_strokes"`
	LastStroke       string  `json:"last_finished_stroke,omitempty"`
	RetracementPct   float64 `json:"retracement_pct,omitempty"`
	Support          float64 `json:"support,omitempty"`
	Resistance       float64 `json:"resistance,omitempty"`
	Invalidation     float64 `json:"invalidation,omitempty"`
	Evidence         string  `json:"evidence"`
	Limitation       string  `json:"limitation"`
}

// AnalyzeWave describes the latest completed daily swing using only strict
// Chan fractals and strokes, the latest stroke-overlap zone, the five-session
// MA20 slope and the close's position in the recent range.
func AnalyzeWave(bars []market.Bar) WaveEvidence {
	evidence := WaveEvidence{
		Version: WaveVersion, Method: WaveMethod,
		Stage: waveStageUnknown, Label: "数据不足", Status: waveUnknown,
		Limitation: waveLimitation,
	}
	if len(bars) < waveMinBars {
		evidence.Evidence = fmt.Sprintf("日线样本 %d 根，少于 %d 根，无法评估完成笔结构", len(bars), waveMinBars)
		return evidence
	}
	converted := make([]chanlun.Bar, 0, len(bars))
	for _, bar := range bars {
		day, err := time.ParseInLocation("2006-01-02", bar.Date, market.Shanghai)
		if err != nil {
			evidence.Evidence = "日线日期无效，无法评估完成笔结构"
			return evidence
		}
		if !validWaveBar(bar) {
			evidence.Evidence = "日线 OHLC 或成交量无效，无法评估完成笔结构"
			return evidence
		}
		converted = append(converted, chanlun.Bar{Time: day, Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume, RawBars: 1})
	}
	analysis, err := chanlun.Analyze(converted, chanlun.DefaultMinStrokeBars)
	if err != nil {
		evidence.Evidence = "日线完成笔结构计算失败"
		return evidence
	}
	evidence.CompletedStrokes = len(analysis.FinishedStrokes)
	if stroke := analysis.LastFinishedStroke(); stroke != nil {
		evidence.LastStroke = string(stroke.Direction)
	}
	if len(analysis.FinishedStrokes) == 0 {
		evidence.Evidence = fmt.Sprintf("已有 %d 根日线，但尚无完成笔，无法给出摆动判断", len(bars))
		return evidence
	}

	evidence.Status = waveReady
	last := bars[len(bars)-1]
	closePrice := last.Close
	ma20Slope := waveMA20Slope(bars)
	rangePosition := waveRangePosition(bars)
	zone := analysis.LatestZone()
	top := latestWaveFractal(analysis.Fractals, chanlun.Top)
	bottom := latestWaveFractal(analysis.Fractals, chanlun.Bottom)
	if zone != nil {
		evidence.Support = zone.Lower
		evidence.Invalidation = zone.Lower
		evidence.Resistance = zone.Upper
	}
	if bottom != nil {
		if evidence.Support <= 0 {
			evidence.Support = bottom.Low
		}
		if evidence.Invalidation <= 0 {
			evidence.Invalidation = bottom.Low
		}
	}
	if top != nil {
		evidence.Resistance = top.High
	}

	up := latestWaveStroke(analysis.FinishedStrokes, chanlun.Up)
	impulseHigh := 0.0
	impulseLow := 0.0
	if up != nil {
		impulseHigh = up.High
		if impulseHigh <= 0 {
			impulseHigh = up.End.High
		}
		impulseLow = up.Low
		if impulseLow <= 0 {
			impulseLow = up.Start.Low
		}
	}
	evidence.RetracementPct = waveRetracement(impulseHigh, impulseLow, closePrice)

	evidence.Stage, evidence.Label = classifyWaveStage(evidence.LastStroke, up != nil, evidence.RetracementPct, ma20Slope, evidence.Invalidation, closePrice, rangePosition, zone)
	evidence.Evidence = fmt.Sprintf(
		"完成笔%d笔，最后一笔%s；最近上行笔高点%s、起点低点%s，当前回撤比例%.1f%%；MA20五日斜率%.2f%%；收盘位于近20日区间%.0f%%；支撑%s，压力%s，失效位%s",
		evidence.CompletedStrokes, waveDirectionLabel(evidence.LastStroke), wavePrice(impulseHigh), wavePrice(impulseLow), evidence.RetracementPct,
		ma20Slope, rangePosition*100, wavePrice(evidence.Support), wavePrice(evidence.Resistance), wavePrice(evidence.Invalidation),
	)
	return evidence
}

func validWaveBar(bar market.Bar) bool {
	values := []float64{bar.Open, bar.High, bar.Low, bar.Close, bar.Volume}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return false
		}
	}
	return bar.Low > 0 && bar.High >= math.Max(bar.Open, bar.Close) && bar.Low <= math.Min(bar.Open, bar.Close)
}

func waveMA20Slope(bars []market.Bar) float64 {
	if len(bars) < 25 {
		return 0
	}
	current, previous := 0.0, 0.0
	for _, bar := range bars[len(bars)-20:] {
		current += bar.Close
	}
	for _, bar := range bars[len(bars)-25 : len(bars)-5] {
		previous += bar.Close
	}
	current /= 20
	previous /= 20
	if previous <= 0 {
		return 0
	}
	return (current/previous - 1) * 100
}

func waveRetracement(impulseHigh, impulseLow, closePrice float64) float64 {
	if impulseHigh <= 0 || impulseLow <= 0 || impulseHigh <= impulseLow || closePrice >= impulseHigh {
		return 0
	}
	retracement := (impulseHigh - closePrice) / (impulseHigh - impulseLow) * 100
	return math.Max(0, math.Min(150, retracement))
}

func waveRangePosition(bars []market.Bar) float64 {
	if len(bars) < 20 {
		return 0.5
	}
	high, low := math.Inf(-1), math.Inf(1)
	for _, bar := range bars[len(bars)-20:] {
		high = math.Max(high, bar.High)
		low = math.Min(low, bar.Low)
	}
	closePrice := bars[len(bars)-1].Close
	if high <= low {
		return 0.5
	}
	return math.Max(0, math.Min(1, (closePrice-low)/(high-low)))
}

func latestWaveStroke(strokes []chanlun.Stroke, direction chanlun.Direction) *chanlun.Stroke {
	for i := len(strokes) - 1; i >= 0; i-- {
		if strokes[i].Direction == direction {
			stroke := strokes[i]
			return &stroke
		}
	}
	return nil
}

func latestWaveFractal(fractals []chanlun.Fractal, mark chanlun.Mark) *chanlun.Fractal {
	for i := len(fractals) - 1; i >= 0; i-- {
		if fractals[i].Mark == mark {
			fractal := fractals[i]
			return &fractal
		}
	}
	return nil
}

func classifyWaveStage(lastStroke string, hasUpStroke bool, retracement, ma20Slope, invalidation, closePrice, rangePosition float64, zone *chanlun.Zone) (string, string) {
	insideZone := zone != nil && closePrice >= zone.Lower && closePrice <= zone.Upper
	switch {
	case invalidation > 0 && closePrice < invalidation*0.999:
		return waveStageWeak, "结构偏弱（简化）"
	case hasUpStroke && retracement <= 1 && ma20Slope > 0:
		return waveStageAdvance, "上行推进（简化）"
	case hasUpStroke && retracement >= 2 && retracement <= 61.8 && ma20Slope >= -1:
		return waveStagePullback, "上升趋势中的调整（简化）"
	case insideZone && math.Abs(ma20Slope) <= 1:
		return waveStageRange, "区间震荡（简化）"
	case ma20Slope > 0 && rangePosition >= .6:
		return waveStageAdvance, "上行推进（简化）"
	case ma20Slope < -.5 && rangePosition <= .4:
		return waveStageWeak, "结构偏弱（简化）"
	case insideZone:
		return waveStageRange, "区间震荡（简化）"
	case hasUpStroke && retracement > 61.8:
		return waveStageWeak, "结构偏弱（简化）"
	case lastStroke == string(chanlun.Down):
		return waveStageWeak, "结构偏弱（简化）"
	default:
		return waveStageRange, "区间震荡（简化）"
	}
}

func waveDirectionLabel(direction string) string {
	switch direction {
	case string(chanlun.Up):
		return "向上"
	case string(chanlun.Down):
		return "向下"
	default:
		return "未知"
	}
}

func wavePrice(price float64) string {
	if price <= 0 {
		return "未形成"
	}
	return fmt.Sprintf("%.2f", price)
}
