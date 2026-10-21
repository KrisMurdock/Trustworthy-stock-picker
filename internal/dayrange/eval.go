package dayrange

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
)

type group struct {
	slice        []modeleval.SliceDimension
	observations []Observation
}
type valueRow struct {
	date, code      string
	value, baseline float64
}
type definition struct {
	id, target  string
	space       modeleval.Space
	unit        modeleval.Unit
	family      modeleval.Family
	role        modeleval.Role
	higher      bool
	kind        string
	extra       []modeleval.SliceDimension
	spec        *modeleval.Spec
	unavailable string
	measure     func(Observation) (float64, float64, bool)
}

// Evaluate computes deterministic, date-weighted and pooled metrics from one
// model's frozen observations. Modes and intraday cutoffs are never pooled
// together. It performs no I/O and never modifies caller-owned observations.
func Evaluate(ctx context.Context, observations []Observation, opts Options) (Report, error) {
	r := Report{SchemaVersion: EvaluationSchemaVersion, Subject: opts.Subject, Status: "insufficient_data", Submitted: len(observations), Groups: []Evidence{}, Metrics: []modeleval.MetricRecord{}}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if strings.TrimSpace(opts.Subject) == "" {
		return r, fmt.Errorf("evaluation subject is required")
	}
	ordered := append([]Observation(nil), observations...)
	seen := map[string]bool{}
	schemas := map[string]string{}
	for i, s := range ordered {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		if err := validateObservation(s, opts); err != nil {
			return r, fmt.Errorf("observation %d: %w", i, err)
		}
		key := observationKey(s)
		if seen[key] {
			return r, fmt.Errorf("duplicate forecast observation")
		}
		seen[key] = true
		base := dimensionKey(dimensions(s)[0])
		schema := forecastSchema(s.Forecast)
		if previous, ok := schemas[base]; ok && previous != schema {
			return r, fmt.Errorf("incompatible forecast schemas at the same mode and cutoff")
		}
		schemas[base] = schema
		if s.Actual == nil {
			r.Unresolved++
		} else {
			r.Resolved++
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return observationKey(ordered[i]) < observationKey(ordered[j]) })
	groups := map[string]*group{}
	for _, s := range ordered {
		for _, dims := range dimensions(s) {
			key := dimensionKey(dims)
			if groups[key] == nil {
				groups[key] = &group{slice: dims}
			}
			groups[key].observations = append(groups[key].observations, s)
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		g := groups[key]
		e := Evidence{Slice: g.slice, Submitted: len(g.observations), UnresolvedReasons: map[string]int{}}
		for _, s := range g.observations {
			if s.Actual == nil {
				e.Unresolved++
				e.UnresolvedReasons[s.UnresolvedReason]++
			} else {
				e.Resolved++
			}
		}
		r.Groups = append(r.Groups, e)
		for _, d := range definitions(g.observations[0].Forecast, opts) {
			if err := ctx.Err(); err != nil {
				return r, err
			}
			rows := []valueRow{}
			if d.unavailable == "" {
				for _, s := range g.observations {
					if s.Actual == nil {
						continue
					}
					v, b, ok := d.measure(s)
					if ok {
						if !finite(v) || !finite(b) {
							return r, fmt.Errorf("non-finite result for %s", d.id)
						}
						rows = append(rows, valueRow{s.Date, s.Code, v, b})
					}
				}
			}
			for _, agg := range []modeleval.Aggregation{modeleval.AggregationDayEqual, modeleval.AggregationPooled} {
				m, err := record(ctx, opts, g.slice, d, rows, agg)
				if err != nil {
					return r, err
				}
				r.Metrics = append(r.Metrics, m)
			}
		}
	}
	modeleval.SortRecords(r.Metrics)
	if r.Resolved > 0 {
		r.Status = "ok"
		if r.Unresolved > 0 {
			r.Status = "partial"
		}
	}
	return r, nil
}

func definitions(f Forecast, opts Options) []definition {
	var out []definition
	for _, target := range []string{modeleval.TargetHigh, modeleval.TargetLow} {
		estimate := f.High
		if target == modeleval.TargetLow {
			estimate = f.Low
		}
		for _, space := range []modeleval.Space{modeleval.SpacePrice, modeleval.SpaceReturn} {
			unit := modeleval.UnitCNY
			if space == modeleval.SpaceReturn {
				unit = modeleval.UnitBPS
			}
			for _, id := range []string{modeleval.MetricPointMAE, modeleval.MetricPointRMSE, modeleval.MetricPointBias} {
				d := definition{id: id, target: target, space: space, unit: unit, family: modeleval.FamilyPoint, role: modeleval.RolePrimary, kind: "mean"}
				if id == modeleval.MetricPointRMSE {
					d.kind = "rmse"
				}
				if id == modeleval.MetricPointBias {
					d.role = modeleval.RoleDiagnostic
				}
				d.measure = func(s Observation) (float64, float64, bool) {
					e, a, _ := targetValues(s, target)
					v := (e.Point - a) * scale(s, space)
					if id != modeleval.MetricPointBias {
						v = math.Abs(v)
					}
					return v, 0, true
				}
				out = append(out, d)
			}
			for _, id := range []string{modeleval.MetricPointSkillMAE, modeleval.MetricPointSkillRMSE} {
				d := definition{id: id, target: target, space: space, unit: modeleval.UnitRatio, family: modeleval.FamilyPoint, role: modeleval.RoleDiagnostic, higher: true, kind: "skill_mae", spec: &modeleval.Spec{Comparison: &modeleval.ComparisonSpec{Formula: "1 - model_mae / baseline_mae", BaselineMetricID: modeleval.MetricPointMAE}}}
				if id == modeleval.MetricPointSkillRMSE {
					d.kind = "skill_rmse"
					d.spec.Comparison = &modeleval.ComparisonSpec{Formula: "1 - model_rmse / baseline_rmse", BaselineMetricID: modeleval.MetricPointRMSE}
				}
				if opts.Baseline == "" {
					d.unavailable = "no baseline was specified"
				}
				d.measure = func(s Observation) (float64, float64, bool) {
					if s.Baseline == nil {
						return 0, 0, false
					}
					e, a, b := targetValues(s, target)
					return math.Abs(e.Point-a) * scale(s, space), math.Abs(b-a) * scale(s, space), true
				}
				out = append(out, d)
			}
			for index, q := range estimate.Quantiles {
				out = append(out, definition{id: modeleval.MetricIntervalPinballLoss, target: target, space: space, unit: unit, family: modeleval.FamilyInterval, role: modeleval.RolePrimary, extra: []modeleval.SliceDimension{{Name: "quantile", Value: number(q.Level)}}, measure: func(s Observation) (float64, float64, bool) {
					e, a, _ := targetValues(s, target)
					delta := (a - e.Quantiles[index].Price) * scale(s, space)
					return math.Max(q.Level*delta, (q.Level-1)*delta), 0, true
				}})
			}
			if len(estimate.Quantiles) == 0 {
				out = append(out, definition{id: modeleval.MetricIntervalPinballLoss, target: target, space: space, unit: unit, family: modeleval.FamilyInterval, unavailable: "forecast has no quantiles"})
			}
		}
		out = append(out, definition{id: modeleval.MetricPointMAE, target: target, space: modeleval.SpaceNone, unit: modeleval.UnitRatio, family: modeleval.FamilyPoint, role: modeleval.RoleDiagnostic, extra: []modeleval.SliceDimension{{Name: "normalization", Value: "volatility"}}, measure: func(s Observation) (float64, float64, bool) {
			if s.ScalePrice == 0 {
				return 0, 0, false
			}
			e, a, _ := targetValues(s, target)
			return math.Abs(e.Point-a) / s.ScalePrice, 0, true
		}})
		out = append(out, intervalDefinitions(target, estimate.Interval)...)
	}
	// These are distinct events: both target intervals cover vs. both observed
	// extrema lie within the outer low-lower/high-upper price envelope.
	for _, id := range []string{modeleval.MetricIntervalJointCoverage, modeleval.MetricIntervalEnvelopeCoverage} {
		d := definition{id: id, target: modeleval.TargetAll, space: modeleval.SpaceNone, unit: modeleval.UnitRatio, family: modeleval.FamilyInterval, role: modeleval.RoleGuardrail, higher: true, extra: intervalDimensions(f.High.Interval)}
		if f.High.Interval == nil {
			d.unavailable = "forecast has no prediction intervals"
		}
		d.measure = func(s Observation) (float64, float64, bool) {
			h, l := s.Forecast.High.Interval, s.Forecast.Low.Interval
			hit := s.Actual.Low >= l.Lower && s.Actual.High <= h.Upper
			if id == modeleval.MetricIntervalJointCoverage {
				hit = hit && s.Actual.Low <= l.Upper && s.Actual.High >= h.Lower
			}
			return indicator(hit), 0, true
		}
		// Do not attach a nominal joint coverage: only marginal coverage is supplied.
		out = append(out, d)
	}
	return out
}

func intervalDefinitions(target string, interval *Interval) []definition {
	var out []definition
	for _, id := range []string{modeleval.MetricIntervalEmpiricalCoverage, modeleval.MetricIntervalLowerBreachRate, modeleval.MetricIntervalUpperBreachRate, modeleval.MetricIntervalWidthMean, modeleval.MetricIntervalScore, modeleval.MetricIntervalLowerExceedanceMean, modeleval.MetricIntervalUpperExceedanceMean} {
		numeric := id == modeleval.MetricIntervalWidthMean || id == modeleval.MetricIntervalScore || id == modeleval.MetricIntervalLowerExceedanceMean || id == modeleval.MetricIntervalUpperExceedanceMean
		spaces := []modeleval.Space{modeleval.SpaceNone}
		if numeric {
			spaces = []modeleval.Space{modeleval.SpacePrice, modeleval.SpaceReturn}
		}
		for _, space := range spaces {
			unit := modeleval.UnitRatio
			if space == modeleval.SpacePrice {
				unit = modeleval.UnitCNY
			} else if space == modeleval.SpaceReturn {
				unit = modeleval.UnitBPS
			}
			d := definition{id: id, target: target, space: space, unit: unit, family: modeleval.FamilyInterval, role: modeleval.RoleGuardrail, higher: id == modeleval.MetricIntervalEmpiricalCoverage, extra: intervalDimensions(interval)}
			if id == modeleval.MetricIntervalScore {
				d.role = modeleval.RolePrimary
			}
			if interval == nil {
				d.unavailable = "forecast has no prediction intervals"
			} else {
				d.spec = &modeleval.Spec{Interval: &modeleval.IntervalSpec{NominalCoverage: interval.NominalCoverage, Method: interval.Method}}
			}
			d.measure = func(s Observation) (float64, float64, bool) {
				e, a, _ := targetValues(s, target)
				i := e.Interval
				lower, upper := math.Max(i.Lower-a, 0), math.Max(a-i.Upper, 0)
				v := 0.0
				switch id {
				case modeleval.MetricIntervalEmpiricalCoverage:
					v = indicator(a >= i.Lower && a <= i.Upper)
				case modeleval.MetricIntervalLowerBreachRate:
					v = indicator(a < i.Lower)
				case modeleval.MetricIntervalUpperBreachRate:
					v = indicator(a > i.Upper)
				case modeleval.MetricIntervalWidthMean:
					v = i.Upper - i.Lower
				case modeleval.MetricIntervalScore:
					v = i.Upper - i.Lower + 2/(1-i.NominalCoverage)*(lower+upper)
				case modeleval.MetricIntervalLowerExceedanceMean:
					v = lower
				case modeleval.MetricIntervalUpperExceedanceMean:
					v = upper
				}
				return v * scale(s, space), 0, true
			}
			out = append(out, d)
		}
	}
	return out
}
func targetValues(s Observation, target string) (Estimate, float64, float64) {
	if target == modeleval.TargetHigh {
		b := 0.0
		if s.Baseline != nil {
			b = s.Baseline.High
		}
		return s.Forecast.High, s.Actual.High, b
	}
	b := 0.0
	if s.Baseline != nil {
		b = s.Baseline.Low
	}
	return s.Forecast.Low, s.Actual.Low, b
}
func scale(s Observation, space modeleval.Space) float64 {
	if space == modeleval.SpaceReturn {
		return 10000 / s.ReferencePrice
	}
	return 1
}
func indicator(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
func number(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
func intervalDimensions(i *Interval) []modeleval.SliceDimension {
	if i == nil {
		return nil
	}
	return []modeleval.SliceDimension{{Name: "coverage", Value: number(i.NominalCoverage)}, {Name: "interval_method", Value: i.Method}}
}

func record(ctx context.Context, opts Options, slice []modeleval.SliceDimension, d definition, rows []valueRow, agg modeleval.Aggregation) (modeleval.MetricRecord, error) {
	m := modeleval.MetricRecord{MetricID: d.id, Family: d.family, Role: d.role, Subject: opts.Subject, Target: d.target, Space: d.space, Unit: d.unit, Aggregation: agg, Slice: appendCopy(slice, d.extra...), Applicability: modeleval.ApplicabilityInsufficientData, Reason: "no resolved observations with required inputs", Samples: len(rows), HigherIsBetter: d.higher, Spec: d.spec}
	if m.Role == "" {
		m.Role = modeleval.RoleDiagnostic
	}
	if agg == modeleval.AggregationPooled {
		m.Role = modeleval.RolePaper
	}
	if strings.HasPrefix(d.kind, "skill_") {
		m.Baseline = opts.Baseline
	}
	days, stocks := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		days[r.date] = true
		stocks[r.code] = true
	}
	m.Days, m.Stocks = len(days), len(stocks)
	if d.unavailable != "" {
		m.Applicability = modeleval.ApplicabilityNotApplicable
		m.Reason = d.unavailable
	} else if len(rows) > 0 {
		daily := summarizeDays(rows, d.kind)
		values := daily
		if agg == modeleval.AggregationPooled {
			values = []dailyValue{summarize(rows, d.kind)}
		}
		v, ok := aggregate(values, d.kind)
		if ok {
			m.Value = modeleval.Float(v)
			m.Applicability = modeleval.ApplicabilityOK
			m.Reason = ""
		} else {
			m.Applicability = modeleval.ApplicabilityNotApplicable
			m.Reason = "baseline error is zero"
		}
		if agg == modeleval.AggregationDayEqual && ok {
			ci, err := bootstrap(ctx, daily, d.kind)
			if err != nil {
				return m, err
			}
			m.CI = ci
		}
	}
	if err := m.Validate(); err != nil {
		return m, fmt.Errorf("invalid computed %s: %w", d.id, err)
	}
	return m, nil
}
