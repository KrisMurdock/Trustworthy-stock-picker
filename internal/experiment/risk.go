package experiment

import "stock-watcher/internal/strategy"

// Review is a read-time assessment under today's acceptance policy. It is not
// written over the archived verdict, hypothesis or simulated executions.
type Review struct {
	Risk  strategy.LossAssessment `json:"risk"`
	Stats strategy.Stats          `json:"stats"`
}

func ReviewStats(stats strategy.Stats) *Review {
	return &Review{Risk: strategy.AssessLoss(stats), Stats: stats}
}

func ReviewRecord(r Record) *Review {
	if r.Status != "completed" || r.Result == nil {
		return nil
	}
	stats := strategy.SummarizeExperimentTrades(r.Result.Development.Trades, r.Data.Parameters)
	review := ReviewStats(stats)
	saved := r.Result.Development.Research.Development.TopRanked
	if stats.Samples != saved.Samples || stats.Resolved != saved.Resolved || stats.Unresolved != saved.Unresolved || stats.SkippedEntries != saved.SkippedEntries {
		if review.Risk.Status != "breached" {
			review.Risk.Status = "unverified"
		}
	}
	return review
}

func developmentVerdict(r Record, parent *Record) string {
	s := r.Result.Development.Research.Development.TopRanked
	switch strategy.AssessLoss(s).Status {
	case "breached":
		return "risk_rejected"
	case "unverified":
		return "risk_unverified"
	}
	if s.Resolved < 30 || s.Dates < 10 || s.HighTouchSamples != s.Samples {
		return "insufficient"
	}
	if parent == nil {
		return "baseline"
	}
	if !Comparable(r, *parent) || parent.Result == nil {
		return "not_comparable"
	}
	b := parent.Result.Development.Research.Development.TopRanked
	if b.Resolved < 30 || b.Dates < 10 || b.HighTouchSamples != b.Samples {
		return "insufficient"
	}
	if s.MeanNet <= 0 {
		return "no_improvement"
	}
	// Restoring the hard risk constraint takes precedence over soft objectives.
	if strategy.AssessLoss(b).Status != "observed_pass" {
		return "promising"
	}
	better, worse := false, false
	changes := []float64{s.HighTouch25Rate - b.HighTouch25Rate, s.HighTouch30Rate - b.HighTouch30Rate}
	if s.HighTouch25 > 0 && b.HighTouch25 > 0 {
		changes = append(changes, b.HighTouch25Days-s.HighTouch25Days)
	}
	if s.HighTouch30 > 0 && b.HighTouch30 > 0 {
		changes = append(changes, b.HighTouch30Days-s.HighTouch30Days)
	}
	for _, change := range changes {
		better = better || change > 1e-9
		worse = worse || change < -1e-9
	}
	if !better {
		return "no_improvement"
	}
	if worse {
		return "tradeoff"
	}
	return "promising"
}
