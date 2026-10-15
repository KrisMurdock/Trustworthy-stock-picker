package strategy

import (
	"context"
	"encoding/json"
	"testing"
)

func TestDevelopmentReservesFinalDatesAndPurgesBoundary(t *testing.T) {
	c, h, r := researchFixture()
	ctx := context.Background()
	baseline, err := AnalyzeDevelopment(ctx, c, h, r, h[0].AsOf, nil)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.HoldoutFrom == "" || baseline.Research.Holdout.SignalDates != 0 || baseline.Research.ValidationPassed || len(baseline.Trades) == 0 {
		t.Fatalf("invalid development split: %+v", baseline)
	}
	for _, trade := range baseline.Trades {
		if trade.Date >= baseline.HoldoutFrom || trade.Outcome.ExitDate >= baseline.HoldoutFrom {
			t.Fatal("reserved outcomes leaked")
		}
	}
	before, _ := json.Marshal(baseline)
	for i, b := range h[0].Bars {
		if b.Date >= baseline.HoldoutFrom {
			h[0].Bars[i].Open *= 2
			h[0].Bars[i].High *= 2
			h[0].Bars[i].Low *= 2
			h[0].Bars[i].Close *= 2
			r[0].Bars[i] = h[0].Bars[i]
		}
	}
	after, err := AnalyzeDevelopment(ctx, c, h, r, h[0].AsOf, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(after)
	if string(before) != string(encoded) {
		t.Fatal("changing reserved prices changed development evidence")
	}
}
func TestDevelopmentFilterChangesEntriesNotBaseline(t *testing.T) {
	c, h, r := researchFixture()
	a, err := AnalyzeDevelopment(context.Background(), c, h, r, h[0].AsOf, nil)
	if err != nil {
		t.Fatal(err)
	}
	threshold := 0.0
	b, err := AnalyzeDevelopment(context.Background(), c, h, r, h[0].AsOf, &threshold)
	if err != nil {
		t.Fatal(err)
	}
	if a.Research.Development.TopRanked.Samples == 0 || b.Research.Development.TopRanked.Samples != 0 {
		t.Fatal("filter did not change entries")
	}
	x, _ := json.Marshal(a.Research.Development.PoolBaseline)
	y, _ := json.Marshal(b.Research.Development.PoolBaseline)
	if string(x) != string(y) {
		t.Fatal("filter contaminated baseline")
	}
	if len(b.Trades) == 0 || b.Trades[0].Selected {
		t.Fatal("rejected candidate evidence missing")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = AnalyzeDevelopment(canceled, c, h, r, h[0].AsOf, nil); err != context.Canceled {
		t.Fatal("cancellation ignored")
	}
	c.Strategy.HoldoutDays = 120
	if _, err = AnalyzeDevelopment(context.Background(), c, h, r, h[0].AsOf, nil); err == nil {
		t.Fatal("insufficient data accepted")
	}
}
