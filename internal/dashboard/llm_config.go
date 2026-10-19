package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/llm"
)

const llmConfigBodyLimit = 16 << 10
const llmTestTimeout = 15 * time.Second

type llmConfigView struct {
	Configured    bool              `json:"configured"`
	Enabled       bool              `json:"enabled"`
	BaseURL       string            `json:"base_url,omitempty"`
	Model         string            `json:"model,omitempty"`
	Timeout       string            `json:"timeout,omitempty"`
	APIKeyPresent bool              `json:"api_key_present"`
	APIKeySuffix  string            `json:"api_key_suffix,omitempty"`
	WriteEnabled  bool              `json:"write_enabled"`
	WriteMode     string            `json:"write_mode"`
	Locked        map[string]bool   `json:"locked,omitempty"`
	Sources       map[string]string `json:"fields,omitempty"`
}

func (h *handler) serveLLMConfig(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, r, http.StatusBadRequest, "llm config does not accept query parameters")
		return
	}
	if h.llm == nil {
		writeError(w, r, http.StatusServiceUnavailable, "llm config unavailable")
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		writeJSON(w, r, http.StatusOK, h.llmView(r))
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", allowedMethods(r.URL.Path))
		writeError(w, r, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allowLLMRequest(r, "llm-config") {
		writeError(w, r, http.StatusForbidden, "llm configuration is not writable from this request")
		return
	}
	var update config.LLMUpdate
	if err := decodeLLMUpdate(r, &update); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid llm configuration")
		return
	}
	if _, err := h.llm.Apply(update); err != nil {
		if !h.llmSourceAllowed(r) {
			writeError(w, r, http.StatusForbidden, "llm configuration is not writable from this request")
		} else {
			writeError(w, r, http.StatusBadRequest, "invalid or unwritable llm configuration")
		}
		return
	}
	writeJSON(w, r, http.StatusOK, h.llmView(r))
}

func (h *handler) serveLLMConfigTest(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, r, http.StatusBadRequest, "llm test does not accept query parameters")
		return
	}
	if h.llm == nil {
		writeError(w, r, http.StatusServiceUnavailable, "llm config unavailable")
		return
	}
	if !h.allowLLMRequest(r, "llm-test") {
		writeError(w, r, http.StatusForbidden, "llm testing is not allowed from this request")
		return
	}
	if err := requireEmptyJSONBody(r); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid llm test request")
		return
	}
	cfg := h.llm.Snapshot()
	if !cfg.Enabled {
		writeError(w, r, http.StatusBadRequest, "llm is disabled")
		return
	}
	client, err := llm.New(llm.Config{BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.Model, Timeout: config.LLMTimeout(cfg), MaxOutputTokens: 1}, nil)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid llm configuration")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), llmTestTimeout)
	defer cancel()
	if err := client.Test(ctx); err != nil {
		status, message := llmTestError(err)
		writeError(w, r, status, message)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]bool{"ok": true})
}

func (h *handler) llmView(r *http.Request) llmConfigView {
	cfg := h.llm.Snapshot()
	suffix := ""
	key := strings.TrimSpace(cfg.APIKey)
	if len(key) > 4 {
		suffix = key[len(key)-4:]
	} else if key != "" {
		suffix = "****"
	}
	locked := map[string]bool{}
	for field, value := range cfg.Locked {
		locked[field] = value
	}
	sources := map[string]string{}
	for field, value := range cfg.Sources {
		sources[field] = value
	}
	return llmConfigView{Configured: cfg.Configured(), Enabled: cfg.Enabled, BaseURL: cfg.BaseURL, Model: cfg.Model, Timeout: cfg.Timeout, APIKeyPresent: key != "", APIKeySuffix: suffix, WriteEnabled: h.llmSourceAllowed(r), WriteMode: cfg.WriteMode, Locked: locked, Sources: sources}
}

func decodeLLMUpdate(r *http.Request, update *config.LLMUpdate) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, llmConfigBodyLimit+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(update); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("multiple llm configuration documents")
	}
	return nil
}

func requireEmptyJSONBody(r *http.Request) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(body))) != 0 && strings.TrimSpace(string(body)) != "{}" {
		return errors.New("llm test body must be empty or an empty object")
	}
	return nil
}

func (h *handler) allowLLMRequest(r *http.Request, action string) bool {
	if !h.llmSourceAllowed(r) {
		return false
	}
	if r.Header.Get("X-Stock-Watcher-Action") != action {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, r.Host) {
			return false
		}
	}
	return true
}

func (h *handler) llmSourceAllowed(r *http.Request) bool {
	if h == nil || h.llm == nil {
		return false
	}
	mode := h.llm.Snapshot().WriteMode
	switch mode {
	case "off":
		return false
	case "all":
		return true
	case "loopback":
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(strings.Trim(host, "[]"))
		return ip != nil && ip.IsLoopback()
	default:
		return false
	}
}

func llmTestError(err error) (int, string) {
	switch {
	case errors.Is(err, llm.ErrMissingAPIKey):
		return http.StatusBadRequest, "llm is not configured"
	case errors.Is(err, llm.ErrAuthentication):
		return http.StatusBadGateway, "llm authentication failed"
	case errors.Is(err, llm.ErrRetryable):
		return http.StatusBadGateway, "llm service is temporarily unavailable"
	case errors.Is(err, llm.ErrRequestRejected), errors.Is(err, llm.ErrInvalidBaseURL):
		return http.StatusBadRequest, "llm rejected the test request"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "llm test timed out"
	default:
		return http.StatusBadGateway, "llm test failed"
	}
}
