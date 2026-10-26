package turnover

import (
	"math"
	"reflect"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestFeatureGroupsPreserveFrozenOrder(t *testing.T) {
	if len(TurnoverFeatureNames) != 6 || len(AmountLevelFeatureNames) != 1 || len(AmountDynamicsFeatureNames) != 2 || len(AmountFeatureNames) != 3 || len(FloatMcapFeatureNames) != 1 {
		t.Fatalf("unexpected group sizes: turnover=%d amount_level=%d amount_dynamics=%d amount=%d float=%d", len(TurnoverFeatureNames), len(AmountLevelFeatureNames), len(AmountDynamicsFeatureNames), len(AmountFeatureNames), len(FloatMcapFeatureNames))
	}
	want := []string{
		"log_turnover_rate", "log_turnover_ratio_5", "log_turnover_ratio_20", "log_turnover_5_20", "turnover_z_20", "turnover_cv_20",
		"log_amount", "log_amount_ratio_5", "amount_z_20", "log_float_mcap",
	}
	got := joinFeatureNames(TurnoverFeatureNames, AmountFeatureNames, FloatMcapFeatureNames)
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(FeatureNames, want) {
		t.Fatalf("feature order changed: got %v want %v", got, want)
	}
}

func TestExtractUsesOnlySignalHistory(t *testing.T) {
	metrics := make([]market.DailyMetric, 0, 24)
	for day := 1; day <= 24; day++ {
		metrics = append(metrics, market.DailyMetric{
			Date:            "2026-01-" + twoDigits(day),
			TurnoverRatePct: float64(day),
			AmountCNY:       float64(day) * 1e8,
		})
	}
	got, err := Extract(metrics, "2026-01-24")
	if err != nil {
		t.Fatal(err)
	}
	wantTurnoverShort := (20.0 + 21 + 22 + 23 + 24) / 5
	wantTurnoverLong := 0.0
	for day := 5; day <= 24; day++ {
		wantTurnoverLong += float64(day)
	}
	wantTurnoverLong /= 20
	wantAmountShort := (20.0 + 21 + 22 + 23 + 24) * 1e8 / 5
	wantAmountMean := 0.0
	for day := 5; day <= 24; day++ {
		wantAmountMean += float64(day) * 1e8
	}
	wantAmountMean /= 20
	wantAmountVariance := 0.0
	for day := 5; day <= 24; day++ {
		delta := float64(day)*1e8 - wantAmountMean
		wantAmountVariance += delta * delta
	}
	wantAmountDeviation := math.Sqrt(wantAmountVariance / 20)
	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"turnover rate", got.LogTurnoverRate, math.Log1p(24)},
		{"turnover ratio 5", got.LogTurnoverRatio5, math.Log(24 / wantTurnoverShort)},
		{"turnover ratio 20", got.LogTurnoverRatio20, math.Log(24 / wantTurnoverLong)},
		{"turnover 5/20", got.LogTurnover5To20, math.Log(wantTurnoverShort / wantTurnoverLong)},
		{"amount", got.LogAmount, math.Log1p(24e8)},
		{"amount ratio 5", got.LogAmountRatio5, math.Log(24e8 / wantAmountShort)},
		{"amount z 20", got.AmountZ20, (24e8 - wantAmountMean) / wantAmountDeviation},
		{"float market value", got.LogFloatMcap, math.Log(24e8 * 100 / 24)},
	}
	for _, check := range checks {
		if math.Abs(check.got-check.want) > 1e-12 {
			t.Fatalf("%s: got %.12f want %.12f", check.name, check.got, check.want)
		}
	}

	changed := append([]market.DailyMetric(nil), metrics...)
	changed = append(changed, market.DailyMetric{Date: "2026-01-25", TurnoverRatePct: 999, AmountCNY: 999e8})
	again, err := Extract(changed, "2026-01-24")
	if err != nil {
		t.Fatal(err)
	}
	if again != got {
		t.Fatal("future observation changed a point-in-time feature")
	}
	if len(got.Values()) != len(FeatureNames) {
		t.Fatalf("feature values %d != names %d", len(got.Values()), len(FeatureNames))
	}
}

func TestExtractRejectsStaleAndInsufficientHistory(t *testing.T) {
	metrics := make([]market.DailyMetric, 25)
	for i := range metrics {
		metrics[i] = market.DailyMetric{Date: "2026-02-" + twoDigits(i+1), TurnoverRatePct: 1, AmountCNY: 1e8}
	}
	if _, err := Extract(metrics, "2026-02-26"); err == nil {
		t.Fatal("accepted a date without a same-day metric")
	}
	if _, err := Extract(metrics[:19], "2026-02-19"); err == nil {
		t.Fatal("accepted insufficient history")
	}
}

func twoDigits(value int) string {
	if value < 10 {
		return "0" + string(rune('0'+value))
	}
	return string([]byte{byte('0' + value/10), byte('0' + value%10)})
}
