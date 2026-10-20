package predictor

import (
	"math"
	"math/rand"
	"sort"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
)

const (
	confidenceMethod    = "paired_day_block_bootstrap"
	confidenceLevel     = 0.95
	confidenceResamples = 10000
	confidenceSeed      = int64(1949)
)

const (
	directionLabelNonzeroSign = "nonzero_sign"
	directionLabelExact3Class = "exact_3class"
	directionLabelDeadband5   = "deadband_5"
	directionLabelDeadband10  = "deadband_10"
	directionLabelQuantile    = "quantile_30_70"
)

// metricSample is the immutable evidence used to derive every metric for one
// prediction. Actual is the realized target; Predicted is the candidate under
// evaluation; Last and Range are the two point baselines.
type metricSample struct {
	Date       string
	Code       string
	InputClose float64
	Actual     TargetMetrics
	Predicted  TargetMetrics
	Last       TargetMetrics
	Range      TargetMetrics
}

// metricAccumulator stores per-sample evidence so every aggregation, slice and
// baseline is derived from exactly the same validation observations.
type metricAccumulator struct {
	samples []metricSample
}

func newMetricAccumulator() *metricAccumulator {
	return &metricAccumulator{}
}

func (a *metricAccumulator) add(sample Sample, predicted map[Target]float64) {
	if a == nil || sample.InputClose <= 0 {
		return
	}
	actual := TargetMetrics{
		High:  sample.Labels[TargetHigh],
		Low:   sample.Labels[TargetLow],
		Close: sample.Labels[TargetClose],
	}
	candidate := TargetMetrics{
		High:  predicted[TargetHigh],
		Low:   predicted[TargetLow],
		Close: predicted[TargetClose],
	}
	if !targetMetricsFinite(actual) || !targetMetricsFinite(candidate) {
		return
	}
	a.samples = append(a.samples, metricSample{
		Date:       sample.Date,
		Code:       sample.Code,
		InputClose: sample.InputClose,
		Actual:     actual,
		Predicted:  candidate,
		Last: TargetMetrics{
			High:  sample.InputHigh/sample.InputClose - 1,
			Low:   sample.InputLow/sample.InputClose - 1,
			Close: 0,
		},
		Range: TargetMetrics{
			High:  sample.BaselineRange / 2,
			Low:   -sample.BaselineRange / 2,
			Close: 0,
		},
	})
}

func (a *metricAccumulator) sampleCount() int {
	if a == nil {
		return 0
	}
	return len(a.samples)
}

// metricSampleSummary returns the sample and validation-day counts represented
// by a subject's records. It prefers the close return-space MAE day-equal
// record and otherwise uses the largest record for the subject.
func metricSampleSummary(records []modeleval.MetricRecord, subject string) (samples, days int, ok bool) {
	for _, record := range records {
		if record.Subject == subject && record.MetricID == modeleval.MetricPointMAE &&
			record.Target == modeleval.TargetClose && record.Space == modeleval.SpaceReturn &&
			record.Aggregation == modeleval.AggregationDayEqual {
			return record.Samples, record.Days, true
		}
	}
	for _, record := range records {
		if record.Subject != subject {
			continue
		}
		if !ok || record.Samples > samples {
			samples = record.Samples
			days = record.Days
			ok = true
		}
	}
	return samples, days, ok
}

func (a *metricAccumulator) bands() TargetMetrics {
	if a == nil || len(a.samples) == 0 {
		return TargetMetrics{}
	}
	errors := map[Target][]float64{
		TargetHigh:  {},
		TargetLow:   {},
		TargetClose: {},
	}
	for _, sample := range canonicalMetricSamples(a.samples) {
		for _, target := range Targets {
			actual, _ := targetMetricsValue(sample.Actual, string(target))
			predicted, _ := targetMetricsValue(sample.Predicted, string(target))
			errors[target] = append(errors[target], math.Abs(predicted-actual))
		}
	}
	return TargetMetrics{
		High:  percentile90(errors[TargetHigh]),
		Low:   percentile90(errors[TargetLow]),
		Close: percentile90(errors[TargetClose]),
	}
}

func (a *metricAccumulator) records(subject string, slice []modeleval.SliceDimension, selector func(metricSample) TargetMetrics, includeReserved bool) []modeleval.MetricRecord {
	if a == nil {
		return nil
	}
	if selector == nil {
		selector = func(sample metricSample) TargetMetrics { return sample.Actual }
	}
	evidence := selectMetricEvidence(a.samples, selector)
	candidate := !strings.HasPrefix(subject, "baseline:")
	records := make([]modeleval.MetricRecord, 0, 128)

	records = append(records, pointReturnRecords(subject, slice, evidence, candidate)...)
	records = append(records, pointPriceRecords(subject, slice, evidence)...)
	if candidate {
		records = append(records, pointSkillRecords(subject, slice, evidence)...)
	}
	records = append(records, envelopeRecords(subject, slice, evidence)...)
	records = append(records, directionRecords(subject, slice, evidence, candidate)...)
	if includeReserved {
		records = append(records, reservedRecords(subject, slice, evidence)...)
	}
	return sortUniqueRecords(records)
}

func (a *metricAccumulator) majorityRecords(subject string, slice []modeleval.SliceDimension) []modeleval.MetricRecord {
	if a == nil {
		return nil
	}
	evidence := selectMetricEvidence(a.samples, func(sample metricSample) TargetMetrics { return sample.Actual })
	dataset := majorityDirectionDataset(evidence)
	label := labelSpec(directionLabelNonzeroSign, 0, dataset.excluded)
	stats := statsForDirection(dataset.observations)
	records := make([]modeleval.MetricRecord, 0, 2)
	for _, aggregation := range []modeleval.Aggregation{modeleval.AggregationDayEqual, modeleval.AggregationPooled} {
		record := baseRecord(subject, modeleval.TargetClose, slice, modeleval.MetricDirectionAccuracy,
			modeleval.FamilyDirection, modeleval.RoleDiagnostic, modeleval.SpaceNone, modeleval.UnitRatio,
			aggregation, stats, true, &modeleval.Spec{Label: label})
		value, ok := dataset.aggregate(aggregation, directionAccuracy)
		setRecordValue(&record, value, ok, "no nonzero actual close labels")
		if aggregation == modeleval.AggregationDayEqual {
			record.CI = confidenceInterval(dataset.dailyValues(directionAccuracy))
		}
		records = append(records, record)
	}
	return sortUniqueRecords(records)
}

func selectMetricEvidence(samples []metricSample, selector func(metricSample) TargetMetrics) []metricSample {
	out := make([]metricSample, 0, len(samples))
	for _, sample := range samples {
		selected := selector(sample)
		if !targetMetricsFinite(sample.Actual) || !targetMetricsFinite(selected) {
			continue
		}
		copied := sample
		copied.Predicted = selected
		out = append(out, copied)
	}
	return canonicalMetricSamples(out)
}

func canonicalMetricSamples(samples []metricSample) []metricSample {
	out := append([]metricSample(nil), samples...)
	sort.SliceStable(out, func(left, right int) bool {
		if out[left].Date != out[right].Date {
			return out[left].Date < out[right].Date
		}
		if out[left].Code != out[right].Code {
			return out[left].Code < out[right].Code
		}
		if out[left].InputClose != out[right].InputClose {
			return out[left].InputClose < out[right].InputClose
		}
		leftActual := [3]float64{out[left].Actual.High, out[left].Actual.Low, out[left].Actual.Close}
		rightActual := [3]float64{out[right].Actual.High, out[right].Actual.Low, out[right].Actual.Close}
		for index := range leftActual {
			if leftActual[index] != rightActual[index] {
				return leftActual[index] < rightActual[index]
			}
		}
		leftPredicted := [3]float64{out[left].Predicted.High, out[left].Predicted.Low, out[left].Predicted.Close}
		rightPredicted := [3]float64{out[right].Predicted.High, out[right].Predicted.Low, out[right].Predicted.Close}
		for index := range leftPredicted {
			if leftPredicted[index] != rightPredicted[index] {
				return leftPredicted[index] < rightPredicted[index]
			}
		}
		return false
	})
	return out
}

func pointReturnRecords(subject string, slice []modeleval.SliceDimension, evidence []metricSample, candidate bool) []modeleval.MetricRecord {
	const metricCount = 5
	records := make([]modeleval.MetricRecord, 0, len(Targets)*2*metricCount)
	stats := statsForSamples(evidence)
	for _, target := range Targets {
		for _, aggregation := range []modeleval.Aggregation{modeleval.AggregationDayEqual, modeleval.AggregationPooled} {
			values := pointReturnAggregate(evidence, string(target), func(sample metricSample) TargetMetrics { return sample.Predicted }, aggregation)
			metrics := []struct {
				id      string
				value   float64
				ok      bool
				primary bool
			}{
				{id: modeleval.MetricPointMAE, value: values.mae, ok: values.ok, primary: candidate},
				{id: modeleval.MetricPointRMSE, value: values.rmse, ok: values.ok},
				{id: modeleval.MetricPointMedAE, value: values.medae, ok: values.ok},
				{id: modeleval.MetricPointP90AbsError, value: values.p90Abs, ok: values.ok},
				{id: modeleval.MetricPointBias, value: values.bias, ok: values.ok},
			}
			for _, metric := range metrics {
				role := modeleval.RoleDiagnostic
				if metric.primary {
					role = modeleval.RolePrimary
				}
				record := baseRecord(subject, string(target), slice, metric.id, modeleval.FamilyPoint, role,
					modeleval.SpaceReturn, modeleval.UnitBPS, aggregation, stats, false, nil)
				setRecordValue(&record, metric.value*10000, metric.ok, "no finite return-space errors")
				if metric.id == modeleval.MetricPointMAE && aggregation == modeleval.AggregationDayEqual {
					record.CI = confidenceInterval(scaleValues(values.dailyMAE, 10000))
				}
				records = append(records, record)
			}
		}
	}
	return records
}

func pointPriceRecords(subject string, slice []modeleval.SliceDimension, evidence []metricSample) []modeleval.MetricRecord {
	records := make([]modeleval.MetricRecord, 0, len(Targets)*5)
	stats := statsForSamples(evidence)
	for _, target := range Targets {
		values := pointPriceAggregate(evidence, string(target), func(sample metricSample) TargetMetrics { return sample.Predicted })
		records = append(records,
			priceRecord(subject, slice, string(target), modeleval.MetricPointMAE, modeleval.UnitCNY, values.mae, values.ok, stats, "no finite price-space errors"),
			priceRecord(subject, slice, string(target), modeleval.MetricPointRMSE, modeleval.UnitCNY, values.rmse, values.ok, stats, "no finite price-space errors"),
			priceRecord(subject, slice, string(target), modeleval.MetricPointMAPE, modeleval.UnitRatio, values.mape, values.mapeOK, stats, "actual prices must be positive"),
			priceRecord(subject, slice, string(target), modeleval.MetricPointR2, modeleval.UnitRatio, values.r2, values.r2OK, stats, "actual prices have no variance"),
			priceRecord(subject, slice, string(target), modeleval.MetricPointSMAPE, modeleval.UnitRatio, values.smape, values.smapeOK, stats, "symmetric percentage error is undefined"),
		)
	}
	return records
}

func priceRecord(subject string, slice []modeleval.SliceDimension, target, metricID string, unit modeleval.Unit, value float64, ok bool, stats recordStats, reason string) modeleval.MetricRecord {
	record := baseRecord(subject, target, slice, metricID, modeleval.FamilyPoint, modeleval.RolePaper,
		modeleval.SpacePrice, unit, modeleval.AggregationPooled, stats, metricID == modeleval.MetricPointR2, nil)
	setRecordValue(&record, value, ok, reason)
	return record
}

func pointSkillRecords(subject string, slice []modeleval.SliceDimension, evidence []metricSample) []modeleval.MetricRecord {
	stats := statsForSamples(evidence)
	baselines := []struct {
		name     string
		selector func(metricSample) TargetMetrics
	}{
		{name: modeleval.BaselineLastPrice, selector: func(sample metricSample) TargetMetrics { return sample.Last }},
		{name: modeleval.BaselineRange, selector: func(sample metricSample) TargetMetrics { return sample.Range }},
	}
	records := make([]modeleval.MetricRecord, 0, len(Targets)*len(baselines)*2)
	for _, target := range Targets {
		candidateMAE := pointReturnAggregate(evidence, string(target), func(sample metricSample) TargetMetrics { return sample.Predicted }, modeleval.AggregationDayEqual)
		candidateRMSE := candidateMAE
		for _, baseline := range baselines {
			baselineMAE := pointReturnAggregate(evidence, string(target), baseline.selector, modeleval.AggregationDayEqual)
			baselineRMSE := baselineMAE
			records = append(records,
				skillRecord(subject, slice, string(target), modeleval.MetricPointSkillMAE, modeleval.MetricPointMAE,
					candidateMAE.mae, candidateMAE.ok, baselineMAE.mae, baselineMAE.ok, baseline.name, stats),
				skillRecord(subject, slice, string(target), modeleval.MetricPointSkillRMSE, modeleval.MetricPointRMSE,
					candidateRMSE.rmse, candidateRMSE.ok, baselineRMSE.rmse, baselineRMSE.ok, baseline.name, stats),
			)
		}
	}
	return records
}

func skillRecord(subject string, slice []modeleval.SliceDimension, target, metricID, baselineMetricID string, candidate float64, candidateOK bool, baseline float64, baselineOK bool, baselineName string, stats recordStats) modeleval.MetricRecord {
	record := baseRecord(subject, target, slice, metricID, modeleval.FamilyPoint, modeleval.RoleGuardrail,
		modeleval.SpaceReturn, modeleval.UnitRatio, modeleval.AggregationDayEqual, stats, true,
		&modeleval.Spec{Comparison: &modeleval.ComparisonSpec{Formula: "1 - candidate / baseline", BaselineMetricID: baselineMetricID}})
	record.Baseline = baselineName
	if !candidateOK || !baselineOK {
		setRecordValue(&record, 0, false, "candidate or baseline metric is unavailable")
		return record
	}
	if baseline == 0 {
		setRecordValue(&record, 0, false, "baseline metric is zero")
		return record
	}
	setRecordValue(&record, 1-candidate/baseline, true, "")
	return record
}

func envelopeRecords(subject string, slice []modeleval.SliceDimension, evidence []metricSample) []modeleval.MetricRecord {
	stats := statsForSamples(evidence)
	records := make([]modeleval.MetricRecord, 0, 6)
	for _, aggregation := range []modeleval.Aggregation{modeleval.AggregationDayEqual, modeleval.AggregationPooled} {
		hitRate, hitOK := aggregateEnvelope(evidence, aggregation, func(sample metricSample) float64 {
			if sample.Predicted.High >= sample.Actual.High && sample.Predicted.Low <= sample.Actual.Low {
				return 1
			}
			return 0
		})
		width, widthOK := aggregateEnvelope(evidence, aggregation, func(sample metricSample) float64 {
			return (sample.Predicted.High - sample.Predicted.Low) * 10000
		})
		contradictions, contradictionOK := aggregateEnvelope(evidence, aggregation, func(sample metricSample) float64 {
			if sample.Predicted.High < sample.Predicted.Low {
				return 1
			}
			return 0
		})
		hit := baseRecord(subject, modeleval.TargetAll, slice, modeleval.MetricPointEnvelopeHitRate,
			modeleval.FamilyPoint, modeleval.RoleGuardrail, modeleval.SpaceNone, modeleval.UnitRatio,
			aggregation, stats, true, nil)
		setRecordValue(&hit, hitRate, hitOK, "no finite envelope predictions")
		widthMean := baseRecord(subject, modeleval.TargetAll, slice, modeleval.MetricPointEnvelopeWidthMean,
			modeleval.FamilyPoint, modeleval.RoleDiagnostic, modeleval.SpaceNone, modeleval.UnitBPS,
			aggregation, stats, false, nil)
		setRecordValue(&widthMean, width, widthOK, "no finite envelope predictions")
		contradiction := baseRecord(subject, modeleval.TargetAll, slice, modeleval.MetricPointContradictionRate,
			modeleval.FamilyPoint, modeleval.RoleGuardrail, modeleval.SpaceNone, modeleval.UnitRatio,
			aggregation, stats, false, nil)
		setRecordValue(&contradiction, contradictions, contradictionOK, "no finite envelope predictions")
		records = append(records, hit, widthMean, contradiction)
	}
	return records
}

func directionRecords(subject string, slice []modeleval.SliceDimension, evidence []metricSample, candidate bool) []modeleval.MetricRecord {
	records := make([]modeleval.MetricRecord, 0, 56)
	records = append(records, closeDirectionRecords(subject, slice, evidence, candidate)...)
	records = append(records, conditionalTargetDirectionRecords(subject, slice, evidence, modeleval.TargetHigh)...)
	records = append(records, conditionalTargetDirectionRecords(subject, slice, evidence, modeleval.TargetLow)...)
	records = append(records, directionVariantRecords(subject, slice, evidence, modeleval.TargetClose, directionLabelExact3Class, 0)...)
	records = append(records, directionVariantRecords(subject, slice, evidence, modeleval.TargetClose, directionLabelDeadband5, 5)...)
	records = append(records, directionVariantRecords(subject, slice, evidence, modeleval.TargetClose, directionLabelDeadband10, 10)...)
	records = append(records, quantileDirectionRecords(subject, slice, evidence)...)
	return records
}

func closeDirectionRecords(subject string, slice []modeleval.SliceDimension, evidence []metricSample, candidate bool) []modeleval.MetricRecord {
	dataset := buildDirectionDataset(evidence, modeleval.TargetClose, directionLabelNonzeroSign, 0, true)
	stats := statsForDirection(dataset.observations)
	label := labelSpec(directionLabelNonzeroSign, 0, dataset.excluded)
	records := make([]modeleval.MetricRecord, 0, 13)
	for _, aggregation := range []modeleval.Aggregation{modeleval.AggregationDayEqual, modeleval.AggregationPooled} {
		role := modeleval.RoleDiagnostic
		if candidate && aggregation == modeleval.AggregationDayEqual {
			role = modeleval.RolePrimary
		} else if candidate {
			role = modeleval.RolePaper
		}
		record := baseRecord(subject, modeleval.TargetClose, slice, modeleval.MetricDirectionAccuracy,
			modeleval.FamilyDirection, role, modeleval.SpaceNone, modeleval.UnitRatio, aggregation, stats, true,
			&modeleval.Spec{Label: label})
		value, ok := dataset.aggregate(aggregation, directionAccuracy)
		setRecordValue(&record, value, ok, "no nonzero actual close labels")
		if aggregation == modeleval.AggregationDayEqual {
			record.CI = confidenceInterval(dataset.dailyValues(directionAccuracy))
		}
		records = append(records, record)
	}
	for _, aggregation := range []modeleval.Aggregation{modeleval.AggregationDayEqual, modeleval.AggregationPooled} {
		for _, metric := range []struct {
			id      string
			measure func([]directionObservation) (float64, bool)
		}{
			{id: modeleval.MetricDirectionBalancedAccuracy, measure: balancedAccuracy},
			{id: modeleval.MetricDirectionMCC, measure: matthewsCorrelation},
		} {
			record := baseRecord(subject, modeleval.TargetClose, slice, metric.id,
				modeleval.FamilyDirection, modeleval.RoleDiagnostic, modeleval.SpaceNone, modeleval.UnitRatio,
				aggregation, stats, true, &modeleval.Spec{Label: label})
			value, ok := dataset.aggregate(aggregation, metric.measure)
			setRecordValue(&record, value, ok, "direction diagnostic requires both classes")
			records = append(records, record)
		}
		for _, class := range []string{"up", "down"} {
			for _, metric := range []struct {
				id      string
				measure func([]directionObservation, string) (float64, bool)
			}{
				{id: modeleval.MetricDirectionPrecision, measure: classPrecision},
				{id: modeleval.MetricDirectionRecall, measure: classRecall},
				{id: modeleval.MetricDirectionF1, measure: classF1},
			} {
				classSlice := []modeleval.SliceDimension{{Name: "class", Value: class}}
				record := baseRecord(subject, modeleval.TargetClose, classSlice, metric.id,
					modeleval.FamilyDirection, modeleval.RoleDiagnostic, modeleval.SpaceNone, modeleval.UnitRatio,
					aggregation, stats, true, &modeleval.Spec{Label: label})
				value, ok := dataset.aggregate(aggregation, func(observations []directionObservation) (float64, bool) {
					return metric.measure(observations, class)
				})
				setRecordValue(&record, value, ok, "class has no supported samples")
				records = append(records, record)
			}
		}
	}
	if candidate {
		records = append(records, majorityDeltaRecord(subject, slice, evidence, dataset)...)
	}
	return records
}

func conditionalTargetDirectionRecords(subject string, slice []modeleval.SliceDimension, evidence []metricSample, target string) []modeleval.MetricRecord {
	dataset := buildDirectionDataset(evidence, target, directionLabelNonzeroSign, 0, true)
	stats := statsForDirection(dataset.observations)
	label := labelSpec(directionLabelNonzeroSign, 0, dataset.excluded)
	eligible := highLowDirectionEligible(dataset.observations)
	records := make([]modeleval.MetricRecord, 0, 2)
	for _, aggregation := range []modeleval.Aggregation{modeleval.AggregationDayEqual, modeleval.AggregationPooled} {
		record := baseRecord(subject, target, slice, modeleval.MetricDirectionAccuracy,
			modeleval.FamilyDirection, modeleval.RoleDiagnostic, modeleval.SpaceNone, modeleval.UnitRatio,
			aggregation, stats, true, &modeleval.Spec{Label: label})
		value, ok := dataset.aggregate(aggregation, directionAccuracy)
		if !eligible {
			ok = false
		}
		setRecordValue(&record, value, ok, "high/low direction requires at least 30 up and 30 down samples, each at least 10% of labels")
		if aggregation == modeleval.AggregationDayEqual && eligible {
			record.CI = confidenceInterval(dataset.dailyValues(directionAccuracy))
		}
		records = append(records, record)
	}
	return records
}

func directionVariantRecords(subject string, slice []modeleval.SliceDimension, evidence []metricSample, target, mode string, flatBandBPS float64) []modeleval.MetricRecord {
	dataset := buildDirectionDataset(evidence, target, mode, flatBandBPS, false)
	stats := statsForDirection(dataset.observations)
	label := labelSpec(mode, flatBandBPS, dataset.excluded)
	variantSlice := appendSlice(slice, modeleval.SliceDimension{Name: "label", Value: mode})
	records := make([]modeleval.MetricRecord, 0, 6)
	for _, aggregation := range []modeleval.Aggregation{modeleval.AggregationDayEqual, modeleval.AggregationPooled} {
		for _, metric := range []struct {
			id      string
			measure func([]directionObservation) (float64, bool)
		}{
			{id: modeleval.MetricDirectionAccuracy, measure: directionAccuracy},
			{id: modeleval.MetricDirectionBalancedAccuracy, measure: balancedAccuracy},
			{id: modeleval.MetricDirectionMCC, measure: matthewsCorrelation},
		} {
			record := baseRecord(subject, target, variantSlice, metric.id,
				modeleval.FamilyDirection, modeleval.RoleDiagnostic, modeleval.SpaceNone, modeleval.UnitRatio,
				aggregation, stats, true, &modeleval.Spec{Label: label})
			value, ok := dataset.aggregate(aggregation, metric.measure)
			setRecordValue(&record, value, ok, "direction variant has no supported samples")
			records = append(records, record)
		}
	}
	return records
}

func quantileDirectionRecords(subject string, slice []modeleval.SliceDimension, evidence []metricSample) []modeleval.MetricRecord {
	stats := statsForSamples(evidence)
	label := &modeleval.LabelSpec{Mode: directionLabelQuantile, QuantileLow: 0.3, QuantileHigh: 0.7}
	variantSlice := appendSlice(slice, modeleval.SliceDimension{Name: "label", Value: directionLabelQuantile})
	records := make([]modeleval.MetricRecord, 0, 6)
	for _, aggregation := range []modeleval.Aggregation{modeleval.AggregationDayEqual, modeleval.AggregationPooled} {
		for _, metricID := range []string{
			modeleval.MetricDirectionAccuracy,
			modeleval.MetricDirectionBalancedAccuracy,
			modeleval.MetricDirectionMCC,
		} {
			record := baseRecord(subject, modeleval.TargetClose, variantSlice, metricID,
				modeleval.FamilyDirection, modeleval.RoleDiagnostic, modeleval.SpaceNone, modeleval.UnitRatio,
				aggregation, stats, true, &modeleval.Spec{Label: label})
			record.Applicability = modeleval.ApplicabilityInsufficientData
			record.Reason = "quantile thresholds require training-window fit"
			record.Value = nil
			records = append(records, record)
		}
	}
	return records
}

func majorityDeltaRecord(subject string, slice []modeleval.SliceDimension, evidence []metricSample, candidate directionDataset) []modeleval.MetricRecord {
	stats := statsForDirection(candidate.observations)
	label := labelSpec(directionLabelNonzeroSign, 0, candidate.excluded)
	record := baseRecord(subject, modeleval.TargetClose, slice, modeleval.MetricDirectionAccuracyDelta,
		modeleval.FamilyDirection, modeleval.RoleGuardrail, modeleval.SpaceNone, modeleval.UnitRatio,
		modeleval.AggregationDayEqual, stats, true, &modeleval.Spec{
			Label:      label,
			Comparison: &modeleval.ComparisonSpec{Formula: "candidate - majority", BaselineMetricID: modeleval.MetricDirectionAccuracy},
		})
	record.Baseline = modeleval.BaselineMajority
	candidateValue, candidateOK := candidate.aggregate(modeleval.AggregationDayEqual, directionAccuracy)
	majority := majorityDirectionDataset(evidence)
	majorityValue, majorityOK := majority.aggregate(modeleval.AggregationDayEqual, directionAccuracy)
	if !candidateOK || !majorityOK {
		setRecordValue(&record, 0, false, "candidate or majority direction accuracy is unavailable")
		return []modeleval.MetricRecord{record}
	}
	setRecordValue(&record, candidateValue-majorityValue, true, "")
	return []modeleval.MetricRecord{record}
}

func reservedRecords(subject string, slice []modeleval.SliceDimension, evidence []metricSample) []modeleval.MetricRecord {
	stats := statsForSamples(evidence)
	reserved := []struct {
		id             string
		family         modeleval.Family
		reason         string
		higherIsBetter bool
	}{
		{modeleval.MetricIntervalEmpiricalCoverage, modeleval.FamilyInterval, "point estimate model has no calibrated interval", true},
		{modeleval.MetricIntervalWidthMean, modeleval.FamilyInterval, "point estimate model has no calibrated interval", false},
		{modeleval.MetricIntervalPinballLoss, modeleval.FamilyInterval, "point estimate model has no calibrated interval", false},
		{modeleval.MetricIntervalCRPS, modeleval.FamilyInterval, "point estimate model has no calibrated interval", false},
		{modeleval.MetricRankingIC, modeleval.FamilyRanking, "cross-sectional ranking is not evaluated for the per-bar predictor", true},
		{modeleval.MetricRankingRankIC, modeleval.FamilyRanking, "cross-sectional ranking is not evaluated for the per-bar predictor", true},
		{modeleval.MetricRankingTopKPrecision, modeleval.FamilyRanking, "cross-sectional ranking is not evaluated for the per-bar predictor", true},
		{modeleval.MetricTradingNetReturn, modeleval.FamilyTrading, "no trading simulation exists for this predictor", true},
		{modeleval.MetricTradingSharpe, modeleval.FamilyTrading, "no trading simulation exists for this predictor", true},
		{modeleval.MetricTradingMaxDrawdown, modeleval.FamilyTrading, "no trading simulation exists for this predictor", false},
		{modeleval.MetricTradingTurnover, modeleval.FamilyTrading, "no trading simulation exists for this predictor", false},
	}
	records := make([]modeleval.MetricRecord, 0, len(reserved))
	for _, metric := range reserved {
		record := baseRecord(subject, modeleval.TargetAll, slice, metric.id, metric.family,
			modeleval.RoleDiagnostic, modeleval.SpaceNone, modeleval.UnitNone,
			modeleval.AggregationPooled, stats, metric.higherIsBetter, nil)
		record.Applicability = modeleval.ApplicabilityNotApplicable
		record.Reason = metric.reason
		record.Value = nil
		records = append(records, record)
	}
	return records
}

type recordStats struct {
	samples int
	days    int
	stocks  int
}

func statsForSamples(samples []metricSample) recordStats {
	days := make(map[string]struct{}, len(samples))
	stocks := make(map[string]struct{}, len(samples))
	for _, sample := range samples {
		days[sample.Date] = struct{}{}
		stocks[sample.Code] = struct{}{}
	}
	return recordStats{samples: len(samples), days: len(days), stocks: len(stocks)}
}

func statsForDirection(observations []directionObservation) recordStats {
	days := make(map[string]struct{}, len(observations))
	stocks := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		days[observation.date] = struct{}{}
		stocks[observation.code] = struct{}{}
	}
	return recordStats{samples: len(observations), days: len(days), stocks: len(stocks)}
}

func baseRecord(subject, target string, slice []modeleval.SliceDimension, metricID string, family modeleval.Family, role modeleval.Role, space modeleval.Space, unit modeleval.Unit, aggregation modeleval.Aggregation, stats recordStats, higherIsBetter bool, spec *modeleval.Spec) modeleval.MetricRecord {
	return modeleval.MetricRecord{
		MetricID:       metricID,
		Family:         family,
		Role:           role,
		Subject:        subject,
		Target:         target,
		Space:          space,
		Unit:           unit,
		Aggregation:    aggregation,
		Slice:          append([]modeleval.SliceDimension{}, slice...),
		Applicability:  modeleval.ApplicabilityOK,
		Samples:        stats.samples,
		Days:           stats.days,
		Stocks:         stats.stocks,
		HigherIsBetter: higherIsBetter,
		Spec:           spec,
	}
}

func setRecordValue(record *modeleval.MetricRecord, value float64, ok bool, reason string) {
	if !ok || !finite(value) {
		record.Applicability = modeleval.ApplicabilityInsufficientData
		record.Reason = reason
		record.Value = nil
		return
	}
	record.Applicability = modeleval.ApplicabilityOK
	record.Reason = ""
	record.Value = modeleval.Float(value)
}

func appendSlice(slice []modeleval.SliceDimension, dimensions ...modeleval.SliceDimension) []modeleval.SliceDimension {
	out := make([]modeleval.SliceDimension, 0, len(slice)+len(dimensions))
	out = append(out, slice...)
	out = append(out, dimensions...)
	sort.SliceStable(out, func(left, right int) bool {
		if out[left].Name != out[right].Name {
			return out[left].Name < out[right].Name
		}
		return out[left].Value < out[right].Value
	})
	return out
}

func labelSpec(mode string, flatBandBPS float64, excluded int) *modeleval.LabelSpec {
	return &modeleval.LabelSpec{
		Mode:            mode,
		FlatBandBPS:     flatBandBPS,
		ExcludedSamples: excluded,
	}
}

func targetMetricsFinite(metrics TargetMetrics) bool {
	return finite(metrics.High) && finite(metrics.Low) && finite(metrics.Close)
}

func targetMetricsValue(metrics TargetMetrics, target string) (float64, bool) {
	switch target {
	case modeleval.TargetHigh:
		return metrics.High, true
	case modeleval.TargetLow:
		return metrics.Low, true
	case modeleval.TargetClose:
		return metrics.Close, true
	default:
		return 0, false
	}
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func sortUniqueRecords(records []modeleval.MetricRecord) []modeleval.MetricRecord {
	modeleval.SortRecords(records)
	out := records[:0]
	previousKey := ""
	for _, record := range records {
		key, err := record.CanonicalKey()
		if err != nil {
			key = record.MetricID
		}
		if len(out) > 0 && key == previousKey {
			continue
		}
		out = append(out, record)
		previousKey = key
	}
	return out
}

type pointReturnValues struct {
	mae      float64
	rmse     float64
	medae    float64
	p90Abs   float64
	bias     float64
	dailyMAE []float64
	ok       bool
}

func pointReturnAggregate(evidence []metricSample, target string, predictor func(metricSample) TargetMetrics, aggregation modeleval.Aggregation) pointReturnValues {
	byDay := make(map[string][]float64, len(evidence))
	pooled := make([]float64, 0, len(evidence))
	for _, sample := range evidence {
		actual, actualOK := targetMetricsValue(sample.Actual, target)
		predictedMetrics := predictor(sample)
		predicted, predictedOK := targetMetricsValue(predictedMetrics, target)
		if !actualOK || !predictedOK || !finite(actual) || !finite(predicted) {
			continue
		}
		errorValue := predicted - actual
		byDay[sample.Date] = append(byDay[sample.Date], errorValue)
		pooled = append(pooled, errorValue)
	}
	if len(pooled) == 0 {
		return pointReturnValues{}
	}
	days := sortedDayKeys(byDay)
	daily := make([]pointReturnValues, 0, len(days))
	for _, day := range days {
		daily = append(daily, pointReturnValuesFromErrors(byDay[day]))
	}
	switch aggregation {
	case modeleval.AggregationDayEqual:
		return pointReturnAverage(daily)
	case modeleval.AggregationPooled:
		values := pointReturnValuesFromErrors(pooled)
		values.dailyMAE = make([]float64, 0, len(daily))
		for _, day := range daily {
			values.dailyMAE = append(values.dailyMAE, day.mae)
		}
		return values
	default:
		return pointReturnValues{}
	}
}

func pointReturnValuesFromErrors(errors []float64) pointReturnValues {
	absolute := make([]float64, len(errors))
	squared := make([]float64, len(errors))
	signedSum := 0.0
	for index, value := range errors {
		absolute[index] = math.Abs(value)
		squared[index] = value * value
		signedSum += value
	}
	return pointReturnValues{
		mae:    mean(absolute),
		rmse:   math.Sqrt(mean(squared)),
		medae:  median(absolute),
		p90Abs: percentile90(absolute),
		bias:   signedSum / float64(len(errors)),
		ok:     true,
	}
}

func pointReturnAverage(days []pointReturnValues) pointReturnValues {
	if len(days) == 0 {
		return pointReturnValues{}
	}
	maes := make([]float64, 0, len(days))
	values := pointReturnValues{ok: true}
	for _, day := range days {
		values.mae += day.mae
		values.rmse += day.rmse
		values.medae += day.medae
		values.p90Abs += day.p90Abs
		values.bias += day.bias
		maes = append(maes, day.mae)
	}
	count := float64(len(days))
	values.mae /= count
	values.rmse /= count
	values.medae /= count
	values.p90Abs /= count
	values.bias /= count
	values.dailyMAE = maes
	return values
}

type pointPriceValues struct {
	mae     float64
	rmse    float64
	mape    float64
	r2      float64
	smape   float64
	mapeOK  bool
	r2OK    bool
	smapeOK bool
	ok      bool
}

func pointPriceAggregate(evidence []metricSample, target string, predictor func(metricSample) TargetMetrics) pointPriceValues {
	actualPrices := make([]float64, 0, len(evidence))
	predictedPrices := make([]float64, 0, len(evidence))
	for _, sample := range evidence {
		actual, actualOK := targetMetricsValue(sample.Actual, target)
		predictedMetrics := predictor(sample)
		predicted, predictedOK := targetMetricsValue(predictedMetrics, target)
		if !actualOK || !predictedOK || sample.InputClose <= 0 || !finite(actual) || !finite(predicted) {
			continue
		}
		actualPrice := sample.InputClose * (1 + actual)
		predictedPrice := sample.InputClose * (1 + predicted)
		if actualPrice <= 0 || !finite(actualPrice) || !finite(predictedPrice) {
			continue
		}
		actualPrices = append(actualPrices, actualPrice)
		predictedPrices = append(predictedPrices, predictedPrice)
	}
	if len(actualPrices) == 0 {
		return pointPriceValues{}
	}
	errors := make([]float64, len(actualPrices))
	absolute := make([]float64, len(actualPrices))
	absolutePercentage := make([]float64, 0, len(actualPrices))
	symmetricPercentage := make([]float64, 0, len(actualPrices))
	for index := range actualPrices {
		errors[index] = predictedPrices[index] - actualPrices[index]
		absolute[index] = math.Abs(errors[index])
		absolutePercentage = append(absolutePercentage, absolute[index]/actualPrices[index])
		denominator := math.Abs(predictedPrices[index]) + math.Abs(actualPrices[index])
		if denominator > 0 {
			symmetricPercentage = append(symmetricPercentage, 2*absolute[index]/denominator)
		}
	}
	values := pointPriceValues{
		mae:  mean(absolute),
		rmse: math.Sqrt(meanSquares(errors)),
		ok:   true,
	}
	if len(absolutePercentage) > 0 {
		values.mape = mean(absolutePercentage)
		values.mapeOK = true
	}
	if len(symmetricPercentage) > 0 {
		values.smape = mean(symmetricPercentage)
		values.smapeOK = true
	}
	meanActual := mean(actualPrices)
	sst := 0.0
	sse := 0.0
	for index := range actualPrices {
		difference := actualPrices[index] - meanActual
		sst += difference * difference
		sse += errors[index] * errors[index]
	}
	if sst > 0 {
		values.r2 = 1 - sse/sst
		values.r2OK = true
	}
	return values
}

func meanSquares(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value * value
	}
	return sum / float64(len(values))
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copied := append([]float64(nil), values...)
	sort.Float64s(copied)
	middle := len(copied) / 2
	if len(copied)%2 == 1 {
		return copied[middle]
	}
	return (copied[middle-1] + copied[middle]) / 2
}

func sortedDayKeys(byDay map[string][]float64) []string {
	days := make([]string, 0, len(byDay))
	for day := range byDay {
		days = append(days, day)
	}
	sort.Strings(days)
	return days
}

func scaleValues(values []float64, factor float64) []float64 {
	out := make([]float64, len(values))
	for index, value := range values {
		out[index] = value * factor
	}
	return out
}

func confidenceInterval(dailyValues []float64) *modeleval.ConfidenceInterval {
	interval := &modeleval.ConfidenceInterval{
		Method:        confidenceMethod,
		Confidence:    confidenceLevel,
		Resamples:     confidenceResamples,
		Seed:          confidenceSeed,
		Applicability: modeleval.ApplicabilityInsufficientData,
		Reason:        "fewer than 10 validation days",
	}
	if len(dailyValues) < 10 {
		return interval
	}
	random := rand.New(rand.NewSource(confidenceSeed))
	means := make([]float64, confidenceResamples)
	for resample := 0; resample < confidenceResamples; resample++ {
		sum := 0.0
		for draw := 0; draw < len(dailyValues); draw++ {
			sum += dailyValues[random.Intn(len(dailyValues))]
		}
		means[resample] = sum / float64(len(dailyValues))
	}
	sort.Float64s(means)
	lower := percentileFraction(means, 0.025)
	upper := percentileFraction(means, 0.975)
	interval.Applicability = modeleval.ApplicabilityOK
	interval.Reason = ""
	interval.Lower = modeleval.Float(lower)
	interval.Upper = modeleval.Float(upper)
	return interval
}

func percentileFraction(values []float64, fraction float64) float64 {
	if len(values) == 0 {
		return 0
	}
	index := int(math.Ceil(fraction*float64(len(values)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func aggregateEnvelope(evidence []metricSample, aggregation modeleval.Aggregation, measure func(metricSample) float64) (float64, bool) {
	if len(evidence) == 0 {
		return 0, false
	}
	byDay := make(map[string][]float64, len(evidence))
	pooled := make([]float64, 0, len(evidence))
	for _, sample := range evidence {
		value := measure(sample)
		if !finite(value) {
			continue
		}
		byDay[sample.Date] = append(byDay[sample.Date], value)
		pooled = append(pooled, value)
	}
	if len(pooled) == 0 {
		return 0, false
	}
	if aggregation == modeleval.AggregationPooled {
		return mean(pooled), true
	}
	dailyMeans := make([]float64, 0, len(byDay))
	for _, day := range sortedDayKeys(byDay) {
		dailyMeans = append(dailyMeans, mean(byDay[day]))
	}
	return mean(dailyMeans), true
}

type directionObservation struct {
	date           string
	code           string
	actualClass    string
	predictedClass string
}

type directionDataset struct {
	observations []directionObservation
	excluded     int
}

func buildDirectionDataset(evidence []metricSample, target, mode string, flatBandBPS float64, excludeZero bool) directionDataset {
	dataset := directionDataset{}
	for _, sample := range evidence {
		actual, actualOK := targetMetricsValue(sample.Actual, target)
		predicted, predictedOK := targetMetricsValue(sample.Predicted, target)
		if !actualOK || !predictedOK || !finite(actual) || !finite(predicted) {
			continue
		}
		if excludeZero && actual == 0 {
			dataset.excluded++
			continue
		}
		dataset.observations = append(dataset.observations, directionObservation{
			date:           sample.Date,
			code:           sample.Code,
			actualClass:    directionClass(actual, mode, flatBandBPS),
			predictedClass: directionClass(predicted, mode, flatBandBPS),
		})
	}
	return dataset
}

func majorityDirectionDataset(evidence []metricSample) directionDataset {
	up, down := 0, 0
	for _, sample := range evidence {
		switch {
		case sample.Actual.Close > 0:
			up++
		case sample.Actual.Close < 0:
			down++
		}
	}
	majorityClass := "down"
	if up >= down {
		majorityClass = "up"
	}
	dataset := directionDataset{}
	for _, sample := range evidence {
		if sample.Actual.Close == 0 {
			dataset.excluded++
			continue
		}
		dataset.observations = append(dataset.observations, directionObservation{
			date:           sample.Date,
			code:           sample.Code,
			actualClass:    directionClass(sample.Actual.Close, directionLabelNonzeroSign, 0),
			predictedClass: majorityClass,
		})
	}
	return dataset
}

func directionClass(value float64, mode string, flatBandBPS float64) string {
	if strings.HasPrefix(mode, "deadband_") && math.Abs(value)*10000 <= flatBandBPS {
		return "flat"
	}
	switch {
	case value > 0:
		return "up"
	case value < 0:
		return "down"
	default:
		return "flat"
	}
}

func (dataset directionDataset) aggregate(aggregation modeleval.Aggregation, measure func([]directionObservation) (float64, bool)) (float64, bool) {
	if len(dataset.observations) == 0 {
		return 0, false
	}
	if aggregation == modeleval.AggregationPooled {
		return measure(dataset.observations)
	}
	byDay := dataset.byDay()
	dailyValues := make([]float64, 0, len(byDay))
	for _, day := range sortedObservationDays(byDay) {
		value, ok := measure(byDay[day])
		if ok {
			dailyValues = append(dailyValues, value)
		}
	}
	if len(dailyValues) == 0 {
		return 0, false
	}
	return mean(dailyValues), true
}

func (dataset directionDataset) dailyValues(measure func([]directionObservation) (float64, bool)) []float64 {
	byDay := dataset.byDay()
	values := make([]float64, 0, len(byDay))
	for _, day := range sortedObservationDays(byDay) {
		value, ok := measure(byDay[day])
		if ok {
			values = append(values, value)
		}
	}
	return values
}

func (dataset directionDataset) byDay() map[string][]directionObservation {
	byDay := make(map[string][]directionObservation, len(dataset.observations))
	for _, observation := range dataset.observations {
		byDay[observation.date] = append(byDay[observation.date], observation)
	}
	return byDay
}

func sortedObservationDays(byDay map[string][]directionObservation) []string {
	days := make([]string, 0, len(byDay))
	for day := range byDay {
		days = append(days, day)
	}
	sort.Strings(days)
	return days
}

func directionAccuracy(observations []directionObservation) (float64, bool) {
	if len(observations) == 0 {
		return 0, false
	}
	hits := 0
	for _, observation := range observations {
		if observation.actualClass == observation.predictedClass {
			hits++
		}
	}
	return float64(hits) / float64(len(observations)), true
}

func balancedAccuracy(observations []directionObservation) (float64, bool) {
	actualCounts := make(map[string]int)
	for _, observation := range observations {
		actualCounts[observation.actualClass]++
	}
	if len(actualCounts) == 0 {
		return 0, false
	}
	recalls := make([]float64, 0, len(actualCounts))
	for _, class := range []string{"up", "down", "flat"} {
		count := actualCounts[class]
		if count == 0 {
			continue
		}
		hits := 0
		for _, observation := range observations {
			if observation.actualClass == class && observation.predictedClass == class {
				hits++
			}
		}
		recalls = append(recalls, float64(hits)/float64(count))
	}
	return mean(recalls), true
}

func matthewsCorrelation(observations []directionObservation) (float64, bool) {
	if len(observations) == 0 {
		return 0, false
	}
	classes := []string{"up", "down", "flat"}
	actualCounts := make(map[string]int, len(classes))
	predictedCounts := make(map[string]int, len(classes))
	correct := 0
	for _, observation := range observations {
		actualCounts[observation.actualClass]++
		predictedCounts[observation.predictedClass]++
		if observation.actualClass == observation.predictedClass {
			correct++
		}
	}
	actualSquares := 0
	predictedSquares := 0
	for _, class := range classes {
		actualSquares += actualCounts[class] * actualCounts[class]
		predictedSquares += predictedCounts[class] * predictedCounts[class]
	}
	count := len(observations)
	denominator := math.Sqrt(float64(count*count-actualSquares) * float64(count*count-predictedSquares))
	if denominator == 0 {
		return 0, false
	}
	numerator := float64(count*correct - actualSquares)
	return numerator / denominator, true
}

func classPrecision(observations []directionObservation, class string) (float64, bool) {
	predicted := 0
	correct := 0
	for _, observation := range observations {
		if observation.predictedClass != class {
			continue
		}
		predicted++
		if observation.actualClass == class {
			correct++
		}
	}
	if predicted == 0 {
		return 0, false
	}
	return float64(correct) / float64(predicted), true
}

func classRecall(observations []directionObservation, class string) (float64, bool) {
	actual := 0
	correct := 0
	for _, observation := range observations {
		if observation.actualClass != class {
			continue
		}
		actual++
		if observation.predictedClass == class {
			correct++
		}
	}
	if actual == 0 {
		return 0, false
	}
	return float64(correct) / float64(actual), true
}

func classF1(observations []directionObservation, class string) (float64, bool) {
	precision, precisionOK := classPrecision(observations, class)
	recall, recallOK := classRecall(observations, class)
	if !precisionOK || !recallOK || precision+recall == 0 {
		return 0, false
	}
	return 2 * precision * recall / (precision + recall), true
}

func highLowDirectionEligible(observations []directionObservation) bool {
	total := len(observations)
	if total == 0 {
		return false
	}
	up, down := 0, 0
	for _, observation := range observations {
		switch observation.actualClass {
		case "up":
			up++
		case "down":
			down++
		}
	}
	return up >= 30 && down >= 30 && float64(up)/float64(total) >= 0.10 && float64(down)/float64(total) >= 0.10
}
