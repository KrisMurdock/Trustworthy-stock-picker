package fourlightstudy

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func TestSameDateComparisonUsesEqualDateWeightsAndExcludesUnknown(t *testing.T) {
	row := func(date, group string, known, success int) GroupStats {
		return GroupStats{Date: date, Name: group, Wave: &strategy.UpwardWaveSummary{Model: strategy.UpwardWaveModel, Windows: []strategy.UpwardWaveStats{{Days: 5, Known: known, Success: success}}}}
	}
	dates := []string{"2026-01-05", "2026-01-06", "2026-01-07", "2026-01-08"}
	rows := []GroupStats{
		row(dates[0], "baseline", 1000, 500), row(dates[0], "first_all_red", 100, 100),
		row(dates[1], "baseline", 100, 50), row(dates[1], "first_all_red", 1, 0),
		row(dates[2], "baseline", 0, 0), row(dates[2], "first_all_red", 1, 1),
		row(dates[3], "baseline", 100, 50), row(dates[3], "first_all_red", 0, 0),
	}
	comparisons, err := compareGroups(context.Background(), dates, rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, comparison := range comparisons {
		if comparison.Group == "first_all_red" && comparison.Days == 5 {
			if comparison.PairedDates != 2 || comparison.UnpairedDates != 2 || comparison.MeanDifference == nil || *comparison.MeanDifference != 0 || comparison.Lower95 != nil || comparison.IntervalStatus != "insufficient_date_blocks" {
				t.Fatalf("comparison wrongly weighted or included unknown: %+v", comparison)
			}
		} else if comparison.PairedDates != 0 || comparison.MeanDifference != nil || comparison.Lower95 != nil || comparison.Upper95 != nil || comparison.IntervalStatus != "no_paired_dates" {
			t.Fatalf("empty comparison treated as zero: %+v", comparison)
		}
	}
}

func TestMovingBlockIntervalDeterministicAndPreservesUnknownDates(t *testing.T) {
	values := make([]*float64, 40)
	for i := range values {
		if i%4 != 0 {
			x := float64(i)/40 - .5
			values[i] = &x
		}
	}
	lower, upper, n, err := movingBlockInterval(context.Background(), values, 10, 2000, 64)
	if err != nil || lower == nil || upper == nil || *lower >= *upper || n != 2000 {
		t.Fatalf("interval %v %v n=%d err=%v", lower, upper, n, err)
	}
	a, b, count, err := movingBlockInterval(context.Background(), values, 10, 2000, 64)
	if err != nil || !reflect.DeepEqual(lower, a) || !reflect.DeepEqual(upper, b) || count != n {
		t.Fatal("same seed must produce same interval")
	}
	compact := []*float64{}
	for _, x := range values {
		if x != nil {
			compact = append(compact, x)
		}
	}
	a, b, _, _ = movingBlockInterval(context.Background(), compact, 10, 2000, 64)
	if *a == *lower && *b == *upper {
		t.Fatal("unknown dates were silently removed from trading-date blocks")
	}
}

func TestMovingBlockIntervalUnknownAndShortSamplesRemainNull(t *testing.T) {
	for _, values := range [][]*float64{nil, make([]*float64, 40), make([]*float64, 19)} {
		lower, upper, n, err := movingBlockInterval(context.Background(), values, 10, 2000, 64)
		if err != nil || lower != nil || upper != nil || n != 0 {
			t.Fatalf("unknown sample produced interval: %v %v %d %v", lower, upper, n, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := movingBlockInterval(ctx, make([]*float64, 40), 10, 2000, 64); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not propagated", err)
	}
}
