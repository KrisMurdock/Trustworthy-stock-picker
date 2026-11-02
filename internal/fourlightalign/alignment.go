// Package fourlightalign evaluates the local four-light approximation against
// a continuous light-state sample exported from a target software.
package fourlightalign

import (
	"errors"
	"sort"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const (
	Version              = 2
	StatusSampleCompared = "sample_compared"

	EventFirstAllRed = "first_all_red"
	EventExitAllRed  = "exit_all_red"

	// EventPairWindowRows bounds one-to-one transition matching after same-day
	// events have been paired. Events farther apart remain missed or extra.
	EventPairWindowRows = 3

	maxMismatches       = 2000
	maxEventDifferences = 2000
)

type Target struct {
	Software string `json:"software"`
	Version  string `json:"version"`
	Period   string `json:"period"`
	Adjust   string `json:"adjust"`
}

type Observation struct {
	Code      string `json:"code"`
	Date      string `json:"date"`
	Trend     string `json:"trend"`
	Energy    string `json:"energy"`
	MidTerm   string `json:"mid_term"`
	ShortTerm string `json:"short_term"`
}

type LightAgreement struct {
	Light                string  `json:"light"`
	Compared             int     `json:"compared"`
	Matches              int     `json:"matches"`
	Agreement            float64 `json:"agreement"`
	ReferenceUnknown     int     `json:"reference_unknown"`
	ApproximationUnknown int     `json:"approximation_unknown"`
}

type AllRedAgreement struct {
	Compared             int     `json:"compared"`
	Matches              int     `json:"matches"`
	Agreement            float64 `json:"agreement"`
	TargetAllRed         int     `json:"target_all_red"`
	ApproximationAllRed  int     `json:"approximation_all_red"`
	ReferenceUnknown     int     `json:"reference_unknown"`
	ApproximationUnknown int     `json:"approximation_unknown"`
}

type TransitionAgreement struct {
	Event               string `json:"event"`
	Compared            int    `json:"compared"`
	TargetEvents        int    `json:"target_events"`
	ApproximationEvents int    `json:"approximation_events"`
	Exact               int    `json:"exact"`
	Missed              int    `json:"missed"`
	Extra               int    `json:"extra"`
	Unknown             int    `json:"unknown"`
}

type Mismatch struct {
	Code          string `json:"code"`
	Date          string `json:"date"`
	Light         string `json:"light"`
	Target        string `json:"target"`
	Approximation string `json:"approximation"`
}

type EventDifference struct {
	Event             string `json:"event"`
	Code              string `json:"code"`
	TargetDate        string `json:"target_date,omitempty"`
	ApproximationDate string `json:"approximation_date,omitempty"`
	Relation          string `json:"relation"`
	// OffsetRows is approximation row index minus target row index. Negative
	// means the approximation changed earlier; positive means later.
	OffsetRows int `json:"offset_rows"`
}

type Quality struct {
	MixedRows               int      `json:"mixed_rows"`
	TargetAllRedRows        int      `json:"target_all_red_rows"`
	TargetAllGreenRows      int      `json:"target_all_green_rows"`
	TargetFirstAllRedEvents int      `json:"target_first_all_red_events"`
	TargetExitAllRedEvents  int      `json:"target_exit_all_red_events"`
	Warnings                []string `json:"warnings,omitempty"`
}

type Report struct {
	Version             int                   `json:"schema_version"`
	Status              string                `json:"status"`
	Target              Target                `json:"target"`
	ApproximationRule   string                `json:"approximation_rule"`
	FormulaVerified     bool                  `json:"formula_verified"`
	ObservedRows        int                   `json:"observed_rows"`
	Codes               int                   `json:"codes"`
	From                string                `json:"from"`
	Through             string                `json:"through"`
	DataFrom            string                `json:"data_from"`
	DataThrough         string                `json:"data_through"`
	DataSources         []string              `json:"data_sources"`
	EventPairWindowRows int                   `json:"event_pair_window_rows"`
	Lights              []LightAgreement      `json:"lights"`
	AllRed              AllRedAgreement       `json:"all_red"`
	Transitions         []TransitionAgreement `json:"transitions"`
	Quality             Quality               `json:"quality"`
	Mismatches          []Mismatch            `json:"mismatches,omitempty"`
	MismatchCount       int                   `json:"mismatch_count"`
	EventDifferences    []EventDifference     `json:"event_differences,omitempty"`
	Limitations         []string              `json:"limitations"`
}

type eventPoint struct {
	date   string
	index  int
	target bool
	approx bool
}

type eventPair struct {
	target eventPoint
	approx eventPoint
}

type groupState struct {
	targetKnown  bool
	targetAllRed bool
	approxKnown  bool
	approxAllRed bool
}

// Compare aligns canonical target light states with the local approximation on
// the same stock, date and qfq daily bars. Rows with an unknown state are kept
// for coverage reporting but excluded from agreement denominators.
func Compare(observations []Observation, target Target, histories map[string]market.History) (Report, error) {
	report := Report{
		Version: Version, Status: StatusSampleCompared, Target: target,
		EventPairWindowRows: EventPairWindowRows,
		ApproximationRule:   strategy.FourLightRule,
		Lights: []LightAgreement{
			{Light: "trend"}, {Light: "energy"}, {Light: "mid_term"}, {Light: "short_term"},
		},
		Transitions: []TransitionAgreement{
			{Event: EventFirstAllRed}, {Event: EventExitAllRed},
		},
		Limitations: []string{
			"仅比较给定样本的灯色和转换，不证明本地公式与目标软件公式一致。",
			"本地规则是价量近似实现；颜色差异可能来自目标软件版本、周期、复权口径或样本转录。",
			"该报告不包含收益、胜率、买卖建议或正式推荐结论。",
		},
	}
	if strings.TrimSpace(target.Software) == "" || strings.TrimSpace(target.Version) == "" ||
		target.Period != "daily" || target.Adjust != "qfq" {
		return report, errors.New("target software, version, daily period and qfq adjustment are required")
	}
	if len(observations) == 0 {
		return report, errors.New("reference sample is empty")
	}

	rows := append([]Observation(nil), observations...)
	sort.Slice(rows, func(first, second int) bool {
		if rows[first].Code == rows[second].Code {
			return rows[first].Date < rows[second].Date
		}
		return rows[first].Code < rows[second].Code
	})
	report.From, report.Through = rows[0].Date, rows[0].Date
	for index := range rows {
		if index > 0 && rows[index].Code == rows[index-1].Code && rows[index].Date == rows[index-1].Date {
			return report, errors.New("reference sample contains duplicate code/date rows")
		}
		if err := validateObservation(rows[index]); err != nil {
			return report, err
		}
		if rows[index].Date < report.From {
			report.From = rows[index].Date
		}
		if rows[index].Date > report.Through {
			report.Through = rows[index].Date
		}
	}
	report.ObservedRows = len(rows)

	eventDifferences := map[string]EventDifference{}
	sources := map[string]bool{}
	codeCount := 0
	for start := 0; start < len(rows); {
		end := start + 1
		for end < len(rows) && rows[end].Code == rows[start].Code {
			end++
		}
		code := rows[start].Code
		history, ok := histories[code]
		if !ok {
			return report, errors.New("missing qfq history for reference code")
		}
		if history.Validate() != nil || history.Adjust != "qfq" {
			return report, errors.New("reference history must be valid qfq daily history")
		}
		codeCount++
		sources[history.Source] = true
		barIndex := make(map[string]int, len(history.Bars))
		for index, bar := range history.Bars {
			barIndex[bar.Date] = index
			if report.DataFrom == "" || bar.Date < report.DataFrom {
				report.DataFrom = bar.Date
			}
			if bar.Date > report.DataThrough {
				report.DataThrough = bar.Date
			}
		}
		group := rows[start:end]
		previousIndices := make([]int, 0, len(group))
		for index, row := range group {
			barAt, exists := barIndex[row.Date]
			if !exists {
				return report, errors.New("reference date is absent from qfq daily history")
			}
			if index > 0 && barAt != previousIndices[len(previousIndices)-1]+1 {
				return report, errors.New("reference sample is not continuous by trading day")
			}
			previousIndices = append(previousIndices, barAt)
		}
		if err := compareCode(&report, group, previousIndices, history, eventDifferences); err != nil {
			return report, err
		}
		start = end
	}
	report.Codes = codeCount
	for source := range sources {
		report.DataSources = append(report.DataSources, source)
	}
	sort.Strings(report.DataSources)
	for index := range report.Lights {
		if report.Lights[index].Compared > 0 {
			report.Lights[index].Agreement = float64(report.Lights[index].Matches) / float64(report.Lights[index].Compared)
		}
	}
	if report.AllRed.Compared > 0 {
		report.AllRed.Agreement = float64(report.AllRed.Matches) / float64(report.AllRed.Compared)
	}
	report.EventDifferences = sortedEventDifferences(eventDifferences)
	report.Quality.Warnings = qualityWarnings(report.Quality, report.ObservedRows)
	return report, nil
}

func compareCode(report *Report, group []Observation, barIndices []int, history market.History, eventDifferences map[string]EventDifference) error {
	state := groupState{}
	events := map[string][]eventPoint{EventFirstAllRed: {}, EventExitAllRed: {}}
	for rowIndex, row := range group {
		evidence := strategy.AnalyzeFourLight(history.Bars[:barIndices[rowIndex]+1])
		targetValues := []string{row.Trend, row.Energy, row.MidTerm, row.ShortTerm}
		approxValues := []string{evidence.Trend, evidence.Energy, evidence.MidTerm, evidence.ShortTerm}
		for lightIndex := range report.Lights {
			light := &report.Lights[lightIndex]
			if targetValues[lightIndex] == strategy.FourLightUnknown {
				light.ReferenceUnknown++
				continue
			}
			if approxValues[lightIndex] == strategy.FourLightUnknown {
				light.ApproximationUnknown++
				continue
			}
			light.Compared++
			if targetValues[lightIndex] == approxValues[lightIndex] {
				light.Matches++
				continue
			}
			report.MismatchCount++
			if len(report.Mismatches) < maxMismatches {
				report.Mismatches = append(report.Mismatches, Mismatch{
					Code: row.Code, Date: row.Date, Light: light.Light,
					Target: targetValues[lightIndex], Approximation: approxValues[lightIndex],
				})
			}
		}

		targetKnown := allKnown(targetValues)
		approxKnown := allKnown(approxValues)
		targetAllRed := targetKnown && allRed(targetValues)
		approxAllRed := approxKnown && evidence.AllRed
		if targetKnown && approxKnown {
			report.AllRed.Compared++
			if targetAllRed {
				report.AllRed.TargetAllRed++
			}
			if approxAllRed {
				report.AllRed.ApproximationAllRed++
			}
			if targetAllRed == approxAllRed {
				report.AllRed.Matches++
			}
		} else {
			if !targetKnown {
				report.AllRed.ReferenceUnknown++
			}
			if !approxKnown {
				report.AllRed.ApproximationUnknown++
			}
		}
		if targetKnown {
			switch {
			case targetAllRed:
				report.Quality.TargetAllRedRows++
			case allGreen(targetValues):
				report.Quality.TargetAllGreenRows++
			default:
				report.Quality.MixedRows++
			}
		}

		if rowIndex > 0 {
			compareTransitions(report, row, rowIndex, state, targetKnown, targetAllRed, approxKnown, approxAllRed, events)
		}
		state = groupState{targetKnown: targetKnown, targetAllRed: targetAllRed, approxKnown: approxKnown, approxAllRed: approxAllRed}
	}
	collectEventDifferences(report, group[0].Code, events, eventDifferences)
	return nil
}

func compareTransitions(report *Report, row Observation, rowIndex int, previous groupState, targetKnown, targetAllRed, approxKnown, approxAllRed bool, events map[string][]eventPoint) {
	for index := range report.Transitions {
		transition := &report.Transitions[index]
		targetEvent := false
		approxEvent := false
		if previous.targetKnown && targetKnown {
			if transition.Event == EventFirstAllRed {
				targetEvent = targetAllRed && !previous.targetAllRed
			} else {
				targetEvent = !targetAllRed && previous.targetAllRed
			}
		}
		if previous.approxKnown && approxKnown {
			if transition.Event == EventFirstAllRed {
				approxEvent = approxAllRed && !previous.approxAllRed
			} else {
				approxEvent = !approxAllRed && previous.approxAllRed
			}
		}
		if targetEvent {
			transition.TargetEvents++
		}
		if approxEvent {
			transition.ApproximationEvents++
		}
		if targetEvent || approxEvent {
			events[transition.Event] = append(events[transition.Event], eventPoint{date: row.Date, index: rowIndex, target: targetEvent, approx: approxEvent})
		}
		if !previous.targetKnown || !previous.approxKnown || !targetKnown || !approxKnown {
			transition.Unknown++
			continue
		}
		transition.Compared++
		switch {
		case targetEvent && approxEvent:
			transition.Exact++
		case targetEvent:
			transition.Missed++
		case approxEvent:
			transition.Extra++
		}
		if transition.Event == EventFirstAllRed && targetEvent {
			report.Quality.TargetFirstAllRedEvents++
		}
		if transition.Event == EventExitAllRed && targetEvent {
			report.Quality.TargetExitAllRedEvents++
		}
	}
}

func collectEventDifferences(report *Report, code string, events map[string][]eventPoint, differences map[string]EventDifference) {
	for _, transition := range report.Transitions {
		pairs, missed, extra := pairEventPoints(events[transition.Event])
		for _, pair := range pairs {
			if pair.target.index == pair.approx.index {
				continue
			}
			difference := EventDifference{
				Event: transition.Event, Code: code,
				TargetDate: pair.target.date, ApproximationDate: pair.approx.date,
				Relation: "shifted", OffsetRows: pair.approx.index - pair.target.index,
			}
			differences[eventDifferenceKey(difference)] = difference
		}
		for _, source := range missed {
			difference := EventDifference{Event: transition.Event, Code: code, TargetDate: source.date, Relation: "missed"}
			differences[eventDifferenceKey(difference)] = difference
		}
		for _, source := range extra {
			difference := EventDifference{Event: transition.Event, Code: code, ApproximationDate: source.date, Relation: "extra"}
			differences[eventDifferenceKey(difference)] = difference
		}
	}
}

func pairEventPoints(points []eventPoint) ([]eventPair, []eventPoint, []eventPoint) {
	targets := make(map[int]eventPoint)
	approximations := make(map[int]eventPoint)
	targetIndices := make([]int, 0)
	approximationIndices := make([]int, 0)
	for _, point := range points {
		if point.target {
			targets[point.index] = point
			targetIndices = append(targetIndices, point.index)
		}
		if point.approx {
			approximations[point.index] = point
			approximationIndices = append(approximationIndices, point.index)
		}
	}
	sort.Ints(targetIndices)
	sort.Ints(approximationIndices)

	usedTargets := make(map[int]bool)
	usedApproximations := make(map[int]bool)
	pairs := make([]eventPair, 0)
	for _, targetIndex := range targetIndices {
		approx, ok := approximations[targetIndex]
		if !ok {
			continue
		}
		pairs = append(pairs, eventPair{target: targets[targetIndex], approx: approx})
		usedTargets[targetIndex] = true
		usedApproximations[targetIndex] = true
	}

	candidates := make([]eventPair, 0)
	for _, targetIndex := range targetIndices {
		if usedTargets[targetIndex] {
			continue
		}
		for offset := -EventPairWindowRows; offset <= EventPairWindowRows; offset++ {
			if offset == 0 {
				continue
			}
			approx, ok := approximations[targetIndex+offset]
			if !ok || usedApproximations[approx.index] {
				continue
			}
			candidates = append(candidates, eventPair{target: targets[targetIndex], approx: approx})
		}
	}
	sort.Slice(candidates, func(first, second int) bool {
		firstDistance := absInt(candidates[first].approx.index - candidates[first].target.index)
		secondDistance := absInt(candidates[second].approx.index - candidates[second].target.index)
		if firstDistance != secondDistance {
			return firstDistance < secondDistance
		}
		if candidates[first].target.index != candidates[second].target.index {
			return candidates[first].target.index < candidates[second].target.index
		}
		return candidates[first].approx.index < candidates[second].approx.index
	})
	for _, candidate := range candidates {
		if usedTargets[candidate.target.index] || usedApproximations[candidate.approx.index] {
			continue
		}
		pairs = append(pairs, candidate)
		usedTargets[candidate.target.index] = true
		usedApproximations[candidate.approx.index] = true
	}

	sort.Slice(pairs, func(first, second int) bool {
		if pairs[first].target.index != pairs[second].target.index {
			return pairs[first].target.index < pairs[second].target.index
		}
		return pairs[first].approx.index < pairs[second].approx.index
	})
	missed := make([]eventPoint, 0)
	for _, targetIndex := range targetIndices {
		if !usedTargets[targetIndex] {
			missed = append(missed, targets[targetIndex])
		}
	}
	extra := make([]eventPoint, 0)
	for _, approximationIndex := range approximationIndices {
		if !usedApproximations[approximationIndex] {
			extra = append(extra, approximations[approximationIndex])
		}
	}
	return pairs, missed, extra
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func sortedEventDifferences(values map[string]EventDifference) []EventDifference {
	out := make([]EventDifference, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	sort.Slice(out, func(first, second int) bool {
		if out[first].Event != out[second].Event {
			return out[first].Event < out[second].Event
		}
		if out[first].Code != out[second].Code {
			return out[first].Code < out[second].Code
		}
		if out[first].TargetDate != out[second].TargetDate {
			return out[first].TargetDate < out[second].TargetDate
		}
		return out[first].ApproximationDate < out[second].ApproximationDate
	})
	if len(out) > maxEventDifferences {
		out = out[:maxEventDifferences]
	}
	return out
}

func eventDifferenceKey(difference EventDifference) string {
	return strings.Join([]string{difference.Event, difference.Code, difference.TargetDate, difference.ApproximationDate, difference.Relation}, "\x00")
}

func qualityWarnings(quality Quality, rows int) []string {
	warnings := []string{}
	if rows < 60 {
		warnings = append(warnings, "样本少于 60 个股票/交易日观测，不足以覆盖预热和多次转换。")
	}
	if quality.MixedRows == 0 {
		warnings = append(warnings, "目标样本没有混合灯色，无法检验逐灯分歧。")
	}
	if quality.TargetFirstAllRedEvents == 0 {
		warnings = append(warnings, "目标样本没有首次四灯全红事件。")
	}
	if quality.TargetExitAllRedEvents == 0 {
		warnings = append(warnings, "目标样本没有退出四灯全红事件。")
	}
	if rows > 0 && quality.TargetAllRedRows == rows {
		warnings = append(warnings, "目标样本全部为持续全红，无法检验启动和退出。")
	}
	return warnings
}

func validateObservation(row Observation) error {
	if row.Code == "" || row.Date == "" {
		return errors.New("reference code and date are required")
	}
	for _, value := range []string{row.Trend, row.Energy, row.MidTerm, row.ShortTerm} {
		if !validLight(value) {
			return errors.New("reference light must be red, green or unknown")
		}
	}
	return nil
}

func validLight(value string) bool {
	return value == strategy.FourLightRed || value == strategy.FourLightGreen || value == strategy.FourLightUnknown
}

func allKnown(values []string) bool {
	for _, value := range values {
		if value == strategy.FourLightUnknown {
			return false
		}
	}
	return true
}

func allRed(values []string) bool {
	for _, value := range values {
		if value != strategy.FourLightRed {
			return false
		}
	}
	return true
}

func allGreen(values []string) bool {
	for _, value := range values {
		if value != strategy.FourLightGreen {
			return false
		}
	}
	return true
}
