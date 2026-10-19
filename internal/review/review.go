// Package review projects frozen recommendation reports against later local
// market data. It never recomputes selection, writes reports, or sends alerts.
package review

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"io/fs"
	"math"
	"time"
)

const Version = 1

// Revision is one archived report revision for a signal date.
type Revision struct {
	ID            string    `json:"revision"`
	AsOf          string    `json:"as_of"`
	GeneratedAt   time.Time `json:"generated_at"`
	SchemaVersion int       `json:"schema_version"`
}

// CalendarWindow is the exact exchange calendar used by a projection.
type CalendarWindow struct {
	From    string
	Through string
	Days    []string
	Source  string
}

// Loader supplies immutable report inputs and current local market windows.
// Implementations must not mutate their sources.
type Loader interface {
	ListRevisions(context.Context, string) ([]Revision, error)
	LoadReport(context.Context, string, string) (selection.Report, error)
	LoadInputs(context.Context, selection.Report) (map[string]market.History, error)
	LoadLatestMarkets(context.Context, []string) (map[string]market.History, string, error)
	LoadCalendar(context.Context, string, string) (CalendarWindow, error)
}

// Service builds deterministic read-time review projections.
type Service struct {
	loader Loader
}

func New(loader Loader) *Service {
	return &Service{loader: loader}
}

type EventState string

const (
	StateMature      EventState = "mature"
	StatePending     EventState = "pending"
	StateNotEntered  EventState = "not_entered"
	StateMissingData EventState = "missing_data"
	StateUnsupported EventState = "unsupported"
)

// Evidence is a stable, minimal fact used by the review UI and the advisor.
type Evidence struct {
	ID     string `json:"evidence_id"`
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"`
	AsOf   string `json:"as_of,omitempty"`
	Value  any    `json:"value"`
}

type EventSummary struct {
	Code                string                `json:"code"`
	Name                string                `json:"name"`
	Rank                int                   `json:"rank"`
	ObservationRank     int                   `json:"observation_rank,omitempty"`
	EligibleRank        int                   `json:"eligible_rank,omitempty"`
	Recommended         bool                  `json:"recommended"`
	DailyRecommended    bool                  `json:"daily_recommended,omitempty"`
	EligibleRecommended bool                  `json:"eligible_recommended,omitempty"`
	State               EventState            `json:"state"`
	StateReason         string                `json:"state_reason,omitempty"`
	SignalDate          string                `json:"signal_date"`
	TargetDate          string                `json:"target_trading_date,omitempty"`
	EntryDate           string                `json:"entry_date,omitempty"`
	ExitDate            string                `json:"exit_date,omitempty"`
	ExitReason          string                `json:"exit_reason,omitempty"`
	NetPct              *float64              `json:"net_pct,omitempty"`
	MFE                 *float64              `json:"max_favorable_net_pct,omitempty"`
	MAE                 *float64              `json:"max_adverse_net_pct,omitempty"`
	Ambiguous           bool                  `json:"ambiguous,omitempty"`
	Checkpoints         []strategy.Checkpoint `json:"checkpoints,omitempty"`
}

type Event struct {
	EventSummary
	ReportRevision  string                           `json:"report_revision,omitempty"`
	ReportGenerated time.Time                        `json:"report_generated_at,omitempty"`
	Plan            *strategy.Plan                   `json:"reference_plan,omitempty"`
	SignalBar       *market.Bar                      `json:"signal_bar,omitempty"`
	Outcome         *strategy.Outcome                `json:"outcome,omitempty"`
	StructureGuard  *strategy.StructureGuardEvidence `json:"structure_guard,omitempty"`
	Risks           []string                         `json:"risks,omitempty"`
	Reasons         []string                         `json:"reasons,omitempty"`
	Rules           []string                         `json:"matched_rules,omitempty"`
	Evidence        []Evidence                       `json:"evidence"`
	Missing         []string                         `json:"missing,omitempty"`
}

type Metrics struct {
	Total       int `json:"total"`
	Mature      int `json:"mature"`
	Pending     int `json:"pending"`
	NotEntered  int `json:"not_entered"`
	Missing     int `json:"missing"`
	Unsupported int `json:"unsupported"`
	Entered     int `json:"entered"`
	Target      int `json:"target"`
	Stop        int `json:"stop"`
	StopGap     int `json:"stop_gap"`
	Timeout     int `json:"timeout"`
	Positive    int `json:"positive"`
	LossBreach  int `json:"loss_breach"`
	Ambiguous   int `json:"ambiguous"`

	MeanNetPct   *float64 `json:"mean_net_pct,omitempty"`
	PositiveRate *float64 `json:"positive_rate,omitempty"`
	WorstMAE     *float64 `json:"worst_max_adverse_net_pct,omitempty"`
}

type Aggregate struct {
	All         Metrics `json:"all"`
	Recommended Metrics `json:"recommended"`
	Observed    Metrics `json:"observed"`
}

type Day struct {
	Version         int            `json:"schema_version"`
	AsOf            string         `json:"as_of"`
	Target          string         `json:"target_trading_date,omitempty"`
	GeneratedAt     time.Time      `json:"generated_at,omitempty"`
	Revision        string         `json:"revision,omitempty"`
	Revisions       []Revision     `json:"revisions"`
	DataAsOf        string         `json:"data_as_of,omitempty"`
	CalendarFrom    string         `json:"calendar_from,omitempty"`
	CalendarThrough string         `json:"calendar_through,omitempty"`
	CalendarSource  string         `json:"calendar_source,omitempty"`
	Strategy        string         `json:"strategy,omitempty"`
	Aggregate       Aggregate      `json:"aggregate"`
	Events          []EventSummary `json:"events"`
	Warnings        []string       `json:"warnings,omitempty"`
}

func (s *Service) Day(ctx context.Context, asof, revision string) (Day, error) {
	day, _, err := s.project(ctx, asof, revision)
	return day, err
}

func (s *Service) Event(ctx context.Context, asof, code, revision string) (Event, error) {
	_, events, err := s.project(ctx, asof, revision)
	if err != nil {
		return Event{}, err
	}
	for _, event := range events {
		if event.Code == code {
			return event, nil
		}
	}
	return Event{}, fs.ErrNotExist
}

func (s *Service) project(ctx context.Context, asof, revision string) (Day, []Event, error) {
	if s == nil || s.loader == nil {
		return Day{}, nil, errors.New("review loader is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return Day{}, nil, err
	}
	report, err := s.loader.LoadReport(ctx, asof, revision)
	if err != nil {
		return Day{}, nil, err
	}
	if report.AsOf != asof {
		return Day{}, nil, errors.New("report date does not match review date")
	}
	revisions, err := s.loader.ListRevisions(ctx, asof)
	if err != nil {
		return Day{}, nil, err
	}
	day := Day{Version: Version, AsOf: asof, Target: report.Target, GeneratedAt: report.GeneratedAt, Revision: report.Revision, Revisions: revisions, Strategy: report.Strategy}
	candidates := reportCandidates(report)
	if len(candidates) == 0 {
		day.Events = []EventSummary{}
		return day, nil, nil
	}
	if report.Version == 1 || report.Strategy != "short_horizon_v1" {
		day.Warnings = append(day.Warnings, "legacy_report_has_no_execution_review")
		events := unsupportedEvents(report, candidates, "legacy_report_has_no_execution_review")
		day.Aggregate = aggregate(events)
		day.Events = summaries(events)
		return day, events, nil
	}
	params, err := reportParameters(report)
	if err != nil {
		day.Warnings = append(day.Warnings, err.Error())
		events := unsupportedEvents(report, candidates, err.Error())
		day.Aggregate = aggregate(events)
		day.Events = summaries(events)
		return day, events, nil
	}
	codes := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		codes = append(codes, candidate.Code)
	}
	inputs, err := s.loader.LoadInputs(ctx, report)
	if errors.Is(err, fs.ErrNotExist) {
		inputs, err = nil, nil
	}
	if err != nil {
		return Day{}, nil, err
	}
	latest, dataAsOf, err := s.loader.LoadLatestMarkets(ctx, codes)
	if err != nil {
		return Day{}, nil, err
	}
	day.DataAsOf = dataAsOf
	var calendarWindow CalendarWindow
	var calendarErr error
	if dataAsOf > report.AsOf {
		calendarWindow, calendarErr = s.loader.LoadCalendar(ctx, report.AsOf, dataAsOf)
	} else {
		calendarErr = fs.ErrNotExist
	}
	if calendarErr != nil {
		day.Warnings = append(day.Warnings, "calendar_unavailable")
	}
	day.CalendarFrom, day.CalendarThrough, day.CalendarSource = calendarWindow.From, calendarWindow.Through, calendarWindow.Source
	calendarObject, calendarBuildErr := buildCalendar(calendarWindow)
	events := make([]Event, 0, len(candidates))
	for i, candidate := range candidates {
		event := Event{EventSummary: summaryFor(report, candidate, i+1), ReportRevision: report.Revision, ReportGenerated: report.GeneratedAt, Plan: candidate.Plan, Risks: append([]string(nil), candidate.Risks...), Reasons: append([]string(nil), candidate.Reasons...), Rules: append([]string(nil), candidate.Rules...), StructureGuard: candidate.StructureGuard}
		event.Evidence = append(event.Evidence, reportEvidence(report), candidateEvidence(candidate))
		input, ok := inputs[candidate.Code]
		if !ok {
			event.State, event.StateReason = StateUnsupported, "frozen_report_input_missing"
			event.Missing = append(event.Missing, "frozen_signal_bar")
			event.Evidence = append(event.Evidence, missingEvidence("frozen_signal_bar", candidate.Code))
			events = append(events, event)
			continue
		}
		signal, ok := findBar(input.Bars, report.AsOf)
		if !ok {
			event.State, event.StateReason = StateMissingData, "signal_bar_missing"
			event.Missing = append(event.Missing, "signal_bar")
			event.Evidence = append(event.Evidence, missingEvidence("signal_bar", candidate.Code))
			events = append(events, event)
			continue
		}
		event.SignalBar = &signal
		event.Evidence = append(event.Evidence, signalEvidence(signal))
		history, ok := latest[candidate.Code]
		if !ok {
			event.State, event.StateReason = StateMissingData, "current_market_history_missing"
			event.Missing = append(event.Missing, "current_market_history")
			event.Evidence = append(event.Evidence, missingEvidence("current_market_history", candidate.Code))
			events = append(events, event)
			continue
		}
		future := barsAfter(history.Bars, report.AsOf)
		if len(future) == 0 {
			event.State, event.StateReason = StatePending, "waiting_for_next_session"
			event.Missing = append(event.Missing, "future_sessions")
			events = append(events, event)
			continue
		}
		if calendarErr != nil || calendarBuildErr != nil || calendarObject == nil {
			event.State, event.StateReason = StateMissingData, "calendar_unavailable"
			event.Missing = append(event.Missing, "trading_calendar")
			event.Evidence = append(event.Evidence, missingEvidence("trading_calendar", candidate.Code))
			events = append(events, event)
			continue
		}
		outcome, simErr := strategy.Simulate(params.Strategy, calendarObject, candidate.Code, signal, future, params.Strategy.MaxHoldingDays)
		if simErr != nil {
			event.State, event.StateReason = StateMissingData, simReason(simErr)
			event.Missing = append(event.Missing, event.StateReason)
			event.Evidence = append(event.Evidence, missingEvidence(event.StateReason, candidate.Code))
			events = append(events, event)
			continue
		}
		event.Outcome = &outcome
		event.EntryDate, event.ExitDate, event.ExitReason = outcome.EntryDate, outcome.ExitDate, outcome.Status
		event.Checkpoints = append([]strategy.Checkpoint(nil), outcome.Checkpoints...)
		event.Evidence = append(event.Evidence, outcomeEvidence(outcome))
		if len(outcome.Checkpoints) > 0 {
			event.Evidence = append(event.Evidence, checkpointEvidence(outcome.Checkpoints))
		}
		if candidate.StructureGuard != nil {
			event.Evidence = append(event.Evidence, structureEvidence(*candidate.StructureGuard))
		}
		if outcome.EntryDate != "" {
			if outcome.ExitDate != "" {
				event.State = StateMature
				event.NetPct = floatPtr(outcome.NetPct)
				event.MFE = floatPtr(outcome.MFE)
				event.MAE = floatPtr(outcome.MAE)
				event.Ambiguous = outcome.Ambiguous
			} else {
				event.State, event.StateReason = StatePending, "horizon_not_reached"
				event.MFE = floatPtr(outcome.MFE)
				event.MAE = floatPtr(outcome.MAE)
				event.Missing = append(event.Missing, "future_sessions")
			}
			events = append(events, event)
			continue
		}
		event.State, event.StateReason = outcomeState(outcome.Status)
		if event.State == StateMissingData {
			event.Missing = append(event.Missing, event.StateReason)
		}
		events = append(events, event)
	}
	day.Aggregate = aggregate(events)
	day.Events = summaries(events)
	return day, events, nil
}

func reportCandidates(report selection.Report) []strategy.Candidate {
	if len(report.Ranking) > 0 {
		return report.Ranking
	}
	out := make([]strategy.Candidate, 0, len(report.Selected))
	for _, candidate := range report.Selected {
		out = append(out, strategy.Candidate{Code: candidate.Code, Name: candidate.Name, Recommended: true})
	}
	return out
}

func summaryFor(report selection.Report, candidate strategy.Candidate, fallbackRank int) EventSummary {
	rank := candidate.Rank
	if rank == 0 {
		rank = fallbackRank
	}
	return EventSummary{Code: candidate.Code, Name: candidate.Name, Rank: rank, ObservationRank: candidate.ObservationRank, EligibleRank: candidate.EligibleRank, Recommended: candidate.Recommended, DailyRecommended: candidate.DailyRecommended, EligibleRecommended: candidate.EligibleRecommended, SignalDate: report.AsOf, TargetDate: report.Target}
}

func unsupportedEvents(report selection.Report, candidates []strategy.Candidate, reason string) []Event {
	out := make([]Event, 0, len(candidates))
	for i, candidate := range candidates {
		summary := summaryFor(report, candidate, i+1)
		summary.State, summary.StateReason = StateUnsupported, reason
		out = append(out, Event{EventSummary: summary, ReportRevision: report.Revision, ReportGenerated: report.GeneratedAt, Risks: append([]string(nil), candidate.Risks...), Reasons: append([]string(nil), candidate.Reasons...), Rules: append([]string(nil), candidate.Rules...), Evidence: []Evidence{reportEvidence(report), candidateEvidence(candidate)}})
	}
	return out
}

func reportParameters(report selection.Report) (config.Config, error) {
	if report.Provenance == nil || len(report.Provenance.Parameters) == 0 {
		return config.Config{}, errors.New("frozen_report_parameters_missing")
	}
	var params config.Config
	if err := json.Unmarshal(report.Provenance.Parameters, &params); err != nil {
		return config.Config{}, errors.New("frozen_report_parameters_invalid")
	}
	if err := params.Strategy.Validate(); err != nil {
		return config.Config{}, errors.New("frozen_strategy_parameters_invalid")
	}
	if params.Strategy.MaxHoldingDays < 1 {
		return config.Config{}, errors.New("frozen_strategy_horizon_invalid")
	}
	if digest, _ := selection.ConfigDigest(params); report.Provenance.ConfigID != "" && digest != report.Provenance.ConfigID {
		return config.Config{}, errors.New("frozen_report_parameter_digest_mismatch")
	}
	return params, nil
}

func buildCalendar(window CalendarWindow) (*calendar.Calendar, error) {
	if window.From == "" || window.Through == "" || len(window.Days) == 0 {
		return nil, fs.ErrNotExist
	}
	var c config.Config
	c.Calendar.ValidFrom = window.From
	c.Calendar.ValidThrough = window.Through
	c.Calendar.TradingDays = append([]string(nil), window.Days...)
	c.Calendar.Source = window.Source
	return calendar.New(c), nil
}

func findBar(bars []market.Bar, date string) (market.Bar, bool) {
	for i := len(bars) - 1; i >= 0; i-- {
		if bars[i].Date == date {
			return bars[i], true
		}
	}
	return market.Bar{}, false
}

func barsAfter(bars []market.Bar, date string) []market.Bar {
	out := make([]market.Bar, 0, len(bars))
	for _, bar := range bars {
		if bar.Date > date {
			out = append(out, bar)
		}
	}
	return out
}

func outcomeState(status string) (EventState, string) {
	switch status {
	case "target", "stop", "stop_gap", "timeout":
		return StateMature, status
	case "unresolved":
		return StatePending, "horizon_not_reached"
	case "entry_skipped", "cash_insufficient", "risk_budget_insufficient":
		return StateNotEntered, status
	case "missing_entry_session", "missing_exit_session":
		return StateMissingData, status
	default:
		return StateMissingData, status
	}
}

func simReason(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "simulation_failed"
}

func summaries(events []Event) []EventSummary {
	out := make([]EventSummary, 0, len(events))
	for _, event := range events {
		out = append(out, event.EventSummary)
	}
	return out
}

func aggregate(events []Event) Aggregate {
	recommended := make([]Event, 0, len(events))
	observed := make([]Event, 0, len(events))
	for _, event := range events {
		if event.Recommended {
			recommended = append(recommended, event)
		} else {
			observed = append(observed, event)
		}
	}
	return Aggregate{All: metricsFor(events), Recommended: metricsFor(recommended), Observed: metricsFor(observed)}
}

func metricsFor(events []Event) Metrics {
	var m Metrics
	var netSum float64
	worstMAE := 0.0
	hasMAE := false
	for _, event := range events {
		m.Total++
		switch event.State {
		case StateMature:
			m.Mature++
			m.Entered++
			switch event.ExitReason {
			case "target":
				m.Target++
			case "stop":
				m.Stop++
			case "stop_gap":
				m.StopGap++
			case "timeout":
				m.Timeout++
			}
			if event.NetPct != nil {
				netSum += *event.NetPct
				m.Positive += boolInt(*event.NetPct > 0)
				m.LossBreach += boolInt(strategy.BreachesLossLimit(*event.NetPct))
			}
			if event.MAE != nil {
				if !hasMAE || *event.MAE < worstMAE {
					worstMAE = *event.MAE
				}
				hasMAE = true
			}
			m.Ambiguous += boolInt(event.Ambiguous)
		case StatePending:
			m.Pending++
			m.Entered += boolInt(event.EntryDate != "")
		case StateNotEntered:
			m.NotEntered++
		case StateMissingData:
			m.Missing++
		case StateUnsupported:
			m.Unsupported++
		}
	}
	if m.Mature > 0 {
		mean := netSum / float64(m.Mature)
		rate := float64(m.Positive) / float64(m.Mature)
		m.MeanNetPct = &mean
		m.PositiveRate = &rate
	}
	if hasMAE {
		m.WorstMAE = &worstMAE
	}
	return m
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func floatPtr(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func reportEvidence(report selection.Report) Evidence {
	return Evidence{ID: "E1", Kind: "report", Source: "selection_run", AsOf: report.AsOf, Value: map[string]any{"as_of": report.AsOf, "target_trading_date": report.Target, "generated_at": report.GeneratedAt, "revision": report.Revision, "strategy": report.Strategy, "schema_version": report.Version}}
}

func candidateEvidence(candidate strategy.Candidate) Evidence {
	return Evidence{ID: "E2", Kind: "candidate", Source: "selection_report", Value: map[string]any{"code": candidate.Code, "rank": candidate.Rank, "observation_rank": candidate.ObservationRank, "eligible_rank": candidate.EligibleRank, "recommended": candidate.Recommended, "opportunity_score": candidate.Score, "risks": candidate.Risks, "reasons": candidate.Reasons, "matched_rules": candidate.Rules}}
}

func signalEvidence(bar market.Bar) Evidence {
	return Evidence{ID: "E3", Kind: "signal_bar", Source: "frozen_report_input", AsOf: bar.Date, Value: map[string]any{"date": bar.Date, "open": bar.Open, "high": bar.High, "low": bar.Low, "close": bar.Close, "volume": bar.Volume}}
}

func outcomeEvidence(out strategy.Outcome) Evidence {
	return Evidence{ID: "E4", Kind: "outcome", Source: "strategy_simulate", AsOf: out.ExitDate, Value: map[string]any{"status": out.Status, "entry_date": out.EntryDate, "exit_date": out.ExitDate, "exit_price": out.ExitPrice, "net_pct": out.NetPct, "holding_days": out.HoldingDays, "max_favorable_net_pct": out.MFE, "max_adverse_net_pct": out.MAE, "ambiguous": out.Ambiguous}}
}

func checkpointEvidence(points []strategy.Checkpoint) Evidence {
	return Evidence{ID: "E5", Kind: "checkpoints", Source: "strategy_simulate", Value: points}
}

func structureEvidence(guard strategy.StructureGuardEvidence) Evidence {
	return Evidence{ID: "E6", Kind: "structure_guard", Source: "selection_report", Value: guard}
}

func missingEvidence(kind, code string) Evidence {
	return Evidence{ID: "E7", Kind: "missing", Source: "review", Value: map[string]string{"code": code, "missing": kind}}
}
