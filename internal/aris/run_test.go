package aris

import (
	"context"
	"encoding/json"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func bundle() Bundle {
	in := maexec.Input{Version: 3, PriceBasis: "qfq_proxy", Model: "adjusted_return_proxy", Provenance: maexec.Provenance{Calendar: "test", Rules: "assumed", Assumptions: "test"}, Config: maexec.DefaultConfig(), Sessions: []string{"2026-01-01", "2026-01-02"}, Bars: map[string]map[string]maexec.Bar{}, Signals: []maexec.Signal{}}
	return Bundle{Request: Request{Version: 1, RunID: "test", Strategy: "ma-proxy"}, HumanContract: "test contract", Contract: Contract{Version: 1, ID: "test", HumanHash: Hash([]byte("test contract")), Build: "test-build", Baseline: "base", Candidates: []string{"candidate"}, InputHashes: map[string]string{"base": Digest(in), "candidate": Digest(in)}, Budget: Budget{Candidates: 1, Attempts: 1, Timeout: "1m", ReviewCalls: 0}, Review: ReviewPolicy{Mode: "off"}}, Inputs: map[string]maexec.Input{"base": in, "candidate": in}}
}
func TestMissingRiskDoesNotRun(t *testing.T) {
	a, e := New(t.TempDir(), "test-build", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	s, e := a.Run(context.Background(), bundle())
	if e != nil || s.Status != "needs_human" || len(s.Attempts) != 0 {
		t.Fatalf("%+v %v", s, e)
	}
	if e = a.Verify("test"); e != nil {
		t.Fatal(e)
	}
	if e = a.Replay(context.Background(), "test"); e != nil {
		t.Fatal(e)
	}
}
func configured() Bundle {
	b := bundle()
	b.Contract.Inference = Inference{BlockSessions: 20, MinBlocks: 4, Samples: 200, Confidence: .95, Seed: 42}
	d, tail := .1, .2
	b.Contract.Constraints = Constraints{Drawdown: &d, TailLoss: &tail, MinTrades: 1, MinDays: 1, MinPF: 1, MinExpectancy: 0, StressSlippage: []float64{.002}}
	return b
}
func TestReplayTamperAndDuplicate(t *testing.T) {
	root := t.TempDir()
	a, e := New(root, "test-build", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b := configured()
	s, e := a.Run(context.Background(), b)
	if e != nil || s.Status != "rejected" || len(s.Attempts) != 2 {
		t.Fatalf("%+v %v", s, e)
	}
	if e = a.Verify("test"); e != nil {
		t.Fatal(e)
	}
	if _, e = a.Run(context.Background(), b); e == nil {
		t.Fatal("reused run")
	}
	path := filepath.Join(root, "runs/test/artifacts/base.json")
	if e = os.WriteFile(path, []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = a.Verify("test"); e == nil {
		t.Fatal("accepted tamper")
	}
}
func TestContractMismatchAndCanceled(t *testing.T) {
	a, e := New(t.TempDir(), "test-build", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b := configured()
	b.Contract.HumanHash = "bad"
	if _, e = a.Run(context.Background(), b); e == nil {
		t.Fatal("accepted mismatch")
	}
	b = configured()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = a.Run(ctx, b); e == nil {
		t.Fatal("ignored cancellation")
	}
	b.Request.RunID = "../escape"
	if _, e = a.Run(context.Background(), b); e == nil {
		t.Fatal("path escape")
	}
}

func TestCandidateCannotChangeComparisonCosts(t *testing.T) {
	b := configured()
	in := b.Inputs["candidate"]
	in.Config.Slippage = .003
	b.Inputs["candidate"] = in
	b.Contract.InputHashes["candidate"] = Digest(in)
	if e := b.Validate("test-build"); e == nil {
		t.Fatal("compared different costs")
	}
}
func TestResumeDoesNotResetDeadlineOrSkipEvidence(t *testing.T) {
	root := t.TempDir()
	a, e := New(root, "test-build", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	s, e := a.Run(context.Background(), configured())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Resume(context.Background(), s.RunID); e == nil {
		t.Fatal("resumed terminal")
	}
	s.Status = "frozen"
	s.Deadline = time.Now().Add(-time.Second)
	raw, _ := json.Marshal(s)
	if e = os.WriteFile(filepath.Join(root, "runs/test/state.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	s, e = a.Resume(context.Background(), s.RunID)
	if e == nil && s.Status != "needs_human" {
		t.Fatal("reset deadline")
	}
}
