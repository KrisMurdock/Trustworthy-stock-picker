package bottomstudy

import (
	"context"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestSpeedCurveCountsFailuresAndUnknowns(t *testing.T) {
	trades := []Trade{
		{Code: "a", Split: "training", EntryDate: "2026-01-02", Status: "target", TargetHit: true, TargetDay: 1},
		{Code: "b", Split: "training", EntryDate: "2026-01-02", Status: "target", TargetHit: true, TargetDay: 5},
		{Code: "c", Split: "training", EntryDate: "2026-01-02", Status: "timeout", HoldingDays: 1, NetPct: -9},
		{Code: "d", Split: "training", EntryDate: "2026-01-02", Status: "corporate_action"},
		{Code: "e", Split: "training", Status: "missing_session"},
		{Code: "f", Split: "training", Status: "entry_skipped"},
		{Code: "g", Split: "later_period", EntryDate: "2026-07-02", Status: "target", TargetHit: true, TargetDay: 1},
	}
	s := summarizeSpeed(trades, "training", nil)
	if s.Samples != 5 || s.Curve[0].Targets != 1 || s.Curve[3].Targets != 1 || s.Curve[4].Targets != 2 || s.Curve[19].Rate != .4 || s.Day80 != 0 || s.Day50 != 0 || s.SuccessDays != 3 || s.PenalizedDays != 69.0/5 {
		t.Fatal("biased speed score", s)
	}
	trades[2].HoldingDays = 20
	if summarizeSpeed(trades, "training", nil).PenalizedDays != s.PenalizedDays {
		t.Fatal("earlier losing liquidation improved speed")
	}
	trades[2].TargetHit, trades[2].TargetDay = true, 3
	trades[3].Status, trades[3].TargetHit, trades[3].TargetDay = "target", true, 10
	s = summarizeSpeed(trades, "training", nil)
	if s.Day50 != 5 || s.Day80 != 10 || s.Curve[19].Rate != s.Rate {
		t.Fatal("wrong cumulative quantile", s)
	}
	empty := summarizeSpeed(nil, "training", nil)
	if empty.Samples != 0 || empty.Day80 != 0 || math.IsNaN(empty.PenalizedDays) {
		t.Fatal("invalid empty state", empty)
	}
}

func TestSpeedConfirmationBeforeDailyRanking(t *testing.T) {
	date := "2026-05-12"
	opps := []opportunity{
		{id: "a", code: "sh600001", bars: []market.Bar{{Date: date}}},
		{id: "b", code: "sh600002", bars: []market.Bar{{Date: date}}},
	}
	features := map[string]map[string]ContextFeature{
		"sh600001": {date: {Close: 9, MA5: 10, TrendScore: 90, MACDRising: true, RSIRising: true}},
		"sh600002": {date: {Close: 11, MA5: 10, TrendScore: 20, MACDRising: true, RSIRising: true}},
	}
	l := manualLookup([]string{"sh600001", "sh600002"}, date)
	for _, arm := range []string{"stock_all", "stock_sector_market"} {
		d := selectSpeedSignals(opps, features, l, arm, 1, "price_and_momentum")
		if d[0].Selected || d[0].Reason != "entry_not_confirmed" || !d[1].Selected {
			t.Fatal("confirmation must free rank slot before selection", d)
		}
		d = selectSpeedSignals(opps, features, l, arm, 1, "momentum_turn")
		if !d[0].Selected || d[1].Selected {
			t.Fatal("momentum confirmation incorrectly required MA5", d)
		}
	}
	if speedConfirmed(ContextFeature{Close: 11, MA5: 10, RSIRising: true}, "momentum_turn") || speedConfirmed(ContextFeature{}, "unsupported") {
		t.Fatal("accepted incomplete confirmation")
	}
}

func TestSpeedObjectiveAndSampleFloor(t *testing.T) {
	a := SpeedStats{Day80: 5, PenalizedDays: 8, Curve: make([]SpeedPoint, 20)}
	b := SpeedStats{Day80: 10, PenalizedDays: 7, Curve: make([]SpeedPoint, 20)}
	if !faster(a, b, "a", "b") {
		t.Fatal("earliest 80% was not primary")
	}
	a.Day80, b.Day80 = 0, 0
	if faster(a, b, "a", "b") {
		t.Fatal("failure-aware waiting score ignored")
	}
	if enough(TradeStats{Entries: 29, Dates: 15, Stocks: 15}) || enough(TradeStats{Entries: 40, Dates: 9, Stocks: 15}) || enough(TradeStats{Entries: 40, Dates: 15, Stocks: 9}) {
		t.Fatal("small cohort promoted")
	}
}

func TestSpeedBatchControlsUseCommonActualEntries(t *testing.T) {
	pairs := []SpeedPair{}
	for batches := 1; batches <= 3; batches++ {
		p := TradePolicy{Rule: "any", Batches: batches, Horizon: 20, Target: 3}
		trades := []Trade{
			{ID: "common", Code: "a", Split: "training", EntryDate: "2026-05-04", Status: "target", TargetHit: true, TargetDay: batches},
			{ID: "expensive", Code: "b", Split: "training", EntryDate: "2026-05-04", Status: "target", TargetHit: true, TargetDay: 1},
			{ID: "unknown", Code: "c", Split: "later_period", EntryDate: "2026-07-04", Status: "corporate_action"},
		}
		if batches == 3 {
			trades[1].EntryDate, trades[1].Status, trades[1].TargetHit = "", "entry_skipped", false
		}
		pairs = append(pairs, SpeedPair{ID: trialID(p), Policy: p, Confirmation: "none", Stock: SpeedArm{Trades: trades}})
	}
	controls := speedBatchControls(pairs, []SpeedWinner{{Target: 3, Arm: "stock_all", ID: pairs[0].ID}})
	if len(controls) != 3 {
		t.Fatal("missing batch controls")
	}
	for i, c := range controls {
		if c.Training.Samples != 1 || c.Training.PenalizedDays != float64(i+1) || c.Later.Samples != 1 || c.Later.Unresolved != 1 || c.Later.PenalizedDays != 21 {
			t.Fatal("batch controls changed cohorts or dropped entered unknown", c)
		}
	}
}

func TestSpeedGridCausalityAndNoStop(t *testing.T) {
	s := gridFixture(t, false)
	c := contextFixture(s)
	o := GridOptions{Options{"2026-01-01", s.AsOf}, "2026-06-30"}
	a, err := OptimizeSpeed(context.Background(), s, c, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Pairs) != 240 || len(a.Selections) != 80 || len(a.Winners) == 0 {
		t.Fatal("incomplete experiment", len(a.Pairs), len(a.Selections), a.Winners)
	}
	changed := gridFixture(t, true)
	future := contextFixture(s)
	future.StockSnapshotID = changed.ID
	for i := range future.Indices {
		for j, b := range future.Indices[i].Bars {
			if b.Date > o.TrainThrough {
				b.Open, b.Close, b.High, b.Low = b.Open*1.2, b.Close*1.2, b.High*1.2, b.Low*1.2
				future.Indices[i].Bars[j] = b
			}
		}
	}
	b, err := OptimizeSpeed(context.Background(), changed, future, o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Winners, b.Winners) {
		t.Fatal("later prices changed training winners")
	}
	for i, p := range a.Pairs {
		if p.Policy.Stop != 0 || p.Policy.Horizon != 20 || !reflect.DeepEqual(p.Stock.Training, b.Pairs[i].Stock.Training) || !reflect.DeepEqual(p.Combined.Training, b.Pairs[i].Combined.Training) {
			t.Fatal("no-stop or training isolation contract broken", p.ID)
		}
		for _, arm := range []SpeedArm{p.Stock, p.Combined} {
			for _, stats := range []SpeedStats{arm.Training, arm.Later} {
				if stats.Curve[19].Rate != stats.Rate {
					t.Fatal("CDF differs from complete path", p.ID)
				}
				for j := 1; j < 20; j++ {
					if stats.Curve[j].Rate < stats.Curve[j-1].Rate {
						t.Fatal("non-monotone cumulative curve", p.ID)
					}
				}
			}
			for _, tr := range arm.Trades {
				if tr.StopTriggered || tr.Status == "stop" || tr.Status == "stop_gap" {
					t.Fatal("stop reintroduced", tr)
				}
			}
		}
	}
	for i, sel := range a.Selections {
		// Later signal counts may change, shifting subsequent stocks' slice
		// offsets. Compare the full training cohort by immutable episode ID.
		before, after := map[string]SelectionDecision{}, map[string]SelectionDecision{}
		for _, d := range sel.Decisions {
			if d.Split == "training" {
				before[d.ID] = d
			}
		}
		for _, d := range b.Selections[i].Decisions {
			if d.Split == "training" {
				after[d.ID] = d
			}
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatal("future changed initial confirmation", sel.Rule, sel.Arm, sel.Confirmation)
		}
	}
	c.Memberships = nil
	empty, err := OptimizeSpeed(context.Background(), s, c, o)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range empty.Pairs {
		if p.Combined.Training.Samples != 0 || !reflect.DeepEqual(p.Stock.Training.TradeStats, a.Pairs[i].Stock.Training.TradeStats) {
			t.Fatal("missing context erased A or invented B")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OptimizeSpeed(ctx, s, c, o); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := OptimizeSpeed(ctx, s, c, o); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("timeout lost", err)
	}
}
