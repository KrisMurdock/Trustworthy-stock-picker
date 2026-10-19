package strategy

import (
	"errors"
	"fmt"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/chanlun"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"strings"
	"time"
)

const (
	ChanSignalRule    = "chan-5-15-30"
	chanContextPeriod = 30
	chanSetupPeriod   = 15
	chanTriggerPeriod = 5
	chanMinBars       = 3 * chanlun.DefaultMinStrokeBars
)

const (
	ChanPass    = "pass"
	ChanFail    = "fail"
	ChanUnknown = "unknown"

	chanPass    = ChanPass
	chanFail    = ChanFail
	chanUnknown = ChanUnknown
)

type ChanZoneEvidence struct {
	From  string  `json:"from,omitempty"`
	To    string  `json:"to,omitempty"`
	Upper float64 `json:"upper"`
	Lower float64 `json:"lower"`
	Mid   float64 `json:"mid"`
}

type ChanLevelEvidence struct {
	Period          int               `json:"period_minutes"`
	Bars            int               `json:"bars"`
	MergedBars      int               `json:"merged_bars"`
	Fractals        int               `json:"fractals"`
	FinishedStrokes int               `json:"finished_strokes"`
	LastFractal     string            `json:"last_fractal,omitempty"`
	LastFractalAt   string            `json:"last_fractal_at,omitempty"`
	LastFractalHigh float64           `json:"last_fractal_high,omitempty"`
	LastStroke      string            `json:"last_finished_stroke,omitempty"`
	Zone            *ChanZoneEvidence `json:"latest_zone,omitempty"`
	Status          string            `json:"status"`
	Reason          string            `json:"reason,omitempty"`
}

type ChanEvidence struct {
	Version int                 `json:"schema_version"`
	Rule    string              `json:"rule"`
	AsOf    string              `json:"as_of"`
	Status  string              `json:"status"`
	Reason  string              `json:"reason,omitempty"`
	Levels  []ChanLevelEvidence `json:"levels"`
}

// AnalyzeChanSignal derives the fixed 5/15/30-minute structure at a completed
// signal-day close. It is read-only and deterministic for frozen minute history.
func AnalyzeChanSignal(history market.MinuteHistory, signalDate string, daily market.Bar) (ChanEvidence, error) {
	return chanSignalEvidence(history, signalDate, daily)
}

// chanSignalEvidence derives 15m and 30m structures from completed 5m bars at
// the signal-day close. Bars after that cutoff and the still-forming bar are
// never visible to the rule.
func chanSignalEvidence(history market.MinuteHistory, signalDate string, daily market.Bar) (ChanEvidence, error) {
	evidence := ChanEvidence{Version: 1, Rule: ChanSignalRule, AsOf: signalDate, Status: chanUnknown, Levels: []ChanLevelEvidence{}}
	cutoff, err := time.ParseInLocation("2006-01-02 15:04", signalDate+" 15:00", market.Shanghai)
	if err != nil || daily.Date != signalDate {
		return evidence, errors.New("invalid chan signal date")
	}
	base := []market.MinuteBar{}
	signalDay := []market.MinuteBar{}
	for _, bar := range history.Bars {
		if !bar.Final || bar.End.After(cutoff) {
			continue
		}
		base = append(base, bar)
		if bar.End.In(market.Shanghai).Format("2006-01-02") == signalDate {
			signalDay = append(signalDay, bar)
		}
	}
	if len(base) == 0 || !completeMinuteDay(signalDay, daily) {
		reason := "信号日 5 分钟路径不完整"
		for _, period := range []int{chanContextPeriod, chanSetupPeriod, chanTriggerPeriod} {
			evidence.Levels = append(evidence.Levels, ChanLevelEvidence{Period: period, Status: chanUnknown, Reason: reason})
		}
		evidence.Reason = reason
		return evidence, nil
	}
	for _, period := range []int{chanContextPeriod, chanSetupPeriod, chanTriggerPeriod} {
		level, analysis, err := analyzeChanLevel(base, period, cutoff)
		if err != nil {
			return evidence, err
		}
		if level.Bars >= chanMinBars {
			switch period {
			case chanContextPeriod:
				level.Status, level.Reason = chanContextState(analysis)
			case chanSetupPeriod:
				level.Status, level.Reason = chanSetupState(analysis)
			case chanTriggerPeriod:
				level.Status, level.Reason = chanTriggerState(analysis)
			}
		}
		evidence.Levels = append(evidence.Levels, level)
	}
	evidence.Status, evidence.Reason = combineChanLevels(evidence.Levels)
	return evidence, nil
}

func analyzeChanLevel(base []market.MinuteBar, period int, cutoff time.Time) (ChanLevelEvidence, chanlun.Analysis, error) {
	bars := market.AggregateMinutes(base, period, cutoff)
	level := ChanLevelEvidence{Period: period, Bars: len(bars), Status: chanUnknown}
	converted := make([]chanlun.Bar, 0, len(bars))
	for _, bar := range bars {
		converted = append(converted, chanlun.Bar{Time: bar.End, Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume, RawBars: 1})
	}
	analysis, err := chanlun.Analyze(converted, chanlun.DefaultMinStrokeBars)
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
	}
	if stroke := analysis.LastFinishedStroke(); stroke != nil {
		level.LastStroke = string(stroke.Direction)
	}
	if zone := analysis.LatestZone(); zone != nil {
		level.Zone = &ChanZoneEvidence{From: zone.From.Format(time.RFC3339), To: zone.To.Format(time.RFC3339), Upper: zone.Upper, Lower: zone.Lower, Mid: zone.Mid}
	}
	if len(bars) < chanMinBars {
		level.Reason = fmt.Sprintf("结构样本不足：%d 根，至少需要 %d 根", len(bars), chanMinBars)
	}
	return level, analysis, nil
}

func chanContextState(analysis chanlun.Analysis) (string, string) {
	if len(analysis.FinishedStrokes) == 0 {
		return chanUnknown, "30m 尚无完成笔"
	}
	zone := analysis.LatestZone()
	if zone != nil {
		if analysis.LastClose+1e-9 < zone.Lower {
			return chanFail, "30m 收盘跌破最近中枢下沿"
		}
		return chanPass, ""
	}
	if analysis.LastFinishedStroke().Direction == chanlun.Down {
		return chanFail, "30m 尚无中枢且最近完成笔向下"
	}
	return chanPass, ""
}

func chanSetupState(analysis chanlun.Analysis) (string, string) {
	last := analysis.LastFractal()
	if last == nil {
		return chanUnknown, "15m 尚无完成分型"
	}
	if last.Mark != chanlun.Bottom {
		return chanFail, "15m 最近分型不是底分型"
	}
	if analysis.LastClose+1e-9 < last.High {
		return chanFail, "15m 收盘未收复最近底分型高点"
	}
	return chanPass, ""
}

func chanTriggerState(analysis chanlun.Analysis) (string, string) {
	last := analysis.LastFractal()
	if last == nil {
		return chanUnknown, "5m 尚无完成分型"
	}
	if last.Mark != chanlun.Bottom {
		return chanFail, "5m 最近分型不是底分型"
	}
	if analysis.LastClose+1e-9 < last.High {
		return chanFail, "5m 收盘未收复最近底分型高点"
	}
	return chanPass, ""
}

func combineChanLevels(levels []ChanLevelEvidence) (string, string) {
	status := chanPass
	reasons := []string{}
	for _, level := range levels {
		if level.Status == chanFail {
			status = chanFail
		} else if level.Status == chanUnknown && status != chanFail {
			status = chanUnknown
		}
		if level.Status != chanPass && level.Reason != "" && !containsString(reasons, level.Reason) {
			reasons = append(reasons, level.Reason)
		}
	}
	return status, strings.Join(reasons, "；")
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
