package strategy

import (
	"fmt"
	"math"
	"stock-watcher/internal/chanlun"
	"stock-watcher/internal/market"
	"time"
)

const (
	StructureGuardRule    = "structure-guard-v1"
	StructureGuardShadow  = "shadow"
	StructureGuardEnforce = "enforce"
	guardDailyMinBars     = 240
	guardThirtyMinBars    = 96
	guardFiveMinBars      = 240
	guardThirtyBarsPerDay = 8
	guardFiveBarsPerDay   = 48
)

const (
	guardDataOK           = "ok"
	guardDataInsufficient = "insufficient"
	guardDataInvalid      = "invalid"

	guardPass    = "pass"
	guardFail    = "fail"
	guardUnknown = "unknown"
)

const (
	StructureGuardPass    = guardPass
	StructureGuardFail    = guardFail
	StructureGuardUnknown = guardUnknown
)

const (
	StructureSignalNotEvaluated = "not_evaluated"
	StructureSignalBlocked      = "blocked"
	StructureSignalUnknown      = "unknown"
)

type StructureLevelEvidence struct {
	Period          string            `json:"period"`
	Bars            int               `json:"bars"`
	MergedBars      int               `json:"merged_bars"`
	Fractals        int               `json:"fractals"`
	FinishedStrokes int               `json:"finished_strokes"`
	LastFractal     string            `json:"last_fractal,omitempty"`
	LastFractalAt   string            `json:"last_fractal_at,omitempty"`
	LastFractalHigh float64           `json:"last_fractal_high,omitempty"`
	LastFractalLow  float64           `json:"last_fractal_low,omitempty"`
	LastStroke      string            `json:"last_finished_stroke,omitempty"`
	Zone            *ChanZoneEvidence `json:"latest_zone,omitempty"`
	Status          string            `json:"status"`
	ReasonCode      string            `json:"reason_code,omitempty"`
	Reason          string            `json:"reason,omitempty"`
}

type StructureGuardEvidence struct {
	Version      int                      `json:"schema_version"`
	Rule         string                   `json:"rule"`
	Mode         string                   `json:"mode"`
	AsOf         string                   `json:"as_of"`
	DataStatus   string                   `json:"data_status"`
	GuardStatus  string                   `json:"guard_status"`
	SignalStatus string                   `json:"signal_status"`
	ReasonCode   string                   `json:"reason_code"`
	Reason       string                   `json:"reason"`
	DailyZone    *ChanZoneEvidence        `json:"daily_zone,omitempty"`
	Levels       []StructureLevelEvidence `json:"levels"`
}

func evaluateStructureGuard(mode, code, asof string, adjusted, raw market.History, minutes []market.MinuteHistory) StructureGuardEvidence {
	if mode == "" {
		mode = StructureGuardShadow
	}
	evidence := StructureGuardEvidence{Version: 1, Rule: StructureGuardRule, Mode: mode, AsOf: asof, DataStatus: guardDataOK, GuardStatus: guardUnknown, SignalStatus: StructureSignalNotEvaluated, ReasonCode: "guard_unknown", Reason: "结构未知", Levels: []StructureLevelEvidence{}}
	dailyLevel, _, dailyOK := analyzeDailyGuard(adjusted, asof)
	evidence.Levels = append(evidence.Levels, dailyLevel)
	if !dailyOK {
		evidence.DataStatus = guardDataInvalid
		evidence.SignalStatus = StructureSignalUnknown
		evidence.ReasonCode = dailyLevel.ReasonCode
		evidence.Reason = dailyLevel.Reason
		return evidence
	}
	if dailyLevel.Zone != nil {
		evidence.DailyZone = dailyLevel.Zone
	}
	if dailyLevel.Status == guardFail {
		evidence.GuardStatus = guardFail
		evidence.SignalStatus = StructureSignalBlocked
		evidence.ReasonCode = dailyLevel.ReasonCode
		evidence.Reason = dailyLevel.Reason
		return evidence
	}
	if dailyLevel.Status != guardPass {
		evidence.GuardStatus = guardUnknown
		evidence.DataStatus = guardDataInsufficient
		evidence.SignalStatus = StructureSignalUnknown
		evidence.ReasonCode = dailyLevel.ReasonCode
		evidence.Reason = dailyLevel.Reason
		return evidence
	}

	factor, factorOK := signalPriceFactor(adjusted, raw, asof)
	if !factorOK {
		evidence.GuardStatus = guardUnknown
		evidence.DataStatus = guardDataInvalid
		evidence.SignalStatus = StructureSignalUnknown
		evidence.ReasonCode = "price_scale_unknown"
		evidence.Reason = "信号日复权换算不可用"
		return evidence
	}
	byPeriod := map[int]market.MinuteHistory{}
	for _, history := range minutes {
		if history.Code != code {
			continue
		}
		if existing, ok := byPeriod[history.Period]; ok {
			_ = existing
			evidence.GuardStatus = guardUnknown
			evidence.DataStatus = guardDataInvalid
			evidence.SignalStatus = StructureSignalUnknown
			evidence.ReasonCode = "minute_snapshot_invalid"
			evidence.Reason = "分钟周期重复"
			return evidence
		}
		byPeriod[history.Period] = history
	}
	base, hasBase := byPeriod[5]
	context, hasContext := byPeriod[30]
	if hasBase && hasContext {
		if err := market.ValidateMinutePair(base, context); err != nil {
			evidence.GuardStatus = guardUnknown
			evidence.DataStatus = guardDataInvalid
			evidence.SignalStatus = StructureSignalUnknown
			evidence.ReasonCode = "minute_snapshot_invalid"
			evidence.Reason = "30m/5m 分钟快照不一致"
			return evidence
		}
	}

	contextLevel, contextOK := analyzeMinuteGuard("30m", context, guardThirtyMinBars, guardThirtyBarsPerDay, asof, hasContext)
	evidence.Levels = append(evidence.Levels, contextLevel)
	fiveLevel, fiveOK := analyzeMinuteGuard("5m", base, guardFiveMinBars, guardFiveBarsPerDay, asof, hasBase)
	evidence.Levels = append(evidence.Levels, fiveLevel)

	if !contextOK || !hasContext {
		evidence.GuardStatus = guardUnknown
		evidence.DataStatus = guardLevelDataStatus(contextLevel.ReasonCode)
		evidence.SignalStatus = StructureSignalUnknown
		evidence.ReasonCode = contextLevel.ReasonCode
		evidence.Reason = contextLevel.Reason
		return evidence
	}
	if !fiveOK || !hasBase {
		evidence.GuardStatus = guardUnknown
		evidence.DataStatus = guardLevelDataStatus(fiveLevel.ReasonCode)
		evidence.SignalStatus = StructureSignalUnknown
		evidence.ReasonCode = fiveLevel.ReasonCode
		evidence.Reason = fiveLevel.Reason
		return evidence
	}

	contextHistory := filterFinalMinuteBars(context, asof)
	contextConv := minuteChanBars(contextHistory)
	_, contextAnalysis, err := analyzeGuardBars(contextConv)
	if err != nil {
		evidence.GuardStatus = guardUnknown
		evidence.DataStatus = guardDataInvalid
		evidence.SignalStatus = StructureSignalUnknown
		evidence.ReasonCode = "minute_30_invalid"
		evidence.Reason = "30m 结构计算失败"
		return evidence
	}
	if dailyLevel.Zone != nil && contextAnalysis.LastClose*factor+1e-9 < dailyLevel.Zone.Lower {
		contextLevel.Status = guardFail
		contextLevel.ReasonCode = "veto_30m_below_daily_zone"
		contextLevel.Reason = "30m 收盘跌破日线笔重叠区下沿"
		evidence.Levels[1] = contextLevel
		evidence.GuardStatus = guardFail
		evidence.SignalStatus = StructureSignalBlocked
		evidence.ReasonCode = contextLevel.ReasonCode
		evidence.Reason = contextLevel.Reason
		return evidence
	}

	bottom := lastBottomFractal(contextAnalysis)
	if bottom == nil {
		contextLevel.Status = guardUnknown
		contextLevel.ReasonCode = "minute_30_no_bottom_fractal"
		contextLevel.Reason = "30m 尚无完成底分型，无法建立失效位"
		evidence.Levels[1] = contextLevel
		evidence.GuardStatus = guardUnknown
		evidence.DataStatus = guardDataInsufficient
		evidence.SignalStatus = StructureSignalUnknown
		evidence.ReasonCode = contextLevel.ReasonCode
		evidence.Reason = contextLevel.Reason
		return evidence
	}
	fiveHistory := filterFinalMinuteBars(base, asof)
	fiveConv := minuteChanBars(fiveHistory)
	_, fiveAnalysis, err := analyzeGuardBars(fiveConv)
	if err != nil || len(fiveConv) == 0 {
		evidence.GuardStatus = guardUnknown
		evidence.DataStatus = guardDataInvalid
		evidence.SignalStatus = StructureSignalUnknown
		evidence.ReasonCode = "minute_5_invalid"
		evidence.Reason = "5m 结构计算失败"
		return evidence
	}
	if fiveAnalysis.LastClose+1e-9 < bottom.Low {
		fiveLevel.Status = guardFail
		fiveLevel.ReasonCode = "veto_5m_below_30m_fractal"
		fiveLevel.Reason = "5m 收盘跌破 30m 最近完成底分型低点"
		evidence.Levels[2] = fiveLevel
		evidence.GuardStatus = guardFail
		evidence.SignalStatus = StructureSignalBlocked
		evidence.ReasonCode = fiveLevel.ReasonCode
		evidence.Reason = fiveLevel.Reason
		return evidence
	}

	contextLevel.Status = guardPass
	contextLevel.ReasonCode = "minute_30_veto_clear"
	contextLevel.Reason = "30m 未跌破日线区间下沿"
	fiveLevel.Status = guardPass
	fiveLevel.ReasonCode = "minute_5_veto_clear"
	fiveLevel.Reason = "5m 未跌破 30m 失效位"
	evidence.Levels[1], evidence.Levels[2] = contextLevel, fiveLevel
	evidence.GuardStatus = guardPass
	evidence.DataStatus = guardDataOK
	evidence.SignalStatus = StructureSignalNotEvaluated
	evidence.ReasonCode = "daily_structure_ok"
	evidence.Reason = "日线结构未逆向，低级别未触发失效"
	return evidence
}

func guardLevelDataStatus(reasonCode string) string {
	switch reasonCode {
	case "minute_invalid", "minute_snapshot_invalid", "minute_30_invalid", "minute_5_invalid":
		return guardDataInvalid
	default:
		return guardDataInsufficient
	}
}

func analyzeDailyGuard(history market.History, asof string) (StructureLevelEvidence, chanlun.Analysis, bool) {
	level := StructureLevelEvidence{Period: "day", Bars: len(history.Bars), Status: guardUnknown, ReasonCode: "daily_invalid", Reason: "日线结构计算失败"}
	if len(history.Bars) == 0 || history.Bars[len(history.Bars)-1].Date != asof {
		level.ReasonCode = "daily_not_asof"
		level.Reason = "日线不是信号日"
		return level, chanlun.Analysis{}, false
	}
	bars := make([]chanlun.Bar, 0, len(history.Bars))
	for _, bar := range history.Bars {
		day, err := time.ParseInLocation("2006-01-02", bar.Date, market.Shanghai)
		if err != nil {
			level.ReasonCode = "daily_invalid"
			level.Reason = "日线日期无效"
			return level, chanlun.Analysis{}, false
		}
		bars = append(bars, chanlun.Bar{Time: day, Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume, RawBars: 1})
	}
	level, analysis, err := analyzeGuardBars(bars)
	level.Period = "day"
	if err != nil {
		return level, analysis, false
	}
	if len(bars) < guardDailyMinBars {
		level.Status = guardUnknown
		level.ReasonCode = "daily_insufficient"
		level.Reason = fmt.Sprintf("日线样本不足：%d 根，至少需要 %d 根", len(bars), guardDailyMinBars)
		return level, analysis, true
	}
	if len(analysis.FinishedStrokes) == 0 {
		level.Status = guardUnknown
		level.ReasonCode = "daily_no_structure"
		level.Reason = "日线尚无完成笔"
		return level, analysis, true
	}
	zone := analysis.LatestZone()
	if zone != nil {
		if analysis.LastClose+1e-9 < zone.Lower {
			level.Status = guardFail
			level.ReasonCode = "daily_zone_broken"
			level.Reason = "日线收盘跌破最近笔重叠区下沿"
			return level, analysis, true
		}
		if len(analysis.Zones) >= 2 {
			previous := analysis.Zones[len(analysis.Zones)-2]
			if zone.Mid+1e-9 < previous.Mid && analysis.LastClose+1e-9 < zone.Mid {
				level.Status = guardFail
				level.ReasonCode = "daily_midpoint_down"
				level.Reason = "最近两个笔重叠区中轴下移且收盘低于最新中轴"
				return level, analysis, true
			}
		}
		level.Status = guardPass
		level.ReasonCode = "daily_zone_ok"
		level.Reason = "日线收盘未跌破笔重叠区下沿"
		return level, analysis, true
	}
	stroke := analysis.LastFinishedStroke()
	if stroke != nil && stroke.Direction == chanlun.Up {
		if bottom := lastBottomFractal(analysis); bottom != nil && analysis.LastClose+1e-9 >= bottom.High {
			level.Status = guardPass
			level.ReasonCode = "daily_stroke_ok"
			level.Reason = "日线最近完成笔向上且收盘站上最近底分型高点"
			return level, analysis, true
		}
	}
	level.Status = guardUnknown
	level.ReasonCode = "daily_no_zone"
	level.Reason = "日线尚无有效笔重叠区，未形成可验证的向上结构"
	return level, analysis, true
}

func analyzeMinuteGuard(period string, history market.MinuteHistory, minBars, barsPerDay int, asof string, present bool) (StructureLevelEvidence, bool) {
	level := StructureLevelEvidence{Period: period, Status: guardUnknown, ReasonCode: "minute_missing", Reason: period + " 分钟数据缺失"}
	if !present {
		return level, false
	}
	if err := history.Validate(); err != nil {
		level.ReasonCode = "minute_invalid"
		level.Reason = period + " 分钟数据无效"
		return level, false
	}
	bars := filterFinalMinuteBars(history, asof)
	level.Bars = len(bars)
	if len(bars) == 0 {
		level.ReasonCode = "minute_not_asof"
		level.Reason = period + " 无信号日已完成数据"
		return level, false
	}
	if bars[len(bars)-1].End.In(market.Shanghai).Format("2006-01-02") != asof {
		level.ReasonCode = "minute_not_asof"
		level.Reason = period + " 数据不是信号日"
		return level, false
	}
	dayBars := 0
	for _, bar := range bars {
		if bar.End.In(market.Shanghai).Format("2006-01-02") == asof {
			dayBars++
		}
	}
	last := bars[len(bars)-1].End.In(market.Shanghai)
	if dayBars != barsPerDay || last.Hour() != 15 || last.Minute() != 0 {
		level.ReasonCode = "minute_incomplete_day"
		level.Reason = fmt.Sprintf("%s 信号日未完整收盘：%d/%d 根", period, dayBars, barsPerDay)
		return level, false
	}
	converted := minuteChanBars(bars)
	level, analysis, err := analyzeGuardBars(converted)
	level.Period = period
	if err != nil {
		return level, false
	}
	if len(bars) < minBars {
		level.Status = guardUnknown
		level.ReasonCode = "minute_insufficient"
		level.Reason = fmt.Sprintf("%s 样本不足：%d 根，至少需要 %d 根", period, len(bars), minBars)
		return level, false
	}
	if len(analysis.Fractals) == 0 && len(analysis.FinishedStrokes) == 0 && len(analysis.Zones) == 0 {
		level.Status = guardUnknown
		level.ReasonCode = "minute_no_structure"
		level.Reason = period + " 尚无完成分型、笔或区间"
		return level, false
	}
	level.Status = guardPass
	level.ReasonCode = "minute_structure_ok"
	level.Reason = period + " 结构可用"
	return level, true
}

func analyzeGuardBars(bars []chanlun.Bar) (StructureLevelEvidence, chanlun.Analysis, error) {
	level := StructureLevelEvidence{Bars: len(bars), Status: guardUnknown, ReasonCode: "structure_invalid", Reason: "结构计算失败"}
	analysis, err := chanlun.Analyze(bars, chanlun.DefaultMinStrokeBars)
	if err != nil {
		return level, analysis, err
	}
	level.MergedBars = len(analysis.MergedBars)
	level.Fractals = len(analysis.Fractals)
	level.FinishedStrokes = len(analysis.FinishedStrokes)
	if fractal := analysis.LastFractal(); fractal != nil {
		level.LastFractal = string(fractal.Mark)
		level.LastFractalAt = fractal.Time.Format(time.RFC3339)
		level.LastFractalHigh = fractal.High
		level.LastFractalLow = fractal.Low
	}
	if stroke := analysis.LastFinishedStroke(); stroke != nil {
		level.LastStroke = string(stroke.Direction)
	}
	if zone := analysis.LatestZone(); zone != nil {
		level.Zone = &ChanZoneEvidence{From: zone.From.Format(time.RFC3339), To: zone.To.Format(time.RFC3339), Upper: zone.Upper, Lower: zone.Lower, Mid: zone.Mid}
	}
	level.Status = guardPass
	level.ReasonCode = "structure_ok"
	level.Reason = "结构可用"
	return level, analysis, nil
}

func filterFinalMinuteBars(history market.MinuteHistory, asof string) []market.MinuteBar {
	out := make([]market.MinuteBar, 0, len(history.Bars))
	for _, bar := range history.Bars {
		if bar.Final && bar.End.In(market.Shanghai).Format("2006-01-02") <= asof {
			out = append(out, bar)
		}
	}
	return out
}

func minuteChanBars(bars []market.MinuteBar) []chanlun.Bar {
	out := make([]chanlun.Bar, 0, len(bars))
	for _, bar := range bars {
		out = append(out, chanlun.Bar{Time: bar.End, Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume, RawBars: 1})
	}
	return out
}

func lastBottomFractal(analysis chanlun.Analysis) *chanlun.Fractal {
	for i := len(analysis.Fractals) - 1; i >= 0; i-- {
		if analysis.Fractals[i].Mark == chanlun.Bottom {
			fractal := analysis.Fractals[i]
			return &fractal
		}
	}
	return nil
}

func signalPriceFactor(adjusted, raw market.History, asof string) (float64, bool) {
	if len(adjusted.Bars) == 0 || len(raw.Bars) == 0 {
		return 0, false
	}
	lastAdjusted, lastRaw := adjusted.Bars[len(adjusted.Bars)-1], raw.Bars[len(raw.Bars)-1]
	if lastAdjusted.Date != asof || lastRaw.Date != asof || lastRaw.Close <= 0 {
		return 0, false
	}
	factor := lastAdjusted.Close / lastRaw.Close
	if math.IsNaN(factor) || math.IsInf(factor, 0) || factor <= 0 || factor < 0.01 || factor > 100 {
		return 0, false
	}
	return factor, true
}
