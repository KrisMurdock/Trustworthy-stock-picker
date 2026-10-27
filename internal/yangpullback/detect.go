package yangpullback

import (
	"fmt"
	"math"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

// Scan detects frozen yang-pullback signals from completed adjusted daily bars.
func Scan(history market.History, opts Options, mode Mode) ([]Signal, error) {
	opts, err := opts.Normalize()
	if err != nil {
		return nil, err
	}
	if !validMode(mode) {
		return nil, fmt.Errorf("invalid yang-pullback mode %q", mode)
	}
	if err := validateScanInput(history, opts.AsOf); err != nil {
		return nil, err
	}

	signals := make([]Signal, 0)
	if len(history.Bars) < 60 {
		return signals, nil
	}

	seenAnchors := make(map[int]bool)
	for i := 59; i < len(history.Bars); i++ {
		signal, anchor, ok := detectRaw(history.Code, history.Bars, i, opts, mode)
		if !ok || seenAnchors[anchor] {
			continue
		}
		seenAnchors[anchor] = true

		if opts.RequirePhase {
			phase, confidence, score, passed, err := phaseGate(history.Bars, i, opts.PhaseWindow)
			if err != nil {
				return nil, fmt.Errorf("evaluate phase at %s: %w", history.Bars[i].Date, err)
			}
			if !passed {
				continue
			}
			signal.Phase = phase
			signal.PhaseConfidence = confidence
			signal.PhaseScore = score
		}
		signals = append(signals, signal)
	}
	return signals, nil
}

func validMode(mode Mode) bool {
	switch mode {
	case ModePattern, ModePullbackOnly, ModeHoldOnly:
		return true
	default:
		return false
	}
}

func validateScanInput(history market.History, asof string) error {
	if len(history.Bars) == 0 {
		return fmt.Errorf("yang-pullback scan requires daily bars")
	}
	lastDate := history.Bars[len(history.Bars)-1].Date
	if asof == "" || asof != lastDate {
		return fmt.Errorf("yang-pullback as-of %q must match last bar %q", asof, lastDate)
	}

	previous := ""
	for _, bar := range history.Bars {
		if bar.Date == "" || (previous != "" && bar.Date <= previous) {
			return fmt.Errorf("yang-pullback bars must have strictly increasing dates")
		}
		previous = bar.Date
		for _, value := range []float64{bar.Open, bar.Close, bar.High, bar.Low, bar.Volume} {
			if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("yang-pullback bar %s has an invalid numeric value", bar.Date)
			}
		}
		if bar.Open <= 0 || bar.Close <= 0 || bar.Low <= 0 ||
			bar.High < math.Max(bar.Open, bar.Close) || bar.Low > math.Min(bar.Open, bar.Close) {
			return fmt.Errorf("yang-pullback bar %s has an invalid OHLC range", bar.Date)
		}
	}
	return nil
}

func detectRaw(code string, bars []market.Bar, i int, opts Options, mode Mode) (Signal, int, bool) {
	low := i - opts.HighWindow + 1
	highEnd := i - opts.PullbackMin - 1
	if low < 0 || highEnd < low {
		return Signal{}, 0, false
	}

	highIndex := low
	for j := low + 1; j <= highEnd; j++ {
		if bars[j].High >= bars[highIndex].High {
			highIndex = j
		}
	}

	pullbackDays := i - highIndex - 1
	if pullbackDays < opts.PullbackMin || pullbackDays > opts.PullbackMax {
		return Signal{}, 0, false
	}
	if mode != ModePattern && pullbackDays != opts.PullbackMin {
		return Signal{}, 0, false
	}

	anchor := -1
	for j := highIndex; j >= low; j-- {
		if bars[j].Close > bars[j].Open {
			anchor = j
			break
		}
	}
	if anchor < 0 {
		return Signal{}, 0, false
	}

	holdsAnchor := true
	for j := highIndex + 1; j < i; j++ {
		if bars[j].Close > bars[highIndex].High {
			return Signal{}, 0, false
		}
		if bars[j].Close < bars[anchor].Low {
			holdsAnchor = false
		}
	}

	switch mode {
	case ModePattern:
		bar := bars[i]
		if !holdsAnchor || bar.Close <= bar.Open || bar.Close <= bars[i-1].High || bar.Close < bars[anchor].Low {
			return Signal{}, 0, false
		}
	case ModeHoldOnly:
		if !holdsAnchor {
			return Signal{}, 0, false
		}
	case ModePullbackOnly:
	}

	return Signal{
		Version:      Version,
		Rule:         Rule,
		Code:         code,
		Date:         bars[i].Date,
		HighDate:     bars[highIndex].Date,
		High:         bars[highIndex].High,
		AnchorDate:   bars[anchor].Date,
		AnchorLow:    bars[anchor].Low,
		PullbackDays: pullbackDays,
	}, anchor, true
}

func phaseGate(bars []market.Bar, i, window int) (string, string, float64, bool, error) {
	start := i + 1 - window
	if start < 0 {
		start = 0
	}
	sample := bars[start : i+1]
	features, err := strategy.Extract(sample)
	if err != nil {
		return "", "", 0, false, err
	}
	wave := strategy.AnalyzeWave(sample)
	phase := strategy.AnalyzePhase(sample, features, &wave)
	if phase.Phase != strategy.PhaseAdvance || phase.Confidence == strategy.PhaseUnknown {
		return "", "", 0, false, nil
	}
	return phase.Phase, phase.Confidence, phase.ConfidenceScore, true, nil
}
