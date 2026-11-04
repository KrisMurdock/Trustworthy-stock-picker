package maexec

import (
	"context"
	"fmt"
	"testing"
)

func TestPrepareFrozenPrices(t *testing.T) {
	s := Snapshot{Version: 1, Stocks: map[string][]Price{}}
	for i := 1; i <= 28; i++ {
		d := fmt.Sprintf("2026-01-%02d", i)
		s.Calendar = append(s.Calendar, d)
		if i == 23 {
			continue
		}
		for _, adjust := range []string{"none", "qfq"} {
			s.Stocks["sz000001"] = append(s.Stocks["sz000001"], Price{Adjust: adjust, Date: d, Open: 10000000, High: 10100000, Low: 9900000, Close: 10000000, Volume: 100000, Source: "fixture"})
		}
	}
	in, a, e := Prepare(context.Background(), s, s.Calendar[20:], []Signal{{"sz000001", s.Calendar[20], 4}}, DefaultConfig(), "board")
	if e != nil {
		t.Fatal(e)
	}
	missing := in.Bars[s.Calendar[22]]["sz000001"]
	if missing.Close != 10 || missing.Buy || missing.Sell || missing.Intraday || a["carried_missing"] != 1 {
		t.Fatalf("missing: %+v %v", missing, a)
	}
	if in.Bars[s.Calendar[20]]["sz000001"].ADV != 1e6 {
		t.Fatal("capacity must use previous twenty dates")
	}
	for i, p := range s.Stocks["sz000001"] {
		if p.Adjust == "qfq" && p.Date == s.Calendar[24] {
			s.Stocks["sz000001"][i].High = 12000000
		}
	}
	in, _, e = Prepare(context.Background(), s, s.Calendar[20:], []Signal{{"sz000001", s.Calendar[20], 4}}, DefaultConfig(), "board")
	if e != nil {
		t.Fatal(e)
	}
	b := in.Bars[s.Calendar[24]]["sz000001"]
	if !b.Buy || !b.Sell || b.Intraday {
		t.Fatal("future daily extrema must not veto opening fill")
	}
	if in.Bars[s.Calendar[22]]["sz000001"] != missing {
		t.Fatal("future change affected carried price")
	}
}
