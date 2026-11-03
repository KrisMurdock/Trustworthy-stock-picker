package strategy

import (
	"context"
	"errors"
	"math"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const WaveHorizonsModel = "upward_wave_horizons_v1"

// WaveHorizons describes longer outcomes from the exact same opening fill as
// its short-wave companion. These are ex-post observations, not new success
// criteria, exits or candidate-selection features. Days is the valid observed
// prefix, and an incomplete prefix never stands in for a complete window.
type WaveHorizons struct {
	Model       string              `json:"model"`
	EntryStatus string              `json:"entry_status"`
	EntryReason string              `json:"entry_reason,omitempty"`
	EntryDate   string              `json:"entry_date,omitempty"`
	EntryPrice  float64             `json:"entry_price,omitempty"`
	StartupFive UpwardWaveWindow    `json:"startup_five"`
	Days        []UpwardWaveDay     `json:"days"`
	Windows     []WaveHorizonWindow `json:"windows"`
}

// WaveHorizonWindow has coverage status only: known / unknown / not_entered.
// All metrics require the entire window; null denotes unavailable, while a
// known zero remains zero. Percentages are percentage points, ratios [0,1].
type WaveHorizonWindow struct {
	Days                    int                `json:"days"`
	Status                  string             `json:"status"`
	Reason                  string             `json:"reason,omitempty"`
	ObservedDays            int                `json:"observed_days"`
	EndDate                 string             `json:"end_date,omitempty"`
	Full                    *UpwardWaveMetrics `json:"full"`
	PeakCloseNetPct         *float64           `json:"peak_close_net_pct"`
	GivebackPct             *float64           `json:"giveback_pct"`
	AfterDay5CloseReturnPct *float64           `json:"after_day5_close_return_pct"`
	AfterDay5UpRatio        *float64           `json:"after_day5_up_ratio"`
	AfterDay5LongestUp      *int               `json:"after_day5_longest_up"`
	NewClosingHighDays      *int               `json:"new_closing_high_days"`
	LastNewClosingHighDay   *int               `json:"last_new_closing_high_day"`
}

// EvaluateWithHorizons runs the existing entry simulation exactly once and
// shares the validated path, price-pair reference, calendar and boundaries.
// The first return is identical to Evaluate and contains at most ten days.
// Only the explicit no-drawdown model supports these independent diagnostics.
func (e *OpeningWaveEvaluator) EvaluateWithHorizons(ctx context.Context, code, signalDate string, adjusted, raw []market.Bar, boundary, asof string) (*UpwardWave, *WaveHorizons, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if e == nil || e.model != UpwardWaveNoDrawdownModel {
		return nil, nil, errors.New("long wave horizons require the no-drawdown opening evaluator")
	}
	observed, stopReason, err := e.evaluatePath(ctx, code, signalDate, adjusted, raw, boundary, asof, 60)
	if err != nil {
		return nil, nil, err
	}
	h := &WaveHorizons{Model: WaveHorizonsModel, EntryStatus: observed.EntryStatus, EntryReason: observed.EntryReason,
		EntryDate: observed.EntryDate, EntryPrice: observed.EntryPrice, StartupFive: detachedUpwardWindow(observed.Windows[0]),
		Days: append([]UpwardWaveDay{}, observed.Days...), Windows: []WaveHorizonWindow{}}
	for _, n := range []int{20, 40, 60} {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		w := WaveHorizonWindow{Days: n, Status: "unknown", Reason: stopReason, ObservedDays: min(n, len(h.Days))}
		if h.EntryStatus == "not_entered" {
			w.Status = "not_entered"
		} else if h.EntryStatus == "entered" && len(h.Days) >= n {
			w = describeWaveHorizon(h.Days[:n], h.EntryPrice)
		}
		h.Windows = append(h.Windows, w)
	}
	short := *observed
	short.Days = append([]UpwardWaveDay{}, observed.Days[:min(10, len(observed.Days))]...)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return &short, h, nil
}

func describeWaveHorizon(days []UpwardWaveDay, entry float64) WaveHorizonWindow {
	n := len(days)
	full := upwardMetrics(days, entry, 1, n)
	peakNet, pricePeak := days[0].CloseNetPct, entry
	highDays, lastHighDay, afterUp, streak, longest := 0, 0, 0, 0, 0
	for i, day := range days {
		peakNet = math.Max(peakNet, day.CloseNetPct)
		if day.Close > pricePeak {
			pricePeak, highDays, lastHighDay = day.Close, highDays+1, day.Day
		}
		if i >= 5 {
			if day.ChangePct > 0 {
				afterUp++
				streak++
				longest = max(longest, streak)
			} else {
				streak = 0
			}
		}
	}
	giveback := peakNet - full.EndNetPct
	afterReturn := (days[n-1].Close/days[4].Close - 1) * 100
	afterRatio := float64(afterUp) / float64(n-5)
	return WaveHorizonWindow{Days: n, Status: "known", ObservedDays: n, EndDate: days[n-1].Date, Full: &full,
		PeakCloseNetPct: &peakNet, GivebackPct: &giveback, AfterDay5CloseReturnPct: &afterReturn,
		AfterDay5UpRatio: &afterRatio, AfterDay5LongestUp: &longest, NewClosingHighDays: &highDays, LastNewClosingHighDay: &lastHighDay}
}

func detachedUpwardWindow(w UpwardWaveWindow) UpwardWaveWindow {
	if w.Full != nil {
		value := *w.Full
		w.Full = &value
	}
	if w.Segment != nil {
		value := *w.Segment
		w.Segment = &value
	}
	if w.PeakCloseNetPct != nil {
		value := *w.PeakCloseNetPct
		w.PeakCloseNetPct = &value
	}
	if w.GivebackPct != nil {
		value := *w.GivebackPct
		w.GivebackPct = &value
	}
	return w
}
