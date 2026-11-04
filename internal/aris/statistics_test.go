package aris

import (
	"context"
	"fmt"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
)

func TestBlockInferencePreservesDatesAndRejectsSparseSamples(t *testing.T) {
	cfg := Inference{BlockSessions: 2, MinBlocks: 4, Samples: 200, Confidence: .95, Seed: 42}
	base := maexec.Result{Config: maexec.DefaultConfig()}
	candidate := base
	for i := 0; i < 8; i++ {
		day := fmt.Sprintf("2026-01-%02d", i+1)
		base.NAV = append(base.NAV, maexec.Equity{Date: day, Equity: 200000})
		candidate.NAV = append(candidate.NAV, maexec.Equity{Date: day, Equity: 200000 + float64(i+1)*100})
		candidate.Trades = append(candidate.Trades, maexec.Trade{SignalDate: day, PNL: 10})
	}
	a, e := infer(context.Background(), candidate, base, cfg)
	if e != nil || a.ExpectancyLower <= 0 || a.ExcessLower <= 0 || a.Blocks != 4 {
		t.Fatalf("%+v %v", a, e)
	}
	again, e := infer(context.Background(), candidate, base, cfg)
	if e != nil || Digest(a) != Digest(again) {
		t.Fatal("nondeterminism")
	}
	candidate.Trades = []maexec.Trade{{SignalDate: candidate.NAV[0].Date, PNL: 10}}
	if _, e = infer(context.Background(), candidate, base, cfg); e == nil {
		t.Fatal("sparse evidence accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = infer(ctx, base, base, cfg); e == nil {
		t.Fatal("ignored cancellation")
	}
}
