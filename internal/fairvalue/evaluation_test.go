package fairvalue

import (
	"math"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fundamentals"
)

func evaluationEstimate(t *testing.T) EstimateV3 {
	t.Helper()
	estimate := CalculateV3(config.Stock{Code: "sh600519", Sector: "白酒"}, 10, "2026-11-01", normalizedFixture(t))
	if err := estimate.Validate(); err != nil {
		t.Fatal(err)
	}
	return estimate
}

func TestEvaluateBandCalibrationComputesCoverageAndDirection(t *testing.T) {
	series := normalizedSeriesFixture(t)
	estimate := evaluationEstimate(t)
	observations := []ForwardObservation{
		{Code: "sh600519", Sector: "白酒", AsOf: "2026-11-01", FutureDate: "2026-12-01", CurrentPrice: 10, FuturePrice: 12, Estimate: estimate, Series: series},
		{Code: "sh600519", Sector: "白酒", AsOf: "2026-11-01", FutureDate: "2026-12-02", CurrentPrice: 10, FuturePrice: 8, Estimate: estimate, Series: series},
		{Code: "sh600519", Sector: "白酒", AsOf: "2026-11-01", FutureDate: "2026-12-03", CurrentPrice: 10, FuturePrice: 11, Estimate: estimate, Series: series},
	}
	result := EvaluateBandCalibration(observations, 3)
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if result.Valid != 3 || result.Invalid != 0 || result.InsufficientData {
		t.Fatalf("unexpected evaluation counts: %+v", result)
	}
	if math.Abs(result.Overall.CoveragePct-(200.0/3)) > 1e-9 || result.Overall.Covered != 2 {
		t.Fatalf("unexpected coverage: %+v", result.Overall)
	}
	if math.Abs(result.Overall.DirectionHitPct-(200.0/3)) > 1e-9 {
		t.Fatalf("unexpected direction hit rate: %+v", result.Overall)
	}
	if result.Overall.MeanWidthPct <= 0 || result.Overall.MeanAbsGapPct <= 0 || result.Overall.PinballLossPct <= 0 {
		t.Fatalf("missing evaluation metrics: %+v", result.Overall)
	}
	if len(result.ByYear) != 1 || result.ByYear[0].Group != "2026" || len(result.BySector) != 1 || result.BySector[0].Group != "白酒" {
		t.Fatalf("missing cohort output: year=%+v sector=%+v", result.ByYear, result.BySector)
	}
}

func TestEvaluateBandCalibrationUsesEstimateSectorSnapshot(t *testing.T) {
	series := normalizedSeriesFixture(t)
	estimate := evaluationEstimate(t)
	observation := ForwardObservation{
		Code: "sh600519", AsOf: "2026-11-01", FutureDate: "2026-12-01",
		CurrentPrice: 10, FuturePrice: 11, Estimate: estimate, Series: series,
	}
	result := EvaluateBandCalibration([]ForwardObservation{observation}, 1)
	if result.Valid != 1 || result.Invalid != 0 || len(result.BySector) != 1 || result.BySector[0].Group != estimate.Sector {
		t.Fatalf("calibration did not use the estimate sector snapshot: %+v", result)
	}

	conflicting := observation
	conflicting.Sector = "银行"
	result = EvaluateBandCalibration([]ForwardObservation{conflicting}, 1)
	if result.Valid != 0 || result.Invalid != 1 || len(result.Problems) != 1 ||
		result.Problems[0] != "sample=0:sector_snapshot_mismatch" {
		t.Fatalf("conflicting sector input was accepted: %+v", result)
	}
}

func TestEvaluateBandCalibrationRejectsLeakageAndMismatch(t *testing.T) {
	series := normalizedSeriesFixture(t)
	estimate := evaluationEstimate(t)
	badFuture := ForwardObservation{Code: "sh600519", AsOf: "2026-11-01", FutureDate: "2026-11-01", CurrentPrice: 10, FuturePrice: 11, Estimate: estimate, Series: series}
	badIdentity := ForwardObservation{Code: "sz000001", AsOf: "2026-11-01", FutureDate: "2026-12-01", CurrentPrice: 10, FuturePrice: 11, Estimate: estimate, Series: series}
	badPrice := ForwardObservation{Code: "sh600519", AsOf: "2026-11-01", FutureDate: "2026-12-01", CurrentPrice: 0, FuturePrice: 11, Estimate: estimate, Series: series}
	badEstimatePrice := ForwardObservation{Code: "sh600519", AsOf: "2026-11-01", FutureDate: "2026-12-01", CurrentPrice: 12, FuturePrice: 11, Estimate: estimate, Series: series}
	spoofedSeries := series
	spoofedSeries.Periods = append([]fundamentals.Period(nil), series.Periods...)
	spoofedSeries.Periods[0].NetProfit = float64Ptr(999)
	badDigest := ForwardObservation{Code: "sh600519", AsOf: "2026-11-01", FutureDate: "2026-12-01", CurrentPrice: 10, FuturePrice: 11, Estimate: estimate, Series: spoofedSeries}
	result := EvaluateBandCalibration([]ForwardObservation{badFuture, badIdentity, badPrice, badEstimatePrice, badDigest}, 2)
	if result.Valid != 0 || result.Invalid != 5 || !result.Overall.InsufficientData || len(result.Problems) != 5 {
		t.Fatalf("invalid observations were accepted: %+v", result)
	}
	if !result.InsufficientData {
		t.Fatal("small sample was not marked insufficient")
	}
}

func TestEvaluateBandCalibrationRejectsVendorBackfill(t *testing.T) {
	fetched := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
	series := normalizedSeriesFixture(t)
	for index := range series.Periods {
		series.Periods[index].FetchedAt = fetched
	}
	series.FetchedAt = fetched
	normalized := fundamentals.NormalizeWithMode(series, "2026-11-01", fundamentals.VisibilityVendorBackfill)
	estimate := CalculateV3(config.Stock{Code: "sh600519"}, 10, "2026-11-01", normalized)
	if err := estimate.Validate(); err != nil {
		t.Fatalf("vendor backfill estimate should remain a valid research record: %v", err)
	}
	observation := ForwardObservation{
		Code: "sh600519", AsOf: "2026-11-01", FutureDate: "2026-12-01",
		CurrentPrice: 10, FuturePrice: 11, Estimate: estimate, Series: series,
	}
	result := EvaluateBandCalibration([]ForwardObservation{observation}, 1)
	if result.Valid != 0 || result.Invalid != 1 || len(result.Problems) != 1 || result.Problems[0] != "sample=0:non_observed_visibility" {
		t.Fatalf("vendor backfill entered calibration: %+v", result)
	}

	spoofed := estimate
	spoofed.VisibilityMode = fundamentals.VisibilityObserved
	if err := spoofed.Validate(); err == nil {
		t.Fatal("vendor backfill evidence was accepted under an observed label")
	}
}

func TestEvaluateBandCalibrationRejectsDuplicateForwardObservation(t *testing.T) {
	series := normalizedSeriesFixture(t)
	estimate := evaluationEstimate(t)
	observation := ForwardObservation{
		Code: "sh600519", Sector: "白酒", AsOf: "2026-11-01", FutureDate: "2026-12-01",
		CurrentPrice: 10, FuturePrice: 11, Estimate: estimate, Series: series,
	}
	result := EvaluateBandCalibration([]ForwardObservation{observation, observation}, 1)
	if result.Valid != 1 || result.Invalid != 1 || len(result.Problems) != 1 ||
		result.Problems[0] != "sample=1:duplicate_forward_observation" {
		t.Fatalf("duplicate forward observation was accepted: %+v", result)
	}
}

func TestBandEvaluationValidateRejectsBrokenCounts(t *testing.T) {
	if err := (BandEvaluation{NominalCoveragePct: 60, MinSample: 1, Observations: 2, Valid: 1, Invalid: 0}).Validate(); err == nil {
		t.Fatal("inconsistent counts were accepted")
	}
	if err := (BandEvaluation{NominalCoveragePct: 0, MinSample: 1}).Validate(); err == nil {
		t.Fatal("invalid coverage contract was accepted")
	}
	if err := (BandEvaluation{NominalCoveragePct: 60, MinSample: 1, Observations: 0, Valid: 0, Invalid: 0}).Validate(); err != nil {
		t.Fatalf("empty evaluation should be valid: %v", err)
	}
}
