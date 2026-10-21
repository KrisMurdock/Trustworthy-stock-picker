// Package modeleval defines a dependency-free contract for publishing model
// evaluation metrics. It deliberately has no dependency on internal/predictor
// so evaluators and consumers can share records without import cycles.
package modeleval

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

type Family string

const (
	FamilyPoint     Family = "point"
	FamilyDirection Family = "direction"
	FamilyInterval  Family = "interval"
	FamilyRanking   Family = "ranking"
	FamilyTrading   Family = "trading"
)

type Role string

const (
	RolePrimary    Role = "primary"
	RoleGuardrail  Role = "guardrail"
	RoleDiagnostic Role = "diagnostic"
	RolePaper      Role = "paper"
)

type Applicability string

const (
	ApplicabilityOK               Applicability = "ok"
	ApplicabilityNotApplicable    Applicability = "not_applicable"
	ApplicabilityInsufficientData Applicability = "insufficient_data"
)

type Space string

const (
	SpaceReturn Space = "return"
	SpacePrice  Space = "price"
	SpaceNone   Space = "none"
)

type Unit string

const (
	UnitBPS     Unit = "bps"
	UnitCNY     Unit = "CNY"
	UnitRatio   Unit = "ratio"
	UnitPercent Unit = "percent"
	UnitCount   Unit = "count"
	UnitNone    Unit = "none"
)

type Aggregation string

const (
	AggregationPooled     Aggregation = "pooled"
	AggregationDayEqual   Aggregation = "day_equal"
	AggregationStockEqual Aggregation = "stock_equal"
)

const (
	MetricPointMAE                    = "point.mae"
	MetricPointRMSE                   = "point.rmse"
	MetricPointMedAE                  = "point.medae"
	MetricPointP90AbsError            = "point.p90_abs_error"
	MetricPointBias                   = "point.bias"
	MetricPointMAPE                   = "point.mape"
	MetricPointR2                     = "point.r2"
	MetricPointSMAPE                  = "point.smape"
	MetricPointEnvelopeHitRate        = "point.envelope_hit_rate"
	MetricPointEnvelopeWidthMean      = "point.envelope_width_mean"
	MetricPointContradictionRate      = "point.contradiction_rate"
	MetricPointSkillMAE               = "point.skill_mae"
	MetricPointSkillRMSE              = "point.skill_rmse"
	MetricDirectionAccuracy           = "direction.accuracy"
	MetricDirectionBalancedAccuracy   = "direction.balanced_accuracy"
	MetricDirectionPrecision          = "direction.precision"
	MetricDirectionRecall             = "direction.recall"
	MetricDirectionF1                 = "direction.f1"
	MetricDirectionMCC                = "direction.mcc"
	MetricDirectionAccuracyDelta      = "direction.accuracy_delta"
	MetricIntervalEmpiricalCoverage   = "interval.empirical_coverage"
	MetricIntervalWidthMean           = "interval.width_mean"
	MetricIntervalPinballLoss         = "interval.pinball_loss"
	MetricIntervalCRPS                = "interval.crps"
	MetricIntervalJointCoverage       = "interval.joint_coverage"
	MetricIntervalEnvelopeCoverage    = "interval.envelope_coverage"
	MetricIntervalScore               = "interval.score"
	MetricIntervalLowerBreachRate     = "interval.lower_breach_rate"
	MetricIntervalUpperBreachRate     = "interval.upper_breach_rate"
	MetricIntervalLowerExceedanceMean = "interval.lower_exceedance_mean"
	MetricIntervalUpperExceedanceMean = "interval.upper_exceedance_mean"
	MetricRankingIC                   = "ranking.ic"
	MetricRankingRankIC               = "ranking.rank_ic"
	MetricRankingTopKPrecision        = "ranking.topk_precision"
	MetricTradingNetReturn            = "trading.net_return"
	MetricTradingSharpe               = "trading.sharpe"
	MetricTradingMaxDrawdown          = "trading.max_drawdown"
	MetricTradingTurnover             = "trading.turnover"
)

const (
	TargetHigh  = "high"
	TargetLow   = "low"
	TargetClose = "close"
	TargetAll   = "all"
)

const (
	BaselineLastPrice = "last_price"
	BaselineRange     = "range"
	BaselineMajority  = "majority"
	BaselineRandom    = "random"
)

var metricFamilies = map[string]Family{
	MetricPointMAE:                    FamilyPoint,
	MetricPointRMSE:                   FamilyPoint,
	MetricPointMedAE:                  FamilyPoint,
	MetricPointP90AbsError:            FamilyPoint,
	MetricPointBias:                   FamilyPoint,
	MetricPointMAPE:                   FamilyPoint,
	MetricPointR2:                     FamilyPoint,
	MetricPointSMAPE:                  FamilyPoint,
	MetricPointEnvelopeHitRate:        FamilyPoint,
	MetricPointEnvelopeWidthMean:      FamilyPoint,
	MetricPointContradictionRate:      FamilyPoint,
	MetricPointSkillMAE:               FamilyPoint,
	MetricPointSkillRMSE:              FamilyPoint,
	MetricDirectionAccuracy:           FamilyDirection,
	MetricDirectionBalancedAccuracy:   FamilyDirection,
	MetricDirectionPrecision:          FamilyDirection,
	MetricDirectionRecall:             FamilyDirection,
	MetricDirectionF1:                 FamilyDirection,
	MetricDirectionMCC:                FamilyDirection,
	MetricDirectionAccuracyDelta:      FamilyDirection,
	MetricIntervalEmpiricalCoverage:   FamilyInterval,
	MetricIntervalWidthMean:           FamilyInterval,
	MetricIntervalPinballLoss:         FamilyInterval,
	MetricIntervalCRPS:                FamilyInterval,
	MetricIntervalJointCoverage:       FamilyInterval,
	MetricIntervalEnvelopeCoverage:    FamilyInterval,
	MetricIntervalScore:               FamilyInterval,
	MetricIntervalLowerBreachRate:     FamilyInterval,
	MetricIntervalUpperBreachRate:     FamilyInterval,
	MetricIntervalLowerExceedanceMean: FamilyInterval,
	MetricIntervalUpperExceedanceMean: FamilyInterval,
	MetricRankingIC:                   FamilyRanking,
	MetricRankingRankIC:               FamilyRanking,
	MetricRankingTopKPrecision:        FamilyRanking,
	MetricTradingNetReturn:            FamilyTrading,
	MetricTradingSharpe:               FamilyTrading,
	MetricTradingMaxDrawdown:          FamilyTrading,
	MetricTradingTurnover:             FamilyTrading,
}

type SliceDimension struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ConfidenceInterval struct {
	Method        string        `json:"method"`
	Confidence    float64       `json:"confidence"`
	Resamples     int           `json:"resamples"`
	Seed          int64         `json:"seed"`
	Lower         *float64      `json:"lower"`
	Upper         *float64      `json:"upper"`
	Applicability Applicability `json:"applicability"`
	Reason        string        `json:"reason,omitempty"`
}

type LabelSpec struct {
	Mode            string  `json:"mode"`
	FlatBandBPS     float64 `json:"flat_band_bps"`
	QuantileLow     float64 `json:"quantile_low"`
	QuantileHigh    float64 `json:"quantile_high"`
	FitFrom         string  `json:"fit_from"`
	FitTo           string  `json:"fit_to"`
	ExcludedSamples int     `json:"excluded_samples"`
}

type IntervalSpec struct {
	NominalCoverage float64 `json:"nominal_coverage"`
	Method          string  `json:"method"`
}

type ComparisonSpec struct {
	Formula          string `json:"formula"`
	BaselineMetricID string `json:"baseline_metric_id"`
}

type Spec struct {
	Label      *LabelSpec      `json:"label,omitempty"`
	Interval   *IntervalSpec   `json:"interval,omitempty"`
	Comparison *ComparisonSpec `json:"comparison,omitempty"`
}

// MetricRecord is one typed observation in the shared evaluation contract.
// MetricID identifies the formula family; Subject, Target, Space, Aggregation,
// Slice and Baseline together identify the concrete comparison.
type MetricRecord struct {
	MetricID       string              `json:"metric_id"`
	Family         Family              `json:"family"`
	Role           Role                `json:"role"`
	Subject        string              `json:"subject"`
	Target         string              `json:"target"`
	Space          Space               `json:"space"`
	Unit           Unit                `json:"unit"`
	Aggregation    Aggregation         `json:"aggregation"`
	Slice          []SliceDimension    `json:"slice"`
	Baseline       string              `json:"baseline,omitempty"`
	Value          *float64            `json:"value"`
	Applicability  Applicability       `json:"applicability"`
	Reason         string              `json:"reason,omitempty"`
	Samples        int                 `json:"samples"`
	Days           int                 `json:"days"`
	Stocks         int                 `json:"stocks"`
	CI             *ConfidenceInterval `json:"ci,omitempty"`
	HigherIsBetter bool                `json:"higher_is_better"`
	Spec           *Spec               `json:"spec,omitempty"`
}

func ModelSubject(modelID string) string {
	return "model:" + modelID
}

func BaselineSubject(name string) string {
	return "baseline:" + name
}

func Float(value float64) *float64 {
	return &value
}

// Validate checks that a record has a complete, internally consistent metric
// identity and compatible applicability, value, sample counts and confidence
// interval. Validation does not mutate the record.
func (record MetricRecord) Validate() error {
	if strings.TrimSpace(record.MetricID) == "" {
		return fmt.Errorf("metric_id is required")
	}
	expectedFamily, known := metricFamilies[record.MetricID]
	if !known {
		return fmt.Errorf("unsupported metric_id %q", record.MetricID)
	}
	if !record.Family.valid() {
		return fmt.Errorf("invalid family %q", record.Family)
	}
	if record.Family != expectedFamily {
		return fmt.Errorf("metric_id %q belongs to family %q, not %q", record.MetricID, expectedFamily, record.Family)
	}
	if !record.Role.valid() {
		return fmt.Errorf("invalid role %q", record.Role)
	}
	if strings.TrimSpace(record.Subject) == "" {
		return fmt.Errorf("subject is required")
	}
	if strings.TrimSpace(record.Target) == "" {
		return fmt.Errorf("target is required")
	}
	if !record.Space.valid() {
		return fmt.Errorf("invalid space %q", record.Space)
	}
	if !record.Unit.valid() {
		return fmt.Errorf("invalid unit %q", record.Unit)
	}
	if !record.Aggregation.valid() {
		return fmt.Errorf("invalid aggregation %q", record.Aggregation)
	}
	if err := validateSlice(record.Slice); err != nil {
		return err
	}
	if !record.Applicability.valid() {
		return fmt.Errorf("invalid applicability %q", record.Applicability)
	}
	if record.Applicability != ApplicabilityOK {
		if record.Value != nil {
			return fmt.Errorf("value must be null when applicability is %q", record.Applicability)
		}
		if strings.TrimSpace(record.Reason) == "" {
			return fmt.Errorf("reason is required when applicability is %q", record.Applicability)
		}
	} else {
		if record.Value == nil {
			return fmt.Errorf("value is required when applicability is %q", record.Applicability)
		}
		if !finite(*record.Value) {
			return fmt.Errorf("value must be finite")
		}
	}
	if record.Samples < 0 {
		return fmt.Errorf("samples must be nonnegative")
	}
	if record.Days < 0 {
		return fmt.Errorf("days must be nonnegative")
	}
	if record.Stocks < 0 {
		return fmt.Errorf("stocks must be nonnegative")
	}
	if record.CI != nil {
		if err := validateConfidenceInterval(record.CI); err != nil {
			return fmt.Errorf("invalid ci: %w", err)
		}
	}
	return nil
}

// CanonicalKey returns a stable JSON identity for a record. MetricID alone is
// not unique: the key also includes subject, target, space, aggregation,
// baseline and sorted slice dimensions. It canonicalizes slice order but does
// not validate the full record; call Validate separately when required.
func (record MetricRecord) CanonicalKey() (string, error) {
	return record.canonicalized().metricKey()
}

// SortRecords canonicalizes every record's slice order and sorts the records
// in place by canonical key.
func SortRecords(records []MetricRecord) {
	type keyedRecord struct {
		record MetricRecord
		key    string
	}
	keyed := make([]keyedRecord, len(records))
	for index := range records {
		records[index] = records[index].canonicalized()
		key, err := records[index].CanonicalKey()
		if err != nil {
			key, _ = records[index].metricKey()
		}
		keyed[index] = keyedRecord{record: records[index], key: key}
	}
	sort.SliceStable(keyed, func(left, right int) bool {
		return keyed[left].key < keyed[right].key
	})
	for index := range keyed {
		records[index] = keyed[index].record
	}
}

func FindRecord(records []MetricRecord, metricID, subject, target string) (MetricRecord, bool) {
	for _, record := range records {
		if record.MetricID == metricID && record.Subject == subject && record.Target == target {
			return record, true
		}
	}
	return MetricRecord{}, false
}

func (record MetricRecord) canonicalized() MetricRecord {
	record.Slice = canonicalSlice(record.Slice)
	return record
}

func (record MetricRecord) metricKey() (string, error) {
	dimensions := record.Slice
	if dimensions == nil {
		dimensions = []SliceDimension{}
	}
	payload, err := json.Marshal(struct {
		MetricID    string           `json:"metric_id"`
		Subject     string           `json:"subject"`
		Target      string           `json:"target"`
		Space       Space            `json:"space"`
		Aggregation Aggregation      `json:"aggregation"`
		Slice       []SliceDimension `json:"slice"`
		Baseline    string           `json:"baseline"`
	}{
		MetricID:    record.MetricID,
		Subject:     record.Subject,
		Target:      record.Target,
		Space:       record.Space,
		Aggregation: record.Aggregation,
		Slice:       dimensions,
		Baseline:    record.Baseline,
	})
	if err != nil {
		return "", fmt.Errorf("encode metric identity: %w", err)
	}
	return string(payload), nil
}

func canonicalSlice(dimensions []SliceDimension) []SliceDimension {
	if len(dimensions) == 0 {
		return nil
	}
	canonical := append([]SliceDimension(nil), dimensions...)
	sort.SliceStable(canonical, func(left, right int) bool {
		return sliceDimensionLess(canonical[left], canonical[right])
	})
	return canonical
}

func sliceDimensionLess(left, right SliceDimension) bool {
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	return left.Value < right.Value
}

func validateSlice(dimensions []SliceDimension) error {
	seen := make(map[string]struct{}, len(dimensions))
	for index, dimension := range dimensions {
		if strings.TrimSpace(dimension.Name) == "" {
			return fmt.Errorf("slice dimension name is required at index %d", index)
		}
		if _, exists := seen[dimension.Name]; exists {
			return fmt.Errorf("slice dimension %q is duplicated", dimension.Name)
		}
		seen[dimension.Name] = struct{}{}
		if index > 0 && sliceDimensionLess(dimension, dimensions[index-1]) {
			return fmt.Errorf("slice dimensions must be sorted by name then value")
		}
	}
	return nil
}

func validateConfidenceInterval(interval *ConfidenceInterval) error {
	if strings.TrimSpace(interval.Method) == "" {
		return fmt.Errorf("method is required")
	}
	if !interval.Applicability.valid() {
		return fmt.Errorf("invalid applicability %q", interval.Applicability)
	}
	if math.IsNaN(interval.Confidence) || math.IsInf(interval.Confidence, 0) || interval.Confidence <= 0 || interval.Confidence > 1 {
		return fmt.Errorf("confidence must be in (0, 1]")
	}
	if interval.Resamples < 0 {
		return fmt.Errorf("resamples must be nonnegative")
	}
	if interval.Applicability != ApplicabilityOK && strings.TrimSpace(interval.Reason) == "" {
		return fmt.Errorf("reason is required when applicability is %q", interval.Applicability)
	}
	if interval.Applicability == ApplicabilityOK {
		if interval.Lower == nil || interval.Upper == nil {
			return fmt.Errorf("lower and upper bounds are required when applicability is %q", interval.Applicability)
		}
	}
	if interval.Lower != nil && !finite(*interval.Lower) {
		return fmt.Errorf("lower bound must be finite")
	}
	if interval.Upper != nil && !finite(*interval.Upper) {
		return fmt.Errorf("upper bound must be finite")
	}
	if interval.Lower != nil && interval.Upper != nil && *interval.Lower > *interval.Upper {
		return fmt.Errorf("lower bound must not exceed upper bound")
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func (family Family) valid() bool {
	switch family {
	case FamilyPoint, FamilyDirection, FamilyInterval, FamilyRanking, FamilyTrading:
		return true
	default:
		return false
	}
}

func (role Role) valid() bool {
	switch role {
	case RolePrimary, RoleGuardrail, RoleDiagnostic, RolePaper:
		return true
	default:
		return false
	}
}

func (applicability Applicability) valid() bool {
	switch applicability {
	case ApplicabilityOK, ApplicabilityNotApplicable, ApplicabilityInsufficientData:
		return true
	default:
		return false
	}
}

func (space Space) valid() bool {
	switch space {
	case SpaceReturn, SpacePrice, SpaceNone:
		return true
	default:
		return false
	}
}

func (unit Unit) valid() bool {
	switch unit {
	case UnitBPS, UnitCNY, UnitRatio, UnitPercent, UnitCount, UnitNone:
		return true
	default:
		return false
	}
}

func (aggregation Aggregation) valid() bool {
	switch aggregation {
	case AggregationPooled, AggregationDayEqual, AggregationStockEqual:
		return true
	default:
		return false
	}
}
