package bottomstudy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"stock-watcher/internal/config"
	"stock-watcher/internal/experiment"
	"stock-watcher/internal/market"
	"testing"
)

func gridFixture(t *testing.T, perturbLater bool) experiment.Snapshot {
	t.Helper()
	c := config.Defaults()
	c.Calendar.Holidays = nil
	c.Calendar.ValidFrom, c.Calendar.ValidThrough = "2026-01-01", "2026-12-31"
	a, raw := []market.History{}, []market.History{}
	for stock := 1; stock <= 18; stock++ {
		h := fixture(220)
		h.Code, h.Name = fmt.Sprintf("sz%06d", stock), fmt.Sprintf("样本%d", stock)
		for i := range h.Bars {
			price := 12 + 2*math.Sin(float64(i+stock)/4)
			if perturbLater && h.Bars[i].Date > "2026-06-30" {
				price += float64(i-120) * .02
			}
			open := price
			if i > 0 {
				open = h.Bars[i-1].Close
			}
			h.Bars[i].Open, h.Bars[i].Close = open, price
			h.Bars[i].High, h.Bars[i].Low = math.Max(open, price)+.2, math.Min(open, price)-.2
		}
		r := h
		r.Adjust, r.DataField = "none", "day"
		a, raw = append(a, h), append(raw, r)
		c.Stocks = append(c.Stocks, config.Stock{Code: h.Code, Name: h.Name})
	}
	s, err := experiment.NewSnapshot(c, a[0].AsOf, a, raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGridBoundaryCohortsAndCancellation(t *testing.T) {
	s := gridFixture(t, false)
	o := GridOptions{Options{"2026-01-01", s.AsOf}, "2026-06-30"}
	data, omissions, err := prepareGrid(context.Background(), s, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(data["any"]) == 0 || omissions["any:crosses_training_boundary"] == 0 {
		t.Fatal("fixture does not exercise cohorts and purge")
	}
	for _, os := range data {
		previousEnd := map[string]string{}
		for _, x := range os {
			if len(x.bars) != 22 || x.bars[0].Date < previousEnd[x.code] {
				t.Fatal("overlapping/short cohort", x.id)
			}
			previousEnd[x.code] = x.bars[21].Date
			if x.split == "training" && x.bars[21].Date > o.TrainThrough || x.split == "later_period" && x.bars[0].Date <= o.TrainThrough {
				t.Fatal("leaked outcome across split", x.id)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Optimize(ctx, s, o); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	o.TrainThrough = ""
	if _, err := Optimize(context.Background(), s, o); err == nil {
		t.Fatal("accepted missing train-through")
	}
}

func TestOptimizationSelectionsDoNotDependOnLaterOutcomes(t *testing.T) {
	s := gridFixture(t, false)
	o := GridOptions{Options{"2026-01-01", s.AsOf}, "2026-06-30"}
	a, err := Optimize(context.Background(), s, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Best) != 2 || len(a.Trials) != 600 {
		for _, trial := range a.Trials {
			if trial.Policy.Batches == 1 && trial.Policy.Horizon == 20 && trial.Policy.Target == 3 {
				t.Logf("%s: %+v", trial.ID, trial.Train)
			}
		}
		t.Fatalf("missing grid coverage: best=%d trials=%d", len(a.Best), len(a.Trials))
	}
	b, err := Optimize(context.Background(), gridFixture(t, true), o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Best, b.Best) || !reflect.DeepEqual(a.ByBatches, b.ByBatches) {
		t.Fatal("later-period prices changed selected policy or stop")
	}
	for i := range a.Trials {
		if !reflect.DeepEqual(a.Trials[i].Train, b.Trials[i].Train) {
			t.Fatal("training depends on future prices", a.Trials[i].ID)
		}
	}
	for _, e := range a.Evidence {
		for _, tr := range e.Trades {
			if tr.TargetHit && (tr.TargetDay < 1 || tr.NetPct < 2.5-1e-9) {
				t.Fatal("invalid target accounting", tr)
			}
			if tr.Invested > s.Parameters.TradeCash+1e-8 {
				t.Fatal("budget exceeded", tr)
			}
			for _, f := range tr.Fills {
				if f.Side == "sell" && f.Date == tr.EntryDate {
					t.Fatal("T+0 exit", tr)
				}
			}
		}
	}
}
