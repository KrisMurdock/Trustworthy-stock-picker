package modeleval

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func testRecord() MetricRecord {
	return MetricRecord{
		MetricID:    MetricPointMAE,
		Family:      FamilyPoint,
		Role:        RolePrimary,
		Subject:     ModelSubject("tree-v1"),
		Target:      TargetClose,
		Space:       SpaceReturn,
		Unit:        UnitBPS,
		Aggregation: AggregationPooled,
		Slice: []SliceDimension{
			{Name: "regime", Value: "bull"},
			{Name: "window", Value: "20"},
		},
		Value:          Float(12.5),
		Applicability:  ApplicabilityOK,
		Samples:        120,
		Days:           12,
		Stocks:         8,
		HigherIsBetter: false,
	}
}

func testConfidenceInterval() *ConfidenceInterval {
	return &ConfidenceInterval{
		Method:        "bootstrap",
		Confidence:    0.95,
		Resamples:     1000,
		Seed:          42,
		Lower:         Float(10.5),
		Upper:         Float(14.5),
		Applicability: ApplicabilityOK,
	}
}

func TestMetricRecordJSONAndRoundTrip(t *testing.T) {
	record := testRecord()
	record.CI = testConfidenceInterval()
	record.Spec = &Spec{
		Label:      &LabelSpec{Mode: "flat_band", FlatBandBPS: 5, QuantileLow: 0.1, QuantileHigh: 0.9, FitFrom: "2026-01-01", FitTo: "2026-06-30", ExcludedSamples: 3},
		Interval:   &IntervalSpec{NominalCoverage: 0.8, Method: "rolling_quantile"},
		Comparison: &ComparisonSpec{Formula: "baseline_mae - model_mae", BaselineMetricID: MetricPointMAE},
	}

	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal valid record: %v", err)
	}
	if !strings.Contains(string(payload), `"value":12.5`) {
		t.Fatalf("ok record lost numeric value: %s", payload)
	}
	var decoded MetricRecord
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal round trip: %v", err)
	}
	if !reflect.DeepEqual(record, decoded) {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v", decoded, record)
	}

	unavailable := record
	unavailable.Applicability = ApplicabilityInsufficientData
	unavailable.Value = nil
	unavailable.Reason = "not enough resolved samples"
	unavailable.CI = &ConfidenceInterval{
		Method:        "bootstrap",
		Confidence:    0.95,
		Resamples:     0,
		Seed:          42,
		Applicability: ApplicabilityInsufficientData,
		Reason:        "not enough resolved samples",
	}
	unavailablePayload, err := json.Marshal(unavailable)
	if err != nil {
		t.Fatalf("marshal unavailable record: %v", err)
	}
	if !strings.Contains(string(unavailablePayload), `"value":null`) {
		t.Fatalf("non-ok value was not encoded as JSON null: %s", unavailablePayload)
	}
	if !strings.Contains(string(unavailablePayload), `"lower":null`) || !strings.Contains(string(unavailablePayload), `"upper":null`) {
		t.Fatalf("non-ok confidence bounds were not encoded as JSON null: %s", unavailablePayload)
	}
	if err := json.Unmarshal(unavailablePayload, &decoded); err != nil {
		t.Fatalf("unmarshal unavailable round trip: %v", err)
	}
	if !reflect.DeepEqual(unavailable, decoded) {
		t.Fatalf("unavailable round trip mismatch:\n got %#v\nwant %#v", decoded, unavailable)
	}
}

func TestMetricRecordValidationFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*MetricRecord)
	}{
		{name: "missing metric id", mutate: func(record *MetricRecord) { record.MetricID = "" }},
		{name: "unknown metric id", mutate: func(record *MetricRecord) { record.MetricID = "point.unknown" }},
		{name: "invalid family", mutate: func(record *MetricRecord) { record.Family = Family("unknown") }},
		{name: "family metric mismatch", mutate: func(record *MetricRecord) { record.Family = FamilyDirection }},
		{name: "invalid role", mutate: func(record *MetricRecord) { record.Role = Role("unknown") }},
		{name: "missing subject", mutate: func(record *MetricRecord) { record.Subject = " " }},
		{name: "missing target", mutate: func(record *MetricRecord) { record.Target = "" }},
		{name: "invalid space", mutate: func(record *MetricRecord) { record.Space = Space("unknown") }},
		{name: "invalid unit", mutate: func(record *MetricRecord) { record.Unit = Unit("unknown") }},
		{name: "invalid aggregation", mutate: func(record *MetricRecord) { record.Aggregation = Aggregation("unknown") }},
		{name: "empty slice name", mutate: func(record *MetricRecord) {
			record.Slice = []SliceDimension{{Name: "", Value: "x"}}
		}},
		{name: "duplicate slice name", mutate: func(record *MetricRecord) {
			record.Slice = []SliceDimension{{Name: "regime", Value: "bull"}, {Name: "regime", Value: "bear"}}
		}},
		{name: "unsorted slice", mutate: func(record *MetricRecord) {
			record.Slice = []SliceDimension{{Name: "window", Value: "20"}, {Name: "regime", Value: "bull"}}
		}},
		{name: "invalid applicability", mutate: func(record *MetricRecord) { record.Applicability = Applicability("unknown") }},
		{name: "ok without value", mutate: func(record *MetricRecord) { record.Value = nil }},
		{name: "non-ok with value", mutate: func(record *MetricRecord) {
			record.Applicability = ApplicabilityNotApplicable
			record.Reason = "target has no labels"
		}},
		{name: "non-ok without reason", mutate: func(record *MetricRecord) {
			record.Applicability = ApplicabilityNotApplicable
			record.Value = nil
		}},
		{name: "negative samples", mutate: func(record *MetricRecord) { record.Samples = -1 }},
		{name: "negative days", mutate: func(record *MetricRecord) { record.Days = -1 }},
		{name: "negative stocks", mutate: func(record *MetricRecord) { record.Stocks = -1 }},
		{name: "ci missing method", mutate: func(record *MetricRecord) {
			record.CI = testConfidenceInterval()
			record.CI.Method = ""
		}},
		{name: "ci invalid confidence", mutate: func(record *MetricRecord) {
			record.CI = testConfidenceInterval()
			record.CI.Confidence = 1.5
		}},
		{name: "ci negative resamples", mutate: func(record *MetricRecord) {
			record.CI = testConfidenceInterval()
			record.CI.Resamples = -1
		}},
		{name: "ci ok missing bound", mutate: func(record *MetricRecord) {
			record.CI = testConfidenceInterval()
			record.CI.Upper = nil
		}},
		{name: "ci non-ok without reason", mutate: func(record *MetricRecord) {
			record.CI = testConfidenceInterval()
			record.CI.Applicability = ApplicabilityInsufficientData
			record.CI.Lower = nil
			record.CI.Upper = nil
		}},
		{name: "ci inverted bounds", mutate: func(record *MetricRecord) {
			record.CI = testConfidenceInterval()
			record.CI.Lower = Float(15)
		}},
		{name: "non-finite value", mutate: func(record *MetricRecord) {
			record.Value = Float(math.Inf(1))
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := testRecord()
			test.mutate(&record)
			if err := record.Validate(); err == nil {
				t.Fatalf("expected validation failure for %#v", record)
			}
		})
	}

	valid := testRecord()
	valid.Value = Float(0)
	reserve := testConfidenceInterval()
	reserve.Resamples = 0
	reserve.Confidence = 1
	valid.CI = reserve
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid record was rejected: %v", err)
	}
}

func TestCanonicalKeyIsIndependentOfSliceOrder(t *testing.T) {
	unsorted := testRecord()
	unsorted.Slice = []SliceDimension{{Name: "window", Value: "20"}, {Name: "regime", Value: "bull"}}
	sorted := testRecord()
	sorted.Slice = []SliceDimension{{Name: "regime", Value: "bull"}, {Name: "window", Value: "20"}}

	unsortedKey, err := unsorted.CanonicalKey()
	if err != nil {
		t.Fatalf("canonical key for unsorted slice: %v", err)
	}
	sortedKey, err := sorted.CanonicalKey()
	if err != nil {
		t.Fatalf("canonical key for sorted slice: %v", err)
	}
	if unsortedKey != sortedKey {
		t.Fatalf("slice order changed canonical key:\n%s\n%s", unsortedKey, sortedKey)
	}

	baseline := sorted
	baseline.Baseline = BaselineLastPrice
	baselineKey, err := baseline.CanonicalKey()
	if err != nil {
		t.Fatalf("canonical key for baseline: %v", err)
	}
	if baselineKey == sortedKey {
		t.Fatal("different baselines produced the same canonical key")
	}

	differentTarget := sorted
	differentTarget.Target = TargetHigh
	targetKey, err := differentTarget.CanonicalKey()
	if err != nil {
		t.Fatalf("canonical key for target: %v", err)
	}
	if targetKey == sortedKey {
		t.Fatal("different targets produced the same canonical key")
	}

	identityOnly := MetricRecord{
		MetricID:    MetricPointMAE,
		Subject:     ModelSubject("tree-v1"),
		Target:      TargetClose,
		Space:       SpaceReturn,
		Aggregation: AggregationPooled,
		Slice:       []SliceDimension{{Name: "window", Value: "20"}, {Name: "regime", Value: "bull"}},
	}
	identityOnlyKey, err := identityOnly.CanonicalKey()
	if err != nil {
		t.Fatalf("canonical key should not require a complete observation: %v", err)
	}
	if identityOnlyKey != sortedKey {
		t.Fatalf("identity-only key differs from full record key:\n%s\n%s", identityOnlyKey, sortedKey)
	}
}

func TestSortRecordsCanonicalizesSlicesAndSortsByKey(t *testing.T) {
	first := testRecord()
	first.Subject = ModelSubject("z-model")
	first.Slice = []SliceDimension{{Name: "window", Value: "20"}, {Name: "regime", Value: "bull"}}

	second := testRecord()
	second.MetricID = MetricPointRMSE
	second.Subject = ModelSubject("a-model")
	second.Slice = []SliceDimension{{Name: "window", Value: "5"}, {Name: "regime", Value: "bear"}}

	third := testRecord()
	third.MetricID = MetricDirectionAccuracy
	third.Family = FamilyDirection
	third.Unit = UnitRatio
	third.Value = Float(0.61)
	third.Subject = BaselineSubject(BaselineMajority)

	records := []MetricRecord{first, second, third}
	SortRecords(records)

	for index, record := range records {
		for dimensionIndex := 1; dimensionIndex < len(record.Slice); dimensionIndex++ {
			previous := record.Slice[dimensionIndex-1]
			current := record.Slice[dimensionIndex]
			if previous.Name > current.Name || (previous.Name == current.Name && previous.Value > current.Value) {
				t.Fatalf("record %d slice was not canonicalized: %#v", index, record.Slice)
			}
		}
	}

	previousKey := ""
	for index, record := range records {
		key, err := record.CanonicalKey()
		if err != nil {
			t.Fatalf("canonical key after sort: %v", err)
		}
		if index > 0 && previousKey > key {
			t.Fatalf("records are not sorted by canonical key:\n%s\n%s", previousKey, key)
		}
		previousKey = key
	}
}

func TestSubjectHelpersAndFindRecord(t *testing.T) {
	if got := ModelSubject("tree-v1"); got != "model:tree-v1" {
		t.Fatalf("ModelSubject() = %q", got)
	}
	if got := BaselineSubject(BaselineLastPrice); got != "baseline:last_price" {
		t.Fatalf("BaselineSubject() = %q", got)
	}
	value := Float(3.25)
	if value == nil || *value != 3.25 {
		t.Fatalf("Float() = %v", value)
	}

	records := []MetricRecord{testRecord()}
	record, found := FindRecord(records, MetricPointMAE, ModelSubject("tree-v1"), TargetClose)
	if !found || record.MetricID != MetricPointMAE {
		t.Fatalf("FindRecord() = %#v, %v", record, found)
	}
	if _, found := FindRecord(records, MetricPointRMSE, ModelSubject("tree-v1"), TargetClose); found {
		t.Fatal("FindRecord matched the wrong metric")
	}
}
