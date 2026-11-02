package strategy

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func wavePath(closes ...float64) []UpwardWaveDay {
	previous := 100.0
	days := []UpwardWaveDay{}
	for i, close := range closes {
		days = append(days, UpwardWaveDay{Day: i + 1, Close: close, ChangePct: (close/previous - 1) * 100, CloseNetPct: close - 100, LowNetPct: math.Min(previous, close) - 100})
		previous = close
	}
	return days
}

func TestUpwardWaveShapes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		closes     []float64
		status     string
		start, end int
	}{
		{"smooth", []float64{101, 102, 103, 104, 105}, "success", 1, 3},
		{"one_small_pullback", []float64{101, 102, 101.8, 103, 104}, "success", 1, 4},
		{"starts_day_two", []float64{99.8, 101, 102, 103, 104}, "success", 2, 4},
		{"late_day_three_start", []float64{99.8, 99.7, 101, 102, 103}, "failed", 0, 0},
		{"one_spike", []float64{105, 104, 103, 102, 101}, "failed", 0, 0},
		{"deep_drop_recovery", []float64{96, 97, 99, 102, 104}, "failed", 0, 0},
		{"flat_not_up", []float64{101, 101, 101, 102, 103}, "failed", 0, 0},
		{"one_up_two_flat", []float64{101, 101, 102, 102, 103}, "failed", 0, 0},
		{"all_down", []float64{99, 98, 97, 96, 95}, "failed", 0, 0},
		{"wave_then_giveback", []float64{101, 102, 103, 100, 98}, "success", 1, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := evaluateUpwardWindow(wavePath(tc.closes...), 100, 2)
			if w.Status != tc.status {
				t.Fatalf("got %+v", w)
			}
			if tc.start != 0 && (w.Segment.StartDay != tc.start || w.Segment.EndDay != tc.end) {
				t.Fatalf("segment %+v", w.Segment)
			}
			if tc.name == "all_down" && w.Full.Efficiency != -1 {
				t.Fatalf("downward efficiency %+v", w.Full)
			}
			if tc.name == "wave_then_giveback" && (*w.GivebackPct != 5 || w.Full.EndNetPct != -2) {
				t.Fatal("giveback hidden", w)
			}
		})
	}
}

func TestUpwardWaveRiskAndAmplitudeBoundaries(t *testing.T) {
	days := wavePath(100.5, 101, 102, 101, 100)
	days[0].LowNetPct = -1
	if w := evaluateUpwardWindow(days, 100, 2); w.Status != "success" {
		t.Fatal("inclusive amplitude/loss boundary", w)
	}
	days[0].LowNetPct = -1.00001
	if w := evaluateUpwardWindow(days, 100, 2); w.Status != "failed" {
		t.Fatal("intraday drawdown not enforced", w)
	}
	days = wavePath(101, 100, 101, 102, 103)
	if w := evaluateUpwardWindow(days, 100, 2); w.Status != "success" {
		t.Fatal("shallow close drawdown", w)
	}
	days = wavePath(102, 100, 101, 103, 104)
	if w := evaluateUpwardWindow(days, 100, 2); w.Status != "failed" {
		t.Fatal("close drawdown not enforced", w)
	}
	days = wavePath(100.3, 100.6, 100.9, 101.2, 101.5)
	if w := evaluateUpwardWindow(days, 100, 2); w.Status != "failed" {
		t.Fatal("amplitude missing", w)
	}
}

func upwardFixture(t *testing.T) (config.Config, ExperimentTrade, Dataset) {
	t.Helper()
	c := config.Defaults()
	c.Calendar.ValidFrom, c.Calendar.ValidThrough = "2026-01-01", "2026-12-31"
	cal := calendar.New(c)
	signal := market.Bar{Date: "2026-03-02", Open: 100, High: 101, Low: 99, Close: 100, Volume: 10000}
	bars := []market.Bar{signal}
	for i := 0; i < 10; i++ {
		date, err := cal.Next(bars[len(bars)-1].Date)
		if err != nil {
			t.Fatal(err)
		}
		price := 101 + float64(i)
		bars = append(bars, market.Bar{Date: date, Open: price - 1, High: price + .1, Low: price - 1, Close: price, Volume: 10000})
	}
	p, err := NewPlan(c.Strategy, "sz000001", 100)
	if err != nil {
		t.Fatal(err)
	}
	trade := ExperimentTrade{Code: "sz000001", Date: signal.Date, Selected: true, Features: Features{ATR14Pct: 2}, Outcome: Outcome{Status: "target", EntryDate: bars[1].Date, ExitDate: bars[2].Date, EntryDay: entryDayValue(c.Strategy, p, 101, "", "opening_price")}}
	d := Dataset{Raw: market.History{Code: trade.Code, Bars: bars}, Adjusted: market.History{Code: trade.Code, Bars: append([]market.Bar{}, bars...)}}
	return c, trade, d
}

func TestUpwardWaveFullWindowAndBoundaryIsolation(t *testing.T) {
	c, tr, d := upwardFixture(t)
	cal := calendar.New(c)
	w := labelUpwardWave(c.Strategy, cal, tr, d, "", d.Raw.Bars[10].Date)
	if len(w.Days) != 10 || w.Windows[0].Status != "success" || w.Windows[1].Status != "success" {
		t.Fatalf("legacy exit truncated path %+v", w)
	}
	if w.Days[0].CloseNetPct >= 1 || w.Windows[0].Segment.EndDay != 3 {
		t.Fatal("costs or endpoint ignored", w)
	}
	boundary := d.Raw.Bars[6].Date
	w = labelUpwardWave(c.Strategy, cal, tr, d, boundary, d.Raw.Bars[10].Date)
	if len(w.Days) != 5 || w.Windows[0].Status != "success" || w.Windows[1].Reason != "reserved_boundary" {
		t.Fatal("independent windows", w)
	}
	before, _ := json.Marshal(w)
	for i := 6; i < len(d.Raw.Bars); i++ {
		d.Raw.Bars[i].Close *= 2
		d.Adjusted.Bars[i].Close *= 3
	}
	after, _ := json.Marshal(labelUpwardWave(c.Strategy, cal, tr, d, boundary, d.Raw.Bars[10].Date))
	if string(before) != string(after) {
		t.Fatal("reserved prices leaked")
	}
	_, tr, d = upwardFixture(t)
	for i := 6; i < len(d.Adjusted.Bars); i++ {
		b := &d.Adjusted.Bars[i]
		b.Open *= 1.01
		b.High *= 1.01
		b.Low *= 1.01
		b.Close *= 1.01
	}
	w = labelUpwardWave(c.Strategy, cal, tr, d, "", d.Raw.Bars[10].Date)
	if w.Windows[0].Status != "success" || w.Windows[1].Reason != "corporate_action" {
		t.Fatal("D6 action contaminated D1-D5", w)
	}
}

func TestUpwardWaveUnknownAndDenominators(t *testing.T) {
	c, tr, d := upwardFixture(t)
	cal := calendar.New(c)
	for _, tc := range []struct {
		name           string
		change         func(*ExperimentTrade, *Dataset)
		status, reason string
	}{
		{"missing", func(_ *ExperimentTrade, d *Dataset) { d.Raw.Bars = d.Raw.Bars[:4] }, "unknown", "session_missing"},
		{"action", func(_ *ExperimentTrade, d *Dataset) {
			d.Adjusted.Bars[3].Open *= 1.01
			d.Adjusted.Bars[3].High *= 1.01
			d.Adjusted.Bars[3].Low *= 1.01
			d.Adjusted.Bars[3].Close *= 1.01
		}, "unknown", "corporate_action"},
		{"suspend", func(_ *ExperimentTrade, d *Dataset) { d.Raw.Bars[2].Volume = 0 }, "unknown", "bar_invalid_or_suspended"},
		{"atr", func(tr *ExperimentTrade, _ *Dataset) { tr.Features.ATR14Pct = 0 }, "unknown", "atr_unknown"},
		{"skipped", func(tr *ExperimentTrade, _ *Dataset) { tr.Outcome = Outcome{Status: "entry_skipped"} }, "not_entered", "entry_skipped"},
		{"entry_missing", func(tr *ExperimentTrade, _ *Dataset) { tr.Outcome = Outcome{Status: "missing_entry_session"} }, "unknown", "entry_evidence_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp, dt := tr, d
			dt.Raw.Bars = append([]market.Bar{}, d.Raw.Bars...)
			dt.Adjusted.Bars = append([]market.Bar{}, d.Adjusted.Bars...)
			tc.change(&cp, &dt)
			w := labelUpwardWave(c.Strategy, cal, cp, dt, "", d.Raw.Bars[10].Date)
			if w.Windows[0].Status != tc.status || w.Windows[0].Reason != tc.reason {
				t.Fatalf("%+v", w)
			}
		})
	}
	tr.UpwardWave = labelUpwardWave(c.Strategy, cal, tr, d, "", d.Raw.Bars[10].Date)
	unknown := tr
	unknown.UpwardWave = labelUpwardWave(c.Strategy, cal, tr, d, d.Raw.Bars[3].Date, d.Raw.Bars[10].Date)
	skipped := tr
	skipped.Outcome = Outcome{Status: "entry_skipped"}
	skipped.UpwardWave = labelUpwardWave(c.Strategy, cal, skipped, d, "", d.Raw.Bars[10].Date)
	missing := tr
	missing.UpwardWave = nil
	s := summarizeUpwardWaves([]ExperimentTrade{tr, unknown, skipped, missing}).Windows[0]
	if s.Selected != 4 || s.Entered != 2 || s.NotEntered != 1 || s.EntryUnknown != 1 || s.Success != 1 || s.Unknown != 1 || *s.Coverage != .5 || *s.SuccessRate != 1 || *s.ConservativeRate != .5 {
		t.Fatalf("%+v", s)
	}
	empty := summarizeUpwardWaves(nil).Windows[0]
	if empty.SuccessRate != nil || empty.Coverage != nil || empty.ConservativeRate != nil || empty.MeanEndNetPct != nil {
		t.Fatal("empty misreported as zero", empty)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := attachUpwardWaves(ctx, c, []Dataset{d}, &DevelopmentResult{Trades: []ExperimentTrade{tr}}, "", d.Raw.Bars[10].Date); err != context.Canceled {
		t.Fatal("cancel ignored", err)
	}
}
