package marketforecast

import (
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	SessionMorning   = "morning"
	SessionAfternoon = "afternoon"

	SessionModelID        = "index_session_tree_v3"
	SessionModelVersion   = 3
	SessionFeatureVersion = 2

	SessionMinTrainingSamples  = 30
	SessionMinValidationSample = 20
	SessionWalkForwardStart    = 40
	SessionWalkForwardStep     = 10

	SessionMorningMaxDepth   = 3
	SessionAfternoonMaxDepth = 2
	SessionMinLeaf           = 8

	SessionHighConfidenceThreshold = 0.65
	SessionTargetAccuracy          = 0.90
)

// SessionCalibration describes the Platt mapping applied to tree leaf
// probabilities. Samples is the walk-forward fit set, while Brier and ECE are
// measured only on later out-of-fold blocks; they describe calibration quality,
// not a promise about future returns.
type SessionCalibration struct {
	Status                   string  `json:"status"`
	Method                   string  `json:"method,omitempty"`
	Samples                  int     `json:"samples"`
	EvaluationSamples        int     `json:"evaluation_samples,omitempty"`
	Slope                    float64 `json:"slope,omitempty"`
	Intercept                float64 `json:"intercept,omitempty"`
	BrierScore               float64 `json:"brier_score,omitempty"`
	ExpectedCalibrationError float64 `json:"expected_calibration_error,omitempty"`
}

var sessionFeatureNames = []string{
	"prev_morning_ret", "prev_afternoon_ret", "morning_5d", "afternoon_5d",
}

// MinuteBar is a native five-minute index bar used by the session model. The
// end timestamp is the close of the interval; only final bars enter training.
type MinuteBar struct {
	End    time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

type sessionSample struct {
	date     string
	weekday  int
	label    int
	features []float64
}

type sessionDay struct {
	date            string
	morningReturn   float64
	afternoonReturn float64
}

// SessionValidation reports a time-ordered holdout plus an expanding-window
// walk-forward estimate. High-confidence metrics are computed on walk-forward
// predictions so the 90% target is never judged on a single small holdout.
type SessionValidation struct {
	Status           string  `json:"status"`
	From             string  `json:"from,omitempty"`
	To               string  `json:"to,omitempty"`
	Samples          int     `json:"samples"`
	Accuracy         float64 `json:"accuracy"`
	MajorityAccuracy float64 `json:"majority_accuracy"`
	WeekdayAccuracy  float64 `json:"weekday_accuracy"`

	WalkForwardSamples          int     `json:"walk_forward_samples,omitempty"`
	WalkForwardAccuracy         float64 `json:"walk_forward_accuracy,omitempty"`
	WalkForwardMajorityAccuracy float64 `json:"walk_forward_majority_accuracy,omitempty"`
	WalkForwardWeekdayAccuracy  float64 `json:"walk_forward_weekday_accuracy,omitempty"`
	CalibratedAccuracy          float64 `json:"calibrated_accuracy,omitempty"`

	HighConfidenceSamples          int                `json:"high_confidence_samples,omitempty"`
	HighConfidenceCoverage         float64            `json:"high_confidence_coverage,omitempty"`
	HighConfidenceAccuracy         float64            `json:"high_confidence_accuracy,omitempty"`
	HighConfidenceMajorityAccuracy float64            `json:"high_confidence_majority_accuracy,omitempty"`
	HighConfidenceWilsonLow        float64            `json:"high_confidence_wilson_low,omitempty"`
	HighConfidenceWilsonHigh       float64            `json:"high_confidence_wilson_high,omitempty"`
	RawHighConfidenceSamples       int                `json:"raw_high_confidence_samples,omitempty"`
	RawHighConfidenceAccuracy      float64            `json:"raw_high_confidence_accuracy,omitempty"`
	HighConfidenceStatus           string             `json:"high_confidence_status,omitempty"`
	Calibration                    SessionCalibration `json:"calibration"`

	TargetMet  bool   `json:"target_met"`
	Conclusion string `json:"conclusion,omitempty"`
}

// SessionSignal is one pre-open direction forecast for an index session.
type SessionSignal struct {
	Session                 string            `json:"session"`
	Label                   string            `json:"label"`
	Mode                    string            `json:"mode"`
	Status                  string            `json:"status"`
	TargetDate              string            `json:"target_date,omitempty"`
	Direction               string            `json:"direction,omitempty"`
	UpProbability           float64           `json:"up_probability"`
	CalibratedUpProbability float64           `json:"calibrated_up_probability"`
	HighConfidence          bool              `json:"high_confidence"`
	Reason                  string            `json:"reason,omitempty"`
	Training                TrainingInfo      `json:"training"`
	Validation              SessionValidation `json:"validation"`
	Weekdays                []WeekdayStat     `json:"weekdays"`
}

// SessionFeatureNames returns the daily feature order followed by the session
// feature order. Korean features are appended only when their cache is usable.
func SessionFeatureNames(useKorea bool) []string {
	out := FeatureNames()
	out = append(out, sessionFeatureNames...)
	if useKorea {
		out = append(out, koreaFeatureNames...)
	}
	return out
}

func buildSessionSignals(bars []Bar, minutes, sessionMinutes []MinuteBar, target string, korea koreaContext, useKorea bool) []SessionSignal {
	if len(bars) < 62 || target == "" {
		return nil
	}
	days := buildSessionDays(bars, minutes, sessionMinutes)
	morningSamples, afternoonSamples := sessionSamplesFor(bars, days, korea, useKorea)
	out := make([]SessionSignal, 0, 2)
	for _, item := range []struct {
		name    string
		label   string
		depth   int
		samples []sessionSample
	}{
		{SessionMorning, "上午", SessionMorningMaxDepth, morningSamples},
		{SessionAfternoon, "下午", SessionAfternoonMaxDepth, afternoonSamples},
	} {
		signal := SessionSignal{
			Session: item.name, Label: item.label, Mode: "pre_open",
			Status: "insufficient_history", TargetDate: target,
			Training:   trainingInfoForSession(item.samples),
			Validation: SessionValidation{Status: "insufficient_history"},
			Weekdays:   sessionWeekdayStats(days, item.name),
		}
		if len(days) == 0 {
			signal.Reason = "minute history unavailable; run stock-watcher sync-index-minutes"
			out = append(out, signal)
			continue
		}
		if len(item.samples) < SessionMinTrainingSamples+SessionMinValidationSample {
			signal.Reason = fmt.Sprintf("need %d session samples, have %d", SessionMinTrainingSamples+SessionMinValidationSample, len(item.samples))
			out = append(out, signal)
			continue
		}
		features, ok := sessionFeatureValues(bars, days, target, korea, useKorea)
		if !ok {
			signal.Reason = "session features unavailable; minute history may end before the latest index bar"
			out = append(out, signal)
			continue
		}
		validation, calibrator := validateSessionTree(item.samples, item.depth)
		signal.Validation = validation
		model := fitSessionTree(item.samples, item.depth, SessionMinLeaf)
		probability := model.probability(features)
		calibrated := calibrator.apply(probability)
		if !finite(probability) || !finite(calibrated) {
			signal.Reason = "session model returned an invalid probability"
			out = append(out, signal)
			continue
		}
		signal.Status = "ok"
		signal.UpProbability = probability
		signal.CalibratedUpProbability = calibrated
		signal.Direction = "down"
		if probability >= 0.5 {
			signal.Direction = "up"
		}
		signal.HighConfidence = validation.HighConfidenceStatus == "ok" && sessionHighConfidence(probability, calibrated)
		out = append(out, signal)
	}
	return out
}

func normalizeSessionBars(input []MinuteBar, period int) []MinuteBar {
	out := make([]MinuteBar, 0, len(input))
	for _, bar := range input {
		if bar.End.IsZero() || bar.Close <= 0 || bar.Open <= 0 || bar.Low <= 0 || bar.High < bar.Open || bar.High < bar.Close || bar.Low > bar.Open || bar.Low > bar.Close {
			continue
		}
		if !finite(bar.Volume) || bar.Volume < 0 {
			continue
		}
		if _, ok := minuteSessionTime(bar.End, period); !ok {
			continue
		}
		out = append(out, bar)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].End.Before(out[j].End) })
	if len(out) < 2 {
		return out
	}
	deduped := out[:1]
	for _, bar := range out[1:] {
		if bar.End.Equal(deduped[len(deduped)-1].End) {
			deduped[len(deduped)-1] = bar
			continue
		}
		deduped = append(deduped, bar)
	}
	return deduped
}

func minuteSessionTime(end time.Time, period int) (int, bool) {
	if period != 5 && period != 30 {
		return 0, false
	}
	t := end.In(marketShanghai())
	n := t.Hour()*60 + t.Minute()
	for _, start := range []int{570, 780} {
		if n > start && n <= start+120 && (n-start)%period == 0 && t.Second() == 0 && t.Nanosecond() == 0 {
			return start, true
		}
	}
	return 0, false
}

func buildSessionDays(bars []Bar, minutes, sessionMinutes []MinuteBar) []sessionDay {
	byDate := map[string]sessionDay{}
	for _, day := range buildSessionDaysForPeriod(bars, sessionMinutes, 30) {
		byDate[day.date] = day
	}
	for _, day := range buildSessionDaysForPeriod(bars, minutes, 5) {
		if _, ok := byDate[day.date]; !ok {
			byDate[day.date] = day
		}
	}
	out := make([]sessionDay, 0, len(byDate))
	for _, day := range byDate {
		out = append(out, day)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].date < out[j].date })
	return out
}

func buildSessionDaysForPeriod(bars []Bar, minutes []MinuteBar, period int) []sessionDay {
	grouped := map[string][]MinuteBar{}
	tz := marketShanghai()
	for _, bar := range normalizeSessionBars(minutes, period) {
		day := bar.End.In(tz).Format("2006-01-02")
		grouped[day] = append(grouped[day], bar)
	}
	byDate := map[string]int{}
	for index, bar := range bars {
		byDate[bar.Date] = index
	}
	dates := make([]string, 0, len(grouped))
	for date := range grouped {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	out := make([]sessionDay, 0, len(dates))
	for _, date := range dates {
		dayBars := grouped[date]
		required, morningRequired, afternoonRequired := 48, 24, 24
		if period == 30 {
			required, morningRequired, afternoonRequired = 8, 4, 4
		}
		if len(dayBars) != required {
			continue
		}
		morningCount, afternoonCount := 0, 0
		var morning, closeBar *MinuteBar
		for index := range dayBars {
			bar := &dayBars[index]
			start, ok := minuteSessionTime(bar.End, period)
			if !ok {
				continue
			}
			if start == 570 {
				morningCount++
			} else {
				afternoonCount++
			}
			local := bar.End.In(tz)
			minute := local.Hour()*60 + local.Minute()
			if minute == 690 {
				morning = bar
			}
			if minute == 900 {
				closeBar = bar
			}
		}
		if morning == nil || closeBar == nil || morningCount != morningRequired || afternoonCount != afternoonRequired {
			continue
		}
		index, ok := byDate[date]
		if !ok || index == 0 || index >= len(bars) {
			continue
		}
		prevClose := bars[index-1].Close
		if prevClose <= 0 || morning.Close <= 0 || closeBar.Close <= 0 {
			continue
		}
		out = append(out, sessionDay{
			date: date, morningReturn: morning.Close/prevClose - 1,
			afternoonReturn: closeBar.Close/morning.Close - 1,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].date < out[j].date })
	return out
}

func sessionSamplesFor(bars []Bar, days []sessionDay, korea koreaContext, useKorea bool) ([]sessionSample, []sessionSample) {
	if len(days) == 0 {
		return nil, nil
	}
	byDate := map[string]int{}
	for index, bar := range bars {
		byDate[bar.Date] = index
	}
	morning := make([]sessionSample, 0, len(days))
	afternoon := make([]sessionSample, 0, len(days))
	for k := 5; k < len(days); k++ {
		day := days[k]
		index, ok := byDate[day.date]
		if !ok || index < 61 || index == 0 {
			continue
		}
		// The prior session must be the immediately preceding daily bar. A gap
		// in minute history would silently mix non-adjacent sessions.
		if days[k-1].date != bars[index-1].Date {
			continue
		}
		features, ok := featureValues(bars, index-1, day.date)
		if !ok {
			continue
		}
		morning5, afternoon5 := 0.0, 0.0
		for j := k - 5; j < k; j++ {
			morning5 += days[j].morningReturn
			afternoon5 += days[j].afternoonReturn
		}
		features = append(features, days[k-1].morningReturn, days[k-1].afternoonReturn, morning5/5, afternoon5/5)
		if useKorea {
			koreaFeatures, ok := korea.features(day.date)
			if !ok {
				continue
			}
			features = append(features, koreaFeatures...)
		}
		weekday := targetWeekday(day.date)
		if day.morningReturn != 0 {
			label := 0
			if day.morningReturn > 0 {
				label = 1
			}
			morning = append(morning, sessionSample{
				date: day.date, weekday: weekday, label: label, features: append([]float64(nil), features...),
			})
		}
		if day.afternoonReturn != 0 {
			label := 0
			if day.afternoonReturn > 0 {
				label = 1
			}
			afternoon = append(afternoon, sessionSample{
				date: day.date, weekday: weekday, label: label, features: append([]float64(nil), features...),
			})
		}
	}
	return morning, afternoon
}

func sessionFeatureValues(bars []Bar, days []sessionDay, target string, korea koreaContext, useKorea bool) ([]float64, bool) {
	if len(bars) < 62 || len(days) < 5 {
		return nil, false
	}
	last := bars[len(bars)-1]
	if days[len(days)-1].date != last.Date {
		return nil, false
	}
	features, ok := featureValues(bars, len(bars)-1, target)
	if !ok {
		return nil, false
	}
	morning5, afternoon5 := 0.0, 0.0
	for index := len(days) - 5; index < len(days); index++ {
		morning5 += days[index].morningReturn
		afternoon5 += days[index].afternoonReturn
	}
	lastDay := days[len(days)-1]
	features = append(features, lastDay.morningReturn, lastDay.afternoonReturn, morning5/5, afternoon5/5)
	if useKorea {
		koreaFeatures, ok := korea.features(target)
		if !ok {
			return nil, false
		}
		features = append(features, koreaFeatures...)
	}
	return features, true
}

func sessionWeekdayStats(days []sessionDay, session string) []WeekdayStat {
	type aggregate struct {
		up, down, flat int
		sum            float64
	}
	values := map[int]*aggregate{}
	for weekday := 1; weekday <= 5; weekday++ {
		values[weekday] = &aggregate{}
	}
	for _, day := range days {
		weekday := parseDateWeekday(day.date)
		value, ok := values[weekday]
		if !ok {
			continue
		}
		returnValue := day.morningReturn
		if session == SessionAfternoon {
			returnValue = day.afternoonReturn
		}
		value.sum += returnValue
		switch {
		case returnValue > 0:
			value.up++
		case returnValue < 0:
			value.down++
		default:
			value.flat++
		}
	}
	out := make([]WeekdayStat, 0, 5)
	for weekday := 1; weekday <= 5; weekday++ {
		value := values[weekday]
		samples := value.up + value.down + value.flat
		winRate := 0.5
		if value.up+value.down > 0 {
			winRate = float64(value.up) / float64(value.up+value.down)
		}
		low, high := wilson(value.up, value.up+value.down)
		mean := 0.0
		if samples > 0 {
			mean = value.sum / float64(samples) * 100
		}
		out = append(out, WeekdayStat{
			Weekday: weekday, Label: weekdayLabels[weekday], Samples: samples,
			Up: value.up, Down: value.down, Flat: value.flat, WinRate: winRate,
			MeanReturnPct: mean, WilsonLow: low, WilsonHigh: high,
		})
	}
	return out
}

func trainingInfoForSession(samples []sessionSample) TrainingInfo {
	out := TrainingInfo{Samples: len(samples)}
	if len(samples) == 0 {
		return out
	}
	out.From, out.To = samples[0].date, samples[len(samples)-1].date
	for _, item := range samples {
		if item.label == 1 {
			out.Up++
		} else {
			out.Down++
		}
	}
	return out
}

func validateSessionTree(samples []sessionSample, depth int) (SessionValidation, probabilityCalibrator) {
	out := SessionValidation{Status: "insufficient_history", Calibration: SessionCalibration{Status: "insufficient_history"}}
	if len(samples) < SessionMinTrainingSamples+SessionMinValidationSample {
		return out, probabilityCalibrator{slope: 1}
	}
	split := len(samples) - SessionMinValidationSample
	training := samples[:split]
	validation := samples[split:]
	model := fitSessionTree(training, depth, SessionMinLeaf)
	out.Status = "ok"
	out.From, out.To = validation[0].date, validation[len(validation)-1].date
	out.Samples = len(validation)

	trainingUp := 0
	for _, item := range training {
		if item.label == 1 {
			trainingUp++
		}
	}
	majorityUp := trainingUp*2 >= len(training)
	weekdayUp := map[int]int{}
	weekdayDown := map[int]int{}
	for _, item := range training {
		if item.label == 1 {
			weekdayUp[item.weekday]++
		} else {
			weekdayDown[item.weekday]++
		}
	}
	correct, majorityCorrect, weekdayCorrect := 0, 0, 0
	holdoutHighTotal, holdoutHighCorrect, holdoutHighUp := 0, 0, 0
	for _, item := range validation {
		probability := model.probability(item.features)
		predictedUp := probability >= 0.5
		actualUp := item.label == 1
		if predictedUp == actualUp {
			correct++
		}
		if majorityUp == actualUp {
			majorityCorrect++
		}
		if (weekdayUp[item.weekday] >= weekdayDown[item.weekday]) == actualUp {
			weekdayCorrect++
		}
		if probability >= SessionHighConfidenceThreshold || probability <= 1-SessionHighConfidenceThreshold {
			holdoutHighTotal++
			if actualUp {
				holdoutHighUp++
			}
			if predictedUp == actualUp {
				holdoutHighCorrect++
			}
		}
	}
	out.Accuracy = float64(correct) / float64(len(validation))
	out.MajorityAccuracy = float64(majorityCorrect) / float64(len(validation))
	out.WeekdayAccuracy = float64(weekdayCorrect) / float64(len(validation))

	walkTotal, walkCorrect, walkMajorityCorrect, walkWeekdayCorrect := 0, 0, 0, 0
	walkCalibratedCorrect := 0
	walkHighTotal, walkHighCorrect, walkHighUp := 0, 0, 0
	rawHighTotal, rawHighCorrect, rawHighUp := 0, 0, 0
	calibrationPairs := make([]calibrationPair, 0, len(samples)-SessionWalkForwardStart)
	calibratedPairs := make([]calibrationPair, 0, len(samples)-SessionWalkForwardStart)
	for cut := SessionWalkForwardStart; cut < len(samples); cut += SessionWalkForwardStep {
		end := cut + SessionWalkForwardStep
		if end > len(samples) {
			end = len(samples)
		}
		walkModel := fitSessionTree(samples[:cut], depth, SessionMinLeaf)
		calibrator := fitProbabilityCalibrator(calibrationPairs)
		cutUp := 0
		weekdayUp := map[int]int{}
		weekdayDown := map[int]int{}
		for _, item := range samples[:cut] {
			if item.label == 1 {
				cutUp++
				weekdayUp[item.weekday]++
			} else {
				weekdayDown[item.weekday]++
			}
		}
		cutMajorityUp := cutUp*2 >= cut
		blockPairs := make([]calibrationPair, 0, end-cut)
		for _, item := range samples[cut:end] {
			probability := walkModel.probability(item.features)
			calibrated := calibrator.apply(probability)
			predictedUp := probability >= 0.5
			actualUp := item.label == 1
			walkTotal++
			if predictedUp == actualUp {
				walkCorrect++
			}
			if (calibrated >= 0.5) == actualUp {
				walkCalibratedCorrect++
			}
			if cutMajorityUp == actualUp {
				walkMajorityCorrect++
			}
			if (weekdayUp[item.weekday] >= weekdayDown[item.weekday]) == actualUp {
				walkWeekdayCorrect++
			}
			if calibrator.status == "ok" && sessionHighConfidence(probability, calibrated) {
				walkHighTotal++
				if actualUp {
					walkHighUp++
				}
				if predictedUp == actualUp {
					walkHighCorrect++
				}
			}
			if calibrator.status == "ok" {
				calibratedPairs = append(calibratedPairs, calibrationPair{probability: calibrated, label: item.label})
			}
			if probability >= SessionHighConfidenceThreshold || probability <= 1-SessionHighConfidenceThreshold {
				rawHighTotal++
				if actualUp {
					rawHighUp++
				}
				if predictedUp == actualUp {
					rawHighCorrect++
				}
			}
			blockPairs = append(blockPairs, calibrationPair{probability: probability, label: item.label})
		}
		calibrationPairs = append(calibrationPairs, blockPairs...)
	}
	calibrator := fitProbabilityCalibrator(calibrationPairs)
	out.Calibration = SessionCalibration{
		Status: calibrator.status, Method: calibrationMethod, Samples: calibrator.samples, EvaluationSamples: len(calibratedPairs),
		Slope: calibrator.slope, Intercept: calibrator.intercept,
		BrierScore: calibrationBrier(calibratedPairs), ExpectedCalibrationError: calibrationECE(calibratedPairs),
	}
	if walkTotal > 0 {
		out.WalkForwardSamples = walkTotal
		out.WalkForwardAccuracy = float64(walkCorrect) / float64(walkTotal)
		out.WalkForwardMajorityAccuracy = float64(walkMajorityCorrect) / float64(walkTotal)
		out.WalkForwardWeekdayAccuracy = float64(walkWeekdayCorrect) / float64(walkTotal)
		out.CalibratedAccuracy = float64(walkCalibratedCorrect) / float64(walkTotal)
	}
	if rawHighTotal > 0 {
		out.RawHighConfidenceSamples = rawHighTotal
		out.RawHighConfidenceAccuracy = float64(rawHighCorrect) / float64(rawHighTotal)
	}
	if walkHighTotal > 0 {
		out.HighConfidenceSamples = walkHighTotal
		out.HighConfidenceCoverage = float64(walkHighTotal) / float64(walkTotal)
		out.HighConfidenceAccuracy = float64(walkHighCorrect) / float64(walkHighTotal)
		out.HighConfidenceMajorityAccuracy = float64(maxInt(walkHighUp, walkHighTotal-walkHighUp)) / float64(walkHighTotal)
		out.HighConfidenceWilsonLow, out.HighConfidenceWilsonHigh = wilson(walkHighCorrect, walkHighTotal)
	} else if holdoutHighTotal > 0 {
		out.HighConfidenceSamples = holdoutHighTotal
		out.HighConfidenceCoverage = float64(holdoutHighTotal) / float64(len(validation))
		out.HighConfidenceAccuracy = float64(holdoutHighCorrect) / float64(holdoutHighTotal)
		out.HighConfidenceMajorityAccuracy = float64(maxInt(holdoutHighUp, holdoutHighTotal-holdoutHighUp)) / float64(holdoutHighTotal)
		out.HighConfidenceWilsonLow, out.HighConfidenceWilsonHigh = wilson(holdoutHighCorrect, holdoutHighTotal)
	}

	out.HighConfidenceStatus = sessionHighConfidenceStatus(out)
	out.TargetMet = out.HighConfidenceStatus == "ok" && out.HighConfidenceSamples >= 10 && out.HighConfidenceAccuracy >= SessionTargetAccuracy
	baseline := math.Max(math.Max(out.MajorityAccuracy, out.WeekdayAccuracy), math.Max(out.HighConfidenceMajorityAccuracy, math.Max(out.WalkForwardMajorityAccuracy, out.WalkForwardWeekdayAccuracy)))
	switch {
	case out.TargetMet:
		out.Conclusion = "high_confidence_above_target"
	case out.HighConfidenceSamples >= 10 && out.HighConfidenceAccuracy >= baseline+0.05:
		out.Conclusion = "high_confidence_above_baseline"
	case out.WalkForwardSamples > 0 && out.WalkForwardAccuracy >= out.WalkForwardMajorityAccuracy+0.02:
		out.Conclusion = "model_above_baseline"
	case out.WalkForwardSamples == 0 && out.Accuracy >= out.MajorityAccuracy+0.02:
		out.Conclusion = "model_above_baseline"
	default:
		out.Conclusion = "no_clear_edge"
	}
	return out, calibrator
}

func sessionHighConfidence(raw, calibrated float64) bool {
	rawUp := raw >= 0.5
	calibratedUp := calibrated >= 0.5
	if rawUp != calibratedUp {
		return false
	}
	return calibrated >= SessionHighConfidenceThreshold || calibrated <= 1-SessionHighConfidenceThreshold
}

func sessionHighConfidenceStatus(out SessionValidation) string {
	if out.Calibration.Status != "ok" {
		return "calibration_insufficient"
	}
	if out.HighConfidenceSamples == 0 {
		return "no_samples"
	}
	if out.HighConfidenceSamples < 20 {
		return "insufficient_samples"
	}
	if out.RawHighConfidenceSamples > 0 && out.HighConfidenceAccuracy < out.RawHighConfidenceAccuracy {
		return "below_raw"
	}
	return "ok"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func marketShanghai() *time.Location { return time.FixedZone("CST", 8*3600) }
