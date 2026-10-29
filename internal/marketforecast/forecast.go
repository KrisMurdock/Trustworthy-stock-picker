package marketforecast

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

var numericFeatureNames = []string{
	"ret1", "ret5", "ret20", "ret60", "vol5", "vol20",
	"ma5_gap", "ma20_gap", "rsi14",
}

var weekdayFeatureNames = []string{
	"target_tuesday", "target_wednesday", "target_thursday", "target_friday",
}

var weekdayLabels = map[int]string{
	1: "周一",
	2: "周二",
	3: "周三",
	4: "周四",
	5: "周五",
}

type sample struct {
	date     string
	target   string
	weekday  int
	label    int
	features []float64
}

// FeatureNames returns the stable model feature order.
func FeatureNames() []string {
	out := append([]string(nil), numericFeatureNames...)
	return append(out, weekdayFeatureNames...)
}

// Build fits one model per supplied index using only bars at or before the
// requested as-of date. Each signal predicts the next trading day's close
// direction from the latest completed index bar.
func Build(series []Series, opts Options) (Report, error) {
	if opts.AsOf != "" {
		if _, err := parseDate(opts.AsOf); err != nil {
			return Report{}, errors.New("market forecast as-of must be YYYY-MM-DD")
		}
	}
	training := opts.TrainingSamples
	if training <= 0 {
		training = MinTrainingSamples
	}
	validation := opts.ValidationSamples
	if validation <= 0 {
		validation = MinValidationSample
	}
	if training < 20 || validation < 10 {
		return Report{}, errors.New("market forecast requires at least 20 training and 10 validation samples")
	}
	report := Report{
		SchemaVersion: SchemaVersion,
		AsOf:          opts.AsOf,
		Model: ModelInfo{
			ID: ModelID, Version: ModelVersion, FeatureVersion: FeatureVersion,
			FeatureNames: FeatureNames(), MinHistoryBars: MinHistoryBars,
			MinTrainingSamples: training, MinValidationSamples: validation,
		},
		SessionModel: SessionModelInfo{
			ID: SessionModelID, Version: SessionModelVersion, FeatureVersion: SessionFeatureVersion,
			FeatureNames: SessionFeatureNames(), HighConfidenceThreshold: SessionHighConfidenceThreshold,
			TargetAccuracy: SessionTargetAccuracy, MorningMaxDepth: SessionMorningMaxDepth,
			AfternoonMaxDepth: SessionAfternoonMaxDepth, MinLeaf: SessionMinLeaf,
			MinTrainingSamples: SessionMinTrainingSamples, MinValidationSamples: SessionMinValidationSample,
		},
		Signals: []Signal{},
	}
	seen := map[string]bool{}
	for _, item := range series {
		if strings.TrimSpace(item.Index.Code) == "" || seen[item.Index.Code] {
			return Report{}, errors.New("market forecast contains a duplicate or empty index code")
		}
		seen[item.Index.Code] = true
		signal, err := buildSignal(item, opts.AsOf, training, validation, opts.NextTradingDay)
		if err != nil {
			return Report{}, err
		}
		report.Signals = append(report.Signals, signal)
		if signal.DataThrough > report.DataThrough {
			report.DataThrough = signal.DataThrough
		}
	}
	if report.AsOf == "" {
		report.AsOf = report.DataThrough
	}
	report.Status = reportStatus(report.Signals)
	for _, signal := range report.Signals {
		if signal.Status == "insufficient_history" {
			report.Warnings = append(report.Warnings, signal.Code+": insufficient index history")
		}
		if signal.Stale {
			report.Warnings = append(report.Warnings, signal.Code+": index history ends before the requested as-of date")
		}
		for _, session := range signal.Sessions {
			if session.Status != "ok" {
				report.Warnings = append(report.Warnings, signal.Code+" "+session.Session+": "+session.Reason)
			}
		}
	}
	return report, nil
}

func buildSignal(series Series, asOf string, training, validation int, nextTradingDay func(string) (string, error)) (Signal, error) {
	signal := Signal{
		Code: series.Index.Code, Name: series.Index.Name, Status: "insufficient_history",
		Weekdays:   make([]WeekdayStat, 0, 5),
		Validation: ValidationInfo{Status: "insufficient_history"},
	}
	bars := normalizeBars(series.Bars, asOf)
	if len(bars) == 0 {
		signal.Reason = "no_index_bars"
		return signal, nil
	}
	signal.DataThrough = bars[len(bars)-1].Date
	signal.Close = bars[len(bars)-1].Close
	if len(bars) > 1 && bars[len(bars)-2].Close > 0 {
		signal.ChangePct = (bars[len(bars)-1].Close/bars[len(bars)-2].Close - 1) * 100
	}
	signal.Stale = asOf != "" && signal.DataThrough < asOf
	signal.Weekdays = weekdayStats(bars)
	target := ""
	if nextTradingDay != nil {
		next, err := nextTradingDay(signal.DataThrough)
		if err == nil && next > signal.DataThrough {
			target = next
		}
	}
	attachSessions := func() {
		if target != "" {
			signal.Sessions = buildSessionSignals(bars, series.Minutes, target)
		}
	}
	if len(bars) < MinHistoryBars {
		signal.Reason = fmt.Sprintf("need %d bars, have %d", MinHistoryBars, len(bars))
		return signal, nil
	}

	samples := buildSamples(bars)
	signal.Training = trainingInfo(samples)
	if len(samples) < training+validation {
		signal.Reason = fmt.Sprintf("need %d labeled sessions, have %d", training+validation, len(samples))
		attachSessions()
		return signal, nil
	}
	split := len(samples) - validation
	validationModel := fitModel(samples[:split])
	signal.Validation = validateModel(validationModel, samples[:split], samples[split:])
	model := fitModel(samples)
	if target == "" {
		if nextTradingDay == nil {
			signal.Reason = "trading calendar unavailable"
		} else {
			signal.Reason = "next trading day unavailable"
		}
		return signal, nil
	}
	features, ok := featureValues(bars, len(bars)-1, target)
	if !ok {
		signal.Reason = "insufficient feature history"
		return signal, nil
	}
	probability := model.probability(features)
	if math.IsNaN(probability) || math.IsInf(probability, 0) {
		signal.Reason = "model returned an invalid probability"
		return signal, nil
	}
	signal.Status = "ok"
	signal.TargetDate = target
	signal.UpProbability = probability
	signal.Direction = "down"
	if probability >= 0.5 {
		signal.Direction = "up"
	}
	signal.Strength = strength(probability)
	attachSessions()
	return signal, nil
}

func normalizeBars(input []Bar, asOf string) []Bar {
	out := make([]Bar, 0, len(input))
	for _, bar := range input {
		if _, err := parseDate(bar.Date); err != nil || bar.Close <= 0 {
			continue
		}
		if asOf != "" && bar.Date > asOf {
			continue
		}
		out = append(out, bar)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	if len(out) < 2 {
		return out
	}
	deduped := out[:1]
	for _, bar := range out[1:] {
		if bar.Date == deduped[len(deduped)-1].Date {
			deduped[len(deduped)-1] = bar
			continue
		}
		deduped = append(deduped, bar)
	}
	return deduped
}

func buildSamples(bars []Bar) []sample {
	out := make([]sample, 0, len(bars)-MinHistoryBars+1)
	for index := MinHistoryBars - 1; index < len(bars)-1; index++ {
		next := bars[index+1]
		label := 0
		switch {
		case next.Close > bars[index].Close:
			label = 1
		case next.Close < bars[index].Close:
			label = 0
		default:
			continue
		}
		features, ok := featureValues(bars, index, next.Date)
		if !ok {
			continue
		}
		out = append(out, sample{
			date: bars[index].Date, target: next.Date, weekday: targetWeekday(next.Date),
			label: label, features: features,
		})
	}
	return out
}

func featureValues(bars []Bar, index int, targetDate string) ([]float64, bool) {
	if index < 60 || index >= len(bars) || bars[index].Close <= 0 {
		return nil, false
	}
	close := bars[index].Close
	values := make([]float64, len(numericFeatureNames)+len(weekdayFeatureNames))
	for _, horizon := range []int{1, 5, 20, 60} {
		previous := bars[index-horizon].Close
		if previous <= 0 {
			return nil, false
		}
		value := close/previous - 1
		if !finite(value) {
			return nil, false
		}
		values[position(fmt.Sprintf("ret%d", horizon))] = value
	}
	for _, horizon := range []int{5, 20} {
		volatility, ok := returnVolatility(bars, index, horizon)
		if !ok {
			return nil, false
		}
		values[position(fmt.Sprintf("vol%d", horizon))] = volatility
	}
	for _, horizon := range []int{5, 20} {
		mean, ok := meanClose(bars, index, horizon)
		if !ok || mean <= 0 {
			return nil, false
		}
		values[position(fmt.Sprintf("ma%d_gap", horizon))] = close/mean - 1
	}
	rsi, ok := rsi14(bars, index)
	if !ok {
		return nil, false
	}
	values[position("rsi14")] = rsi / 100
	weekday := targetWeekday(targetDate)
	for _, item := range []struct {
		day  int
		name string
	}{
		{2, "target_tuesday"}, {3, "target_wednesday"},
		{4, "target_thursday"}, {5, "target_friday"},
	} {
		if weekday == item.day {
			values[position(item.name)] = 1
		}
	}
	for _, value := range values {
		if !finite(value) {
			return nil, false
		}
	}
	return values, true
}

func position(name string) int {
	for index, candidate := range numericFeatureNames {
		if candidate == name {
			return index
		}
	}
	for index, candidate := range weekdayFeatureNames {
		if candidate == name {
			return len(numericFeatureNames) + index
		}
	}
	panic("unknown market forecast feature: " + name)
}

func returnVolatility(bars []Bar, index, window int) (float64, bool) {
	if index-window < 0 {
		return 0, false
	}
	returns := make([]float64, 0, window)
	for i := index - window + 1; i <= index; i++ {
		if bars[i-1].Close <= 0 || bars[i].Close <= 0 {
			return 0, false
		}
		returns = append(returns, bars[i].Close/bars[i-1].Close-1)
	}
	return std(returns), true
}

func meanClose(bars []Bar, index, window int) (float64, bool) {
	if index-window+1 < 0 {
		return 0, false
	}
	sum := 0.0
	for i := index - window + 1; i <= index; i++ {
		if bars[i].Close <= 0 {
			return 0, false
		}
		sum += bars[i].Close
	}
	return sum / float64(window), true
}

func rsi14(bars []Bar, index int) (float64, bool) {
	if index < 14 {
		return 0, false
	}
	gain, loss := 0.0, 0.0
	for i := index - 13; i <= index; i++ {
		change := bars[i].Close - bars[i-1].Close
		if change > 0 {
			gain += change
		} else {
			loss -= change
		}
	}
	if gain == 0 && loss == 0 {
		return 50, true
	}
	if loss == 0 {
		return 100, true
	}
	return 100 - 100/(1+gain/loss), true
}

func std(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	sum := 0.0
	for _, value := range values {
		delta := value - mean
		sum += delta * delta
	}
	return math.Sqrt(sum / float64(len(values)-1))
}

func weekdayStats(bars []Bar) []WeekdayStat {
	type aggregate struct {
		up, down, flat int
		sum            float64
	}
	values := map[int]*aggregate{}
	for weekday := 1; weekday <= 5; weekday++ {
		values[weekday] = &aggregate{}
	}
	for index := 1; index < len(bars); index++ {
		if bars[index-1].Close <= 0 || bars[index].Close <= 0 {
			continue
		}
		weekday := parseDateWeekday(bars[index].Date)
		value, ok := values[weekday]
		if !ok {
			continue
		}
		returnValue := bars[index].Close/bars[index-1].Close - 1
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
			Weekday: int(weekday), Label: weekdayLabels[weekday], Samples: samples,
			Up: value.up, Down: value.down, Flat: value.flat, WinRate: winRate,
			MeanReturnPct: mean, WilsonLow: low, WilsonHigh: high,
		})
	}
	return out
}

func wilson(successes, total int) (float64, float64) {
	if total <= 0 {
		return 0, 1
	}
	const z = 1.959963984540054
	n := float64(total)
	p := float64(successes) / n
	denominator := 1 + z*z/n
	center := (p + z*z/(2*n)) / denominator
	margin := z * math.Sqrt((p*(1-p)+z*z/(4*n))/n) / denominator
	return math.Max(0, center-margin), math.Min(1, center+margin)
}

func trainingInfo(samples []sample) TrainingInfo {
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

func validateModel(model *directionModel, training, validation []sample) ValidationInfo {
	out := ValidationInfo{Status: "insufficient_history", Samples: len(validation)}
	if len(training) == 0 || len(validation) == 0 {
		return out
	}
	out.Status = "ok"
	out.From, out.To = validation[0].target, validation[len(validation)-1].target
	trainingUp := 0
	weekdayUp := map[int]int{}
	weekdayDown := map[int]int{}
	for _, item := range training {
		if item.label == 1 {
			trainingUp++
			weekdayUp[item.weekday]++
		} else {
			weekdayDown[item.weekday]++
		}
	}
	majorityUp := trainingUp*2 >= len(training)
	correct, majorityCorrect, weekdayCorrect := 0, 0, 0
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
		weekdayPrediction := weekdayUp[item.weekday] >= weekdayDown[item.weekday]
		if weekdayPrediction == actualUp {
			weekdayCorrect++
		}
	}
	n := float64(len(validation))
	out.Accuracy = float64(correct) / n
	out.MajorityAccuracy = float64(majorityCorrect) / n
	out.WeekdayAccuracy = float64(weekdayCorrect) / n
	baseline := math.Max(out.MajorityAccuracy, out.WeekdayAccuracy)
	switch {
	case out.Accuracy >= baseline+0.02:
		out.Conclusion = "model_above_baseline"
	case out.WeekdayAccuracy >= out.Accuracy+0.02:
		out.Conclusion = "weekday_above_model"
	default:
		out.Conclusion = "no_clear_edge"
	}
	return out
}

func reportStatus(signals []Signal) string {
	if len(signals) == 0 {
		return "insufficient_history"
	}
	ok, stale := 0, 0
	for _, signal := range signals {
		if signal.Status == "ok" {
			ok++
			if signal.Stale {
				stale++
			}
		}
	}
	switch {
	case ok == 0:
		return "insufficient_history"
	case ok < len(signals):
		return "partial"
	case stale > 0:
		return "stale"
	default:
		return "ok"
	}
}

func strength(probability float64) string {
	distance := math.Abs(probability - 0.5)
	switch {
	case distance < 0.03:
		return "weak"
	case distance < 0.08:
		return "medium"
	default:
		return "strong"
	}
}

func targetWeekday(date string) int {
	return parseDateWeekday(date)
}

func parseDateWeekday(date string) int {
	parsed, err := time.ParseInLocation("2006-01-02", date, time.UTC)
	if err != nil {
		return 0
	}
	return int(parsed.Weekday())
}

func parseDate(date string) (time.Time, error) {
	parsed, err := time.ParseInLocation("2006-01-02", date, time.UTC)
	if err != nil || parsed.Format("2006-01-02") != date {
		return time.Time{}, errors.New("invalid date")
	}
	return parsed, nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
