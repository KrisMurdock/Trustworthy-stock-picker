package aris

import (
	"context"
	"fmt"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
	"testing"
)

func featureFixture() (maexec.Snapshot, []maexec.Signal) {
	s := maexec.Snapshot{Version: 1, Stocks: map[string][]maexec.Price{}}
	for i := 1; i <= 28; i++ {
		d := fmt.Sprintf("2026-01-%02d", i)
		s.Calendar = append(s.Calendar, d)
		p := int64(100+i) * 1000000
		s.Stocks["sz000001"] = append(s.Stocks["sz000001"], maexec.Price{Adjust: "qfq", Date: d, Open: p, High: p + 1000000, Low: p - 1000000, Close: p, Volume: float64(100 + i), Source: "fixture"})
	}
	return s, []maexec.Signal{{Code: "sz000001", Date: "2026-01-21", ATR: .04}, {Code: "sz000001", Date: "2026-01-28", ATR: .04}}
}
func TestSearchFeatureCausality(t *testing.T) {
	s, signals := featureFixture()
	f, err := SearchFeatures(context.Background(), s, signals)
	if err != nil {
		t.Fatal(err)
	}
	if v := f[signalKey(signals[0])]["return_20"]; v <= 0 {
		t.Fatal(v)
	}
	if v := f[signalKey(signals[0])]["volume_ratio_20"]; v <= 1 {
		t.Fatal(v)
	}
	for i, p := range s.Stocks["sz000001"] {
		if p.Date > signals[0].Date {
			s.Stocks["sz000001"][i].Close *= 9
			s.Stocks["sz000001"][i].Volume *= 99
		}
	}
	changed, err := SearchFeatures(context.Background(), s, signals)
	if err != nil {
		t.Fatal(err)
	}
	if Digest(f[signalKey(signals[0])]) != Digest(changed[signalKey(signals[0])]) {
		t.Fatal("future affected past")
	}
	if Digest(searchPool(f, signals[:1])) != Digest(searchPool(changed, signals[:1])) {
		t.Fatal("validation affected thresholds")
	}
}
func TestSearchPredicateMissingAndUnknown(t *testing.T) {
	if (Predicate{Feature: "unknown", Op: "ge", Threshold: 0}).valid() {
		t.Fatal("unknown feature accepted")
	}
	if matches(map[string]float64{}, []Predicate{{Feature: "return_20", Op: "le", Threshold: 0}}) {
		t.Fatal("missing is not zero")
	}
}
