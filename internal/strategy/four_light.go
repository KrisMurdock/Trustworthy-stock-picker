package strategy

import (
	"fmt"
	"math"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	FourLightRule = "four_light_approx_v1"

	FourLightRed     = "red"
	FourLightGreen   = "green"
	FourLightUnknown = "unknown"

	FourLightDataReady   = "ready"
	FourLightDataPartial = "partial"
	FourLightDataUnknown = "unknown"

	FourLightRuleAllRed  = "all-red"
	FourLightRuleFlipAny = "flip-any"
	FourLightRuleFlipAll = "flip-all"

	FourLightAlignmentUnverified = "unverified"
)

// FourLightState is the fixed four-light proxy vocabulary. Unknown is never
// treated as red or green by a resonance rule.
type FourLightState struct {
	Trend     string `json:"trend"`
	Energy    string `json:"energy"`
	MidTerm   string `json:"mid_term"`
	ShortTerm string `json:"short_term"`
}

type FourLightTransitions struct {
	Trend     bool `json:"trend"`
	Energy    bool `json:"energy"`
	MidTerm   bool `json:"mid_term"`
	ShortTerm bool `json:"short_term"`
}

// FourLightEvidence is a price/volume proxy, not the undisclosed formula of
// any commercial indicator. It is calculated from completed daily closes only.
type FourLightEvidence struct {
	Version         int                  `json:"schema_version"`
	Rule            string               `json:"rule"`
	Approximation   bool                 `json:"approximation"`
	AlignmentStatus string               `json:"alignment_status"`
	AsOf            string               `json:"as_of"`
	FinalOnly       bool                 `json:"final_only"`
	DataStatus      string               `json:"data_status"`
	Trend           string               `json:"trend"`
	Energy          string               `json:"energy"`
	MidTerm         string               `json:"mid_term"`
	ShortTerm       string               `json:"short_term"`
	PreviousState   FourLightState       `json:"previous_state"`
	Transitions     FourLightTransitions `json:"transitions"`
	AllRed          bool                 `json:"all_red"`
	FlipCount       int                  `json:"flip_count"`
	AllFlipped      bool                 `json:"all_flipped"`
	UnknownLights   int                  `json:"unknown_lights"`
	PreviousUnknown int                  `json:"previous_unknown_lights"`
	Reason          string               `json:"reason"`
}

const fourLightMinBars = 60

// AnalyzeFourLight derives the frozen four-light shadow signal at the final
// completed daily bar. It never accepts intraday bars and never mutates input.
func AnalyzeFourLight(bars []market.Bar) FourLightEvidence {
	evidence := FourLightEvidence{
		Version: 1, Rule: FourLightRule, Approximation: true,
		AlignmentStatus: FourLightAlignmentUnverified, FinalOnly: true,
		DataStatus: FourLightDataUnknown,
		Trend:      FourLightUnknown, Energy: FourLightUnknown,
		MidTerm: FourLightUnknown, ShortTerm: FourLightUnknown,
		PreviousState: FourLightState{
			Trend: FourLightUnknown, Energy: FourLightUnknown,
			MidTerm: FourLightUnknown, ShortTerm: FourLightUnknown,
		},
	}
	if len(bars) == 0 {
		evidence.Reason = fourLightReason([]string{"没有已完成日线"})
		return evidence
	}
	evidence.AsOf = bars[len(bars)-1].Date

	current, currentUnknown, currentReasons := fourLightState(bars)
	evidence.Trend = current.Trend
	evidence.Energy = current.Energy
	evidence.MidTerm = current.MidTerm
	evidence.ShortTerm = current.ShortTerm
	evidence.UnknownLights = currentUnknown

	previous, previousUnknown, previousReasons := fourLightState(nil)
	if len(bars) > 1 {
		previous, previousUnknown, previousReasons = fourLightState(bars[:len(bars)-1])
	} else {
		previousReasons = append(previousReasons, "缺少上一交易日")
	}
	evidence.PreviousState = previous
	evidence.PreviousUnknown = previousUnknown

	evidence.Transitions = FourLightTransitions{
		Trend:     flippedRed(previous.Trend, current.Trend),
		Energy:    flippedRed(previous.Energy, current.Energy),
		MidTerm:   flippedRed(previous.MidTerm, current.MidTerm),
		ShortTerm: flippedRed(previous.ShortTerm, current.ShortTerm),
	}
	evidence.FlipCount = countFourLightFlips(evidence.Transitions)
	evidence.AllRed = current.Trend == FourLightRed && current.Energy == FourLightRed &&
		current.MidTerm == FourLightRed && current.ShortTerm == FourLightRed
	evidence.AllFlipped = evidence.Transitions.Trend && evidence.Transitions.Energy &&
		evidence.Transitions.MidTerm && evidence.Transitions.ShortTerm

	reasons := append([]string{}, currentReasons...)
	for _, reason := range previousReasons {
		reasons = append(reasons, "上一交易日："+reason)
	}
	evidence.Reason = fourLightReason(reasons)
	switch {
	case currentUnknown > 0:
		evidence.DataStatus = FourLightDataUnknown
	case previousUnknown > 0:
		evidence.DataStatus = FourLightDataPartial
	default:
		evidence.DataStatus = FourLightDataReady
	}
	return evidence
}

func fourLightState(bars []market.Bar) (FourLightState, int, []string) {
	state := FourLightState{
		Trend: FourLightUnknown, Energy: FourLightUnknown,
		MidTerm: FourLightUnknown, ShortTerm: FourLightUnknown,
	}
	if len(bars) < fourLightMinBars {
		return state, 4, []string{fmt.Sprintf("日线不足 %d 根", fourLightMinBars)}
	}
	window := bars[len(bars)-fourLightMinBars:]
	if reason := invalidFourLightWindow(window); reason != "" {
		return state, 4, []string{reason}
	}
	last := window[len(window)-1]
	close := last.Close
	ma5 := fourLightSMA(window, 5)
	ma20 := fourLightSMA(window, 20)
	ma60 := fourLightSMA(window, 60)
	return5 := close/window[len(window)-6].Close - 1
	volumeAverage := 0.0
	for _, bar := range window[len(window)-6 : len(window)-1] {
		volumeAverage += bar.Volume
	}
	volumeAverage /= 5

	reasons := []string{}
	if close > ma20 {
		state.Trend = FourLightRed
	} else {
		state.Trend = FourLightGreen
	}
	if volumeAverage > 0 && last.Volume > 0 {
		if last.Volume/volumeAverage >= 1.2 {
			state.Energy = FourLightRed
		} else {
			state.Energy = FourLightGreen
		}
	} else {
		reasons = append(reasons, "量能基准或当日成交量为零")
	}
	if close > ma60 {
		state.MidTerm = FourLightRed
	} else {
		state.MidTerm = FourLightGreen
	}
	if close > ma5 && return5 > 0 {
		state.ShortTerm = FourLightRed
	} else {
		state.ShortTerm = FourLightGreen
	}
	unknown := 0
	for _, value := range []string{state.Trend, state.Energy, state.MidTerm, state.ShortTerm} {
		if value == FourLightUnknown {
			unknown++
		}
	}
	return state, unknown, reasons
}

func invalidFourLightWindow(bars []market.Bar) string {
	for i, bar := range bars {
		if bar.Date == "" || bar.Close <= 0 || math.IsNaN(bar.Close) || math.IsInf(bar.Close, 0) ||
			bar.Volume < 0 || math.IsNaN(bar.Volume) || math.IsInf(bar.Volume, 0) {
			return "日线日期、收盘价或成交量无效"
		}
		if i > 0 && bar.Date <= bars[i-1].Date {
			return "日线日期未严格递增"
		}
	}
	return ""
}

func fourLightSMA(bars []market.Bar, period int) float64 {
	sum := 0.0
	for _, bar := range bars[len(bars)-period:] {
		sum += bar.Close
	}
	return sum / float64(period)
}

func flippedRed(previous, current string) bool {
	return previous == FourLightGreen && current == FourLightRed
}

func countFourLightFlips(transitions FourLightTransitions) int {
	count := 0
	for _, flipped := range []bool{transitions.Trend, transitions.Energy, transitions.MidTerm, transitions.ShortTerm} {
		if flipped {
			count++
		}
	}
	return count
}

func fourLightReason(reasons []string) string {
	base := []string{"价量四灯近似；未对齐目标软件；仅使用信号日已完成日线"}
	base = append(base, reasons...)
	return strings.Join(base, "；")
}

func validFourLightRule(rule string) bool {
	switch rule {
	case "", FourLightRuleAllRed, FourLightRuleFlipAny, FourLightRuleFlipAll:
		return true
	}
	return false
}

func matchesFourLightRule(rule string, evidence FourLightEvidence) bool {
	switch rule {
	case "", FourLightRuleAllRed:
		return evidence.AllRed
	case FourLightRuleFlipAny:
		return evidence.AllRed && evidence.FlipCount > 0
	case FourLightRuleFlipAll:
		return evidence.AllRed && evidence.AllFlipped
	default:
		return false
	}
}
