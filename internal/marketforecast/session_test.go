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

func TestBuildSessionSignalsUsesPriorSessionsAndReportsMetrics(t *testing.T) {
	bars := sessionTestBars(150)
	minutes := sessionTestMinutes(bars)
	target, _ := nextWeekday(bars[len(bars)-1].Date)
	signals := buildSessionSignals(bars, minutes, target)
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
		if signal.Training.Samples < SessionMinTrainingSamples+SessionMinValidationSample || signal.Validation.Samples != SessionMinValidationSample {
			t.Fatalf("invalid sample evidence: %+v", signal)
		}
		if signal.Validation.WalkForwardSamples == 0 || len(signal.Weekdays) != 5 {
			t.Fatalf("walk-forward or weekday evidence missing: %+v", signal)
		}
	}
}

func TestBuildSessionSignalsDeterministicAndIgnoresTargetDayMinutes(t *testing.T) {
	bars := sessionTestBars(150)
	minutes := sessionTestMinutes(bars)
	target, _ := nextWeekday(bars[len(bars)-1].Date)
	first, _ := json.Marshal(buildSessionSignals(bars, minutes, target))

	// Target-day minutes are not training features for a pre-open signal.
	day, _ := time.ParseInLocation("2006-01-02", target, marketShanghai())
	extra := make([]MinuteBar, 0, 48)
	for _, start := range []int{570, 780} {
		for minute := start + 5; minute <= start+120; minute += 5 {
			end := time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, marketShanghai())
			extra = append(extra, MinuteBar{End: end, Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 1})
		}
	}
	second, _ := json.Marshal(buildSessionSignals(bars, append(append([]MinuteBar(nil), minutes...), extra...), target))
	if string(first) != string(second) {
		t.Fatalf("target-day minutes changed the pre-open signal")
	}
}

func TestBuildSessionSignalsMarksMissingMinutes(t *testing.T) {
	bars := sessionTestBars(150)
	target, _ := nextWeekday(bars[len(bars)-1].Date)
	signals := buildSessionSignals(bars, nil, target)
	if len(signals) != 2 {
		t.Fatalf("missing minute signals = %+v", signals)
	}
	for _, signal := range signals {
		if signal.Status != "insufficient_history" || !strings.Contains(signal.Reason, "minute history") {
			t.Fatalf("missing minute history was not reported: %+v", signal)
		}
	}
}

func TestBuildSessionSignalsMarksInsufficientSamples(t *testing.T) {
	bars := sessionTestBars(90)
	minutes := sessionTestMinutes(bars)
	target, _ := nextWeekday(bars[len(bars)-1].Date)
	signals := buildSessionSignals(bars, minutes, target)
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
