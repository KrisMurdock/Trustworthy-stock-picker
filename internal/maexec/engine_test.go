package maexec

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"
)

func fixture(n int) Input {
	in := Input{Version: 3, PriceBasis: "qfq_proxy", Model: "adjusted_return_proxy", Config: DefaultConfig(), Provenance: Provenance{Calendar: "fixture", Rules: "assumed", Assumptions: "synthetic units"}, Bars: map[string]map[string]Bar{}}
	for i := 0; i < n; i++ {
		d := time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		in.Sessions = append(in.Sessions, d)
		in.Bars[d] = map[string]Bar{"sz000001": {Open: 100, High: 101, Low: 99, Close: 100, MA10: 100, Assumed: true, Observation: "observed", Buy: true, Sell: true, Intraday: true, Lower: 1, Upper: 1000, Commission: .0003, Minimum: 5, Transfer: .00001, Stamp: .0005, ADV: 1e9}}
	}
	in.Signals = []Signal{{Code: "sz000001", Date: in.Sessions[0], ATR: 4}}
	return in
}
func TestT1AndOpenExit(t *testing.T) {
	in := fixture(5)
	b := in.Bars[in.Sessions[1]]["sz000001"]
	b.Low = 90
	in.Bars[in.Sessions[1]]["sz000001"] = b
	b = in.Bars[in.Sessions[2]]["sz000001"]
	b.Low = 20
	b.High = 200
	in.Bars[in.Sessions[2]]["sz000001"] = b
	r, e := Simulate(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Trades) != 1 || r.Trades[0].Reason != "stop_t1" || r.Trades[0].ExitDate != in.Sessions[2] || r.Metrics.Ambiguous != 0 {
		t.Fatalf("wrong T+1/open order: %+v", r)
	}
	if r.Trades[0].ExitPrice < 99 {
		t.Fatal("later daily low affected open exit")
	}
}
func TestTimeExitAndBudget(t *testing.T) {
	in := fixture(23)
	r, e := Simulate(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Trades) != 1 || r.Trades[0].ExitDate != in.Sessions[21] || r.Trades[0].Reason != "time" {
		t.Fatalf("D21: %+v", r.Trades)
	}
	in.Config.Capital = 4
	in.Config.Fraction = 1
	r, e = Simulate(context.Background(), in)
	if e != nil || len(r.Orders) != 0 || r.Decisions[0].Status != "cash_or_capacity" {
		t.Fatalf("minimum fee: %+v %v", r, e)
	}
	in.Config.Capital = 100
	r, e = Simulate(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	for _, n := range r.NAV {
		if n.Cash < -1e-8 {
			t.Fatal("negative cash")
		}
	}
	if r.Orders[0].Shares >= 1 {
		t.Fatal("expected fractional synthetic units")
	}
}
func TestStopWinsAmbiguityAndMissingSell(t *testing.T) {
	in := fixture(5)
	b := in.Bars[in.Sessions[2]]["sz000001"]
	b.Low = 90
	b.High = 110
	in.Bars[in.Sessions[2]]["sz000001"] = b
	r, e := Simulate(context.Background(), in)
	if e != nil || r.Trades[0].Reason != "stop" || r.Metrics.Ambiguous != 1 {
		t.Fatalf("%+v %v", r, e)
	}
	b.Sell = false
	b.Intraday = false
	in.Bars[in.Sessions[2]]["sz000001"] = b
	r, e = Simulate(context.Background(), in)
	if e != nil || r.Trades[0].Reason != "stop_blocked" || r.Trades[0].ExitDate != in.Sessions[3] {
		t.Fatalf("blocked: %+v %v", r, e)
	}
}
func TestValidationCancellationAndDeterminism(t *testing.T) {
	in := fixture(5)
	a, e := Simulate(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	b, e := Simulate(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	if string(aj) != string(bj) {
		t.Fatal("nondeterministic")
	}
	if len(a.Open) != 1 || a.Metrics.PF != nil {
		t.Fatal("unclosed positions must remain and undefined PF must be null")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = Simulate(ctx, in); e == nil {
		t.Fatal("ignored cancellation")
	}
	in.Config.Capital = math.NaN()
	if _, e = Simulate(context.Background(), in); e == nil {
		t.Fatal("accepted NaN")
	}
	in = fixture(5)
	in.Version = 1
	if _, e = Simulate(context.Background(), in); e == nil {
		t.Fatal("accepted strict schema")
	}
	in = fixture(5)
	in.Signals = append(in.Signals, in.Signals[0])
	if _, e = Simulate(context.Background(), in); e == nil {
		t.Fatal("accepted duplicate")
	}
}
