package fairvalue

import (
	"errors"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fundamentals"
)

const (
	NominalBandCoveragePct = 60.0
	DefaultMinEvaluationN  = 30
)

// ForwardObservation is one point-in-time valuation paired with a later,
// unadjusted market price. It is an evaluation record, not a trading signal.
// FutureDate must be strictly after AsOf so a revision or price from the future
// cannot be used as an input.
type ForwardObservation struct {
	Code         string              `json:"code"`
	Sector       string              `json:"sector,omitempty"`
	AsOf         string              `json:"as_of"`
	FutureDate   string              `json:"future_date"`
	CurrentPrice float64             `json:"current_price"`
	FuturePrice  float64             `json:"future_price"`
	Estimate     EstimateV3          `json:"estimate"`
	Series       fundamentals.Series `json:"series"`
}

// BandGroupEvaluation summarizes a deterministic cohort. It measures whether
// the research band covered later prices and whether its implied direction was
// useful; it does not assume prices converge to the central value.
type BandGroupEvaluation struct {
	Group            string  `json:"group"`
	Observations     int     `json:"observations"`
	Covered          int     `json:"covered"`
	CoveragePct      float64 `json:"coverage_pct"`
	MeanWidthPct     float64 `json:"mean_width_pct"`
	MeanAbsGapPct    float64 `json:"mean_abs_gap_pct"`
	DirectionHitPct  float64 `json:"direction_hit_pct"`
	PinballLossPct   float64 `json:"pinball_loss_pct"`
	InsufficientData bool    `json:"insufficient_data"`
}

// BandEvaluation is the overall walk-forward result plus year and sector
// cohorts. A fixed-parameter research model has no fitted coefficients to leak,
// but every observation still has to pass the as-of/future-date boundary.
type BandEvaluation struct {
	NominalCoveragePct float64               `json:"nominal_coverage_pct"`
	MinSample          int                   `json:"min_sample"`
	Observations       int                   `json:"observations"`
	Valid              int                   `json:"valid"`
	Invalid            int                   `json:"invalid"`
	InsufficientData   bool                  `json:"insufficient_data"`
	Overall            BandGroupEvaluation   `json:"overall"`
	ByYear             []BandGroupEvaluation `json:"by_year"`
	BySector           []BandGroupEvaluation `json:"by_sector"`
	Problems           []string              `json:"problems,omitempty"`
}

// EvaluateBandCalibration computes deterministic forward calibration. The
// lower/upper scenario prices are treated as the predeclared 20th/80th
// percentile research band, so the nominal coverage is 60%. Pinball loss uses
// fractional returns to keep the two edges comparable.
func EvaluateBandCalibration(observations []ForwardObservation, minSample int) BandEvaluation {
	if minSample <= 0 {
		minSample = DefaultMinEvaluationN
	}
	result := BandEvaluation{
		NominalCoveragePct: NominalBandCoveragePct, MinSample: minSample,
		Observations: len(observations), ByYear: []BandGroupEvaluation{}, BySector: []BandGroupEvaluation{},
	}
	valid := make([]ForwardObservation, 0, len(observations))
	seen := map[string]bool{}
	for index, observation := range observations {
		if problem := validateForwardObservation(observation); problem != "" {
			result.Invalid++
			result.Problems = append(result.Problems, "sample="+strconv.Itoa(index)+":"+problem)
			continue
		}
		key := observation.Code + "|" + observation.AsOf + "|" + observation.FutureDate
		if seen[key] {
			result.Invalid++
			result.Problems = append(result.Problems, "sample="+strconv.Itoa(index)+":duplicate_forward_observation")
			continue
		}
		seen[key] = true
		valid = append(valid, observation)
	}
	result.Valid = len(valid)
	result.Overall = evaluateBandGroup("overall", valid, minSample)
	result.InsufficientData = result.Overall.InsufficientData
	result.ByYear = evaluateBandGroups(valid, minSample, func(observation ForwardObservation) string {
		date, _ := time.Parse("2006-01-02", observation.AsOf)
		return date.Format("2006")
	})
	result.BySector = evaluateBandGroups(valid, minSample, func(observation ForwardObservation) string {
		if strings.TrimSpace(observation.Sector) == "" {
			return "unknown"
		}
		return observation.Sector
	})
	return result
}

func validateForwardObservation(observation ForwardObservation) string {
	if !strings.HasPrefix(observation.Code, "sh") && !strings.HasPrefix(observation.Code, "sz") {
		return "invalid_code"
	}
	asOf, err := time.Parse("2006-01-02", observation.AsOf)
	if err != nil {
		return "invalid_as_of"
	}
	future, err := time.Parse("2006-01-02", observation.FutureDate)
	if err != nil || !future.After(asOf) {
		return "future_date_not_after_as_of"
	}
	if observation.CurrentPrice <= 0 || !finite(observation.CurrentPrice) ||
		observation.FuturePrice <= 0 || !finite(observation.FuturePrice) {
		return "invalid_price"
	}
	if err := observation.Estimate.Validate(); err != nil || !observation.Estimate.Available() {
		return "unavailable_estimate"
	}
	if observation.Estimate.VisibilityMode != fundamentals.VisibilityObserved {
		return "non_observed_visibility"
	}
	if observation.Estimate.AsOf != observation.AsOf || observation.Estimate.Code != observation.Code {
		return "estimate_identity_mismatch"
	}
	if math.Abs(observation.Estimate.CurrentPrice-observation.CurrentPrice) > 1e-9 {
		return "estimate_current_price_mismatch"
	}
	if err := observation.Series.Validate(); err != nil || observation.Series.Code != observation.Code {
		return "invalid_series"
	}
	normalized := fundamentals.Normalize(observation.Series, observation.AsOf)
	if err := normalized.Validate(); err != nil || !normalized.Available {
		return "unavailable_normalized_series"
	}
	if normalized.VisibilityMode != fundamentals.VisibilityObserved {
		return "non_observed_visibility"
	}
	if normalized.InputDigest != observation.Estimate.InputDigest {
		return "estimate_digest_mismatch"
	}
	recomputed := CalculateV3(config.Stock{Code: observation.Code, Sector: observation.Sector},
		observation.CurrentPrice, observation.AsOf, normalized)
	if err := recomputed.Validate(); err != nil || !recomputed.Available() {
		return "unavailable_estimate"
	}
	if !sameEstimate(observation.Estimate, recomputed) {
		return "estimate_provenance_mismatch"
	}
	return ""
}

func sameEstimate(left, right EstimateV3) bool {
	if left.Version != right.Version || left.Code != right.Code || left.AsOf != right.AsOf ||
		left.SourceVersion != right.SourceVersion || left.InputMode != right.InputMode ||
		left.VisibilityMode != right.VisibilityMode || left.StatementBasis != right.StatementBasis ||
		left.Confidence != right.Confidence || left.DataQuality != right.DataQuality || left.Reason != right.Reason ||
		!slices.Equal(left.Warnings, right.Warnings) ||
		left.InputDigest != right.InputDigest || left.Source != right.Source || left.Method != right.Method ||
		left.LatestReportEnd != right.LatestReportEnd || left.LatestAnnouncementDate != right.LatestAnnouncementDate ||
		left.LatestAvailableFrom != right.LatestAvailableFrom || !left.LatestFetchedAt.Equal(right.LatestFetchedAt) ||
		left.WindowStart != right.WindowStart || left.WindowEnd != right.WindowEnd || left.PayoutYears != right.PayoutYears {
		return false
	}
	for _, pair := range [][2]float64{
		{left.CurrentPrice, right.CurrentPrice}, {left.EPSNormalized, right.EPSNormalized},
		{left.BookValuePerShare, right.BookValuePerShare}, {left.ROEPct, right.ROEPct},
		{left.CurrentPE, right.CurrentPE}, {left.CurrentPB, right.CurrentPB},
		{left.PayoutPct, right.PayoutPct}, {left.SustainableGrowthPct, right.SustainableGrowthPct},
		{left.CostOfEquityPct, right.CostOfEquityPct}, {left.FairPB, right.FairPB},
		{left.FairPE, right.FairPE}, {left.Lower, right.Lower}, {left.Central, right.Central},
		{left.Upper, right.Upper}, {left.UpsidePct, right.UpsidePct},
	} {
		if !nearlyEqualV2(pair[0], pair[1]) {
			return false
		}
	}
	if len(left.FiscalYears) != len(right.FiscalYears) || len(left.Scenarios) != len(right.Scenarios) {
		return false
	}
	for index := range left.FiscalYears {
		if left.FiscalYears[index] != right.FiscalYears[index] {
			return false
		}
	}
	for index := range left.Scenarios {
		first, second := left.Scenarios[index], right.Scenarios[index]
		if !nearlyEqualV2(first.CostOfEquityPct, second.CostOfEquityPct) ||
			!nearlyEqualV2(first.GrowthPct, second.GrowthPct) ||
			!nearlyEqualV2(first.FairPB, second.FairPB) ||
			!nearlyEqualV2(first.FairPE, second.FairPE) ||
			!nearlyEqualV2(first.Price, second.Price) {
			return false
		}
	}
	return true
}

func evaluateBandGroups(observations []ForwardObservation, minSample int, group func(ForwardObservation) string) []BandGroupEvaluation {
	groups := map[string][]ForwardObservation{}
	for _, observation := range observations {
		name := group(observation)
		groups[name] = append(groups[name], observation)
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]BandGroupEvaluation, 0, len(names))
	for _, name := range names {
		out = append(out, evaluateBandGroup(name, groups[name], minSample))
	}
	return out
}

func evaluateBandGroup(name string, observations []ForwardObservation, minSample int) BandGroupEvaluation {
	out := BandGroupEvaluation{Group: name, Observations: len(observations)}
	if len(observations) == 0 {
		out.InsufficientData = true
		return out
	}
	var width, absGap, pinball float64
	var directionTotal, directionHits int
	for _, observation := range observations {
		estimate := observation.Estimate
		current := observation.CurrentPrice
		future := observation.FuturePrice
		lowerReturn := estimate.Lower/current - 1
		upperReturn := estimate.Upper/current - 1
		centralReturn := estimate.Central/current - 1
		actualReturn := future/current - 1
		if future >= estimate.Lower && future <= estimate.Upper {
			out.Covered++
		}
		width += (estimate.Upper - estimate.Lower) / current * 100
		absGap += math.Abs(centralReturn-actualReturn) * 100
		pinball += pinballLoss(actualReturn, lowerReturn, 0.2) + pinballLoss(actualReturn, upperReturn, 0.8)
		if centralReturn != 0 && actualReturn != 0 {
			directionTotal++
			if math.Signbit(centralReturn) == math.Signbit(actualReturn) {
				directionHits++
			}
		}
	}
	n := float64(len(observations))
	out.CoveragePct = float64(out.Covered) / n * 100
	out.MeanWidthPct = width / n
	out.MeanAbsGapPct = absGap / n
	out.PinballLossPct = pinball / n * 100
	if directionTotal > 0 {
		out.DirectionHitPct = float64(directionHits) / float64(directionTotal) * 100
	}
	out.InsufficientData = len(observations) < minSample
	return out
}

func pinballLoss(actual, forecast, quantile float64) float64 {
	error := actual - forecast
	if error >= 0 {
		return quantile * error
	}
	return (quantile - 1) * error
}

func (e BandEvaluation) Validate() error {
	if e.Observations < 0 || e.Valid < 0 || e.Invalid < 0 || e.Valid+e.Invalid != e.Observations {
		return errors.New("invalid band evaluation counts")
	}
	if e.NominalCoveragePct <= 0 || e.NominalCoveragePct > 100 || e.MinSample <= 0 {
		return errors.New("invalid band evaluation contract")
	}
	return nil
}
