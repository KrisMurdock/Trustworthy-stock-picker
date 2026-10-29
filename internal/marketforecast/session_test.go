package marketforecast

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func sessionTestBars(count int) []Bar {
	days := businessDays(time.Date(2025, 1, 6, 0, 0, 0, 0, time.UTC), count)
	bars := make([]Bar, 0, len(days))
	close := 3000.0
	for index, day := range days {
		close *= 1 + 0.0004 + math.Sin(float64(index)/9)*0.001
		bars = append(bars, Bar{Date: day.Format("2006-01-02"), Open: close * 0.999, Close: close, High: close * 1.004, Low: close * 0.996})
	}
	return bars
}

func sessionTestMinutes(bars []Bar) []MinuteBar {
	out := make([]MinuteBar, 0, len(bars)*48)
	for index := 1; index < len(bars); index++ {
		day, _ := time.ParseInLocation("2006-01-02", bars[index].Date, marketShanghai())
		prevClose := bars[index-1].Close
		morningReturn := math.Sin(float64(index)/4) * 0.003
		afternoonReturn := math.Cos(float64(index)/6) * 0.003
		morningClose := prevClose * (1 + morningReturn)
		close := morningClose * (1 + afternoonReturn)
		for _, start := range []int{570, 780} {
			for minute := start + 5; minute <= start+120; minute += 5 {
				end := time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, marketShanghai())
				progress := float64(minute-start) / 120
				price := prevClose + (morningClose-prevClose)*progress
				if start == 780 {
					price = morningClose + (close-morningClose)*progress
				}
				out = append(out, MinuteBar{
					End: end, Open: price * 0.9995, High: price * 1.0005, Low: price * 0.999, Close: price, Volume: 1000,
				})
			}
		}
	}
	return out
}

func sessionTestThirtyMinutes(bars []Bar) []MinuteBar {
	out := make([]MinuteBar, 0, len(bars)*8)
	for index := 1; index < len(bars); index++ {
		day, _ := time.ParseInLocation("2006-01-02", bars[index].Date, marketShanghai())
		prevClose := bars[index-1].Close
		morningReturn := math.Sin(float64(index)/4) * 0.003
		afternoonReturn := math.Cos(float64(index)/6) * 0.003
		morningClose := prevClose * (1 + morningReturn)
		close := morningClose * (1 + afternoonReturn)
		for _, minute := range []int{600, 630, 660, 690, 810, 840, 870, 900} {
			end := time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, marketShanghai())
			price := prevClose + (morningClose-prevClose)*float64((minute-570)%120)/120
			if minute >= 780 {
				price = morningClose + (close-morningClose)*float64(minute-780+30)/150
			}
			if minute == 690 {
				price = morningClose
			}
			if minute == 900 {
				price = close
			}
			out = append(out, MinuteBar{End: end, Open: price * 0.9995, High: price * 1.0005, Low: price * 0.999, Close: price, Volume: 1000})
		}
	}
	return out
}

func TestBuildSessionSignalsUsesPriorSessionsAndReportsMetrics(t *testing.T) {
	bars := sessionTestBars(150)
	minutes := sessionTestMinutes(bars)
	target, _ := nextWeekday(bars[len(bars)-1].Date)
	signals := buildSessionSignals(bars, minutes, nil, target, koreaContext{}, false)
	if len(signals) != 2 {
		t.Fatalf("session signals = %+v", signals)
	}
	for _, signal := range signals {
		if signal.Status != "ok" || signal.Session == "" || signal.TargetDate != target {
			t.Fatalf("unexpected session signal: %+v", signal)
		}
		if signal.UpProbability <= 0 || signal.UpProbability >= 1 || signal.Direction == "" {
			t.Fatalf("invalid probability: %+v", signal)
		}
		if signal.CalibratedUpProbability <= 0 || signal.CalibratedUpProbability >= 1 {
			t.Fatalf("invalid calibrated probability: %+v", signal)
		}
		if signal.Training.Samples < SessionMinTrainingSamples+SessionMinValidationSample || signal.Validation.Samples != SessionMinValidationSample {
			t.Fatalf("invalid sample evidence: %+v", signal)
		}
		if signal.Validation.Calibration.Status != "ok" || signal.Validation.Calibration.Samples == 0 || signal.Validation.Calibration.EvaluationSamples == 0 {
			t.Fatalf("calibration evidence missing: %+v", signal.Validation)
		}
		if signal.Validation.Calibration.BrierScore < 0 || signal.Validation.Calibration.BrierScore > 1 {
			t.Fatalf("invalid calibration Brier score: %+v", signal.Validation.Calibration)
		}
		if signal.Validation.WalkForwardSamples == 0 || len(signal.Weekdays) != 5 {
			t.Fatalf("walk-forward or weekday evidence missing: %+v", signal)
		}
	}
}

func TestProbabilityCalibratorShrinksMiscalibratedProbabilities(t *testing.T) {
	pairs := make([]calibrationPair, 0, 60)
	for index := 0; index < 60; index++ {
		pairs = append(pairs, calibrationPair{probability: 0.8, label: index % 2})
	}
	calibrator := fitProbabilityCalibrator(pairs)
	if calibrator.status != "ok" || calibrator.samples != len(pairs) {
		t.Fatalf("unexpected calibrator: %+v", calibrator)
	}
	calibrated := calibrator.apply(0.8)
	if calibrated < 0.4 || calibrated > 0.6 {
		t.Fatalf("miscalibrated probability was not shrunk: %.4f", calibrated)
	}
	if calibrator.brier >= 0.3 {
		t.Fatalf("calibrated Brier score did not improve: %.4f", calibrator.brier)
	}
	second := fitProbabilityCalibrator(pairs)
	if second.slope != calibrator.slope || second.intercept != calibrator.intercept || second.brier != calibrator.brier {
		t.Fatal("calibration is not deterministic")
	}
}

func TestProbabilityCalibratorUsesIdentityUntilEnoughOutOfFoldSamples(t *testing.T) {
	pairs := make([]calibrationPair, calibrationMinSamples-1)
	for index := range pairs {
		pairs[index] = calibrationPair{probability: 0.8, label: 0}
	}
	calibrator := fitProbabilityCalibrator(pairs)
	if calibrator.status != "insufficient_history" || calibrator.apply(0.8) != 0.8 {
		t.Fatalf("unexpected early calibrator: %+v", calibrator)
	}
}

func TestSessionHighConfidenceRequiresDirectionAgreement(t *testing.T) {
	if !sessionHighConfidence(0.8, 0.7) || !sessionHighConfidence(0.2, 0.3) {
		t.Fatal("agreeing high-confidence direction was rejected")
	}
	if sessionHighConfidence(0.8, 0.4) || sessionHighConfidence(0.2, 0.6) {
		t.Fatal("disagreeing direction was accepted as high confidence")
	}
}

func TestSessionHighConfidenceStatusRejectsSmallOrWorseSubsets(t *testing.T) {
	cases := []struct {
		name string
		in   SessionValidation
		want string
	}{
		{"calibration", SessionValidation{Calibration: SessionCalibration{Status: "insufficient_history"}}, "calibration_insufficient"},
		{"empty", SessionValidation{Calibration: SessionCalibration{Status: "ok"}}, "no_samples"},
		{"small", SessionValidation{Calibration: SessionCalibration{Status: "ok"}, HighConfidenceSamples: 19, HighConfidenceAccuracy: 0.9}, "insufficient_samples"},
		{"below raw", SessionValidation{Calibration: SessionCalibration{Status: "ok"}, HighConfidenceSamples: 40, HighConfidenceAccuracy: 0.55, RawHighConfidenceSamples: 100, RawHighConfidenceAccuracy: 0.60}, "below_raw"},
		{"usable", SessionValidation{Calibration: SessionCalibration{Status: "ok"}, HighConfidenceSamples: 40, HighConfidenceAccuracy: 0.65, RawHighConfidenceSamples: 100, RawHighConfidenceAccuracy: 0.60}, "ok"},
	}
	for _, tc := range cases {
		if got := sessionHighConfidenceStatus(tc.in); got != tc.want {
			t.Fatalf("%s: status = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestBuildSessionSignalsDeterministicAndIgnoresTargetDayMinutes(t *testing.T) {
	bars := sessionTestBars(150)
	minutes := sessionTestMinutes(bars)
	target, _ := nextWeekday(bars[len(bars)-1].Date)
	first, _ := json.Marshal(buildSessionSignals(bars, minutes, nil, target, koreaContext{}, false))

	// Target-day minutes are not training features for a pre-open signal.
	day, _ := time.ParseInLocation("2006-01-02", target, marketShanghai())
	extra := make([]MinuteBar, 0, 48)
	for _, start := range []int{570, 780} {
		for minute := start + 5; minute <= start+120; minute += 5 {
			end := time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, marketShanghai())
			extra = append(extra, MinuteBar{End: end, Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 1})
		}
	}
	second, _ := json.Marshal(buildSessionSignals(bars, append(append([]MinuteBar(nil), minutes...), extra...), nil, target, koreaContext{}, false))
	if string(first) != string(second) {
		t.Fatalf("target-day minutes changed the pre-open signal")
	}
}

func TestBuildSessionSignalsMarksMissingMinutes(t *testing.T) {
	bars := sessionTestBars(150)
	target, _ := nextWeekday(bars[len(bars)-1].Date)
	signals := buildSessionSignals(bars, nil, nil, target, koreaContext{}, false)
	if len(signals) != 2 {
		t.Fatalf("missing minute signals = %+v", signals)
	}
	for _, signal := range signals {
		if signal.Status != "insufficient_history" || !strings.Contains(signal.Reason, "minute history") {
			t.Fatalf("missing minute history was not reported: %+v", signal)
		}
	}
}

func TestBuildSessionSignalsUsesThirtyMinuteHistory(t *testing.T) {
	bars := sessionTestBars(150)
	minutes := sessionTestThirtyMinutes(bars)
	target, _ := nextWeekday(bars[len(bars)-1].Date)
	signals := buildSessionSignals(bars, nil, minutes, target, koreaContext{}, false)
	if len(signals) != 2 {
		t.Fatalf("session signals = %+v", signals)
	}
	for _, signal := range signals {
		if signal.Status != "ok" || signal.Training.Samples < SessionMinTrainingSamples+SessionMinValidationSample {
			t.Fatalf("unexpected thirty-minute signal: %+v", signal)
		}
	}
}

func TestBuildSessionSignalsMarksInsufficientSamples(t *testing.T) {
	bars := sessionTestBars(90)
	minutes := sessionTestMinutes(bars)
	target, _ := nextWeekday(bars[len(bars)-1].Date)
	signals := buildSessionSignals(bars, minutes, nil, target, koreaContext{}, false)
	if len(signals) != 2 {
		t.Fatalf("insufficient session signals = %+v", signals)
	}
	for _, signal := range signals {
		if signal.Status != "insufficient_history" || signal.Reason == "" {
			t.Fatalf("insufficient history was not reported: %+v", signal)
		}
	}
}

func TestSessionTreeDeterministicAndDepthLimited(t *testing.T) {
	samples := make([]sessionSample, 0, 40)
	for index := 0; index < 40; index++ {
		label := index % 3
		if label == 2 {
			label = 0
		}
		samples = append(samples, sessionSample{
			date: "2026-01-01", label: label,
			features: []float64{float64(index%7) / 7, float64(index%5) / 5},
		})
	}
	first := fitSessionTree(samples, 3, 4)
	second := fitSessionTree(samples, 3, 4)
	for index := 0; index < 20; index++ {
		values := []float64{float64(index) / 20, float64(index%3) / 3}
		if first.probability(values) != second.probability(values) {
			t.Fatal("session tree is not deterministic")
		}
	}
	stump := fitSessionTree(samples, 0, 4)
	if stump.root == nil || !stump.root.leaf {
		t.Fatal("depth zero tree is not a leaf")
	}
}
