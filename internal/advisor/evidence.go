package advisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/review"
)

type weightedEvidence struct {
	evidence Evidence
	priority int
	sequence int
}

type evidenceCollector struct {
	order       []string
	byIntent    map[string][]weightedEvidence
	sequence    int
	totalBefore int
}

func newEvidenceCollector(intents []string) *evidenceCollector {
	order := make([]string, 0, len(intents))
	seen := map[string]bool{}
	for _, intent := range intents {
		if seen[intent] {
			continue
		}
		seen[intent] = true
		order = append(order, intent)
	}
	return &evidenceCollector{order: order, byIntent: map[string][]weightedEvidence{}}
}

func (c *evidenceCollector) add(intent string, items []weightedEvidence) {
	for _, item := range items {
		if item.evidence.Intent == "" {
			item.evidence.Intent = intent
		}
		if item.evidence.Facts == nil {
			item.evidence.Facts = map[string]any{}
		}
		c.sequence++
		item.sequence = c.sequence
		c.byIntent[intent] = append(c.byIntent[intent], item)
		c.totalBefore++
	}
}

func (c *evidenceCollector) bundle() ([]Evidence, int, []string) {
	merged := make([]weightedEvidence, 0, c.totalBefore)
	trimmed := 0
	for _, intent := range c.order {
		items := append([]weightedEvidence(nil), c.byIntent[intent]...)
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].priority != items[j].priority {
				return items[i].priority > items[j].priority
			}
			return items[i].sequence < items[j].sequence
		})
		if len(items) > MaxEvidencePerIntent {
			trimmed += len(items) - MaxEvidencePerIntent
			items = items[:MaxEvidencePerIntent]
		}
		merged = append(merged, items...)
	}
	if len(merged) > MaxEvidence {
		sort.SliceStable(merged, func(i, j int) bool {
			if merged[i].priority != merged[j].priority {
				return merged[i].priority > merged[j].priority
			}
			return merged[i].sequence < merged[j].sequence
		})
		trimmed += len(merged) - MaxEvidence
		merged = merged[:MaxEvidence]
	}

	final := make([]Evidence, 0, len(merged))
	for _, item := range merged {
		final = append(final, item.evidence)
	}
	assignEvidenceIDs(final)
	for serializedEvidenceSize(final) > MaxEvidenceBytes && len(final) > 0 {
		drop := lowestPriorityIndex(merged)
		trimmed++
		merged = append(merged[:drop], merged[drop+1:]...)
		final = final[:0]
		for _, item := range merged {
			final = append(final, item.evidence)
		}
		assignEvidenceIDs(final)
	}

	warnings := []string{}
	if trimmed > 0 {
		warnings = append(warnings, "evidence_budget_trimmed")
	}
	return final, trimmed, warnings
}

func lowestPriorityIndex(items []weightedEvidence) int {
	if len(items) == 0 {
		return 0
	}
	index := 0
	for i := 1; i < len(items); i++ {
		if items[i].priority < items[index].priority || (items[i].priority == items[index].priority && items[i].sequence > items[index].sequence) {
			index = i
		}
	}
	return index
}

func assignEvidenceIDs(items []Evidence) {
	for i := range items {
		items[i].ID = fmt.Sprintf("E%d", i+1)
	}
}

func serializedEvidenceSize(items []Evidence) int {
	raw, err := json.Marshal(items)
	if err != nil {
		return MaxEvidenceBytes + 1
	}
	return len(raw)
}

func (s *Service) queryIntent(ctx context.Context, intent Intent) ([]weightedEvidence, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch intent.Name {
	case IntentReviewExplain:
		if intent.Code != "" {
			return s.queryReviewEvent(ctx, intent)
		}
		return s.queryReviewDay(ctx, intent, true)
	case IntentReportQuery:
		return s.queryReviewDay(ctx, intent, false)
	case IntentStockEvidence:
		return s.queryReviewEvent(ctx, intent)
	case IntentExperimentQuery:
		return s.queryExperiments(ctx, intent)
	case IntentMethodology, IntentClarify, IntentUnsupported:
		return nil, nil
	default:
		return nil, errors.New("unsupported advisor intent")
	}
}

func (s *Service) queryReviewDay(ctx context.Context, intent Intent, anomaliesOnly bool) ([]weightedEvidence, error) {
	if s.reviews == nil {
		return nil, errors.New("review source is unavailable")
	}
	day, err := s.reviews.Day(ctx, intent.AsOf, intent.Revision)
	if err != nil {
		return nil, err
	}
	return reviewDayEvidence(day, intent, anomaliesOnly), nil
}

func (s *Service) queryReviewEvent(ctx context.Context, intent Intent) ([]weightedEvidence, error) {
	if s.reviews == nil {
		return nil, errors.New("review source is unavailable")
	}
	event, err := s.reviews.Event(ctx, intent.AsOf, intent.Code, intent.Revision)
	if errors.Is(err, fs.ErrNotExist) {
		return []weightedEvidence{makeEvidence(intent.Name, "not_found", "review.event", intent.AsOf, intent.Code, "", map[string]any{
			"message": "no matching review event for this code and date",
		}, 55)}, nil
	}
	if err != nil {
		return nil, err
	}
	return reviewEventEvidence(event, intent.Name), nil
}

func (s *Service) queryExperiments(ctx context.Context, intent Intent) ([]weightedEvidence, error) {
	if s.experiments == nil {
		return nil, errors.New("experiment source is unavailable")
	}
	if intent.ExperimentID != "" {
		record, err := s.experiments.Get(ctx, intent.ExperimentID)
		if errors.Is(err, fs.ErrNotExist) {
			return []weightedEvidence{makeEvidence(intent.Name, "not_found", "experiments.get", intent.AsOf, "", "", map[string]any{
				"experiment_id": intent.ExperimentID,
				"message":       "experiment record not found",
			}, 55)}, nil
		}
		if err != nil {
			return nil, err
		}
		return []weightedEvidence{experimentEvidence(record, intent.Name, "experiments.get")}, nil
	}

	records, err := s.experiments.List(ctx)
	if err != nil {
		return nil, err
	}
	listAsOf := intent.AsOf
	if len(records) > 0 && records[0].Data.AsOf != "" {
		listAsOf = records[0].Data.AsOf
	}
	items := []weightedEvidence{makeEvidence(intent.Name, "experiment_list", "experiments.list", "", "", "", map[string]any{
		"count":     len(records),
		"truncated": len(records) > 10,
	}, 70)}
	items[0].evidence.AsOf = listAsOf
	if len(records) > 10 {
		records = records[:10]
	}
	for _, record := range records {
		items = append(items, experimentEvidence(record, intent.Name, "experiments.list"))
	}
	return items, nil
}

func reviewDayEvidence(day review.Day, intent Intent, anomaliesOnly bool) []weightedEvidence {
	items := []weightedEvidence{
		makeEvidence(intent.Name, "report_metadata", "review.day", day.AsOf, "", "", map[string]any{
			"schema_version":      day.Version,
			"target_trading_date": day.Target,
			"revision":            day.Revision,
			"generated_at":        formatTime(day.GeneratedAt),
			"data_as_of":          day.DataAsOf,
			"strategy":            day.Strategy,
			"calendar_from":       day.CalendarFrom,
			"calendar_through":    day.CalendarThrough,
			"calendar_source":     day.CalendarSource,
			"revision_count":      len(day.Revisions),
			"warning_count":       len(day.Warnings),
			"warnings":            limitStrings(day.Warnings, 5),
		}, 65),
		makeEvidence(intent.Name, "aggregate", "review.day", day.AsOf, "", "", map[string]any{
			"all":         metricsFacts(day.Aggregate.All),
			"recommended": metricsFacts(day.Aggregate.Recommended),
			"observed":    metricsFacts(day.Aggregate.Observed),
		}, 72),
	}

	summaries := append([]review.EventSummary(nil), day.Events...)
	sort.SliceStable(summaries, func(i, j int) bool {
		pi, pj := summaryPriority(summaries[i]), summaryPriority(summaries[j])
		if pi != pj {
			return pi > pj
		}
		return summaries[i].Rank < summaries[j].Rank
	})
	added := 0
	for _, summary := range summaries {
		priority := summaryPriority(summary)
		if anomaliesOnly && priority < 80 {
			continue
		}
		if !anomaliesOnly && added >= 8 && priority < 80 {
			continue
		}
		items = append(items, makeEvidence(intent.Name, "event", "review.day", day.AsOf, summary.Code, summary.Name, summaryFacts(summary), priority))
		added++
	}
	return items
}

func summaryPriority(summary review.EventSummary) int {
	if summary.State == review.StateMissingData || summary.State == review.StateUnsupported {
		return 100
	}
	if summary.State == review.StateNotEntered {
		return 94
	}
	if summary.State == review.StatePending {
		return 76
	}
	if summary.Ambiguous {
		return 96
	}
	if summary.NetPct != nil && *summary.NetPct < 0 {
		return 95
	}
	if summary.ExitReason == "stop" || summary.ExitReason == "stop_gap" || summary.ExitReason == "loss_breach" {
		return 93
	}
	if summary.State != review.StateMature {
		return 85
	}
	return 45
}

func summaryFacts(summary review.EventSummary) map[string]any {
	facts := map[string]any{
		"state":            summary.State,
		"signal_date":      summary.SignalDate,
		"recommended":      summary.Recommended,
		"rank":             summary.Rank,
		"ambiguous":        summary.Ambiguous,
		"checkpoint_count": len(summary.Checkpoints),
	}
	if summary.StateReason != "" {
		facts["state_reason"] = summary.StateReason
	}
	if summary.TargetDate != "" {
		facts["target_trading_date"] = summary.TargetDate
	}
	if summary.EntryDate != "" {
		facts["entry_date"] = summary.EntryDate
	}
	if summary.ExitDate != "" {
		facts["exit_date"] = summary.ExitDate
	}
	if summary.ExitReason != "" {
		facts["exit_reason"] = summary.ExitReason
	}
	if summary.ObservationRank > 0 {
		facts["observation_rank"] = summary.ObservationRank
	}
	if summary.EligibleRank > 0 {
		facts["eligible_rank"] = summary.EligibleRank
	}
	if summary.NetPct != nil {
		facts["net_pct"] = *summary.NetPct
	}
	if summary.MFE != nil {
		facts["max_favorable_net_pct"] = *summary.MFE
	}
	if summary.MAE != nil {
		facts["max_adverse_net_pct"] = *summary.MAE
	}
	return facts
}

func reviewEventEvidence(event review.Event, intent string) []weightedEvidence {
	items := []weightedEvidence{
		makeEvidence(intent, "event", "review.event", event.SignalDate, event.Code, event.Name, mergeFacts(summaryFacts(event.EventSummary), map[string]any{
			"report_revision":     event.ReportRevision,
			"report_generated_at": formatTime(event.ReportGenerated),
		}), summaryPriority(event.EventSummary)),
	}
	if event.Outcome != nil {
		priority := 88
		if event.Outcome.NetPct < 0 || event.Outcome.Status == "stop" || event.Outcome.Status == "stop_gap" {
			priority = 100
		}
		items = append(items, makeEvidence(intent, "outcome", "review.event", event.SignalDate, event.Code, event.Name, map[string]any{
			"status":                event.Outcome.Status,
			"entry_date":            event.Outcome.EntryDate,
			"exit_date":             event.Outcome.ExitDate,
			"exit_price":            event.Outcome.ExitPrice,
			"net_pct":               event.Outcome.NetPct,
			"holding_days":          event.Outcome.HoldingDays,
			"max_favorable_net_pct": event.Outcome.MFE,
			"max_adverse_net_pct":   event.Outcome.MAE,
			"ambiguous":             event.Outcome.Ambiguous,
		}, priority))
	}
	if event.SignalBar != nil {
		items = append(items, makeEvidence(intent, "signal_bar", "review.event", event.SignalBar.Date, event.Code, event.Name, map[string]any{
			"open":   event.SignalBar.Open,
			"close":  event.SignalBar.Close,
			"high":   event.SignalBar.High,
			"low":    event.SignalBar.Low,
			"volume": event.SignalBar.Volume,
		}, 62))
	}
	if event.Plan != nil {
		items = append(items, makeEvidence(intent, "reference_plan", "review.event", event.SignalDate, event.Code, event.Name, map[string]any{
			"entry_price":        event.Plan.EntryPrice,
			"shares":             event.Plan.Shares,
			"buy_cost":           event.Plan.BuyCost,
			"target_net_3_price": event.Plan.Target30,
			"stop_price":         event.Plan.StopPrice,
			"reference_only":     event.Plan.ReferenceOnly,
		}, 60))
	}
	if event.StructureGuard != nil {
		items = append(items, makeEvidence(intent, "structure_guard", "review.event", event.StructureGuard.AsOf, event.Code, event.Name, map[string]any{
			"mode":          event.StructureGuard.Mode,
			"data_status":   event.StructureGuard.DataStatus,
			"guard_status":  event.StructureGuard.GuardStatus,
			"signal_status": event.StructureGuard.SignalStatus,
			"reason_code":   event.StructureGuard.ReasonCode,
			"reason":        event.StructureGuard.Reason,
		}, 78))
	}
	if len(event.Risks) > 0 {
		items = append(items, makeEvidence(intent, "risks", "review.event", event.SignalDate, event.Code, event.Name, map[string]any{
			"items": limitStrings(event.Risks, 6),
			"count": len(event.Risks),
		}, 74))
	}
	if len(event.Reasons) > 0 || len(event.Rules) > 0 {
		items = append(items, makeEvidence(intent, "selection_reasons", "review.event", event.SignalDate, event.Code, event.Name, map[string]any{
			"reasons":       limitStrings(event.Reasons, 6),
			"matched_rules": limitStrings(event.Rules, 6),
		}, 58))
	}
	if len(event.Missing) > 0 {
		items = append(items, makeEvidence(intent, "missing_data", "review.event", event.SignalDate, event.Code, event.Name, map[string]any{
			"items": limitStrings(event.Missing, 10),
			"count": len(event.Missing),
		}, 98))
	}
	return items
}

func experimentEvidence(record experiment.Record, intent, source string) weightedEvidence {
	facts := map[string]any{
		"experiment_id": record.ID,
		"status":        record.Status,
		"verdict":       record.Verdict,
		"stage":         record.Stage,
		"engine":        record.Engine,
		"created_at":    formatTime(record.CreatedAt),
		"title":         record.Spec.Title,
		"direction":     record.Spec.Direction,
		"hypothesis":    record.Spec.Hypothesis,
		"data_as_of":    record.Data.AsOf,
		"pool_size":     record.Data.PoolSize,
		"included":      record.Data.Included,
	}
	if record.ParentID != "" {
		facts["parent_id"] = record.ParentID
	}
	if record.FinishedAt != nil {
		facts["finished_at"] = formatTime(*record.FinishedAt)
	}
	if record.Holdout != nil {
		facts["holdout"] = map[string]any{"from": record.Holdout.From, "through": record.Holdout.Through, "contaminated": record.Holdout.Contaminated}
	}
	if record.Review != nil {
		facts["review"] = map[string]any{
			"risk_status":   record.Review.Risk.Status,
			"joint_verdict": record.Review.JointVerdict,
			"next_day":      record.Review.NextDayVerdict,
			"samples":       record.Review.Stats.Samples,
			"resolved":      record.Review.Stats.Resolved,
			"target_rate":   record.Review.Stats.HitRate,
			"mean_net_pct":  record.Review.Stats.MeanNet,
			"worst_mae_pct": record.Review.Stats.WorstMAE,
		}
	}
	return makeEvidence(intent, "experiment", source, record.Data.AsOf, "", "", facts, 82)
}

func metricsFacts(metrics review.Metrics) map[string]any {
	facts := map[string]any{
		"total":       metrics.Total,
		"mature":      metrics.Mature,
		"pending":     metrics.Pending,
		"not_entered": metrics.NotEntered,
		"missing":     metrics.Missing,
		"unsupported": metrics.Unsupported,
		"entered":     metrics.Entered,
		"target":      metrics.Target,
		"stop":        metrics.Stop,
		"stop_gap":    metrics.StopGap,
		"timeout":     metrics.Timeout,
		"positive":    metrics.Positive,
		"loss_breach": metrics.LossBreach,
		"ambiguous":   metrics.Ambiguous,
	}
	if metrics.MeanNetPct != nil {
		facts["mean_net_pct"] = *metrics.MeanNetPct
	}
	if metrics.PositiveRate != nil {
		facts["positive_rate"] = *metrics.PositiveRate
	}
	if metrics.WorstMAE != nil {
		facts["worst_max_adverse_net_pct"] = *metrics.WorstMAE
	}
	return facts
}

func makeEvidence(intent, kind, source, asOf, code, name string, facts map[string]any, priority int) weightedEvidence {
	return weightedEvidence{evidence: Evidence{
		Intent: intent,
		Kind:   kind,
		Source: source,
		AsOf:   asOf,
		Code:   code,
		Name:   name,
		Facts:  compactFacts(facts),
	}, priority: priority}
}

func compactFacts(facts map[string]any) map[string]any {
	out := make(map[string]any, len(facts))
	for key, value := range facts {
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				out[key] = typed
			}
		case []string:
			if len(typed) > 0 {
				out[key] = typed
			}
		case map[string]any:
			if len(typed) > 0 {
				out[key] = typed
			}
		case nil:
			continue
		default:
			out[key] = value
		}
	}
	return out
}

func mergeFacts(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for key, value := range a {
		out[key] = value
	}
	for key, value := range b {
		out[key] = value
	}
	return out
}

func limitStrings(values []string, limit int) []string {
	if len(values) <= limit {
		return append([]string(nil), values...)
	}
	return append([]string(nil), values[:limit]...)
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}
