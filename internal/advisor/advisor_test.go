package advisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/llm"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/review"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

type capturedRequest struct {
	Raw         string
	System      string
	Messages    []capturedMessage
	Temperature float64
	MaxTokens   int
}

type capturedMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type testOpenAI struct {
	mu        sync.Mutex
	calls     int
	requests  []capturedRequest
	responses []string
	status    int
	delay     time.Duration
}

func (s *testOpenAI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	var payload struct {
		Messages    []capturedMessage `json:"messages"`
		Temperature float64           `json:"temperature"`
		MaxTokens   int               `json:"max_tokens"`
	}
	_ = json.Unmarshal(raw, &payload)
	captured := capturedRequest{Raw: string(raw), Messages: payload.Messages, Temperature: payload.Temperature, MaxTokens: payload.MaxTokens}
	if len(payload.Messages) > 0 && payload.Messages[0].Role == "system" {
		captured.System = payload.Messages[0].Content
	}

	s.mu.Lock()
	index := s.calls
	s.calls++
	response := ""
	if index < len(s.responses) {
		response = s.responses[index]
	}
	status := s.status
	delay := s.delay
	s.requests = append(s.requests, captured)
	s.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "upstream error")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	chunk, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": response}}},
	})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func (s *testOpenAI) snapshot() []capturedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capturedRequest(nil), s.requests...)
}

func (s *testOpenAI) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type fakeReviewSource struct {
	day        review.Day
	event      review.Event
	dayErr     error
	eventErr   error
	dayCalls   int
	eventCalls int
}

func (f *fakeReviewSource) Day(ctx context.Context, _ string, _ string) (review.Day, error) {
	if err := ctx.Err(); err != nil {
		return review.Day{}, err
	}
	f.dayCalls++
	if f.dayErr != nil {
		return review.Day{}, f.dayErr
	}
	return f.day, nil
}

func (f *fakeReviewSource) Event(ctx context.Context, _ string, _ string, _ string) (review.Event, error) {
	if err := ctx.Err(); err != nil {
		return review.Event{}, err
	}
	f.eventCalls++
	if f.eventErr != nil {
		return review.Event{}, f.eventErr
	}
	return f.event, nil
}

type fakeExperimentSource struct {
	records   []experiment.Record
	listErr   error
	getErr    error
	listCalls int
	getCalls  int
}

func (f *fakeExperimentSource) List(ctx context.Context) ([]experiment.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]experiment.Record(nil), f.records...), nil
}

func (f *fakeExperimentSource) Get(ctx context.Context, _ string) (experiment.Record, error) {
	if err := ctx.Err(); err != nil {
		return experiment.Record{}, err
	}
	f.getCalls++
	if f.getErr != nil {
		return experiment.Record{}, f.getErr
	}
	if len(f.records) == 0 {
		return experiment.Record{}, fs.ErrNotExist
	}
	return f.records[0], nil
}

type eventLog struct {
	events    []StreamEvent
	failOn    string
	failAfter int
	err       error
}

func (l *eventLog) emit(event StreamEvent) error {
	if l.failOn != "" && event.Type == l.failOn && len(l.events) >= l.failAfter {
		return l.err
	}
	l.events = append(l.events, event)
	return nil
}

func (l *eventLog) ofType(kind string) []StreamEvent {
	var out []StreamEvent
	for _, event := range l.events {
		if event.Type == kind {
			out = append(out, event)
		}
	}
	return out
}

func (l *eventLog) last(kind string) StreamEvent {
	var out StreamEvent
	for _, event := range l.events {
		if event.Type == kind {
			out = event
		}
	}
	return out
}

func (l *eventLog) text() string {
	var builder strings.Builder
	for _, event := range l.events {
		if event.Type == EventDelta {
			builder.WriteString(event.Delta)
		}
	}
	return builder.String()
}

func (l *eventLog) types() []string {
	out := make([]string, 0, len(l.events))
	for _, event := range l.events {
		out = append(out, event.Type)
	}
	return out
}

func floatPointer(value float64) *float64 { return &value }

func testDay() review.Day {
	negative := -3.2
	positive := 2.8
	return review.Day{
		Version:         1,
		AsOf:            "2026-10-12",
		Target:          "2026-10-13",
		Revision:        "rev-1",
		Strategy:        "short_horizon_v1",
		CalendarFrom:    "2026-10-12",
		CalendarThrough: "2026-10-19",
		Aggregate: review.Aggregate{
			All: review.Metrics{Total: 2, Mature: 1, Missing: 1, Stop: 1, MeanNetPct: &negative},
		},
		Events: []review.EventSummary{
			{
				Code: "sz000001", Name: "测试银行", Rank: 1, Recommended: true,
				State: review.StateMature, SignalDate: "2026-10-12", TargetDate: "2026-10-13",
				EntryDate: "2026-10-13", ExitDate: "2026-10-16", ExitReason: "stop",
				NetPct: &negative, MFE: &positive, MAE: &negative,
			},
			{
				Code: "sz000002", Name: "缺失股", Rank: 2, State: review.StateMissingData,
				StateReason: "signal_bar_missing", SignalDate: "2026-10-12",
			},
		},
	}
}

func testEvent() review.Event {
	day := testDay()
	return review.Event{
		EventSummary:   day.Events[0],
		ReportRevision: "rev-1",
		Outcome: &strategy.Outcome{
			Status: "stop", EntryDate: "2026-10-13", ExitDate: "2026-10-16", ExitPrice: 9.7,
			NetPct: -3.2, HoldingDays: 4, MFE: 2.8, MAE: -3.2,
		},
		Risks:   []string{"风险一", "风险二"},
		Reasons: []string{"入选理由"},
		Rules:   []string{"规则一"},
		Missing: []string{"minute_structure"},
	}
}

func testExperimentRecord(index int) experiment.Record {
	id := fmt.Sprintf("%032x", index+1)
	return experiment.Record{
		Version: 1, ID: id, Status: "completed", Verdict: "promising", Engine: "engine",
		CreatedAt: time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC),
		Spec:      experiment.Spec{Title: fmt.Sprintf("实验%d", index), Direction: "方向", Hypothesis: "假设"},
		Data:      experiment.DataSummary{AsOf: "2026-10-12", PoolSize: 20, Included: 18},
		Review: &experiment.Review{
			Risk:  strategy.LossAssessment{Status: "observed_pass"},
			Stats: strategy.Stats{Samples: 80, Resolved: 76, HitRate: 0.42, MeanNet: 1.2, WorstMAE: -3.0},
		},
	}
}

func newClient(t *testing.T, serverURL, key string, timeout time.Duration) *llm.Client {
	t.Helper()
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	client, err := llm.New(llm.Config{BaseURL: serverURL, APIKey: key, Model: "test-model", Timeout: timeout, MaxOutputTokens: 2048}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newService(client *llm.Client, reviews ReviewSource, experiments ExperimentSource) *Service {
	cfg := config.Defaults()
	cfg.LLM.APIKey = "cfg-secret"
	return New(cfg, client, reviews, experiments)
}

func newServer(t *testing.T, responses ...string) (*testOpenAI, *httptest.Server) {
	t.Helper()
	fake := &testOpenAI{responses: responses}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return fake, server
}

func TestRolesAreStableAndRoleSwitchDoesNotShareHistory(t *testing.T) {
	roles := Roles()
	if len(roles) != 8 {
		t.Fatalf("expected 8 roles, got %d", len(roles))
	}
	want := []string{"buffett", "munger", "graham", "livermore", "soros", "simons", "lynch", "duan-yongping"}
	for i, role := range roles {
		if role.ID != want[i] || role.Name == "" || role.Description == "" || role.PromptVersion != rolePromptVersion {
			t.Fatalf("unstable role %d: %+v", i, role)
		}
	}
	roles[0].Name = "changed"
	if Roles()[0].Name == "changed" {
		t.Fatal("Roles exposed mutable package state")
	}
	if _, ok := RoleByID("missing"); ok {
		t.Fatal("unknown role accepted")
	}

	fake, server := newServer(t, "第一轮 [E1]", "第二轮 [E2]")
	reviews := &fakeReviewSource{day: testDay(), event: testEvent()}
	service := newService(newClient(t, server.URL, "sk-role", 0), reviews, nil)

	first := &eventLog{}
	if err := service.Chat(context.Background(), Request{
		Role: "buffett", Question: "查询 2026-10-12 的报告", AsOf: "2026-10-12",
		History: []Turn{{Role: "user", Content: "old-only-context"}},
	}, first.emit); err != nil {
		t.Fatal(err)
	}
	second := &eventLog{}
	if err := service.Chat(context.Background(), Request{
		Role: "munger", Question: "查询 2026-10-12 的报告", AsOf: "2026-10-12",
	}, second.emit); err != nil {
		t.Fatal(err)
	}
	requests := fake.snapshot()
	if len(requests) != 2 {
		t.Fatalf("expected two generation requests, got %d", len(requests))
	}
	if !strings.Contains(requests[0].System, "企业所有者") || !strings.Contains(requests[1].System, "多元思维模型") {
		t.Fatal("role prompts were not selected independently")
	}
	if strings.Contains(requests[1].Raw, "old-only-context") {
		t.Fatal("new role inherited the prior role's history")
	}
	if first.last(EventPrompt).Prompt.Role != "buffett" || second.last(EventPrompt).Prompt.Role != "munger" {
		t.Fatal("prompt summaries did not report the selected role")
	}
}

func TestRuleSingleIntentUsesDeterministicQueryAndCitations(t *testing.T) {
	fake, server := newServer(t, "报告证据见 [E1]")
	reviews := &fakeReviewSource{day: testDay(), event: testEvent()}
	service := newService(newClient(t, server.URL, "sk-test", 0), reviews, nil)
	log := &eventLog{}
	err := service.Chat(context.Background(), Request{Role: "buffett", Question: "查询 2026-10-12 的报告"}, log.emit)
	if err != nil {
		t.Fatal(err)
	}
	intent := log.last(EventIntent)
	if len(intent.Intents) != 1 || intent.Intents[0] != IntentReportQuery || intent.Classification != "rule" {
		t.Fatalf("unexpected intent: %+v", intent)
	}
	if reviews.dayCalls != 1 || reviews.eventCalls != 0 {
		t.Fatalf("unexpected query calls: day=%d event=%d", reviews.dayCalls, reviews.eventCalls)
	}
	if len(log.last(EventEvidence).Evidence) == 0 {
		t.Fatal("report evidence missing")
	}
	if fake.callCount() != 1 {
		t.Fatalf("classification should not call the model, calls=%d", fake.callCount())
	}
	citation := log.last(EventCitation).Citation
	if citation == nil || len(citation.Valid) != 1 || citation.Valid[0] != "E1" || citation.Zero {
		t.Fatalf("unexpected citation: %+v", citation)
	}
	if got := strings.Join(log.types(), ","); got != "intent,plan,evidence,prompt,delta,citation,done" {
		t.Fatalf("unexpected event order: %s", got)
	}
}

func TestLowConfidenceWithoutLLMClarifiesWithoutQuery(t *testing.T) {
	reviews := &fakeReviewSource{day: testDay(), event: testEvent()}
	service := newService(nil, reviews, nil)
	log := &eventLog{}
	if err := service.Chat(context.Background(), Request{Question: "这个怎么样"}, log.emit); err != nil {
		t.Fatal(err)
	}
	if reviews.dayCalls != 0 || reviews.eventCalls != 0 {
		t.Fatal("clarification queried evidence")
	}
	intent := log.last(EventIntent)
	if intent.Classification != "fallback" || len(intent.Intents) != 1 || intent.Intents[0] != IntentClarify {
		t.Fatalf("unexpected clarification: %+v", intent)
	}
	if log.last(EventError).Error != nil || !strings.Contains(log.text(), "请补充一个关键信息") {
		t.Fatalf("deterministic clarification missing: %+v", log.events)
	}
}

func TestLLMClassificationValidAndInvalidJSON(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		classification := `{"intents":[{"intent":"methodology"}],"needs_clarification":false}`
		fake, server := newServer(t, classification, "这是方法说明，不对应当前行情。")
		service := newService(newClient(t, server.URL, "sk-classify", 0), nil, nil)
		log := &eventLog{}
		if err := service.Chat(context.Background(), Request{Question: "帮我看看这个说法"}, log.emit); err != nil {
			t.Fatal(err)
		}
		intent := log.last(EventIntent)
		if intent.Classification != "llm" || len(intent.Intents) != 1 || intent.Intents[0] != IntentMethodology {
			t.Fatalf("unexpected LLM classification: %+v", intent)
		}
		if fake.callCount() != 2 {
			t.Fatalf("expected classification plus generation, got %d", fake.callCount())
		}
		requests := fake.snapshot()
		if requests[0].Temperature != 0 {
			t.Fatalf("classification should use low temperature, got %v", requests[0].Temperature)
		}
		if len(log.last(EventEvidence).Evidence) != 0 {
			t.Fatal("methodology should not query evidence")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		fake, server := newServer(t, "not-json")
		service := newService(newClient(t, server.URL, "sk-classify", 0), nil, nil)
		log := &eventLog{}
		if err := service.Chat(context.Background(), Request{Question: "帮我看看这个说法"}, log.emit); err != nil {
			t.Fatal(err)
		}
		intent := log.last(EventIntent)
		if intent.Classification != "fallback" || len(intent.Intents) != 1 || intent.Intents[0] != IntentClarify {
			t.Fatalf("invalid classification did not degrade to clarify: %+v", intent)
		}
		if fake.callCount() != 1 {
			t.Fatalf("invalid classification must not generate an answer, calls=%d", fake.callCount())
		}
	})
}

func TestMultipleIndependentIntentsAndEvidenceMerge(t *testing.T) {
	twoResponses := "联合回答 [E1]"
	t.Run("two intents", func(t *testing.T) {
		_, server := newServer(t, twoResponses)
		reviews := &fakeReviewSource{day: testDay(), event: testEvent()}
		service := newService(newClient(t, server.URL, "sk-multi", 0), reviews, nil)
		log := &eventLog{}
		question := "复盘 2026-10-12 的失败，并查询 sz000001 2026-10-12 的证据"
		if err := service.Chat(context.Background(), Request{Question: question}, log.emit); err != nil {
			t.Fatal(err)
		}
		intent := log.last(EventIntent)
		if len(intent.Intents) != 2 || intent.Intents[0] != IntentReviewExplain || intent.Intents[1] != IntentStockEvidence {
			t.Fatalf("unexpected two-intent result: %+v", intent)
		}
		if reviews.dayCalls != 1 || reviews.eventCalls != 1 {
			t.Fatalf("expected day plus event query, got day=%d event=%d", reviews.dayCalls, reviews.eventCalls)
		}
		seen := map[string]bool{}
		for _, item := range log.last(EventEvidence).Evidence {
			seen[item.Intent] = true
		}
		if !seen[IntentReviewExplain] || !seen[IntentStockEvidence] {
			t.Fatalf("merged evidence missing intents: %+v", seen)
		}
	})

	t.Run("three intents", func(t *testing.T) {
		_, server := newServer(t, "三意图回答 [E1]")
		reviews := &fakeReviewSource{day: testDay(), event: testEvent()}
		experiments := &fakeExperimentSource{records: []experiment.Record{testExperimentRecord(0)}}
		service := newService(newClient(t, server.URL, "sk-multi", 0), reviews, experiments)
		log := &eventLog{}
		question := "查询 2026-10-12 的报告，复盘 2026-10-12 的失败，查看实验"
		if err := service.Chat(context.Background(), Request{Question: question}, log.emit); err != nil {
			t.Fatal(err)
		}
		intent := log.last(EventIntent)
		if len(intent.Intents) != 3 || intent.Intents[0] != IntentReportQuery || intent.Intents[1] != IntentReviewExplain || intent.Intents[2] != IntentExperimentQuery {
			t.Fatalf("unexpected three-intent result: %+v", intent)
		}
		if reviews.dayCalls != 2 || experiments.listCalls != 1 {
			t.Fatalf("unexpected three-intent calls: day=%d experiments=%d", reviews.dayCalls, experiments.listCalls)
		}
	})

	t.Run("four intents clarify without query", func(t *testing.T) {
		reviews := &fakeReviewSource{day: testDay(), event: testEvent()}
		experiments := &fakeExperimentSource{records: []experiment.Record{testExperimentRecord(0)}}
		service := newService(nil, reviews, experiments)
		log := &eventLog{}
		question := "查询报告，复盘失败，查看实验，解释方法论"
		if err := service.Chat(context.Background(), Request{Question: question, AsOf: "2026-10-12"}, log.emit); err != nil {
			t.Fatal(err)
		}
		intent := log.last(EventIntent)
		if len(intent.Intents) != 1 || intent.Intents[0] != IntentClarify {
			t.Fatalf("four intents were not reduced to clarify: %+v", intent)
		}
		if reviews.dayCalls != 0 || experiments.listCalls != 0 {
			t.Fatal("clarification queried evidence")
		}
		if len(log.last(EventEvidence).Evidence) != 0 {
			t.Fatal("clarification returned evidence")
		}
	})
}

func TestDependencyAndSlotConflictClarifyWithoutQuery(t *testing.T) {
	cases := []string{
		"先查询 2026-10-12 的报告然后复盘失败",
		"查询 2026-10-12 的报告，查询 2026-10-13 的报告",
		"查询 sz000001 2026-10-12 的证据，查询 sz000002 2026-10-12 的证据",
	}
	for _, question := range cases {
		t.Run(question, func(t *testing.T) {
			reviews := &fakeReviewSource{day: testDay(), event: testEvent()}
			service := newService(nil, reviews, nil)
			log := &eventLog{}
			if err := service.Chat(context.Background(), Request{Question: question}, log.emit); err != nil {
				t.Fatal(err)
			}
			intent := log.last(EventIntent)
			if len(intent.Intents) != 1 || intent.Intents[0] != IntentClarify {
				t.Fatalf("conflict was not clarified: %+v", intent)
			}
			if reviews.dayCalls != 0 || reviews.eventCalls != 0 {
				t.Fatal("conflicting request queried evidence")
			}
		})
	}
}

func TestEvidenceBudgetTrimmingAndVisibility(t *testing.T) {
	day := testDay()
	day.Events = nil
	for i := 0; i < 20; i++ {
		day.Events = append(day.Events, review.EventSummary{
			Code: "sz000001", Name: "异常股", Rank: i + 1, State: review.StateMissingData,
			StateReason: "signal_bar_missing", SignalDate: "2026-10-12",
		})
	}
	records := make([]experiment.Record, 15)
	for i := range records {
		records[i] = testExperimentRecord(i)
	}
	_, server := newServer(t, "预算回答")
	reviews := &fakeReviewSource{day: day, event: testEvent()}
	experiments := &fakeExperimentSource{records: records}
	service := newService(newClient(t, server.URL, "sk-budget", 0), reviews, experiments)
	log := &eventLog{}
	question := "查询 2026-10-12 的报告，复盘 2026-10-12 的失败，查看实验"
	if err := service.Chat(context.Background(), Request{Question: question}, log.emit); err != nil {
		t.Fatal(err)
	}
	evidenceEvent := log.last(EventEvidence)
	if len(evidenceEvent.Evidence) != MaxEvidence || evidenceEvent.Truncated == 0 || !hasString(evidenceEvent.Warnings, "evidence_budget_trimmed") {
		t.Fatalf("budget trimming not visible: count=%d truncated=%d warnings=%v", len(evidenceEvent.Evidence), evidenceEvent.Truncated, evidenceEvent.Warnings)
	}
	perIntent := map[string]int{}
	for _, item := range evidenceEvent.Evidence {
		perIntent[item.Intent]++
	}
	for intent, count := range perIntent {
		if count > MaxEvidencePerIntent {
			t.Fatalf("intent %s exceeded per-intent cap: %d", intent, count)
		}
	}
	if log.last(EventPrompt).Prompt.TrimmedCount == 0 {
		t.Fatal("prompt summary did not expose trimming")
	}
}

func TestEvidenceJSONNeverExceedsBudget(t *testing.T) {
	event := testEvent()
	event.Risks = make([]string, 6)
	for i := range event.Risks {
		event.Risks[i] = strings.Repeat("风险", 3000)
	}
	event.Missing = make([]string, 10)
	for i := range event.Missing {
		event.Missing[i] = strings.Repeat("缺失", 3000)
	}
	event.Reasons = make([]string, 6)
	for i := range event.Reasons {
		event.Reasons[i] = strings.Repeat("理由", 1500)
	}
	_, server := newServer(t, "裁剪回答")
	reviews := &fakeReviewSource{day: testDay(), event: event}
	service := newService(newClient(t, server.URL, "sk-size", 0), reviews, nil)
	log := &eventLog{}
	if err := service.Chat(context.Background(), Request{Question: "查询 sz000001 2026-10-12 的证据"}, log.emit); err != nil {
		t.Fatal(err)
	}
	bundle := log.last(EventEvidence).Evidence
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > MaxEvidenceBytes {
		t.Fatalf("evidence exceeded budget: %d", len(raw))
	}
	if log.last(EventEvidence).Truncated == 0 || !hasString(log.last(EventEvidence).Warnings, "evidence_budget_trimmed") {
		t.Fatal("size trimming was not visible")
	}
}

func TestPartialFailureAnswersAndAllFailureDoesNot(t *testing.T) {
	t.Run("partial", func(t *testing.T) {
		_, server := newServer(t, "部分回答 [E1]")
		reviews := &fakeReviewSource{dayErr: errors.New("review unavailable")}
		experiments := &fakeExperimentSource{records: []experiment.Record{testExperimentRecord(0)}}
		service := newService(newClient(t, server.URL, "sk-partial", 0), reviews, experiments)
		log := &eventLog{}
		if err := service.Chat(context.Background(), Request{Question: "查询 2026-10-12 的报告，查看实验", AsOf: "2026-10-12"}, log.emit); err != nil {
			t.Fatal(err)
		}
		if log.text() == "" {
			t.Fatal("partial answer was not generated")
		}
		done := log.last(EventDone).Done
		if done == nil || !done.Partial || !hasString(done.FailedIntents, IntentReportQuery) {
			t.Fatalf("partial completion not visible: %+v", done)
		}
		if !hasString(log.last(EventEvidence).FailedIntents, IntentReportQuery) {
			t.Fatal("evidence event did not mark the failed intent")
		}
	})

	t.Run("all fail", func(t *testing.T) {
		reviews := &fakeReviewSource{dayErr: errors.New("review unavailable")}
		experiments := &fakeExperimentSource{listErr: errors.New("experiments unavailable")}
		service := newService(nil, reviews, experiments)
		log := &eventLog{}
		err := service.Chat(context.Background(), Request{Question: "查询 2026-10-12 的报告，查看实验", AsOf: "2026-10-12"}, log.emit)
		if !errors.Is(err, ErrAllIntentsFailed) {
			t.Fatalf("expected all-intents failure, got %v", err)
		}
		if log.text() != "" || log.last(EventDelta).Type == EventDelta || log.last(EventPrompt).Type == EventPrompt {
			t.Fatal("failed turn generated an answer")
		}
		if code := log.last(EventError).Error; code == nil || code.Code != "all_intents_failed" {
			t.Fatalf("stable all-failure code missing: %+v", code)
		}
	})
}

func TestCitationValidationDoesNotRewriteAnswer(t *testing.T) {
	evidence := []Evidence{{ID: "E1"}, {ID: "E2"}}
	cases := []struct {
		name    string
		answer  string
		valid   []string
		invalid []string
		zero    bool
	}{
		{name: "valid", answer: "见 [E1] 与 [E2]", valid: []string{"E1", "E2"}, invalid: []string{}},
		{name: "invalid", answer: "见 [E1] 和 [E9]", valid: []string{"E1"}, invalid: []string{"E9"}},
		{name: "zero", answer: "没有引用", valid: []string{}, invalid: []string{}, zero: true},
		{name: "leading zero is invalid", answer: "[E01]", valid: []string{}, invalid: []string{"E01"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkCitations(tc.answer, evidence)
			if strings.Join(got.Valid, ",") != strings.Join(tc.valid, ",") || strings.Join(got.Invalid, ",") != strings.Join(tc.invalid, ",") || got.Zero != tc.zero {
				t.Fatalf("unexpected citation check: %+v", got)
			}
		})
	}

	_, server := newServer(t, "模型原文包含 [E99]")
	reviews := &fakeReviewSource{day: testDay()}
	service := newService(newClient(t, server.URL, "sk-cite", 0), reviews, nil)
	log := &eventLog{}
	if err := service.Chat(context.Background(), Request{Question: "查询 2026-10-12 的报告", AsOf: "2026-10-12"}, log.emit); err != nil {
		t.Fatal(err)
	}
	if log.text() != "模型原文包含 [E99]" {
		t.Fatal("answer text was silently modified")
	}
	if !hasString(log.last(EventCitation).Citation.Invalid, "E99") {
		t.Fatal("invalid citation was not reported")
	}
}

func TestReviewAnalysisUsesFixedStructurePrompt(t *testing.T) {
	response := "一句话结论\n风险有限。\n\n关键证据\n[E1]\n\n可能判断偏差\n信号偏差\n\n其他解释\n市场波动\n\n缺失数据\n无\n\n风险提示\n样本有限"
	fake, server := newServer(t, response)
	reviews := &fakeReviewSource{day: testDay(), event: testEvent()}
	service := newService(newClient(t, server.URL, "sk-review", 0), reviews, nil)
	log := &eventLog{}
	if err := service.ReviewAnalysis(context.Background(), Request{Mode: ModeChat, Question: "复盘 2026-10-12 的失败", AsOf: "2026-10-12"}, log.emit); err != nil {
		t.Fatal(err)
	}
	requests := fake.snapshot()
	if len(requests) != 1 {
		t.Fatalf("expected one generation request, got %d", len(requests))
	}
	for _, heading := range []string{"一句话结论", "关键证据", "可能判断偏差", "其他解释", "缺失数据", "风险提示"} {
		if !strings.Contains(requests[0].System, heading) {
			t.Fatalf("review system prompt missing heading %q", heading)
		}
	}
	prompt := log.last(EventPrompt).Prompt
	if prompt == nil || prompt.Version != "advisor-review-v1" || prompt.Mode != ModeReview {
		t.Fatalf("review prompt summary mismatch: %+v", prompt)
	}
	if log.text() != response {
		t.Fatal("review response was not streamed unchanged")
	}
}

func TestLLMNotConfiguredTimeoutCancellationAndEmitFailure(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		service := newService(nil, nil, nil)
		log := &eventLog{}
		err := service.Chat(context.Background(), Request{Question: "什么是机会分"}, log.emit)
		if !errors.Is(err, ErrLLMNotConfigured) {
			t.Fatalf("expected llm_not_configured, got %v", err)
		}
		if code := log.last(EventError).Error; code == nil || code.Code != "llm_not_configured" {
			t.Fatalf("stable llm_not_configured event missing: %+v", code)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		fake := &testOpenAI{responses: []string{"late"}, delay: 300 * time.Millisecond}
		server := httptest.NewServer(fake)
		defer server.Close()
		reviews := &fakeReviewSource{day: testDay()}
		service := newService(newClient(t, server.URL, "sk-timeout", 20*time.Millisecond), reviews, nil)
		log := &eventLog{}
		err := service.Chat(context.Background(), Request{Question: "查询 2026-10-12 的报告", AsOf: "2026-10-12"}, log.emit)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline exceeded, got %v", err)
		}
		if code := log.last(EventError).Error; code == nil || code.Code != "deadline_exceeded" {
			t.Fatalf("timeout event missing: %+v", code)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		service := newService(nil, &fakeReviewSource{day: testDay()}, nil)
		log := &eventLog{}
		err := service.Chat(ctx, Request{Question: "查询 2026-10-12 的报告", AsOf: "2026-10-12"}, log.emit)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
		if code := log.last(EventError).Error; code == nil || code.Code != "canceled" {
			t.Fatalf("cancel event missing: %+v", code)
		}
	})

	t.Run("in-flight cancellation", func(t *testing.T) {
		fake := &testOpenAI{responses: []string{"late"}, delay: 2 * time.Second}
		server := httptest.NewServer(fake)
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reviews := &fakeReviewSource{day: testDay()}
		service := newService(newClient(t, server.URL, "sk-cancel", 0), reviews, nil)
		log := &eventLog{}
		emit := func(event StreamEvent) error {
			if event.Type == EventPrompt {
				time.AfterFunc(20*time.Millisecond, cancel)
			}
			return log.emit(event)
		}
		err := service.Chat(ctx, Request{Question: "查询 2026-10-12 的报告", AsOf: "2026-10-12"}, emit)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected in-flight cancellation, got %v", err)
		}
		if fake.callCount() != 1 {
			t.Fatalf("generation request was not issued, calls=%d", fake.callCount())
		}
		if code := log.last(EventError).Error; code == nil || code.Code != "canceled" {
			t.Fatalf("cancel event missing: %+v", code)
		}
	})

	t.Run("emit failure", func(t *testing.T) {
		sentinel := errors.New("emit stopped")
		service := newService(nil, &fakeReviewSource{day: testDay()}, nil)
		log := &eventLog{failOn: EventIntent, err: sentinel}
		err := service.Chat(context.Background(), Request{Question: "查询 2026-10-12 的报告", AsOf: "2026-10-12"}, log.emit)
		if !errors.Is(err, sentinel) {
			t.Fatalf("emit failure was not returned unchanged: %v", err)
		}
		if len(log.events) != 0 {
			t.Fatal("work continued after emit failure")
		}
	})

	t.Run("delta emit failure", func(t *testing.T) {
		sentinel := errors.New("delta emit stopped")
		_, server := newServer(t, "stale")
		reviews := &fakeReviewSource{day: testDay()}
		service := newService(newClient(t, server.URL, "sk-emit", 0), reviews, nil)
		log := &eventLog{failOn: EventDelta, err: sentinel}
		err := service.Chat(context.Background(), Request{Question: "查询 2026-10-12 的报告", AsOf: "2026-10-12"}, log.emit)
		if !errors.Is(err, sentinel) {
			t.Fatalf("delta emit failure was not returned unchanged: %v", err)
		}
	})
}

func TestExplicitlyDisabledLLMDoesNotCallModel(t *testing.T) {
	fake, server := newServer(t, "should not be used")
	cfg := config.Defaults()
	cfg.LLM.Enabled = false
	cfg.LLM.APIKey = "disabled-key"
	service := New(cfg, newClient(t, server.URL, "disabled-key", 0), &fakeReviewSource{day: testDay()}, nil)
	log := &eventLog{}
	err := service.Chat(context.Background(), Request{Question: "查询 2026-10-12 的报告", AsOf: "2026-10-12"}, log.emit)
	if !errors.Is(err, ErrLLMNotConfigured) {
		t.Fatalf("disabled LLM was used: %v", err)
	}
	if fake.callCount() != 0 {
		t.Fatal("disabled LLM received a request")
	}
}

func TestSensitiveConfigurationAndPromptTextAreNotSent(t *testing.T) {
	fake, server := newServer(t, "答案 [E1]")
	reviews := &fakeReviewSource{day: testDay()}
	cfg := config.Defaults()
	cfg.LLM.APIKey = "sk-baked-secret"
	cfg.LLM.BaseURL = "https://private.example/v1"
	cfg.DataDir = "/srv/private/data"
	cfg.Stocks = []config.Stock{{Code: "sz000001", Name: "full-pool-secret-name"}}
	service := New(cfg, newClient(t, server.URL, "sk-live-secret", 0), reviews, nil)
	log := &eventLog{}
	if err := service.Chat(context.Background(), Request{Question: "查询 2026-10-12 的报告", AsOf: "2026-10-12"}, log.emit); err != nil {
		t.Fatal(err)
	}
	requests := fake.snapshot()
	if len(requests) != 1 {
		t.Fatalf("expected one request, got %d", len(requests))
	}
	for _, forbidden := range []string{"sk-live-secret", "sk-baked-secret", "private.example", "/srv/private/data", "full-pool-secret-name", server.URL} {
		if strings.Contains(requests[0].Raw, forbidden) {
			t.Fatalf("request leaked %q", forbidden)
		}
	}
	eventsJSON, err := json.Marshal(log.events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(eventsJSON), "企业所有者") {
		t.Fatal("prompt text leaked into stream events")
	}
}

func TestStreamEventJSONAliasesMatchDashboardContract(t *testing.T) {
	_, server := newServer(t, "答案 [E1]")
	reviews := &fakeReviewSource{day: testDay()}
	service := newService(newClient(t, server.URL, "sk-json", 0), reviews, nil)
	log := &eventLog{}
	if err := service.Chat(context.Background(), Request{Question: "查询 2026-10-12 的报告", AsOf: "2026-10-12"}, log.emit); err != nil {
		t.Fatal(err)
	}
	encoded := make([]map[string]any, 0, len(log.events))
	for _, event := range log.events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, value)
	}
	if steps, ok := encoded[1]["steps"].([]any); !ok || len(steps) == 0 {
		t.Fatalf("plan event lacks dashboard steps: %+v", encoded[1])
	}
	if _, ok := encoded[2]["evidence_ids"].([]any); !ok || encoded[2]["count"] == nil {
		t.Fatalf("evidence event lacks dashboard aliases: %+v", encoded[2])
	}
	if encoded[3]["evidence_count"] == nil {
		t.Fatalf("prompt event lacks dashboard evidence_count: %+v", encoded[3])
	}
	if encoded[4]["text"] != "答案 [E1]" || encoded[4]["delta"] != "答案 [E1]" {
		t.Fatalf("delta event lacks text alias: %+v", encoded[4])
	}
	if valid, ok := encoded[5]["valid"].([]any); !ok || len(valid) != 1 {
		t.Fatalf("citation event lacks valid alias: %+v", encoded[5])
	}
	if encoded[6]["type"] != EventDone {
		t.Fatalf("unexpected final event: %+v", encoded[6])
	}
}

func TestErrorEventJSONAliasesMatchDashboardContract(t *testing.T) {
	service := newService(nil, nil, nil)
	log := &eventLog{}
	err := service.Chat(context.Background(), Request{Question: "什么是机会分"}, log.emit)
	if !errors.Is(err, ErrLLMNotConfigured) {
		t.Fatal(err)
	}
	raw, marshalErr := json.Marshal(log.last(EventError))
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var value map[string]any
	if marshalErr = json.Unmarshal(raw, &value); marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if value["code"] != "llm_not_configured" || value["message"] == "" {
		t.Fatalf("error event lacks dashboard aliases: %s", raw)
	}
}

func TestHistoryKeepsOnlyRecentThreeTurns(t *testing.T) {
	fake, server := newServer(t, "答案")
	reviews := &fakeReviewSource{day: testDay()}
	service := newService(newClient(t, server.URL, "sk-history", 0), reviews, nil)
	history := []Turn{
		{Role: "system", Content: "system-secret"},
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: "three"},
		{Role: "assistant", Content: "four"},
	}
	log := &eventLog{}
	if err := service.Chat(context.Background(), Request{Question: "查询 2026-10-12 的报告", AsOf: "2026-10-12", History: history}, log.emit); err != nil {
		t.Fatal(err)
	}
	if log.last(EventPrompt).Prompt.HistoryTurns != 3 {
		t.Fatalf("unexpected history summary: %+v", log.last(EventPrompt).Prompt)
	}
	request := fake.snapshot()[0]
	if len(request.Messages) != 5 || strings.Contains(request.Raw, "system-secret") || strings.Contains(request.Raw, "one") {
		t.Fatalf("history window was not sanitized: %+v", request.Messages)
	}
	if request.Messages[1].Content != "two" || request.Messages[2].Content != "three" || request.Messages[3].Content != "four" {
		t.Fatalf("wrong recent history: %+v", request.Messages)
	}
}

func TestRequestAcceptsDashboardRoleAndMessagesShape(t *testing.T) {
	raw := []byte(`{
		"as_of":"2026-10-12",
		"revision":"rev-1",
		"role_id":"simons",
		"messages":[
			{"role":"user","content":"之前的问题"},
			{"role":"assistant","content":"之前的回答"},
			{"role":"user","content":"查询报告"}
		]
	}`)
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if req.Role != "simons" || req.RoleID != "simons" || req.Question != "查询报告" {
		t.Fatalf("dashboard request was not mapped: %+v", req)
	}
	if len(req.History) != 2 || req.History[0].Content != "之前的问题" || req.History[1].Content != "之前的回答" {
		t.Fatalf("dashboard history was not mapped: %+v", req.History)
	}
	if err := json.Unmarshal([]byte(`{"messages":[],"unknown":true}`), &req); err == nil {
		t.Fatal("unknown request field was accepted")
	}
}

func TestEvidenceIDsAreStableAndSmall(t *testing.T) {
	_, server := newServer(t, "答案 [E1]", "答案 [E2]")
	reviews := &fakeReviewSource{day: testDay(), event: testEvent()}
	service := newService(newClient(t, server.URL, "sk-id", 0), reviews, nil)
	for i := 0; i < 2; i++ {
		log := &eventLog{}
		if err := service.Chat(context.Background(), Request{Question: "查询 2026-10-12 的报告", AsOf: "2026-10-12"}, log.emit); err != nil {
			t.Fatal(err)
		}
		evidence := log.last(EventEvidence).Evidence
		for j, item := range evidence {
			if item.ID != fmt.Sprintf("E%d", j+1) {
				t.Fatalf("unstable evidence id at %d: %+v", j, item)
			}
		}
	}
}

func hasString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

var _ = utf8.RuneCountInString
