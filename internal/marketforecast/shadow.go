package marketforecast

import (
	"fmt"
	"sort"
	"strconv"
)

const (
	shadowWalkForwardStart = 120
	shadowWalkForwardStep  = 10
	shadowWindowMinSamples = 30
)

// SessionShadowWindows are fixed lookback sizes used by the shadow ensemble.
// Zero means an expanding window. These models never change the formal
// direction or high-confidence fields.
var SessionShadowWindows = []int{120, 180, 240, 0}

// SessionShadow groups exploratory models that are rendered separately from
// the formal pre-open tree. A shadow result must never be treated as a
// recommendation or as evidence that the 90% target has been reached.
type SessionShadow struct {
	Status      string          `json:"status"`
	Note        string          `json:"note,omitempty"`
	MultiWindow *ShadowEnsemble `json:"multi_window,omitempty"`
	HangSeng    *ShadowVariant  `json:"hang_seng,omitempty"`
}

type ShadowEnsemble struct {
	Status           string             `json:"status"`
	Windows          []int              `json:"windows"`
	WindowMinSamples int                `json:"window_min_samples"`
	UpProbability    float64            `json:"up_probability"`
	Direction        string             `json:"direction,omitempty"`
	Validation       ShadowValidation   `json:"validation"`
	ByWindow         []ShadowWindowStat `json:"by_window"`
	ByYear           []ShadowYear       `json:"by_year,omitempty"`
	Reason           string             `json:"reason,omitempty"`
}

type ShadowWindowStat struct {
	Window           int     `json:"window"`
	TrainingSamples  int     `json:"training_samples"`
	Evaluation       int     `json:"evaluation_samples"`
	Predictions      int     `json:"predictions"`
	Coverage         float64 `json:"coverage"`
	Accuracy         float64 `json:"accuracy"`
	MajorityAccuracy float64 `json:"majority_accuracy"`
	UpProbability    float64 `json:"up_probability"`
}

type ShadowValidation struct {
	Status           string  `json:"status"`
	From             string  `json:"from,omitempty"`
	To               string  `json:"to,omitempty"`
	Samples          int     `json:"samples"`
	Predictions      int     `json:"predictions"`
	Coverage         float64 `json:"coverage"`
	Accuracy         float64 `json:"accuracy"`
	MajorityAccuracy float64 `json:"majority_accuracy"`
	StrongSamples    int     `json:"strong_samples,omitempty"`
	StrongCoverage   float64 `json:"strong_coverage,omitempty"`
	StrongAccuracy   float64 `json:"strong_accuracy,omitempty"`
	correct          int
	majorityCorrect  int
	strongCorrect    int
}

type ShadowYear struct {
	Year             int     `json:"year"`
	Samples          int     `json:"samples"`
	Predictions      int     `json:"predictions"`
	Coverage         float64 `json:"coverage"`
	Accuracy         float64 `json:"accuracy"`
	MajorityAccuracy float64 `json:"majority_accuracy"`
}

type ShadowVariant struct {
	Status        string           `json:"status"`
	Reason        string           `json:"reason,omitempty"`
	FeatureNames  []string         `json:"feature_names"`
	UpProbability float64          `json:"up_probability"`
	Direction     string           `json:"direction,omitempty"`
	Validation    ShadowValidation `json:"validation"`
	ByYear        []ShadowYear     `json:"by_year,omitempty"`
}

type shadowCounts struct {
	evaluation      int
	predictions     int
	correct         int
	majorityCorrect int
	strong          int
	strongCorrect   int
}

func buildSessionShadow(samples []sessionSample, depth int, session string, baseFeatures []float64, baseFeatureNames []string, hangSeng hangSengContext, useHangSeng bool, target string) *SessionShadow {
	out := &SessionShadow{Status: "insufficient_history"}
	ensemble, err := validateShadowSamples(samples, depth, SessionShadowWindows)
	if err != nil {
		out.Note = err.Error()
		return out
	}
	out.MultiWindow = &ensemble
	out.Status = ensemble.Status
	if ensemble.Status == "ok" && baseFeatures != nil {
		probability, byWindow, err := shadowProbability(samples, depth, SessionShadowWindows, baseFeatures)
		if err != nil {
			out.Status = "insufficient_history"
			out.Note = err.Error()
		} else {
			ensemble.UpProbability = probability
			ensemble.Direction = direction(probability)
			for index := range ensemble.ByWindow {
				ensemble.ByWindow[index].TrainingSamples = byWindow[index].TrainingSamples
				ensemble.ByWindow[index].UpProbability = byWindow[index].UpProbability
			}
		}
	}
	if session == SessionAfternoon {
		variant := buildHangSengShadow(samples, depth, baseFeatures, baseFeatureNames, hangSeng, useHangSeng, target)
		out.HangSeng = &variant
	}
	return out
}

func buildHangSengShadow(samples []sessionSample, depth int, baseFeatures []float64, baseFeatureNames []string, hangSeng hangSengContext, useHangSeng bool, target string) ShadowVariant {
	names := append([]string(nil), baseFeatureNames...)
	names = append(names, hangSengFeatureNames...)
	out := ShadowVariant{Status: "insufficient_history", FeatureNames: names}
	if !useHangSeng {
		out.Reason = "hang seng daily history unavailable; run stock-watcher sync-hang-seng-context"
		return out
	}
	targetFeatures, status := hangSeng.features(target)
	if status != "" {
		out.Status = status
		if status == "stale" {
			out.Reason = "hang seng history is older than the target date"
		} else {
			out.Reason = "hang seng history is insufficient or missing"
		}
		return out
	}
	if baseFeatures == nil {
		out.Reason = "session features unavailable for the afternoon shadow"
		return out
	}
	filtered := make([]sessionSample, 0, len(samples))
	for _, item := range samples {
		if len(item.hangSengFeatures) != len(hangSengFeatureNames) {
			continue
		}
		combined := append([]float64(nil), item.features...)
		combined = append(combined, item.hangSengFeatures...)
		filtered = append(filtered, sessionSample{
			date: item.date, weekday: item.weekday, label: item.label, features: combined,
		})
	}
	ensemble, err := validateShadowSamples(filtered, depth, []int{0})
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	out.Validation = ensemble.Validation
	out.ByYear = ensemble.ByYear
	if ensemble.Status != "ok" {
		out.Reason = ensemble.Reason
		return out
	}
	current := append([]float64(nil), baseFeatures...)
	current = append(current, targetFeatures...)
	probability, _, err := shadowProbability(filtered, depth, []int{0}, current)
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	out.Status = "ok"
	out.UpProbability = probability
	out.Direction = direction(probability)
	return out
}

func validateShadowSamples(samples []sessionSample, depth int, windows []int) (ShadowEnsemble, error) {
	out := ShadowEnsemble{
		Status: "insufficient_history", Windows: append([]int(nil), windows...),
		WindowMinSamples: shadowWindowMinSamples,
		Validation:       ShadowValidation{Status: "insufficient_history"},
	}
	if len(windows) == 0 || len(samples) < shadowWalkForwardStart+shadowWindowMinSamples {
		out.Reason = fmt.Sprintf("need %d shadow samples, have %d", shadowWalkForwardStart+shadowWindowMinSamples, len(samples))
		return out, nil
	}
	for _, window := range windows {
		if window < 0 {
			return out, fmt.Errorf("invalid shadow window %d", window)
		}
	}
	windowCounts := make([]shadowCounts, len(windows))
	yearCounts := map[int]*shadowCounts{}
	validation := ShadowValidation{Status: "ok"}
	for cut := shadowWalkForwardStart; cut < len(samples); cut += shadowWalkForwardStep {
		end := cut + shadowWalkForwardStep
		if end > len(samples) {
			end = len(samples)
		}
		models := make([]*sessionTree, len(windows))
		for index, window := range windows {
			start := 0
			if window > 0 && cut > window {
				start = cut - window
			}
			if cut-start < shadowWindowMinSamples {
				continue
			}
			models[index] = fitSessionTree(samples[start:cut], depth, SessionMinLeaf)
		}
		cutUp := 0
		for _, item := range samples[:cut] {
			if item.label == 1 {
				cutUp++
			}
		}
		majorityUp := cutUp*2 >= cut
		for _, item := range samples[cut:end] {
			if validation.From == "" {
				validation.From = item.date
			}
			validation.To = item.date
			validation.Samples++
			windowProbabilities := make([]float64, len(windows))
			available := true
			for index, model := range models {
				if model == nil {
					available = false
					continue
				}
				probability := model.probability(item.features)
				windowProbabilities[index] = probability
				counts := &windowCounts[index]
				counts.evaluation++
				counts.predictions++
				actualUp := item.label == 1
				if (probability >= 0.5) == actualUp {
					counts.correct++
				}
				if majorityUp == actualUp {
					counts.majorityCorrect++
				}
			}
			if !available {
				continue
			}
			probability := 0.0
			for _, value := range windowProbabilities {
				probability += value
			}
			probability /= float64(len(windowProbabilities))
			actualUp := item.label == 1
			validation.Predictions++
			if (probability >= 0.5) == actualUp {
				validation.correct++
			}
			if majorityUp == actualUp {
				validation.majorityCorrect++
			}
			if probability >= SessionHighConfidenceThreshold || probability <= 1-SessionHighConfidenceThreshold {
				validation.StrongSamples++
				if (probability >= 0.5) == actualUp {
					validation.strongCorrect++
				}
			}
			year, err := strconv.Atoi(item.date[:4])
			if err == nil {
				counts := yearCounts[year]
				if counts == nil {
					counts = &shadowCounts{}
					yearCounts[year] = counts
				}
				counts.evaluation++
				counts.predictions++
				if (probability >= 0.5) == actualUp {
					counts.correct++
				}
				if majorityUp == actualUp {
					counts.majorityCorrect++
				}
			}
		}
	}
	if validation.Samples == 0 || validation.Predictions == 0 {
		out.Reason = "shadow walk-forward produced no forecasts"
		return out, nil
	}
	finalizeShadowValidation(&validation)
	out.Validation = validation
	out.Status = "ok"
	out.ByWindow = make([]ShadowWindowStat, len(windows))
	for index, window := range windows {
		stat := ShadowWindowStat{Window: window}
		counts := windowCounts[index]
		stat.Evaluation = counts.evaluation
		stat.Predictions = counts.predictions
		stat.Coverage = shadowRatio(counts.predictions, counts.evaluation)
		stat.Accuracy = shadowRatio(counts.correct, counts.predictions)
		stat.MajorityAccuracy = shadowRatio(counts.majorityCorrect, counts.predictions)
		out.ByWindow[index] = stat
	}
	out.ByYear = shadowYears(yearCounts)
	return out, nil
}

func shadowProbability(samples []sessionSample, depth int, windows []int, features []float64) (float64, []ShadowWindowStat, error) {
	if len(samples) == 0 || len(windows) == 0 || features == nil {
		return 0, nil, fmt.Errorf("shadow model has no samples or features")
	}
	stats := make([]ShadowWindowStat, len(windows))
	probabilities := make([]float64, len(windows))
	for index, window := range windows {
		start := 0
		if window > 0 && len(samples) > window {
			start = len(samples) - window
		}
		training := samples[start:]
		if len(training) < shadowWindowMinSamples {
			return 0, nil, fmt.Errorf("shadow window %d needs %d samples, have %d", window, shadowWindowMinSamples, len(training))
		}
		model := fitSessionTree(training, depth, SessionMinLeaf)
		probability := model.probability(features)
		if !finite(probability) {
			return 0, nil, fmt.Errorf("shadow window %d returned an invalid probability", window)
		}
		probabilities[index] = probability
		stats[index] = ShadowWindowStat{Window: window, TrainingSamples: len(training), UpProbability: probability}
	}
	sum := 0.0
	for _, probability := range probabilities {
		sum += probability
	}
	return sum / float64(len(probabilities)), stats, nil
}

func finalizeShadowValidation(out *ShadowValidation) {
	out.Coverage = shadowRatio(out.Predictions, out.Samples)
	out.Accuracy = shadowRatio(out.correct, out.Predictions)
	out.MajorityAccuracy = shadowRatio(out.majorityCorrect, out.Predictions)
	out.StrongCoverage = shadowRatio(out.StrongSamples, out.Predictions)
	out.StrongAccuracy = shadowRatio(out.strongCorrect, out.StrongSamples)
}

func shadowYears(counts map[int]*shadowCounts) []ShadowYear {
	years := make([]int, 0, len(counts))
	for year := range counts {
		years = append(years, year)
	}
	sort.Ints(years)
	out := make([]ShadowYear, 0, len(years))
	for _, year := range years {
		value := counts[year]
		out = append(out, ShadowYear{
			Year: year, Samples: value.evaluation, Predictions: value.predictions,
			Coverage: shadowRatio(value.predictions, value.evaluation), Accuracy: shadowRatio(value.correct, value.predictions),
			MajorityAccuracy: shadowRatio(value.majorityCorrect, value.predictions),
		})
	}
	return out
}

func shadowRatio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func direction(probability float64) string {
	if probability >= 0.5 {
		return "up"
	}
	return "down"
}
