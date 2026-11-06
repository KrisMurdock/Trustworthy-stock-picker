package aris

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
)

// SearchPlan freezes the search budget and temporal boundary. No parameter can
// be extended on resume. Validation is evaluated only after proposal generation.
type SearchPlan struct {
	Version           int    `json:"schema_version"`
	ID                string `json:"id"`
	TrainThrough      string `json:"train_through"`
	ValidationFrom    string `json:"validation_from"`
	ValidationThrough string `json:"validation_through"`
	MaxTrials         int    `json:"max_trials"`
	Patience          int    `json:"patience"`
	MaxTerms          int    `json:"max_terms"`
	Timeout           string `json:"timeout"`
}
type Hypothesis struct {
	ID          string      `json:"id"`
	Terms       []Predicate `json:"terms"`
	SignalsHash string      `json:"signals_sha256"`
	Reason      string      `json:"reason"`
	RemovedLoss float64     `json:"removed_training_loss"`
	RemovedGain float64     `json:"removed_training_gain"`
}
type SearchTrial struct {
	Hypothesis Hypothesis `json:"hypothesis"`
	Status     string     `json:"status"`
	File       string     `json:"file"`
	SHA256     string     `json:"sha256"`
	Score      float64    `json:"worst_stress_portfolio_return"`
	Eligible   bool       `json:"support_and_risk_passed"`
	Failures   []string   `json:"failures"`
}
type SearchState struct {
	Version            int           `json:"schema_version"`
	ID                 string        `json:"id"`
	Build              string        `json:"build_sha256"`
	FrozenHash         string        `json:"frozen_sha256"`
	Deadline           time.Time     `json:"deadline"`
	Status             string        `json:"status"`
	StopReason         string        `json:"stop_reason"`
	PoolSize           int           `json:"predicate_pool_size"`
	Trials             []SearchTrial `json:"trials"`
	Best               int           `json:"best_trial"` // -1 is unchanged baseline, not a discovered strategy.
	Stale              int           `json:"rounds_without_improvement"`
	BaselineHash       string        `json:"baseline_sha256"`
	ValidationHash     string        `json:"validation_sha256"`
	ValidationFailures []string      `json:"validation_failures"`
	Limitations        []string      `json:"limitations"`
}
type searchCampaign struct {
	Version  int          `json:"schema_version"`
	Plan     SearchPlan   `json:"plan"`
	Bundle   Bundle       `json:"bundle"`
	Features FeatureTable `json:"features"`
	Pool     []Predicate  `json:"predicate_pool"`
}
type searchValidation struct {
	Baseline  Artifact `json:"baseline"`
	Candidate Artifact `json:"candidate"`
	Failures  []string `json:"failures"`
}

func searchWindows(base maexec.Input, p SearchPlan) (maexec.Input, maexec.Input, error) {
	train, validation := base, base
	duration, err := time.ParseDuration(p.Timeout)
	if p.Version != 1 || !ValidID(p.ID) || p.MaxTrials < 1 || p.MaxTrials > 100 || p.Patience < 1 || p.Patience > p.MaxTrials || p.MaxTerms < 1 || p.MaxTerms > 2 || err != nil || duration <= 0 || duration > 24*time.Hour {
		return train, validation, errors.New("invalid search plan or budget")
	}
	index := map[string]int{}
	for i, d := range base.Sessions {
		index[d] = i
	}
	end, ok1 := index[p.TrainThrough]
	start, ok2 := index[p.ValidationFrom]
	last, ok3 := index[p.ValidationThrough]
	if !ok1 || !ok2 || !ok3 || start-end-1 < base.Config.Holding+1 || last <= start || end < base.Config.Holding+1 || last-start < base.Config.Holding+1 {
		return train, validation, errors.New("invalid search dates or insufficient holding-period embargo")
	}
	window := func(from, through int) maexec.Input {
		in := base
		in.Sessions = append([]string{}, base.Sessions[from:through+1]...)
		in.Bars = map[string]map[string]maexec.Bar{}
		for _, d := range in.Sessions {
			in.Bars[d] = base.Bars[d]
		}
		in.Signals = []maexec.Signal{}
		// Leave a complete entry/holding window; unavailable exits remain open and
		// invalidate qualification rather than being forcibly closed or dropped.
		for _, sig := range base.Signals {
			i := index[sig.Date]
			if i >= from && i <= through-base.Config.Holding-1 {
				in.Signals = append(in.Signals, sig)
			}
		}
		return in
	}
	train = window(0, end)
	validation = window(start, last)
	if len(train.Signals) == 0 || len(validation.Signals) == 0 {
		return train, validation, errors.New("training and validation both require signals")
	}
	return train, validation, nil
}

func searchAssessment(ar Artifact, c Constraints) (float64, bool, []string) {
	score := ar.Result.Metrics.Return
	eligible := true
	failures := []string{}
	all := []maexec.Result{ar.Result}
	for _, s := range ar.Stress {
		all = append(all, s.Result)
	}
	for i, r := range all {
		score = math.Min(score, r.Metrics.Return)
		days := map[string]bool{}
		worst := 0.0
		for _, tr := range r.Trades {
			days[tr.SignalDate] = true
			worst = math.Max(worst, -tr.PNL)
		}
		if r.Metrics.Closed < c.MinTrades || len(days) < c.MinDays || len(r.Open) > 0 || -r.Metrics.Drawdown > *c.Drawdown || worst > *c.TailLoss {
			eligible = false
		}
		if !thresholds(r, c) || len(r.Open) > 0 {
			failures = append(failures, fmt.Sprintf("scenario_%d_numeric_or_unclosed", i))
		}
	}
	return score, eligible, failures
}
func searchHypothesis(terms []Predicate, signals []maexec.Signal, table FeatureTable) Hypothesis {
	ordered := append([]Predicate{}, terms...)
	sort.Slice(ordered, func(i, j int) bool { return Digest(ordered[i]) < Digest(ordered[j]) })
	return Hypothesis{ID: "h-" + Digest(ordered)[:16], Terms: ordered, SignalsHash: Digest(filterSignals(signals, table, ordered))}
}
func propose(c searchCampaign, s SearchState, training maexec.Input, incumbent Artifact) (Hypothesis, bool) {
	tried := map[string]bool{Digest(training.Signals): true}
	for _, trial := range s.Trials {
		tried[trial.Hypothesis.SignalsHash] = true
	}
	parents := [][]Predicate{{}}
	if s.Best >= 0 && len(s.Trials[s.Best].Hypothesis.Terms) < c.Plan.MaxTerms {
		parents = append(parents, s.Trials[s.Best].Hypothesis.Terms)
	}
	candidates := []Hypothesis{}
	for _, parent := range parents {
		for _, pred := range c.Pool {
			repeated := false
			for _, t := range parent {
				if t.Feature == pred.Feature {
					repeated = true
				}
			}
			if repeated {
				continue
			}
			terms := append(append([]Predicate{}, parent...), pred)
			h := searchHypothesis(terms, training.Signals, c.Features)
			if tried[h.SignalsHash] {
				continue
			}
			selected := filterSignals(training.Signals, c.Features, terms)
			if len(selected) < c.Bundle.Contract.Constraints.MinTrades {
				continue
			}
			for _, tr := range incumbent.Result.Trades {
				if !matches(c.Features[tr.Code+"/"+tr.SignalDate], terms) {
					if tr.PNL < 0 {
						h.RemovedLoss -= tr.PNL
					} else {
						h.RemovedGain += tr.PNL
					}
				}
			}
			h.Reason = "Training-only loss attribution; prioritize removed net losses minus removed net gains; validation unopened"
			candidates = append(candidates, h)
		}
	}
	if len(candidates) == 0 {
		return Hypothesis{}, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		x, y := a.RemovedLoss-a.RemovedGain, b.RemovedLoss-b.RemovedGain
		if x != y {
			return x > y
		}
		return a.ID < b.ID
	})
	return candidates[0], true
}

func (a *App) executeSearch(ctx context.Context, dir string, c searchCampaign, s SearchState) (SearchState, error) {
	return a.executeSearchReplay(ctx, dir, c, s, nil)
}

func (a *App) executeSearchReplay(ctx context.Context, dir string, c searchCampaign, s SearchState, interruptions map[int]SearchTrial) (SearchState, error) {
	ctx, cancel := context.WithDeadline(ctx, s.Deadline)
	defer cancel()
	save := func(status string) (SearchState, error) { s.Status = status; return s, a.saveSearch(dir, s) }
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return searchCanceled(dir)
	}
	pause := func(err error) (SearchState, error) {
		status := "paused"
		if errors.Is(err, context.DeadlineExceeded) {
			status = "timed_out"
		}
		s.Status = status
		if e := a.saveSearch(dir, s); e != nil {
			return s, e
		}
		return s, err
	}
	training, validation, err := searchWindows(c.Bundle.Inputs[c.Bundle.Contract.Baseline], c.Plan)
	if err != nil {
		return s, err
	}
	if err = check(); err != nil {
		return pause(err)
	}
	s.Status = "searching"
	var baseline Artifact
	if s.BaselineHash == "" {
		baseline, err = evaluate(ctx, MAAdapter{}, training, c.Bundle.Contract.Constraints.StressSlippage)
		if err != nil {
			return pause(err)
		}
		s.BaselineHash, err = saveSearchArtifact(dir, "baseline.json", baseline)
		if err != nil {
			return s, err
		}
		if err = a.saveSearch(dir, s); err != nil {
			return s, err
		}
	} else if err = readSearchArtifact(dir, "baseline.json", s.BaselineHash, &baseline); err != nil {
		return s, err
	}
	incumbent := baseline
	bestScore, _, _ := searchAssessment(baseline, c.Bundle.Contract.Constraints)
	if s.Best >= 0 {
		tr := s.Trials[s.Best]
		if err = readSearchArtifact(dir, tr.File, tr.SHA256, &incumbent); err != nil {
			return s, err
		}
		bestScore = tr.Score
	}
	// A crash after reservation spends the attempt. No hidden retry or budget reset.
	for i := range s.Trials {
		if s.Trials[i].Status == "started" {
			s.Trials[i].Status = "interrupted"
			s.Stale++
		}
	}
	if err = a.saveSearch(dir, s); err != nil {
		return s, err
	}
	for s.StopReason == "" {
		if err = check(); err != nil {
			return pause(err)
		}
		if len(s.Trials) >= c.Plan.MaxTrials {
			s.StopReason = "trial_budget_exhausted"
			break
		}
		if s.Stale >= c.Plan.Patience {
			s.StopReason = "no_improvement"
			break
		}
		hypothesis, ok := propose(c, s, training, incumbent)
		if !ok {
			s.StopReason = "hypotheses_exhausted"
			break
		}
		index := len(s.Trials)
		s.Trials = append(s.Trials, SearchTrial{Hypothesis: hypothesis, Status: "started", File: fmt.Sprintf("trial-%03d.json", index)})
		if err = a.saveSearch(dir, s); err != nil {
			return s, err
		}
		if interrupted, ok := interruptions[index]; ok {
			if Digest(interrupted.Hypothesis) != Digest(hypothesis) {
				return s, errors.New("interrupted proposal replay mismatch")
			}
			s.Trials[index].Status = "interrupted"
			s.Stale++
			if err = a.saveSearch(dir, s); err != nil {
				return s, err
			}
			continue
		}
		input := training
		input.Signals = filterSignals(training.Signals, c.Features, hypothesis.Terms)
		result, e := evaluate(ctx, MAAdapter{}, input, c.Bundle.Contract.Constraints.StressSlippage)
		if e != nil {
			return pause(e)
		}
		if err = check(); err != nil {
			return pause(err)
		}
		tr := &s.Trials[index]
		tr.SHA256, err = saveSearchArtifact(dir, tr.File, result)
		if err != nil {
			return s, err
		}
		tr.Status = "completed"
		tr.Score, tr.Eligible, tr.Failures = searchAssessment(result, c.Bundle.Contract.Constraints)
		if tr.Eligible && tr.Score > bestScore+1e-12 {
			s.Best = index
			incumbent = result
			bestScore = tr.Score
			s.Stale = 0
		} else {
			s.Stale++
		}
		if err = a.saveSearch(dir, s); err != nil {
			return s, err
		}
	}
	if err = check(); err != nil {
		return pause(err)
	}
	// Lock the chosen hypothesis before validation. No validation result is used
	// by propose, thresholds, budget, best index or stopping decisions.
	s.Status = "validating"
	if err = a.saveSearch(dir, s); err != nil {
		return s, err
	}
	var v searchValidation
	if s.ValidationHash == "" {
		v.Baseline, err = evaluate(ctx, MAAdapter{}, validation, c.Bundle.Contract.Constraints.StressSlippage)
		if err != nil {
			return pause(err)
		}
		candidate := validation
		if s.Best >= 0 {
			candidate.Signals = filterSignals(validation.Signals, c.Features, s.Trials[s.Best].Hypothesis.Terms)
		}
		v.Candidate, err = evaluate(ctx, MAAdapter{}, candidate, c.Bundle.Contract.Constraints.StressSlippage)
		if err != nil {
			return pause(err)
		}
		_, _, v.Failures = searchAssessment(v.Candidate, c.Bundle.Contract.Constraints)
		if s.Best >= 0 {
			for _, failure := range s.Trials[s.Best].Failures {
				v.Failures = append(v.Failures, "development_"+failure)
			}
		}
		interval, e := infer(ctx, v.Candidate.Result, v.Baseline.Result, c.Bundle.Contract.Inference)
		if e != nil || interval.ExpectancyLower <= c.Bundle.Contract.Constraints.MinExpectancy || interval.ExcessLower <= 0 {
			v.Failures = append(v.Failures, "validation_time_block_evidence_insufficient")
		}
		if s.Best < 0 {
			v.Failures = append(v.Failures, "no_development_improvement")
		}
		if err = check(); err != nil {
			return pause(err)
		}
		s.ValidationHash, err = saveSearchArtifact(dir, "validation.json", v)
		if err != nil {
			return s, err
		}
	} else if err = readSearchArtifact(dir, "validation.json", s.ValidationHash, &v); err != nil {
		return s, err
	}
	s.ValidationFailures = v.Failures
	if len(v.Failures) > 0 {
		return save("completed_rejected")
	}
	return save("completed_research_only")
}
