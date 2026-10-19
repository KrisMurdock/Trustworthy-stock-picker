package advisor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/llm"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/review"
)

const (
	// Version is the public advisor contract version.
	Version = 1

	// MaxSubIntents bounds independent questions handled in one turn.
	MaxSubIntents = 3
	// MaxEvidencePerIntent bounds evidence copied from one query plan.
	MaxEvidencePerIntent = 12
	// MaxEvidence bounds merged evidence sent to the model.
	MaxEvidence = 30
	// MaxEvidenceBytes bounds the serialized evidence array.
	MaxEvidenceBytes = 32 << 10
)

const (
	// ModeChat is the normal dashboard conversation mode.
	ModeChat = "chat"
	// ModeReview requests the fixed review-attribution answer structure.
	ModeReview = "review"
)

const (
	// IntentReviewExplain explains an archived recommendation outcome.
	IntentReviewExplain = "review_explain"
	// IntentReportQuery reads report metadata, aggregate, and representative events.
	IntentReportQuery = "report_query"
	// IntentStockEvidence reads one stock's frozen review event.
	IntentStockEvidence = "stock_evidence"
	// IntentExperimentQuery reads experiment archive metadata.
	IntentExperimentQuery = "experiment_query"
	// IntentMethodology answers a conceptual method question without market facts.
	IntentMethodology = "methodology"
	// IntentClarify asks one focused question before querying evidence.
	IntentClarify = "clarify"
	// IntentUnsupported rejects a request outside the read-only contract.
	IntentUnsupported = "unsupported"
)

const (
	// EventIntent reports validated sub-intents and classification provenance.
	EventIntent = "intent"
	// EventPlan reports the fixed query plan. It never contains SQL.
	EventPlan = "plan"
	// EventEvidence reports the minimal evidence bundle after budgeting.
	EventEvidence = "evidence"
	// EventPrompt reports only prompt and evidence summary metadata.
	EventPrompt = "prompt"
	// EventDelta carries one streamed answer fragment.
	EventDelta = "delta"
	// EventCitation reports valid and invalid evidence citations.
	EventCitation = "citation"
	// EventDone reports successful completion, including partial answers.
	EventDone = "done"
	// EventError reports a stable failure code and a sanitized message.
	EventError = "error"
)

var (
	// ErrLLMNotConfigured indicates that answer generation needs a configured client.
	ErrLLMNotConfigured = errors.New("llm_not_configured")
	// ErrAllIntentsFailed indicates that no queried sub-intent produced usable evidence.
	ErrAllIntentsFailed = errors.New("all_intents_failed")
	// ErrInvalidMode indicates a mode other than chat or review.
	ErrInvalidMode = errors.New("invalid_mode")
	// ErrUnknownRole indicates an unknown role ID.
	ErrUnknownRole = errors.New("unknown_role")
	// ErrInvalidRequest indicates a malformed or excessively large request.
	ErrInvalidRequest = errors.New("invalid_request")
	// ErrGenerationFailed is the sanitized error for a non-cancellation model failure.
	ErrGenerationFailed = errors.New("llm_request_failed")
)

// ReviewSource is the deterministic review projection used as evidence.
type ReviewSource interface {
	Day(context.Context, string, string) (review.Day, error)
	Event(context.Context, string, string, string) (review.Event, error)
}

// ExperimentSource is the read-only experiment archive used as evidence.
type ExperimentSource interface {
	List(context.Context) ([]experiment.Record, error)
	Get(context.Context, string) (experiment.Record, error)
}

// Turn is one prior visible conversation message. Hidden reasoning is never accepted.
type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is one dashboard advisor turn.
type Request struct {
	Mode     string `json:"mode,omitempty"`
	Role     string `json:"role,omitempty"`
	RoleID   string `json:"role_id,omitempty"`
	Question string `json:"question"`
	AsOf     string `json:"as_of,omitempty"`
	Code     string `json:"code,omitempty"`
	Revision string `json:"revision,omitempty"`
	History  []Turn `json:"history,omitempty"`
	Messages []Turn `json:"messages,omitempty"`
}

// Plan is a fixed, non-executable query description for one sub-intent.
type Plan struct {
	Intent string   `json:"intent"`
	Source string   `json:"source,omitempty"`
	Steps  []string `json:"steps"`
}

// QueryPlan is an alias retained for dashboard code that prefers the explicit name.
type QueryPlan = Plan

// Evidence is a stable minimal fact used in an answer. Facts are deliberately small;
// full reports, histories, prompts, and paths are never included.
type Evidence struct {
	ID     string         `json:"evidence_id"`
	Intent string         `json:"intent"`
	Kind   string         `json:"kind"`
	Source string         `json:"source"`
	AsOf   string         `json:"as_of,omitempty"`
	Code   string         `json:"code,omitempty"`
	Name   string         `json:"name,omitempty"`
	Facts  map[string]any `json:"facts"`
}

// PromptSummary exposes prompt provenance and size metadata, never prompt text.
type PromptSummary struct {
	Version           string   `json:"version"`
	Role              string   `json:"role,omitempty"`
	RolePromptVersion string   `json:"role_prompt_version,omitempty"`
	Mode              string   `json:"mode"`
	Intents           []string `json:"intents,omitempty"`
	EvidenceCount     int      `json:"evidence_count"`
	TrimmedCount      int      `json:"trimmed_count,omitempty"`
	HistoryTurns      int      `json:"history_turns"`
}

// Citation reports evidence references found in the answer. Zero means the
// answer contained no [E#] reference; the answer text itself is never edited.
type Citation struct {
	Valid   []string `json:"valid"`
	Invalid []string `json:"invalid"`
	Zero    bool     `json:"zero"`
	Total   int      `json:"total"`
}

// CitationCheck is an alias retained for callers that prefer the explicit name.
type CitationCheck = Citation

// Done describes a completed turn. Partial answers list failed sub-intents.
type Done struct {
	Intents       []string `json:"intents"`
	Partial       bool     `json:"partial,omitempty"`
	FailedIntents []string `json:"failed_intents,omitempty"`
	AnswerRunes   int      `json:"answer_runes"`
}

// StreamError is a stable, sanitized error payload.
type StreamError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// StreamEvent is a stable NDJSON-friendly stream item. A caller can encode each
// event independently with encoding/json.
type StreamEvent struct {
	Type           string         `json:"type"`
	Intent         string         `json:"intent,omitempty"`
	Intents        []string       `json:"intents,omitempty"`
	Summary        string         `json:"summary,omitempty"`
	Classification string         `json:"classification,omitempty"`
	Plan           []Plan         `json:"plan,omitempty"`
	Steps          []string       `json:"steps,omitempty"`
	Evidence       []Evidence     `json:"evidence,omitempty"`
	EvidenceIDs    []string       `json:"evidence_ids,omitempty"`
	Count          int            `json:"count,omitempty"`
	Prompt         *PromptSummary `json:"prompt,omitempty"`
	EvidenceCount  int            `json:"evidence_count,omitempty"`
	Trimmed        bool           `json:"trimmed,omitempty"`
	Delta          string         `json:"delta,omitempty"`
	Text           string         `json:"text,omitempty"`
	Citation       *Citation      `json:"citation,omitempty"`
	Valid          []string       `json:"valid,omitempty"`
	Invalid        []string       `json:"invalid,omitempty"`
	ZeroReference  bool           `json:"zero_reference,omitempty"`
	Done           *Done          `json:"done,omitempty"`
	Error          *StreamError   `json:"error,omitempty"`
	Code           string         `json:"code,omitempty"`
	Message        string         `json:"message,omitempty"`
	Partial        bool           `json:"partial,omitempty"`
	FailedIntents  []string       `json:"failed_intents,omitempty"`
	Warnings       []string       `json:"warnings,omitempty"`
	Truncated      int            `json:"truncated,omitempty"`
}

// Emit receives stream events synchronously. Returning an error stops all work.
type Emit func(StreamEvent) error

// Service is a stateless advisor turn executor. It is safe for concurrent use
// when its sources and llm.Client are safe for concurrent use.
type Service struct {
	client          *llm.Client
	llmEnabled      bool
	reviews         ReviewSource
	experiments     ExperimentSource
	maxOutputTokens int
}

// New constructs an advisor service. A nil llm.Client still permits rule-only
// clarification, unsupported responses, and deterministic evidence queries.
func New(cfg config.Config, client *llm.Client, reviews ReviewSource, experiments ExperimentSource) *Service {
	enabled := client != nil
	if cfg.LLM.Configured() && !cfg.LLM.Enabled {
		enabled = false
	}
	return &Service{
		client:          client,
		llmEnabled:      enabled,
		reviews:         reviews,
		experiments:     experiments,
		maxOutputTokens: config.MaxOutputTokens,
	}
}

// Chat executes one normal chat or review-mode turn.
func (s *Service) Chat(ctx context.Context, req Request, emit Emit) error {
	return s.run(ctx, req, emit, false)
}

// ReviewAnalysis executes a manually triggered review with the fixed attribution
// answer structure, regardless of the request's chat/review mode.
func (s *Service) ReviewAnalysis(ctx context.Context, req Request, emit Emit) error {
	return s.run(ctx, req, emit, true)
}

func (s *Service) run(ctx context.Context, req Request, emit Emit, forceReview bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return emitFailure(emit, ErrInvalidRequest, "advisor service is unavailable", nil)
	}

	mode := strings.TrimSpace(req.Mode)
	if forceReview {
		mode = ModeReview
	} else if mode == "" {
		mode = ModeChat
	}
	if mode != ModeChat && mode != ModeReview {
		return emitFailure(emit, ErrInvalidMode, "mode 必须为 chat 或 review", nil)
	}
	req.Mode = mode

	role, roleText, err := rolePrompt(req.Role)
	if err != nil {
		return emitFailure(emit, ErrUnknownRole, "未知的角色", nil)
	}
	req.Role = role.ID
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" || utf8.RuneCountInString(req.Question) > 12000 {
		return emitFailure(emit, ErrInvalidRequest, "问题为空或过长", nil)
	}
	req.History = sanitizeHistory(req.History)
	if err := ctx.Err(); err != nil {
		return emitFailure(emit, err, "请求已取消", nil)
	}

	decision, err := s.classify(ctx, req)
	if err != nil {
		return emitFailure(emit, err, "请求已取消", nil)
	}
	resolution := resolveIntents(req, decision, s.experiments != nil)
	intents := resolution.Intents
	names := intentNames(intents)
	if err := emitEvent(emit, StreamEvent{
		Type:           EventIntent,
		Intent:         first(names),
		Intents:        names,
		Summary:        strings.Join(names, "；"),
		Classification: resolution.Classification,
		Warnings:       resolution.Warnings,
	}); err != nil {
		return err
	}

	plans := plansFor(intents)
	steps := flattenPlanSteps(plans)
	if err := emitEvent(emit, StreamEvent{Type: EventPlan, Plan: plans, Steps: steps, Summary: strings.Join(steps, "；")}); err != nil {
		return err
	}

	if deterministicOnly(intents) {
		return s.finishDeterministic(emit, req, role, intents, resolution)
	}

	collector := newEvidenceCollector(names)
	var failed []string
	var warnings []string
	for _, intent := range intents {
		evidence, err := s.queryIntent(ctx, intent)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return emitFailure(emit, ctxErr, "请求已取消", failed)
			}
			failed = append(failed, intent.Name)
			warnings = append(warnings, "evidence_unavailable:"+intent.Name)
			continue
		}
		collector.add(intent.Name, evidence)
	}

	bundle, trimmed, budgetWarnings := collector.bundle()
	warnings = append(warnings, budgetWarnings...)
	warnings = uniqueStrings(warnings)
	if err := emitEvent(emit, StreamEvent{
		Type:          EventEvidence,
		Evidence:      bundle,
		EvidenceIDs:   evidenceIDs(bundle),
		Count:         len(bundle),
		Summary:       fmt.Sprintf("已收集 %d 条本地证据", len(bundle)),
		Warnings:      warnings,
		FailedIntents: failed,
		Partial:       len(failed) > 0,
		Truncated:     trimmed,
	}); err != nil {
		return err
	}
	if len(failed) == len(intents) {
		return emitFailure(emit, ErrAllIntentsFailed, "没有可用的证据，未生成回答", failed)
	}
	if !s.llmEnabled {
		return emitFailure(emit, ErrLLMNotConfigured, "未配置模型，无法生成回答", failed)
	}

	promptVersion, systemPrompt := buildSystemPrompt(mode, roleText, intents)
	summary := PromptSummary{
		Version:           promptVersion,
		Role:              role.ID,
		RolePromptVersion: role.PromptVersion,
		Mode:              mode,
		Intents:           names,
		EvidenceCount:     len(bundle),
		TrimmedCount:      trimmed,
		HistoryTurns:      len(req.History),
	}
	if err := emitEvent(emit, StreamEvent{Type: EventPrompt, Prompt: &summary, EvidenceCount: summary.EvidenceCount, Trimmed: summary.TrimmedCount > 0}); err != nil {
		return err
	}

	answer, err := s.generate(ctx, systemPrompt, req, intents, bundle, failed, emit)
	if err != nil {
		var streamErr streamEmitError
		if errors.As(err, &streamErr) {
			return streamErr.err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return emitFailure(emit, ctxErr, "请求已取消", failed)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return emitFailure(emit, context.DeadlineExceeded, "请求超时", failed)
		}
		if errors.Is(err, context.Canceled) {
			return emitFailure(emit, context.Canceled, "请求已取消", failed)
		}
		if errors.Is(err, ErrLLMNotConfigured) {
			return emitFailure(emit, ErrLLMNotConfigured, "未配置模型，无法生成回答", failed)
		}
		return emitFailure(emit, ErrGenerationFailed, "模型请求失败，请稍后重试", failed)
	}

	check := checkCitations(answer, bundle)
	if err := emitCitation(emit, check); err != nil {
		return err
	}
	if err := emitEvent(emit, StreamEvent{
		Type:    EventDone,
		Intents: names,
		Done: &Done{
			Intents:       names,
			Partial:       len(failed) > 0,
			FailedIntents: failed,
			AnswerRunes:   utf8.RuneCountInString(answer),
		},
		Partial:       len(failed) > 0,
		FailedIntents: failed,
		Warnings:      warnings,
	}); err != nil {
		return err
	}
	return nil
}

func (s *Service) finishDeterministic(emit Emit, req Request, role Role, intents []Intent, resolution resolution) error {
	names := intentNames(intents)
	if err := emitEvent(emit, StreamEvent{Type: EventEvidence, Evidence: []Evidence{}, Summary: "未查询数据库", Warnings: resolution.Warnings}); err != nil {
		return err
	}
	summary := PromptSummary{
		Version:           "deterministic-v1",
		Role:              role.ID,
		RolePromptVersion: role.PromptVersion,
		Mode:              "deterministic",
		Intents:           names,
		EvidenceCount:     0,
		HistoryTurns:      len(req.History),
	}
	if err := emitEvent(emit, StreamEvent{Type: EventPrompt, Prompt: &summary, EvidenceCount: 0}); err != nil {
		return err
	}
	answer := deterministicAnswer(intents, resolution.ClarifyQuestion)
	if err := emitEvent(emit, StreamEvent{Type: EventDelta, Delta: answer, Text: answer}); err != nil {
		return err
	}
	check := checkCitations(answer, nil)
	if err := emitCitation(emit, check); err != nil {
		return err
	}
	return emitEvent(emit, StreamEvent{
		Type:    EventDone,
		Intents: names,
		Done: &Done{
			Intents:     names,
			AnswerRunes: utf8.RuneCountInString(answer),
		},
		Warnings: resolution.Warnings,
	})
}

func sanitizeHistory(history []Turn) []Turn {
	out := make([]Turn, 0, len(history))
	total := 0
	for i := len(history) - 1; i >= 0 && len(out) < 3; i-- {
		role := strings.ToLower(strings.TrimSpace(history[i].Role))
		if role != "user" && role != "assistant" {
			continue
		}
		content := strings.TrimSpace(history[i].Content)
		if content == "" {
			continue
		}
		content = truncateRunes(content, 4000)
		remaining := 10000 - total
		if remaining <= 0 {
			break
		}
		content = truncateRunes(content, remaining)
		total += utf8.RuneCountInString(content)
		out = append(out, Turn{Role: role, Content: content})
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func emitEvent(emit Emit, event StreamEvent) error {
	if emit == nil {
		return nil
	}
	return emit(event)
}

func emitFailure(emit Emit, err error, message string, failed []string) error {
	code := "internal_error"
	switch {
	case errors.Is(err, context.Canceled):
		code = "canceled"
		message = "请求已取消"
	case errors.Is(err, context.DeadlineExceeded):
		code = "deadline_exceeded"
		message = "请求超时"
	case errors.Is(err, ErrLLMNotConfigured):
		code = "llm_not_configured"
	case errors.Is(err, ErrAllIntentsFailed):
		code = "all_intents_failed"
	case errors.Is(err, ErrInvalidMode):
		code = "invalid_mode"
	case errors.Is(err, ErrUnknownRole):
		code = "unknown_role"
	case errors.Is(err, ErrInvalidRequest):
		code = "invalid_request"
	case errors.Is(err, ErrGenerationFailed):
		code = "llm_request_failed"
	}
	event := StreamEvent{
		Type:          EventError,
		Error:         &StreamError{Code: code, Message: message},
		Code:          code,
		Message:       message,
		FailedIntents: append([]string(nil), failed...),
	}
	if emitErr := emitEvent(emit, event); emitErr != nil {
		return emitErr
	}
	return err
}

func flattenPlanSteps(plans []Plan) []string {
	var steps []string
	for _, plan := range plans {
		for _, step := range plan.Steps {
			steps = append(steps, plan.Intent+": "+step)
		}
	}
	return steps
}

func evidenceIDs(evidence []Evidence) []string {
	ids := make([]string, 0, len(evidence))
	for _, item := range evidence {
		ids = append(ids, item.ID)
	}
	return ids
}

func emitCitation(emit Emit, check Citation) error {
	return emitEvent(emit, StreamEvent{
		Type:          EventCitation,
		Citation:      &check,
		Valid:         append([]string(nil), check.Valid...),
		Invalid:       append([]string(nil), check.Invalid...),
		ZeroReference: check.Zero,
	})
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func (s *Service) String() string {
	if s == nil {
		return "advisor.Service(nil)"
	}
	return fmt.Sprintf("advisor.Service(review=%t,experiments=%t,llm=%t)", s.reviews != nil, s.experiments != nil, s.client != nil)
}
