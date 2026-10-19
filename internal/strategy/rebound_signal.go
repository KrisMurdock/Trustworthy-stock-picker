package strategy

import (
	"fmt"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/chanlun"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"time"
)

const (
	ReboundWatch     = "watch"
	ReboundArmed     = "armed"
	ReboundConfirmed = "confirmed"
	ReboundFailed    = "failed"
	ReboundExpired   = "expired"

	reboundRule = "rebound-signal-v1"
)

// ReboundSignal is an experimental pullback state machine. It never creates a
// formal recommendation and must not be described as a first, second or third
// class buy point. Every transition is derived from completed bars only.
type ReboundSignal struct {
	Version     int     `json:"schema_version"`
	Rule        string  `json:"rule"`
	Status      string  `json:"status"`
	AsOf        string  `json:"as_of"`
	ZoneLower   float64 `json:"zone_lower"`
	ZoneUpper   float64 `json:"zone_upper"`
	ArmedAt     string  `json:"armed_at,omitempty"`
	ArmedLow    float64 `json:"armed_low,omitempty"`
	ConfirmedAt string  `json:"confirmed_at,omitempty"`
	FailedAt    string  `json:"failed_at,omitempty"`
	ReasonCode  string  `json:"reason_code"`
	Reason      string  `json:"reason"`
}

// EvaluateReboundSignal evaluates watch -> armed -> confirmed/failed/expired
// over the same frozen 30m/5m snapshot used by the structure guard. The daily
// guard must pass before an armed state can exist. Minute histories are raw
// while the daily zone is qfq, so every comparison is converted with the
// signal-day qfq/raw factor.
func EvaluateReboundSignal(guard StructureGuardEvidence, adjusted, raw market.History, context, five market.MinuteHistory, asof string) ReboundSignal {
	out := ReboundSignal{Version: 1, Rule: reboundRule, Status: ReboundWatch, AsOf: asof, ReasonCode: "daily_guard_not_pass", Reason: "日线结构未通过，不进入实验回抽观察"}
	if guard.DailyZone == nil || guard.GuardStatus != guardPass {
		return out
	}
	factor, ok := signalPriceFactor(adjusted, raw, asof)
	if !ok {
		out.Status = ReboundFailed
		out.ReasonCode = "price_scale_unknown"
		out.Reason = "信号日复权换算不可用"
		return out
	}
	out.ZoneLower, out.ZoneUpper = guard.DailyZone.Lower, guard.DailyZone.Upper
	if err := context.Validate(); err != nil {
		out.Status = ReboundFailed
		out.ReasonCode = "minute_30_invalid"
		out.Reason = "30m 数据无效"
		return out
	}
	if err := five.Validate(); err != nil {
		out.Status = ReboundFailed
		out.ReasonCode = "minute_5_invalid"
		out.Reason = "5m 数据无效"
		return out
	}
	contextBars := filterFinalMinuteBars(context, asof)
	fiveBars := filterFinalMinuteBars(five, asof)
	if len(contextBars) == 0 || len(fiveBars) == 0 {
		out.Status = ReboundFailed
		out.ReasonCode = "minute_not_asof"
		out.Reason = "缺少信号日已完成分钟线"
		return out
	}
	last := contextBars[len(contextBars)-1]
	if last.Close*factor+1e-9 < out.ZoneLower {
		out.Status = ReboundFailed
		out.FailedAt = last.End.Format(time.RFC3339)
		out.ReasonCode = "thirty_minute_below_zone"
		out.Reason = "30m 收盘跌破日线区间下沿"
		return out
	}
	armed := -1
	for i, bar := range contextBars {
		if bar.End.In(market.Shanghai).Format("2006-01-02") != asof {
			continue
		}
		low, close := bar.Low*factor, bar.Close*factor
		if low+1e-9 >= out.ZoneLower && low <= out.ZoneUpper+1e-9 && close+1e-9 >= out.ZoneLower {
			armed = i
		}
	}
	if armed < 0 {
		out.ReasonCode = "awaiting_pullback"
		out.Reason = "日线结构通过，尚未进入回抽区"
		return out
	}
	armedBar := contextBars[armed]
	out.ArmedAt = armedBar.End.Format(time.RFC3339)
	out.ArmedLow = armedBar.Low
	after := make([]market.MinuteBar, 0, 6)
	for _, bar := range fiveBars {
		if bar.End.After(armedBar.End) {
			after = append(after, bar)
		}
	}
	limit := len(after)
	if limit > 6 {
		limit = 6
	}
	prefix := make([]market.MinuteBar, 0, len(fiveBars))
	for _, bar := range fiveBars {
		if bar.End.After(armedBar.End) {
			break
		}
		prefix = append(prefix, bar)
	}
	for i := 0; i < limit; i++ {
		bar := after[i]
		prefix = append(prefix, bar)
		if bar.Close+1e-9 < out.ArmedLow {
			out.Status = ReboundFailed
			out.FailedAt = bar.End.Format(time.RFC3339)
			out.ReasonCode = "five_minute_below_armed_low"
			out.Reason = "5m 收盘跌破回抽低点"
			return out
		}
		analysis, err := chanlun.Analyze(minuteChanBars(prefix), chanlun.DefaultMinStrokeBars)
		if err != nil {
			continue
		}
		bottom := lastBottomFractal(analysis)
		if bottom != nil && bottom.Time.After(armedBar.End) && bar.Close+1e-9 >= bottom.High {
			out.Status = ReboundConfirmed
			out.ConfirmedAt = bar.End.Format(time.RFC3339)
			out.ReasonCode = "five_minute_bottom_reclaimed"
			out.Reason = fmt.Sprintf("回抽后第 %d 根 5m 收盘站上底分型高点", i+1)
			return out
		}
	}
	if limit == 6 {
		out.Status = ReboundExpired
		out.ReasonCode = "confirmation_window_expired"
		out.Reason = "回抽后 6 根 5m 内未确认且未失效"
		return out
	}
	out.Status = ReboundArmed
	out.ReasonCode = "awaiting_five_minute_confirmation"
	out.Reason = "已进入日线回抽区，等待 5m 底分型确认"
	return out
}
