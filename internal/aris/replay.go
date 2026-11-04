package aris

import (
	"context"
	"errors"
	"path/filepath"
)

// Replay verifies persisted evidence against fresh deterministic evaluation.
// It is read-only and consumes no new research attempts or reviewer calls.
func (a *App) Replay(ctx context.Context, id string) error {
	if e := a.Verify(id); e != nil {
		return e
	}
	s, e := a.Status(id)
	if e != nil {
		return e
	}
	if !terminal(s.Status) {
		return errors.New("replay requires a terminal run")
	}
	dir, e := a.dir(id)
	if e != nil {
		return e
	}
	raw, e := Read(filepath.Join(dir, "bundle.json"), MaxDocument)
	if e != nil {
		return e
	}
	var b Bundle
	if e = Decode(raw, &b); e != nil {
		return e
	}
	artifacts := map[string]Artifact{}
	for _, at := range s.Attempts {
		if at.Status != "completed" {
			continue
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		raw, e := Read(filepath.Join(dir, at.File), MaxDocument)
		if e != nil {
			return e
		}
		var stored Artifact
		if e = Decode(raw, &stored); e != nil {
			return e
		}
		actual, e := evaluate(ctx, MAAdapter{}, b.Inputs[at.Candidate], b.Contract.Constraints.StressSlippage)
		if e != nil {
			return e
		}
		if Digest(actual) != Digest(stored) {
			return errors.New("recomputed artifact differs from persisted result")
		}
		artifacts[at.Candidate] = actual
	}
	if len(s.Gates) > 0 {
		if len(artifacts) != len(b.Inputs) {
			return errors.New("gates committed without all results")
		}
		gates := checkGates(ctx, b, artifacts, s.Attempts)
		if e = ctx.Err(); e != nil {
			return e
		}
		if Digest(gates) != Digest(s.Gates) {
			return errors.New("recomputed gates differ from state")
		}
	}
	if s.Status == "accepted" || s.Status == "development_passed" {
		return errors.New("proxy evidence cannot establish this terminal verdict")
	}
	report, e := Read(filepath.Join(dir, "report.json"), 8<<20)
	if e != nil {
		return e
	}
	var published State
	if e = Decode(report, &published); e != nil {
		return e
	}
	if Digest(published) != Digest(s) {
		return errors.New("published report differs from committed state")
	}
	return nil
}
