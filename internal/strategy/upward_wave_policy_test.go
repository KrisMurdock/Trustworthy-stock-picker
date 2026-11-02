package strategy

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestNoDrawdownWaveKeepsShapeAndAmplitudeRequirements(t *testing.T) {
	for _, tc := range []struct {
		name   string
		closes []float64
		low    float64
		want   string
	}{
		{"intraday_loss", []float64{101, 102, 103, 104, 105}, -8, "success"},
		{"close_drawdown", []float64{102, 100, 101, 103, 104}, 0, "success"},
		{"day_two_recovery", []float64{96, 97, 99, 102, 104}, 0, "success"},
		{"late_start", []float64{99, 98, 101, 102, 103}, 0, "failed"},
		{"insufficient_up_days", []float64{101, 100, 100, 102, 101}, 0, "failed"},
		{"insufficient_density", []float64{101, 101, 102, 102, 103}, 0, "failed"},
		{"insufficient_amplitude", []float64{100.3, 100.6, 100.9, 101.2, 101.5}, 0, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			days := wavePath(tc.closes...)
			if tc.low != 0 {
				days[0].LowNetPct = tc.low
			}
			legacy := evaluateUpwardWindow(days, 100, 2)
			current := evaluateUpwardWindowForModel(days, 100, 2, UpwardWaveNoDrawdownModel)
			if current.Status != tc.want || legacy.Status != "failed" {
				t.Fatalf("status: legacy=%+v current=%+v", legacy, current)
			}
			if !reflect.DeepEqual(legacy.Full, current.Full) || *legacy.GivebackPct != *current.GivebackPct || *legacy.PeakCloseNetPct != *current.PeakCloseNetPct {
				t.Fatal("model selection changed descriptive risk/path measurements")
			}
			if tc.want == "success" && (current.Segment.MaxLossPct <= 1 && current.Segment.CloseDrawdownPct <= 1) {
				t.Fatal("regression fixture no longer exceeds an old risk threshold", current.Segment)
			}
		})
	}
}

func TestNoDrawdownOpeningWavePreservesLegacyAndRiskEvidence(t *testing.T) {
	c, tr, d := openingWaveFixture(t)
	i := len(d.Raw.Bars) - 11
	d.Raw.Bars[i+1].Low = 94
	d.Adjusted.Bars[i+1].Low = 94
	asof := d.Raw.Bars[len(d.Raw.Bars)-1].Date
	legacy, err := NewOpeningWaveEvaluator(c)
	if err != nil {
		t.Fatal(err)
	}
	legacyExplicit, err := NewOpeningWaveEvaluatorForModel(c, UpwardWaveModel)
	if err != nil {
		t.Fatal(err)
	}
	current, err := NewOpeningWaveEvaluatorForModel(c, UpwardWaveNoDrawdownModel)
	if err != nil {
		t.Fatal(err)
	}
	before, err := legacy.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := legacyExplicit.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
	if err != nil || !reflect.DeepEqual(before, explicit) {
		t.Fatal("legacy constructor behavior changed", before, explicit, err)
	}
	after, err := current.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
	if err != nil || before.Model != UpwardWaveModel || before.PricePairReference != nil || before.Windows[0].Status != "failed" || after.Model != UpwardWaveNoDrawdownModel || after.Windows[0].Status != "success" || after.PricePairReference == nil {
		t.Fatalf("versioned outcomes: legacy=%+v current=%+v err=%v", before, after, err)
	}
	if !reflect.DeepEqual(before.Windows[0].Full, after.Windows[0].Full) || after.Windows[0].Segment.MaxLossPct < 6 || after.EntryDate != before.EntryDate || after.EntryPrice != before.EntryPrice {
		t.Fatal("risk metrics or entry changed", before, after)
	}
}

func TestNoDrawdownOpeningWaveValidatesAdditivePairing(t *testing.T) {
	c, tr, d := openingWaveFixture(t)
	for i := range d.Adjusted.Bars {
		b := &d.Adjusted.Bars[i]
		b.Open -= 50
		b.High -= 50
		b.Low -= 50
		b.Close -= 50
	}
	asof := d.Raw.Bars[len(d.Raw.Bars)-1].Date
	old, err := EvaluateOpeningUpwardWave(context.Background(), c, tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
	if err != nil || old.Windows[0].Reason != "corporate_action" {
		t.Fatal("fixture no longer demonstrates legacy ratio false positive", old, err)
	}
	e, err := NewOpeningWaveEvaluatorForModel(c, UpwardWaveNoDrawdownModel)
	if err != nil {
		t.Fatal(err)
	}
	w, err := e.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
	if err != nil || w.Windows[0].Status != "success" || len(w.Days) != 10 || w.PricePairReference == nil {
		t.Fatal("stable additive pairing rejected", w, err)
	}
	i := len(d.Raw.Bars) - 11
	for j := i + 6; j < len(d.Adjusted.Bars); j++ {
		b := &d.Adjusted.Bars[j]
		b.Open += 1
		b.High += 1
		b.Low += 1
		b.Close += 1
	}
	changed, err := e.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
	if err != nil || changed.EntryStatus != "entered" || changed.Windows[0].Status != "success" || changed.Windows[1].Reason != "price_transform_changed" || len(changed.Days) != 5 || !reflect.DeepEqual(w.PricePairReference, changed.PricePairReference) {
		t.Fatal("D6 transformation refitted or contaminated completed five-day window", changed, err)
	}
}

func TestNoDrawdownPairFailuresKeepEventDenominators(t *testing.T) {
	c, tr, d := openingWaveFixture(t)
	i := len(d.Raw.Bars) - 11
	asof := d.Raw.Bars[len(d.Raw.Bars)-1].Date
	e, err := NewOpeningWaveEvaluatorForModel(c, UpwardWaveNoDrawdownModel)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewUpwardWaveAccumulator(UpwardWaveNoDrawdownModel)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, entry, reason string
		change              func(*Dataset)
	}{
		{"signal_volume", "unknown", "pairing_volume_mismatch", func(d *Dataset) { d.Adjusted.Bars[i].Volume *= 2 }},
		{"future_volume", "entered", "pairing_volume_mismatch", func(d *Dataset) { d.Adjusted.Bars[i+1].Volume *= 2 }},
		{"future_price", "entered", "price_transform_changed", func(d *Dataset) {
			d.Adjusted.Bars[i+1].Open += 1
			d.Adjusted.Bars[i+1].High += 1
			d.Adjusted.Bars[i+1].Low += 1
			d.Adjusted.Bars[i+1].Close += 1
		}},
		{"skipped", "not_entered", "entry_skipped", func(d *Dataset) { d.Raw.Bars[i+1].Volume = 0 }},
		{"raw_missing", "unknown", "signal_data_missing", func(d *Dataset) { d.Raw.Bars = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dt := d
			dt.Raw.Bars = append(d.Raw.Bars[:0:0], d.Raw.Bars...)
			dt.Adjusted.Bars = append(d.Adjusted.Bars[:0:0], d.Adjusted.Bars...)
			tc.change(&dt)
			w, err := e.Evaluate(context.Background(), tr.Code, tr.Date, dt.Adjusted.Bars, dt.Raw.Bars, "", asof)
			if err != nil || w.Model != UpwardWaveNoDrawdownModel || w.EntryStatus != tc.entry || w.Windows[0].Reason != tc.reason {
				t.Fatalf("failure evidence: %+v, %v", w, err)
			}
			a.Add(tr.Date, w)
		})
	}
	s := a.Summary().Windows[0]
	if s.Selected != 5 || s.Entered != 2 || s.EntryUnknown != 2 || s.NotEntered != 1 || s.Unknown != 2 || s.Known != 0 || s.Coverage == nil || *s.Coverage != 0 || s.SuccessRate != nil {
		t.Fatal("pair failures removed or reclassified events", s)
	}
}

func TestNoDrawdownPairChecksRespectReservedBoundary(t *testing.T) {
	c, tr, d := openingWaveFixture(t)
	i := len(d.Raw.Bars) - 11
	asof := d.Raw.Bars[len(d.Raw.Bars)-1].Date
	e, err := NewOpeningWaveEvaluatorForModel(c, UpwardWaveNoDrawdownModel)
	if err != nil {
		t.Fatal(err)
	}
	boundary := d.Raw.Bars[i+6].Date
	before, err := e.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, boundary, asof)
	if err != nil || before.Windows[0].Status != "success" || before.Windows[1].Reason != "reserved_boundary" {
		t.Fatal("independent window boundaries", before, err)
	}
	for j := i + 6; j < len(d.Raw.Bars); j++ {
		d.Raw.Bars[j].Open = math.NaN()
		d.Adjusted.Bars[j].Volume *= 2
	}
	after, err := e.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, boundary, asof)
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if err != nil || string(a) != string(b) {
		t.Fatal("reserved data affected reference or path", err)
	}
	entryBoundary := d.Raw.Bars[i+1].Date
	d.Raw.Bars[i+1].Open = math.NaN()
	w, err := e.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, entryBoundary, asof)
	if err != nil || w.Model != UpwardWaveNoDrawdownModel || w.EntryReason != "reserved_boundary" {
		t.Fatal("D1 beyond boundary was read", w, err)
	}
}

func TestUpwardWaveModelsCannotMixInSummary(t *testing.T) {
	c, tr, d := openingWaveFixture(t)
	e, err := NewOpeningWaveEvaluatorForModel(c, UpwardWaveNoDrawdownModel)
	if err != nil {
		t.Fatal(err)
	}
	w, err := e.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", d.Raw.Bars[len(d.Raw.Bars)-1].Date)
	if err != nil {
		t.Fatal(err)
	}
	var legacy UpwardWaveAccumulator
	legacy.Add(tr.Date, w)
	s := legacy.Summary()
	if s.Model != UpwardWaveModel || s.Windows[0].EntryUnknown != 1 || s.Windows[0].UnknownReasons["wave_model_mismatch"] != 1 || s.Windows[0].Known != 0 {
		t.Fatal("legacy accumulator mixed models", s)
	}
	current, err := NewUpwardWaveAccumulator(UpwardWaveNoDrawdownModel)
	if err != nil {
		t.Fatal(err)
	}
	current.Add(tr.Date, w)
	current.Add(tr.Date, unknownOpeningWave("session_missing"))
	s = current.Summary()
	if s.Model != UpwardWaveNoDrawdownModel || s.Windows[0].Selected != 2 || s.Windows[0].Success != 1 || s.Windows[0].EntryUnknown != 1 || s.Windows[0].UnknownReasons["wave_model_mismatch"] != 1 {
		t.Fatal("explicit accumulator mixed models", s)
	}
	for _, model := range []string{"", "upward_wave_unknown"} {
		if _, err := NewOpeningWaveEvaluatorForModel(c, model); err == nil {
			t.Fatal("unknown evaluator model accepted")
		}
		if _, err := NewUpwardWaveAccumulator(model); err == nil {
			t.Fatal("unknown accumulator model accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Evaluate(ctx, tr.Code, tr.Date, nil, nil, "", tr.Date); err != context.Canceled {
		t.Fatal("new model ignored cancellation", err)
	}
}

func TestNoDrawdownShareDefectsDoNotInvalidatePriceOnlyATR(t *testing.T) {
	c, tr, original := openingWaveFixture(t)
	i := len(original.Raw.Bars) - 11
	asof := original.Raw.Bars[len(original.Raw.Bars)-1].Date
	e, err := NewOpeningWaveEvaluatorForModel(c, UpwardWaveNoDrawdownModel)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		adjusted bool
		offset   int
	}{
		{"old_qfq_share", true, -6},
		{"old_raw_share", false, -6},
		{"recent_qfq_share", true, -4},
		{"recent_raw_share", false, -4},
		{"signal_qfq_share", true, 0},
		{"signal_raw_share", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := original
			d.Raw.Bars = append(original.Raw.Bars[:0:0], original.Raw.Bars...)
			d.Adjusted.Bars = append(original.Adjusted.Bars[:0:0], original.Adjusted.Bars...)
			bars := d.Raw.Bars
			if tc.adjusted {
				bars = d.Adjusted.Bars
			}
			bars[i+tc.offset].Volume = math.NaN()
			w, err := e.Evaluate(context.Background(), tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
			if err != nil {
				t.Fatal(err)
			}
			if tc.offset == -6 {
				if w.EntryStatus != "entered" || w.Windows[0].Status != "success" {
					t.Fatal("old share defect invalidated independent ATR/pairing evidence", w)
				}
			} else if w.EntryStatus != "unknown" || w.EntryReason != "pairing_bar_invalid" {
				t.Fatal("recent share defect lost its pairing reason", w)
			}
			if tc.name == "old_qfq_share" {
				legacy, err := EvaluateOpeningUpwardWave(context.Background(), c, tr.Code, tr.Date, d.Adjusted.Bars, d.Raw.Bars, "", asof)
				if err != nil || legacy.EntryReason != "atr_unknown" {
					t.Fatal("legacy validation semantics changed", legacy, err)
				}
			}
		})
	}
}
