package dashboard

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/advisor"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/review"
)

func advisorTestHandler(t *testing.T, fake *reviewFake, settings *config.LLM) *handler {
	t.Helper()
	h := newHandler(config.Defaults(), &testReader{}, nil).(*handler)
	h.review = fake
	if settings != nil {
		h.llm = config.NewLLMRuntime(*settings)
	}
	return h
}

func postAdvisor(t *testing.T, h http.Handler, path, action string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Stock-Watcher-Action", action)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func readAdvisorEvents(t *testing.T, body []byte) []advisor.StreamEvent {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var events []advisor.StreamEvent
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event advisor.StreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid NDJSON line %q: %v", line, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func eventTypes(events []advisor.StreamEvent) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Type)
	}
	return out
}

func TestMasterRolesAreStableAndReadOnly(t *testing.T) {
	h := advisorTestHandler(t, &reviewFake{}, nil)
	w := request(t, h, http.MethodGet, "/api/master/roles")
	if w.Code != http.StatusOK {
		t.Fatalf("roles failed: %d %s", w.Code, w.Body)
	}
	var response masterRolesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Version != advisor.Version || response.DefaultRole != advisor.DefaultRoleID || len(response.Roles) != 8 {
		t.Fatalf("unexpected roles: %+v", response)
	}
	if response.Roles[0].PromptVersion == "" || response.Roles[0].ID == "" {
		t.Fatalf("role metadata is incomplete: %+v", response.Roles[0])
	}
	if w = request(t, h, http.MethodGet, "/api/master/roles?x=1"); w.Code != http.StatusBadRequest {
		t.Fatalf("roles accepted a query: %d", w.Code)
	}
	if w = request(t, h, http.MethodGet, "/api/master/chat"); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("chat accepted GET: %d", w.Code)
	}
}

func TestAdvisorEndpointsRequireSameOriginActionAndJSON(t *testing.T) {
	h := advisorTestHandler(t, &reviewFake{}, nil)
	body := bytes.NewBufferString(`{"question":"什么是RSI？"}`)
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		func(r *http.Request) { r.Header.Set("X-Stock-Watcher-Action", "wrong") },
		func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/master/chat", bytes.NewReader(body.Bytes()))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Stock-Watcher-Action", "master-chat")
		r.Header.Set("Origin", "http://example.com")
		mutate(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("unsafe advisor request was accepted: %d %s", w.Code, w.Body)
		}
	}
	bad := []map[string]any{
		{"question": "什么是RSI？", "role": "unknown"},
		{"question": "什么是RSI？", "as_of": "2026-13-01"},
		{"question": "什么是RSI？", "code": "600519"},
		{"question": "什么是RSI？", "revision": "short"},
		{"question": "什么是RSI？", "mode": "other"},
		{"question": ""},
		{"question": "什么是RSI？", "unknown": true},
	}
	for _, payload := range bad {
		w := postAdvisor(t, h, "/api/master/chat", "master-chat", payload)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid advisor payload accepted: %+v -> %d %s", payload, w.Code, w.Body)
		}
	}
	w := postAdvisor(t, h, "/api/review/analysis", "review-analysis", map[string]any{"code": "sz000001"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("review analysis accepted a missing date: %d %s", w.Code, w.Body)
	}
}

func TestMasterChatStreamsRuleOnlyAnswer(t *testing.T) {
	h := advisorTestHandler(t, &reviewFake{}, nil)
	w := postAdvisor(t, h, "/api/master/chat", "master-chat", map[string]any{
		"question": "帮我下单买入这只股票",
		"role":     "buffett",
	})
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("chat stream failed: %d %s %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	events := readAdvisorEvents(t, w.Body.Bytes())
	types := eventTypes(events)
	for _, want := range []string{advisor.EventIntent, advisor.EventPlan, advisor.EventEvidence, advisor.EventPrompt, advisor.EventDelta, advisor.EventCitation, advisor.EventDone} {
		if !strings.Contains(strings.Join(types, ","), want) {
			t.Fatalf("missing %s in %v", want, types)
		}
	}
	if events[len(events)-1].Done == nil || events[len(events)-1].Done.AnswerRunes == 0 {
		t.Fatalf("done event is incomplete: %+v", events[len(events)-1])
	}
}

func TestReviewAnalysisExplainsMissingLLMAfterEvidence(t *testing.T) {
	revision := strings.Repeat("a", 64)
	fake := &reviewFake{day: review.Day{
		Version: review.Version, AsOf: "2026-10-12", Revision: revision,
		Revisions: []review.Revision{{ID: revision, AsOf: "2026-10-12"}},
		Events:    []review.EventSummary{{Code: "sz000001", Name: "测试银行", Rank: 1, State: review.StateNotEntered}},
	}}
	h := advisorTestHandler(t, fake, nil)
	w := postAdvisor(t, h, "/api/review/analysis", "review-analysis", map[string]any{"as_of": "2026-10-12", "revision": revision})
	if w.Code != http.StatusOK {
		t.Fatalf("analysis failed: %d %s", w.Code, w.Body)
	}
	events := readAdvisorEvents(t, w.Body.Bytes())
	var sawEvidence, sawError bool
	for _, event := range events {
		if event.Type == advisor.EventEvidence && len(event.Evidence) > 0 {
			sawEvidence = true
		}
		if event.Type == advisor.EventError && event.Error != nil && event.Error.Code == advisor.ErrLLMNotConfigured.Error() {
			sawError = true
		}
	}
	if !sawEvidence || !sawError {
		t.Fatalf("analysis events missing evidence or config error: %v", eventTypes(events))
	}
	if fake.asOf != "2026-10-12" || fake.revision != revision {
		t.Fatalf("analysis ignored scope: %+v", fake)
	}
}

func TestMasterChatStreamsConfiguredModelDeltas(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing upstream authorization")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"[E1] 检查点尚未成熟。\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	settings := config.DefaultLLM()
	settings.BaseURL = upstream.URL
	settings.APIKey = "test-key"
	settings.Model = "test-model"
	settings.Timeout = "5s"
	settings.ConfigFile = filepath.Join(t.TempDir(), "llm.config")
	settings.WriteMode = "off"
	fake := &reviewFake{
		day:   review.Day{Version: review.Version, AsOf: "2026-10-12", Revision: strings.Repeat("b", 64)},
		event: review.Event{EventSummary: review.EventSummary{Code: "sz000001", Name: "测试银行", Rank: 1, State: review.StatePending}},
	}
	h := advisorTestHandler(t, fake, &settings)
	w := postAdvisor(t, h, "/api/master/chat", "master-chat", map[string]any{
		"question": "请复盘 2026-10-12 的 sz000001 推荐结果。",
		"role":     "graham",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("chat failed: %d %s", w.Code, w.Body)
	}
	events := readAdvisorEvents(t, w.Body.Bytes())
	var answer strings.Builder
	var citation *advisor.Citation
	for _, event := range events {
		if event.Type == advisor.EventDelta {
			answer.WriteString(event.Delta)
		}
		if event.Type == advisor.EventCitation {
			citation = event.Citation
		}
	}
	if !strings.Contains(answer.String(), "[E1]") {
		t.Fatalf("streamed answer was not preserved: %q", answer.String())
	}
	if citation == nil || len(citation.Valid) != 1 || citation.Valid[0] != "E1" || citation.Zero {
		t.Fatalf("citation check is wrong: %+v", citation)
	}
	if fake.code != "sz000001" {
		t.Fatalf("event scope was not forwarded: %+v", fake)
	}
}
