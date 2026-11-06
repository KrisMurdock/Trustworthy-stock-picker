package aris

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
)

func (a *App) searchDir(id string) (string, error) {
	if !ValidID(id) {
		return "", errors.New("invalid campaign ID")
	}
	dir := filepath.Join(a.root, "campaigns", id)
	if err := SafePath(dir); err != nil {
		return "", err
	}
	return dir, localPath(dir)
}
func (a *App) Search(ctx context.Context, b Bundle, p SearchPlan) (SearchState, error) {
	var state SearchState
	if err := ctx.Err(); err != nil {
		return state, err
	}
	if err := b.Validate(a.build); err != nil {
		return state, err
	}
	if b.Request.Version != 2 || b.Shared == nil {
		return state, errors.New("autonomous search requires a sealed shared snapshot")
	}
	if b.Contract.Constraints.missing() || !b.Contract.Inference.valid() {
		return state, errors.New("explicit risk, sample, stress and inference configuration required")
	}
	for _, slip := range b.Contract.Constraints.StressSlippage {
		if slip <= b.Shared.Config.Slippage {
			return state, errors.New("stress slippage must exceed baseline")
		}
	}
	if err := b.Inputs[b.Contract.Baseline].Validate(ctx); err != nil {
		return state, err
	}
	training, _, err := searchWindows(b.Inputs[b.Contract.Baseline], p)
	if err != nil {
		return state, err
	}
	dir, err := a.searchDir(p.ID)
	if err != nil {
		return state, err
	}
	if _, e := os.Lstat(dir); e == nil {
		return state, errors.New("campaign already exists")
	} else if !os.IsNotExist(e) {
		return state, e
	}
	duration, _ := time.ParseDuration(p.Timeout)
	deadline := time.Now().UTC().Add(duration)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	z, err := gzip.NewReader(bytes.NewReader(b.Shared.Snapshot))
	if err != nil {
		return state, err
	}
	raw, err := io.ReadAll(io.LimitReader(z, MaxSharedSnapshot+1))
	z.Close()
	if err != nil {
		return state, err
	}
	if len(raw) > MaxSharedSnapshot {
		return state, errors.New("shared snapshot expansion limit")
	}
	var snapshot maexec.Snapshot
	// The sealed bundle already underwent strict duplicate/unknown-field checks.
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		return state, err
	}
	features, err := SearchFeatures(ctx, snapshot, b.Inputs[b.Contract.Baseline].Signals)
	if err != nil {
		return state, err
	}
	campaign := searchCampaign{Version: 1, Plan: p, Bundle: b, Features: features, Pool: searchPool(features, training.Signals)}
	if len(campaign.Pool) == 0 {
		return state, errors.New("no usable training features")
	}
	// Preparation is read-only and cancelable. Reserve the identity only when
	// the complete frozen campaign can be persisted; concurrent creators race
	// on Mkdir and exactly one wins.
	if err = ctx.Err(); err != nil {
		return state, err
	}
	if err = os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return state, err
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return state, err
	}
	unlock, err := lock(dir)
	if err != nil {
		return state, err
	}
	defer unlock()
	hash, err := saveSearchArtifact(dir, "campaign.json", campaign)
	if err != nil {
		return state, err
	}
	state = SearchState{Version: 1, ID: p.ID, Build: a.build, FrozenHash: hash, Deadline: deadline, Status: "searching", PoolSize: len(campaign.Pool), Best: -1, Trials: []SearchTrial{}, Limitations: []string{
		"Adaptive multiple search on development data; training metrics are selection-biased.",
		"Validation is isolated within this campaign, not independently unseen historical data; reuse across campaigns is contaminated.",
		"Qfq synthetic-unit execution, assumed availability; no automatic promotion or production changes.",
	}}
	if err = a.saveSearch(dir, state); err != nil {
		return state, err
	}
	return a.executeSearch(ctx, dir, campaign, state)
}
func saveSearchArtifact(dir, name string, value any) (string, error) {
	if filepath.Base(name) != name {
		return "", errors.New("invalid artifact path")
	}
	if err := atomic(filepath.Join(dir, name), value); err != nil {
		return "", err
	}
	raw, err := Read(filepath.Join(dir, name), MaxDocument)
	if err != nil {
		return "", err
	}
	return Hash(raw), nil
}
func readSearchArtifact(dir, name, hash string, value any) error {
	if filepath.Base(name) != name || hash == "" {
		return errors.New("invalid artifact identity")
	}
	raw, err := Read(filepath.Join(dir, name), MaxDocument)
	if err != nil {
		return err
	}
	if Hash(raw) != hash {
		return errors.New("search artifact hash mismatch: " + name)
	}
	return Decode(raw, value)
}
func (a *App) saveSearch(dir string, s SearchState) error {
	if err := atomic(filepath.Join(dir, "state.json"), s); err != nil {
		return err
	}
	a.log.Info("Autonomous research checkpoint saved", "campaign_id", s.ID, "status", s.Status, "attempts", len(s.Trials), "best_trial", s.Best)
	return nil
}
func (a *App) SearchStatus(id string) (SearchState, error) {
	var s SearchState
	dir, err := a.searchDir(id)
	if err != nil {
		return s, err
	}
	raw, err := Read(filepath.Join(dir, "state.json"), MaxDocument)
	if err != nil {
		return s, err
	}
	if err = Decode(raw, &s); err != nil {
		return s, err
	}
	if s.Version != 1 || s.ID != id || s.Build != a.build {
		return s, errors.New("campaign identity or build mismatch")
	}
	return s, nil
}
func (a *App) loadSearch(id string) (searchCampaign, SearchState, error) {
	var c searchCampaign
	s, err := a.SearchStatus(id)
	if err != nil {
		return c, s, err
	}
	dir, _ := a.searchDir(id)
	if err = readSearchArtifact(dir, "campaign.json", s.FrozenHash, &c); err != nil {
		return c, s, err
	}
	if c.Version != 1 || c.Plan.ID != id || len(s.Trials) > c.Plan.MaxTrials || s.Best < -1 || s.Best >= len(s.Trials) {
		return c, s, errors.New("invalid campaign checkpoint")
	}
	if err = c.Bundle.Validate(a.build); err != nil {
		return c, s, err
	}
	for i, tr := range s.Trials {
		if tr.File != fmt.Sprintf("trial-%03d.json", i) || tr.Hypothesis.ID != "h-"+Digest(tr.Hypothesis.Terms)[:16] {
			return c, s, errors.New("invalid trial identity")
		}
		if tr.Status == "completed" {
			var ar Artifact
			if err = readSearchArtifact(dir, tr.File, tr.SHA256, &ar); err != nil {
				return c, s, err
			}
			score, eligible, failures := searchAssessment(ar, c.Bundle.Contract.Constraints)
			if score != tr.Score || eligible != tr.Eligible || Digest(failures) != Digest(tr.Failures) {
				return c, s, errors.New("trial assessment checkpoint mismatch")
			}
		}
	}
	if s.Best >= 0 && s.Trials[s.Best].Status != "completed" {
		return c, s, errors.New("best trial is incomplete")
	}
	return c, s, nil
}
func (a *App) ResumeSearch(ctx context.Context, id string) (SearchState, error) {
	dir, err := a.searchDir(id)
	if err != nil {
		return SearchState{}, err
	}
	unlock, err := lock(dir)
	if err != nil {
		return SearchState{}, err
	}
	defer unlock()
	c, s, err := a.loadSearch(id)
	if err != nil {
		return s, err
	}
	if strings.HasPrefix(s.Status, "completed_") || s.Status == "timed_out" {
		return s, errors.New("campaign is terminal")
	}
	if err = os.Remove(filepath.Join(dir, "cancel.json")); err != nil && !os.IsNotExist(err) {
		return s, err
	}
	return a.executeSearch(ctx, dir, c, s)
}
func searchCanceled(dir string) error {
	_, err := os.Lstat(filepath.Join(dir, "cancel.json"))
	if err == nil {
		return context.Canceled
	}
	if !os.IsNotExist(err) {
		return err
	}
	return nil
}
func (a *App) CancelSearch(id string) error {
	dir, err := a.searchDir(id)
	if err != nil {
		return err
	}
	if _, err = a.SearchStatus(id); err != nil {
		return err
	}
	return atomic(filepath.Join(dir, "cancel.json"), map[string]string{"id": id})
}
func (a *App) VerifySearch(ctx context.Context, id string) error {
	dir, err := a.searchDir(id)
	if err != nil {
		return err
	}
	unlock, err := lock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	c, s, err := a.loadSearch(id)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(s.Status, "completed_") {
		return errors.New("only completed campaigns can be replayed")
	}
	var validation searchValidation
	if err = readSearchArtifact(dir, "validation.json", s.ValidationHash, &validation); err != nil {
		return err
	}
	// Replay the complete adaptive sequence, not just its winning hypothesis.
	// Interrupted reservations are preserved, never transformed into extra trials.
	tmp, err := os.MkdirTemp(a.root, ".search-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	duration, _ := time.ParseDuration(c.Plan.Timeout)
	initial := s
	initial.Deadline = time.Now().UTC().Add(duration)
	initial.Status = "searching"
	initial.StopReason = ""
	initial.Trials = []SearchTrial{}
	initial.Best = -1
	initial.Stale = 0
	initial.BaselineHash = ""
	initial.ValidationHash = ""
	initial.ValidationFailures = nil
	interruptions := map[int]SearchTrial{}
	for i, tr := range s.Trials {
		if tr.Status == "interrupted" {
			interruptions[i] = tr
		}
	}
	replay, err := a.executeSearchReplay(ctx, tmp, c, initial, interruptions)
	if err != nil {
		return err
	}
	replay.Deadline = s.Deadline
	if Digest(replay) != Digest(s) {
		return errors.New("adaptive campaign replay mismatch")
	}
	return nil
}
