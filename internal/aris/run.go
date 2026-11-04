package aris

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
)

// Adapter is an offline deterministic execution boundary. Registration is a
// build-time choice; requests cannot load plugins, commands or network clients.
type Adapter interface {
	ID() string
	Validate(context.Context, maexec.Input) error
	Evaluate(context.Context, maexec.Input) (maexec.Result, error)
}
type MAAdapter struct{}

func (MAAdapter) ID() string                                          { return "ma-proxy" }
func (MAAdapter) Validate(ctx context.Context, in maexec.Input) error { return in.Validate(ctx) }
func (MAAdapter) Evaluate(ctx context.Context, in maexec.Input) (maexec.Result, error) {
	return maexec.Simulate(ctx, in)
}

type StressResult struct {
	Slippage float64       `json:"slippage"`
	Result   maexec.Result `json:"result"`
}
type Artifact struct {
	Result     maexec.Result  `json:"result"`
	ReplayHash string         `json:"replay_sha256"`
	Stress     []StressResult `json:"stress"`
}

func (a *App) Run(ctx context.Context, b Bundle) (State, error) {
	s, dir, unlock, e := a.freeze(ctx, b)
	if e != nil {
		return s, e
	}
	defer unlock()
	return a.execute(ctx, dir, b, s)
}
func (a *App) freeze(ctx context.Context, b Bundle) (s State, dir string, unlock func(), err error) {
	if e := ctx.Err(); e != nil {
		return s, dir, nil, e
	}
	if e := b.validate(a.build); e != nil {
		return s, dir, nil, e
	}
	// Validate before reserving the ID; malformed input never becomes executable.
	adapter := MAAdapter{}
	for _, id := range append([]string{b.Contract.Baseline}, b.Contract.Candidates...) {
		if e := adapter.Validate(ctx, b.Inputs[id]); e != nil {
			return s, dir, nil, fmt.Errorf("input %s: %w", id, e)
		}
	}
	var e error
	dir, e = a.dir(b.Request.RunID)
	if e != nil {
		return s, dir, nil, e
	}
	if e = os.Mkdir(dir, 0700); e != nil {
		return s, dir, nil, fmt.Errorf("run ID already reserved or unavailable: %w", e)
	}
	unlock, e = lock(dir)
	if e != nil {
		return s, dir, nil, e
	}
	release := unlock
	defer func() {
		if err != nil {
			release()
		}
	}()
	if e = os.Mkdir(filepath.Join(dir, "artifacts"), 0700); e != nil {
		return s, dir, nil, e
	}
	if e = atomic(filepath.Join(dir, "bundle.json"), b); e != nil {
		return s, dir, nil, e
	}
	duration, _ := time.ParseDuration(b.Contract.Budget.Timeout)
	now := time.Now().UTC()
	s = State{Version: 1, RunID: b.Request.RunID, Status: "frozen", BundleHash: Digest(b), Build: a.build, Created: now, Deadline: now.Add(duration), Attempts: []Attempt{}, Gates: []GateResult{}}
	if e = a.save(dir, s); e != nil {
		return s, dir, nil, e
	}
	return s, dir, unlock, nil
}
func (a *App) Resume(ctx context.Context, id string) (State, error) {
	var s State
	if e := ctx.Err(); e != nil {
		return s, e
	}
	dir, e := a.dir(id)
	if e != nil {
		return s, e
	}
	unlock, e := lock(dir)
	if e != nil {
		return s, e
	}
	defer unlock()
	if e = a.Verify(id); e != nil {
		return s, e
	}
	s, e = a.Status(id)
	if e != nil {
		return s, e
	}
	if terminal(s.Status) {
		return s, errors.New("terminal run requires a new contract and run ID")
	}
	switch s.Status {
	case "frozen", "baseline_done", "candidates_done", "development_verified", "reviewing":
	default:
		return s, errors.New("unknown phase")
	}
	raw, e := Read(filepath.Join(dir, "bundle.json"), MaxDocument)
	if e != nil {
		return s, e
	}
	var b Bundle
	if e = Decode(raw, &b); e != nil {
		return s, e
	}
	for i := range s.Attempts {
		if s.Attempts[i].Status == "started" {
			s.Attempts[i].Status = "interrupted"
		}
	}
	if e = a.save(dir, s); e != nil {
		return s, e
	}
	return a.execute(ctx, dir, b, s)
}
func (a *App) execute(parent context.Context, dir string, b Bundle, s State) (State, error) {
	finish := func(status, reason string) (State, error) {
		s.Status = status
		s.Reason = reason
		if e := a.report(dir, s); e != nil {
			return s, e
		}
		if e := a.save(dir, s); e != nil {
			return s, e
		}
		return s, nil
	}
	if b.Contract.Constraints.missing() || !b.Contract.Inference.valid() {
		return finish("needs_human", "Risk, sample, cost-stress or block-inference thresholds are missing; no attempts executed.")
	}
	if b.Contract.Review.Mode == "advisory" && b.Contract.Review.Backend == "exec" {
		return finish("needs_human", "Exec reviewer unavailable: enforced process sandbox is not implemented.")
	}
	if b.Contract.Review.Mode == "advisory" && a.Reviewer == nil {
		return finish("needs_human", "Configured HTTP reviewer is unavailable; no attempts executed.")
	}
	if !time.Now().Before(s.Deadline) {
		return finish("needs_human", "Original wall-clock budget exhausted.")
	}
	ctx, cancel := context.WithDeadline(parent, s.Deadline)
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, e := os.Lstat(filepath.Join(dir, "cancel.json")); e == nil {
					cancel()
					return
				}
			}
		}
	}()
	canceled := func() (State, error) {
		if !time.Now().Before(s.Deadline) {
			return finish("needs_human", "Original wall-clock budget exhausted.")
		}
		return finish("canceled", "Execution canceled; completed artifacts retained.")
	}
	if _, e := os.Lstat(filepath.Join(dir, "cancel.json")); e == nil {
		return finish("canceled", "Cancellation requested.")
	}
	artifacts := map[string]Artifact{}
	for _, id := range append([]string{b.Contract.Baseline}, b.Contract.Candidates...) {
		if ctx.Err() != nil {
			return canceled()
		}
		attempts := 0
		completed := false
		for _, at := range s.Attempts {
			if at.Candidate != id {
				continue
			}
			attempts++
			if at.Status == "completed" {
				raw, e := Read(filepath.Join(dir, at.File), MaxDocument)
				if e != nil {
					return s, e
				}
				var ar Artifact
				if e = Decode(raw, &ar); e != nil {
					return s, e
				}
				artifacts[id] = ar
				completed = true
			}
		}
		if completed {
			continue
		}
		if attempts >= b.Contract.Budget.Attempts {
			return finish("needs_human", "Attempt budget exhausted for "+id)
		}
		s.Attempts = append(s.Attempts, Attempt{ID: fmt.Sprintf("%s-%d", id, attempts+1), Candidate: id, Status: "started"})
		idx := len(s.Attempts) - 1
		if e := a.save(dir, s); e != nil {
			return s, e
		}
		ar, e := evaluate(ctx, MAAdapter{}, b.Inputs[id], b.Contract.Constraints.StressSlippage)
		if e != nil {
			s.Attempts[idx].Status = "failed"
			if ctx.Err() != nil {
				return canceled()
			}
			return finish("failed", "Execution failed for "+id+": "+e.Error())
		}
		file := "artifacts/" + id + ".json"
		if e = atomic(filepath.Join(dir, file), ar); e != nil {
			return s, e
		}
		raw, e := Read(filepath.Join(dir, file), MaxDocument)
		if e != nil {
			return s, e
		}
		s.Attempts[idx].Status = "completed"
		s.Attempts[idx].File = file
		s.Attempts[idx].ResultHash = Hash(raw)
		artifacts[id] = ar
		if id == b.Contract.Baseline {
			s.Status = "baseline_done"
		} else {
			s.Status = "candidates_done"
		}
		if e = a.save(dir, s); e != nil {
			return s, e
		}
	}
	if ctx.Err() != nil {
		return canceled()
	}
	s.Gates = checkGates(ctx, b, artifacts, s.Attempts)
	if ctx.Err() != nil {
		return canceled()
	}
	s.Status = "development_verified"
	if e := a.save(dir, s); e != nil {
		return s, e
	}
	for _, g := range s.Gates {
		if g.Status == "failed" {
			return finish("rejected", "At least one deterministic gate failed; see evidence.")
		}
	}
	if b.Contract.Review.Mode == "advisory" {
		s.Status = "reviewing"
		if e := a.save(dir, s); e != nil {
			return s, e
		}
		verdict, e := a.review(ctx, dir, b, &s)
		if ctx.Err() != nil {
			return canceled()
		}
		if e != nil {
			return finish("needs_human", "Review unavailable or invalid; see review evidence.")
		}
		if verdict == "veto" {
			return finish("rejected", "Advisory reviewer vetoed with blocking findings.")
		}
		if verdict != "pass" {
			return finish("needs_human", "Review requires human resolution.")
		}
	}
	// Proxy input has no historical visible_at lineage or independent holdout.
	// Neither a positive backtest nor a reviewer can invent that missing evidence.
	return finish("needs_human", "Proxy diagnostic gates passed; test attestation, historical visibility and independent holdout remain unverified.")
}
func evaluate(ctx context.Context, adapter Adapter, in maexec.Input, slips []float64) (Artifact, error) {
	var ar Artifact
	r, e := adapter.Evaluate(ctx, in)
	if e != nil {
		return ar, e
	}
	again, e := adapter.Evaluate(ctx, in)
	if e != nil {
		return ar, e
	}
	ar = Artifact{Result: r, ReplayHash: Digest(again), Stress: []StressResult{}}
	if Digest(r) == "" || Digest(r) != ar.ReplayHash {
		return ar, errors.New("deterministic replay mismatch")
	}
	for _, slip := range slips {
		stress := in
		stress.Config.Slippage = slip
		r, e := adapter.Evaluate(ctx, stress)
		if e != nil {
			return ar, e
		}
		ar.Stress = append(ar.Stress, StressResult{slip, r})
	}
	return ar, nil
}
