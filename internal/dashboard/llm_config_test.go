package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

func llmConfigHandler(t *testing.T, settings config.LLM) *handler {
	t.Helper()
	settings.ConfigFile = filepath.Join(t.TempDir(), "llm.config")
	h := newHandler(config.Defaults(), &testReader{}, nil).(*handler)
	h.llm = config.NewLLMRuntime(settings)
	return h
}

func llmRequest(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(payload))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://example.com")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-Stock-Watcher-Action", "llm-config")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func llmTestRequest(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/llm/config/test", strings.NewReader(`{}`))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://example.com")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-Stock-Watcher-Action", "llm-test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestLLMConfigViewMasksKeyAndSavesUnlockedFields(t *testing.T) {
	settings := config.DefaultLLM()
	settings.WriteMode = "loopback"
	settings.BaseURL = "https://old.example/v1"
	settings.APIKey = "locked-secret-key"
	settings.Model = "old-model"
	settings.Timeout = "30s"
	settings.Locked = map[string]bool{"api_key": true}
	h := llmConfigHandler(t, settings)

	w := llmRequest(t, h, http.MethodGet, "/api/llm/config", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"api_key_suffix":"-key"`) || strings.Contains(w.Body.String(), "locked-secret-key") {
		t.Fatalf("config view leaked or missed key metadata: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"write_enabled":true`) {
		t.Fatalf("loopback write was not advertised: %s", w.Body)
	}

	w = llmRequest(t, h, http.MethodPut, "/api/llm/config", map[string]any{
		"base_url": "https://new.example/v1",
		"api_key":  "must-not-replace",
		"model":    "new-model",
		"timeout":  "45s",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("config save failed: %d %s", w.Code, w.Body)
	}
	snapshot := h.llm.Snapshot()
	if snapshot.APIKey != "locked-secret-key" || snapshot.BaseURL != "https://new.example/v1" || snapshot.Model != "new-model" || snapshot.Timeout != "45s" {
		t.Fatalf("wrong effective config: %+v", snapshot)
	}
	raw, err := os.ReadFile(snapshot.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-not-replace") || strings.Contains(string(raw), "locked-secret-key") {
		t.Fatalf("saved config contains a secret: %s", raw)
	}
	info, err := os.Stat(snapshot.ConfigFile)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("config permission = %v, %v", info.Mode().Perm(), err)
	}
}

func TestLLMConfigWriteModesAndRequestGuards(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		remote   string
		wantCode int
	}{
		{"loopback_allowed", "loopback", "127.0.0.1:1234", http.StatusOK},
		{"loopback_remote_denied", "loopback", "192.0.2.10:1234", http.StatusForbidden},
		{"off_denied", "off", "127.0.0.1:1234", http.StatusForbidden},
		{"all_allowed", "all", "192.0.2.10:1234", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := config.DefaultLLM()
			settings.WriteMode = tc.mode
			settings.BaseURL = "https://old.example/v1"
			h := llmConfigHandler(t, settings)
			r := httptest.NewRequest(http.MethodPut, "/api/llm/config", strings.NewReader(`{"model":"next-model"}`))
			r.RemoteAddr = tc.remote
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "http://example.com")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("X-Stock-Watcher-Action", "llm-config")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.wantCode {
				t.Fatalf("write mode %s from %s = %d %s", tc.mode, tc.remote, w.Code, w.Body)
			}
		})
	}
	settings := config.DefaultLLM()
	settings.WriteMode = "loopback"
	h := llmConfigHandler(t, settings)
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		func(r *http.Request) { r.Header.Set("X-Stock-Watcher-Action", "wrong") },
	} {
		r := httptest.NewRequest(http.MethodPut, "/api/llm/config", strings.NewReader(`{}`))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://example.com")
		r.Header.Set("X-Stock-Watcher-Action", "llm-config")
		mutate(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("unsafe config request accepted: %d %s", w.Code, w.Body)
		}
	}
	if w := llmRequest(t, h, http.MethodPost, "/api/llm/config", map[string]any{}); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST config = %d", w.Code)
	}
	if w := llmRequest(t, h, http.MethodPut, "/api/llm/config", map[string]any{"unknown": true}); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field accepted: %d %s", w.Code, w.Body)
	}
}

func TestLLMConfigTestSendsNoUserDataAndClassifiesErrors(t *testing.T) {
	var sawBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		sawBody = buf.String()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	settings := config.DefaultLLM()
	settings.BaseURL = upstream.URL
	settings.APIKey = "test-key"
	settings.Model = "test-model"
	settings.Timeout = "5s"
	settings.WriteMode = "loopback"
	h := llmConfigHandler(t, settings)
	w := llmTestRequest(t, h)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("test endpoint failed: %d %s", w.Code, w.Body)
	}
	if strings.Contains(sawBody, "复盘") || strings.Contains(sawBody, "sz000001") || !strings.Contains(sawBody, "Reply with OK") {
		t.Fatalf("test request included unexpected content: %s", sawBody)
	}

	upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"private upstream body"}`)
	})
	settings.BaseURL = upstream.URL
	h = llmConfigHandler(t, settings)
	w = llmTestRequest(t, h)
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "private upstream body") {
		t.Fatalf("authentication error leaked: %d %s", w.Code, w.Body)
	}
}
