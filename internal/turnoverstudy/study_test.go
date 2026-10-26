package turnoverstudy

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/turnover"
)

func TestRunRejectsUnknownFeatureSet(t *testing.T) {
	if _, err := Run(context.Background(), Data{}, Options{FeatureSet: "unknown"}); err == nil {
		t.Fatal("unknown feature set was accepted")
	}
}

func TestCandidateFeatureSets(t *testing.T) {
	cases := []struct {
		set  string
		want []string
	}{
		{FeatureSetFull, turnover.FeatureNames},
		{FeatureSetTurnover, turnover.TurnoverFeatureNames},
		{FeatureSetAmount, turnover.AmountFeatureNames},
		{FeatureSetAmountLevel, turnover.AmountLevelFeatureNames},
		{FeatureSetAmountDynamics, turnover.AmountDynamicsFeatureNames},
		{FeatureSetAmountSize, append(append([]string(nil), turnover.AmountFeatureNames...), turnover.FloatMcapFeatureNames...)},
	}
	for _, tt := range cases {
		got := CandidateFeatureNamesFor(tt.set)
		want := append(append([]string(nil), BaselineFeatureNames...), tt.want...)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s names = %v, want %v", tt.set, got, want)
		}
		indexes := additionalFeatureIndexes(tt.set)
		if len(indexes) != len(tt.want) {
			t.Fatalf("%s indexes = %v", tt.set, indexes)
		}
		for i, index := range indexes {
			if turnover.FeatureNames[index] != tt.want[i] {
				t.Fatalf("%s index %d maps to %s, want %s", tt.set, i, turnover.FeatureNames[index], tt.want[i])
			}
		}
	}
	if _, err := normalizeFeatureSet("unknown"); err == nil {
		t.Fatal("unknown feature set was accepted")
	}
	if got, err := normalizeFeatureSet(""); err != nil || got != FeatureSetFull {
		t.Fatalf("empty feature set normalized to %q, %v", got, err)
	}
}

func TestShuffleTurnoverBlockPreservesBaselineAndBlockDistribution(t *testing.T) {
	rows := []rawRow{
		{values: []float64{1, 10, 100}},
		{values: []float64{2, 20, 200}},
		{values: []float64{3, 30, 300}},
	}
	baseline := make([][]float64, len(rows))
	for i := range rows {
		baseline[i] = append([]float64(nil), rows[i].values[:1]...)
	}
	shuffleTurnoverBlock(rows, 1, rand.New(rand.NewSource(7)))
	for i := range rows {
		if rows[i].values[0] != baseline[i][0] {
			t.Fatalf("baseline changed at row %d: %v", i, rows[i].values)
		}
	}
	got := make([]string, len(rows))
	for i := range rows {
		got[i] = fmt.Sprint(rows[i].values[1:])
	}
	sort.Strings(got)
	want := []string{"[10 100]", "[20 200]", "[30 300]"}
	sort.Strings(want)
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("turnover block changed distribution: got %q want %q", got, want)
		}
	}
}

func TestBootstrapDailyConstantDifference(t *testing.T) {
	base := []float64{1, -2, 3}
	candidate := []float64{2, -1, 4}
	got := bootstrapDaily(base, candidate, 1000, 42, 2)
	if math.Abs(got.MeanNetDiff-1) > 1e-12 || math.Abs(got.Lower95Pct-1) > 1e-12 || math.Abs(got.Upper95Pct-1) > 1e-12 {
		t.Fatalf("unexpected bootstrap %+v", got)
	}
	if got.PositiveRate != 100 {
		t.Fatalf("positive rate %v", got.PositiveRate)
	}
	again := bootstrapDaily(base, candidate, 1000, 42, 2)
	if got != again {
		t.Fatal("bootstrap is not deterministic for the same seed")
	}
}

func TestCohortsCompoundAndDrawdown(t *testing.T) {
	daily := []float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	count, compounded, drawdown := cohorts(daily, 5)
	if count != 2 {
		t.Fatalf("cohort count %d", count)
	}
	if math.Abs(compounded-2.01) > 1e-9 {
		t.Fatalf("compounded %v", compounded)
	}
	if math.Abs(drawdown) > 1e-12 {
		t.Fatalf("drawdown %v", drawdown)
	}
}
