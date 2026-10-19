package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testAPIKey = "test-key-not-a-credential"
	testModel  = "test-model"
)

func newTestClient(t *testing.T, baseURL, apiKey string, timeout time.Duration) *Client {
	t.Helper()
	client, err := New(Config{
		BaseURL:         baseURL,
		APIKey:          apiKey,
		Model:           testModel,
		Timeout:         timeout,
		MaxOutputTokens: 128,
	}, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func writeSSE(w http.ResponseWriter, payload string) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "data: "+payload+"\n\n")
}

func TestConfigNormalize(t *testing.T) {
	got := (Config{BaseURL: "  https://example.com/v1  ", APIKey: " key ", Model: " model "}).Normalize()
	if got.BaseURL != "https://example.com/v1" || got.APIKey != "key" || got.Model != "model" {
		t.Fatalf("Normalize() = %+v", got)
	}
	if got.Timeout != DefaultTimeout {
		t.Fatalf("Timeout = %v, want %v", got.Timeout, DefaultTimeout)
	}
	if got.MaxOutputTokens != DefaultMaxOutputTokens {
		t.Fatalf("MaxOutputTokens = %d, want %d", got.MaxOutputTokens, DefaultMaxOutputTokens)
	}
}

func TestChatStreamsSSEChunks(t *testing.T) {
	var requestBody struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Temperature float64 `json:"temperature"`
		MaxTokens   int     `json:"max_tokens"`
		Stream      bool    `json:"stream"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected route", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testAPIKey {
			http.Error(w, "bad authorization", http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			http.Error(w, "bad content type", http.StatusUnsupportedMediaType)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": keep-alive\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()

		_, _ = io.WriteString(w, "data: {\"choices\":[\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "data: {\"delta\":{\"content\":\" world\"},\"finish_reason\":null}\n")
		_, _ = io.WriteString(w, "data: ],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
		w.(http.Flusher).Flush()

		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := newTestClient(t, server.URL+"/v1", testAPIKey, time.Second)
	var deltas []string
	result, err := client.Chat(context.Background(), ChatRequest{
		System:          "be concise",
		Messages:        []Message{{Role: "user", Content: "hello"}},
		Temperature:     0.25,
		MaxOutputTokens: 42,
	}, func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if result.Text != "Hello world" {
		t.Fatalf("Text = %q, want %q", result.Text, "Hello world")
	}
	if result.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want stop", result.FinishReason)
	}
	if result.Usage != (Usage{PromptTokens: 3, CompletionTokens: 2}) {
		t.Fatalf("Usage = %+v", result.Usage)
	}
	if strings.Join(deltas, "|") != "Hello| world" {
		t.Fatalf("deltas = %#v", deltas)
	}
	if requestBody.Model != testModel || !requestBody.Stream || requestBody.MaxTokens != 42 || requestBody.Temperature != 0.25 {
		t.Fatalf("request body = %+v", requestBody)
	}
	if len(requestBody.Messages) != 2 || requestBody.Messages[0].Role != "system" || requestBody.Messages[1].Content != "hello" {
		t.Fatalf("messages = %+v", requestBody.Messages)
	}
}

func TestChatMissingAPIKey(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, "", time.Second)
	if _, err := client.Chat(context.Background(), ChatRequest{MaxOutputTokens: 1}, nil); !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("Chat() error = %v, want ErrMissingAPIKey", err)
	}
	if err := client.Test(context.Background()); !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("Test() error = %v, want ErrMissingAPIKey", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("server received %d requests, want 0", calls.Load())
	}
}

func TestChatClassifiesHTTPErrorsWithoutLeakingBody(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrAuthentication},
		{http.StatusForbidden, ErrAuthentication},
		{http.StatusRequestTimeout, ErrRetryable},
		{http.StatusTooManyRequests, ErrRetryable},
		{http.StatusInternalServerError, ErrRetryable},
		{http.StatusBadGateway, ErrRetryable},
		{http.StatusBadRequest, ErrRequestRejected},
		{http.StatusNotFound, ErrRequestRejected},
	}

	const responseBody = "server-body-with-secret-value"
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, responseBody)
			}))
			defer server.Close()

			client := newTestClient(t, server.URL, testAPIKey, time.Second)
			_, err := client.Chat(context.Background(), ChatRequest{}, nil)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Chat() error = %v, want %v", err, tt.want)
			}
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != tt.status {
				t.Fatalf("HTTPError = %#v", httpErr)
			}
			for _, forbidden := range []string{responseBody, testAPIKey, "Authorization", "Bearer"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error %q leaks %q", err, forbidden)
				}
			}
		})
	}
}

func TestChatTimeoutUsesContextSemantics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, testAPIKey, 20*time.Millisecond)
	_, err := client.Chat(context.Background(), ChatRequest{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Chat() error = %v, want context deadline exceeded", err)
	}
}

func TestChatCallerCancellationUsesContextSemantics(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, testAPIKey, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.Chat(ctx, ChatRequest{}, nil)
		done <- err
	}()

	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Chat() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Chat() did not return after cancellation")
	}
}

func TestChatRejectsOversizedStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		line := ": " + strings.Repeat("x", 64*1024) + "\n"
		for i := 0; i < 33; i++ {
			if _, err := io.WriteString(w, line); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, testAPIKey, 5*time.Second)
	_, err := client.Chat(context.Background(), ChatRequest{}, nil)
	if !errors.Is(err, ErrStreamTooLarge) {
		t.Fatalf("Chat() error = %v, want ErrStreamTooLarge", err)
	}
}

func TestChatRejectsOversizedSSELine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": "+strings.Repeat("x", maxStreamLineBytes)+"\n")
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, testAPIKey, 5*time.Second)
	_, err := client.Chat(context.Background(), ChatRequest{}, nil)
	if !errors.Is(err, ErrLineTooLarge) {
		t.Fatalf("Chat() error = %v, want ErrLineTooLarge", err)
	}
}

func TestChatAcceptsSSELineAtLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": "+strings.Repeat("x", maxStreamLineBytes-2)+"\n")
		_, _ = io.WriteString(w, "data: [DONE]\n")
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, testAPIKey, 5*time.Second)
	if _, err := client.Chat(context.Background(), ChatRequest{}, nil); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
}

func TestNewRejectsUnsafeBaseURLs(t *testing.T) {
	tests := []string{
		"",
		"ftp://example.com/v1",
		"http://example.com/v1",
		"http://localhost.example.com/v1",
		"https://user:pass@example.com/v1",
		"https://@example.com/v1",
		"https://example.com/v1?token=secret",
		"https://example.com/v1?",
		"https://example.com/v1#fragment",
		"https://example.com/v1#",
		"https:///v1",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			_, err := New(Config{BaseURL: raw, APIKey: testAPIKey, Model: testModel}, nil)
			if !errors.Is(err, ErrInvalidBaseURL) {
				t.Fatalf("New(%q) error = %v, want ErrInvalidBaseURL", raw, err)
			}
		})
	}
}

func TestNewAcceptsAllowedBaseURLs(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"https://api.example.com", "https://api.example.com/chat/completions"},
		{"https://api.example.com/v1", "https://api.example.com/v1/chat/completions"},
		{"https://api.example.com/v1/", "https://api.example.com/v1/chat/completions"},
		{"http://localhost:8080/v1", "http://localhost:8080/v1/chat/completions"},
		{"http://127.0.0.1:8080/v1", "http://127.0.0.1:8080/v1/chat/completions"},
		{"http://127.255.255.255/v1", "http://127.255.255.255/v1/chat/completions"},
		{"http://[::1]:8080/v1", "http://[::1]:8080/v1/chat/completions"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			client, err := New(Config{BaseURL: tt.raw, APIKey: testAPIKey, Model: testModel}, nil)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if client.endpoint != tt.want {
				t.Fatalf("endpoint = %q, want %q", client.endpoint, tt.want)
			}
		})
	}
}

func TestChatUsesPathPrefixWithoutDoubleSlash(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		wantPath string
	}{
		{"root", "", "/chat/completions"},
		{"root slash", "/", "/chat/completions"},
		{"version", "/v1", "/v1/chat/completions"},
		{"version slash", "/v1/", "/v1/chat/completions"},
		{"nested trailing slash", "/gateway/v1//", "/gateway/v1/chat/completions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				writeSSE(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()

			client := newTestClient(t, server.URL+tt.prefix, testAPIKey, time.Second)
			if _, err := client.Chat(context.Background(), ChatRequest{}, nil); err != nil {
				t.Fatalf("Chat() error = %v", err)
			}
			if gotPath != tt.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

func TestChatStopsOnDeltaError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"content":"first"},"finish_reason":null}]}`)
		writeSSE(w, `{"choices":[{"delta":{"content":"second"},"finish_reason":"stop"}]}`)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, testAPIKey, time.Second)
	callbackErr := errors.New("stop callback")
	var calls int
	_, err := client.Chat(context.Background(), ChatRequest{}, func(string) error {
		calls++
		return callbackErr
	})
	if err != callbackErr {
		t.Fatalf("Chat() error = %v, want callback error", err)
	}
	if calls != 1 {
		t.Fatalf("callback calls = %d, want 1", calls)
	}
}

func TestChatAllowsMissingUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, testAPIKey, time.Second)
	result, err := client.Chat(context.Background(), ChatRequest{}, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if result.Usage != (Usage{}) {
		t.Fatalf("Usage = %+v, want zero", result.Usage)
	}
}

func TestChatRequiresDoneMarker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"content":"partial"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, testAPIKey, time.Second)
	_, err := client.Chat(context.Background(), ChatRequest{}, nil)
	if !errors.Is(err, ErrIncompleteStream) {
		t.Fatalf("Chat() error = %v, want ErrIncompleteStream", err)
	}
}

func TestChatAcceptsDoneWithoutTrailingBlankLine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"content":"complete"},"finish_reason":"stop"}]}`)
		_, _ = io.WriteString(w, "data: [DONE]\n")
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, testAPIKey, time.Second)
	result, err := client.Chat(context.Background(), ChatRequest{}, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if result.Text != "complete" {
		t.Fatalf("Text = %q", result.Text)
	}
}

func TestChatRejectsMalformedAndEmbeddedErrorChunks(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    error
	}{
		{"malformed", `{"choices":[`, ErrMalformedStream},
		{"embedded error", `{"error":{"message":"upstream failed"}}`, ErrUpstream},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeSSE(w, tt.payload)
			}))
			defer server.Close()

			client := newTestClient(t, server.URL, testAPIKey, time.Second)
			_, err := client.Chat(context.Background(), ChatRequest{}, nil)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Chat() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestChatDoesNotFollowRedirects(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		writeSSE(w, `{"choices":[{"delta":{"content":"wrong"},"finish_reason":"stop"}]}`)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer target.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, testAPIKey, time.Second)
	_, err := client.Chat(context.Background(), ChatRequest{}, nil)
	if !errors.Is(err, ErrRedirectNotAllowed) {
		t.Fatalf("Chat() error = %v, want ErrRedirectNotAllowed", err)
	}
	if redirected.Load() != 0 {
		t.Fatalf("redirect target received %d requests, want 0", redirected.Load())
	}
}

func TestNewDoesNotMutateInjectedClient(t *testing.T) {
	injected := &http.Client{Timeout: 2 * time.Second}
	if _, err := New(Config{BaseURL: "https://example.com/v1", APIKey: testAPIKey, Model: testModel}, injected); err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if injected.CheckRedirect != nil {
		t.Fatal("New() mutated the injected client's CheckRedirect")
	}
}

func TestTestSendsMinimalFixedRequest(t *testing.T) {
	var gotBody struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Stream    bool `json:"stream"`
		MaxTokens int  `json:"max_tokens"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		writeSSE(w, `{"choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}]}`)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, testAPIKey, time.Second)
	if err := client.Test(context.Background()); err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if !gotBody.Stream || gotBody.MaxTokens != 1 {
		t.Fatalf("request = %+v", gotBody)
	}
	if len(gotBody.Messages) != 1 || gotBody.Messages[0].Role != "user" || gotBody.Messages[0].Content != "Reply with OK." {
		t.Fatalf("messages = %+v", gotBody.Messages)
	}
}

func TestClientIsSafeForConcurrentUse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, testAPIKey, 2*time.Second)
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := client.Chat(context.Background(), ChatRequest{}, nil)
			if err == nil && result.Text != "ok" {
				err = fmt.Errorf("text = %q", result.Text)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Chat() error = %v", err)
		}
	}
}
