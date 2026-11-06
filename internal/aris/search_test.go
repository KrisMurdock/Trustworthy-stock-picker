package aris

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func searchFixture(t *testing.T) (Bundle, SearchPlan) {
	t.Helper()
	snap := maexec.Snapshot{Version: 1, Stocks: map[string][]maexec.Price{}}
	signals := []maexec.Signal{}
	for i := 0; i < 180; i++ {
		d := time.Date(2020, 1, 1+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		snap.Calendar = append(snap.Calendar, d)
		for _, code := range []string{"sz000001", "sz000002"} {
			p := int64(10000000 + (i%8)*10000)
			if code == "sz000002" {
				p = 11000000 - int64(i%8)*20000
			}
			for _, adjust := range []string{"qfq", "none"} {
				snap.Stocks[code] = append(snap.Stocks[code], maexec.Price{Adjust: adjust, Date: d, Open: p, High: p + 50000, Low: p - 50000, Close: p, Volume: float64(1000000 + (i%7)*100000), Source: "fixture"})
			}
			if i >= 65 && i%8 == 1 {
				signals = append(signals, maexec.Signal{Code: code, Date: d, ATR: .04})
			}
		}
	}
	raw, _ := json.Marshal(snap)
	var z bytes.Buffer
	w := gzip.NewWriter(&z)
	w.Write(raw)
	w.Close()
	b := configured()
	b.Request.Version = 2
	b.Shared = &SharedInput{Version: 1, Snapshot: z.Bytes(), SHA256: Hash(z.Bytes()), Sessions: snap.Calendar, Config: maexec.DefaultConfig(), Signals: map[string][]maexec.Signal{"base": signals, "candidate": signals}, LimitMode: "historical_board"}
	b.Shared.Config.Holding = 3
	raw, _ = json.Marshal(b)
	if err := Decode(raw, &b); err != nil {
		t.Fatal(err)
	}
	for id, in := range b.Inputs {
		b.Contract.InputHashes[id] = Digest(in)
	}
	tail := 50000.0
	b.Contract.Constraints.TailLoss = &tail
	return b, SearchPlan{Version: 1, ID: "search-test", TrainThrough: "2020-04-20", ValidationFrom: "2020-05-01", ValidationThrough: "2020-06-28", MaxTrials: 3, Patience: 3, MaxTerms: 2, Timeout: "1m"}
}
func TestAutonomousSearchReplayAndIsolation(t *testing.T) {
	b, p := searchFixture(t)
	a, err := New(t.TempDir(), "test-build", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	state, err := a.Search(context.Background(), b, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Trials) == 0 || len(state.Trials) > p.MaxTrials {
		t.Fatal("budget or search missing", state)
	}
	if err = a.VerifySearch(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Search(context.Background(), b, p); err == nil {
		t.Fatal("overwrote campaign")
	}
	// Change ONLY validation execution prices; proposals and training scores stay fixed.
	for id, in := range b.Inputs {
		for d, stocks := range in.Bars {
			if d >= p.ValidationFrom {
				for code, bar := range stocks {
					bar.High *= 2
					stocks[code] = bar
				}
			}
		}
		b.Inputs[id] = in
		b.Contract.InputHashes[id] = Digest(in)
	}
	// Shared bundles must not be mutated independently of their snapshot. Test the
	// scheduler directly using the already-frozen feature table and training slice.
	campaign, _, err := a.loadSearch(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := Digest(campaign.Pool)
	for date, stocks := range campaign.Bundle.Inputs[campaign.Bundle.Contract.Baseline].Bars {
		if date >= p.ValidationFrom {
			for code, bar := range stocks {
				bar.High *= 2
				stocks[code] = bar
			}
		}
	}
	initial := state
	initial.Status = "searching"
	initial.StopReason = ""
	initial.Trials = []SearchTrial{}
	initial.Best = -1
	initial.Stale = 0
	initial.BaselineHash = ""
	initial.ValidationHash = ""
	initial.ValidationFailures = nil
	changed, err := a.executeSearch(context.Background(), t.TempDir(), campaign, initial)
	if err != nil {
		t.Fatal(err)
	}
	if Digest(changed.Trials) != Digest(state.Trials) || changed.Best != state.Best || changed.StopReason != state.StopReason {
		t.Fatal("validation outcomes leaked into adaptive search")
	}
	if changed.ValidationHash == state.ValidationHash {
		t.Fatal("fixture did not perturb validation outcomes")
	}
	train, _, err := searchWindows(b.Inputs[b.Contract.Baseline], p)
	if err != nil {
		t.Fatal(err)
	}
	if Digest(searchPool(campaign.Features, train.Signals)) != before {
		t.Fatal("validation changed hypotheses")
	}
	file := filepath.Join(a.root, "campaigns", p.ID, "trial-000.json")
	if err = os.WriteFile(file, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = a.VerifySearch(context.Background(), p.ID); err == nil {
		t.Fatal("tamper accepted")
	}
}
func TestSearchRejectsMissingConfigAndOverlap(t *testing.T) {
	b, p := searchFixture(t)
	a, _ := New(t.TempDir(), "test-build", nil)
	defer a.Close()
	p.ValidationFrom = p.TrainThrough
	if _, err := a.Search(context.Background(), b, p); err == nil {
		t.Fatal("overlap accepted")
	}
	_, p = searchFixture(t)
	b.Contract.Constraints.Drawdown = nil
	if _, err := a.Search(context.Background(), b, p); err == nil {
		t.Fatal("missing risk accepted")
	}
}

func TestSearchResumeRetainsReservationsAndLock(t *testing.T) {
	b, p := searchFixture(t)
	a, _ := New(t.TempDir(), "test-build", nil)
	defer a.Close()
	state, err := a.Search(context.Background(), b, p)
	if err != nil {
		t.Fatal(err)
	}
	campaign, _, err := a.loadSearch(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := a.searchDir(p.ID)
	// Simulate a crash immediately after the first proposal was reserved.
	state.Status = "paused"
	state.Trials = state.Trials[:1]
	state.Trials[0].Status = "started"
	state.Trials[0].SHA256 = ""
	state.Trials[0].Score = 0
	state.Trials[0].Eligible = false
	state.Trials[0].Failures = nil
	state.Best = -1
	state.Stale = 0
	state.StopReason = ""
	state.ValidationHash = ""
	state.ValidationFailures = nil
	if err = a.saveSearch(dir, state); err != nil {
		t.Fatal(err)
	}
	unlock, err := lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.ResumeSearch(context.Background(), p.ID); err == nil {
		t.Fatal("live lock ignored")
	}
	unlock()
	if err = a.CancelSearch(p.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := a.ResumeSearch(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Trials[0].Status != "interrupted" || len(resumed.Trials) > campaign.Plan.MaxTrials {
		t.Fatal("reservation reset", resumed)
	}
	if err = a.VerifySearch(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.ID = "canceled"
	if _, err = a.Search(ctx, b, p); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestSearchDeadlineDoesNotReset(t *testing.T) {
	b, p := searchFixture(t)
	a, _ := New(t.TempDir(), "test-build", nil)
	defer a.Close()
	state, err := a.Search(context.Background(), b, p)
	if err != nil {
		t.Fatal(err)
	}
	state.Status = "paused"
	state.Deadline = time.Now().Add(-time.Minute)
	dir, _ := a.searchDir(p.ID)
	if err = a.saveSearch(dir, state); err != nil {
		t.Fatal(err)
	}
	resumed, err := a.ResumeSearch(context.Background(), p.ID)
	if err == nil || resumed.Status != "timed_out" || !resumed.Deadline.Equal(state.Deadline) {
		t.Fatal("deadline reset", resumed, err)
	}
}

func TestProposalsFollowFailureFeedback(t *testing.T) {
	signals := []maexec.Signal{{Code: "sz000001", Date: "2020-01-01"}, {Code: "sz000002", Date: "2020-01-01"}, {Code: "sz000003", Date: "2020-01-01"}}
	table := FeatureTable{}
	for i, s := range signals {
		table[signalKey(s)] = map[string]float64{"return_20": float64(i)}
	}
	c := searchCampaign{Plan: SearchPlan{MaxTerms: 2}, Features: table, Pool: []Predicate{{"return_20", "ge", 1}, {"return_20", "le", 1}}, Bundle: configured()}
	in := maexec.Input{Signals: signals}
	state := SearchState{Best: -1}
	ar := Artifact{Result: maexec.Result{Trades: []maexec.Trade{{Code: signals[0].Code, SignalDate: signals[0].Date, PNL: -100}, {Code: signals[2].Code, SignalDate: signals[2].Date, PNL: 50}}}}
	first, ok := propose(c, state, in, ar)
	if !ok || first.Terms[0].Op != "ge" {
		t.Fatal(first)
	}
	ar.Result.Trades[0].PNL = 50
	ar.Result.Trades[1].PNL = -100
	next, ok := propose(c, state, in, ar)
	if !ok || next.Terms[0].Op != "le" {
		t.Fatal("feedback ignored", next)
	}
}

func TestCanceledPreparationDoesNotReserveCampaign(t *testing.T) {
	b, p := searchFixture(t)
	p.Timeout = "1ns"
	a, _ := New(t.TempDir(), "test-build", nil)
	defer a.Close()
	if _, err := a.Search(context.Background(), b, p); err == nil {
		t.Fatal("preparation deadline ignored")
	}
	dir, _ := a.searchDir(p.ID)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("canceled preparation reserved an unrecoverable ID")
	}
}
