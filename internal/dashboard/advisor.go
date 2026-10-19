package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/advisor"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/llm"
)

const (
	advisorBodyLimit      = 128 << 10
	advisorStreamTimeout  = 75 * time.Second
	advisorMaxQuestion    = 12000
	advisorMaxHistory     = 12
	advisorMaxHistoryItem = 4000
)

type masterRolesResponse struct {
	Roles       []advisor.Role `json:"roles"`
	DefaultRole string         `json:"default_role"`
	Version     int            `json:"schema_version"`
}

type advisorHTTPRequest struct {
	Mode     string         `json:"mode,omitempty"`
	Role     string         `json:"role,omitempty"`
	RoleID   string         `json:"role_id,omitempty"`
	Question string         `json:"question,omitempty"`
	History  []advisor.Turn `json:"history,omitempty"`
	Messages []advisor.Turn `json:"messages,omitempty"`
	AsOf     string         `json:"as_of,omitempty"`
	Code     string         `json:"code,omitempty"`
	Revision string         `json:"revision,omitempty"`
}

func (h *handler) serveMasterRoles(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, r, http.StatusBadRequest, "roles do not accept query parameters")
		return
	}
	writeJSON(w, r, http.StatusOK, masterRolesResponse{
		Roles:       advisor.Roles(),
		DefaultRole: advisor.DefaultRoleID,
		Version:     advisor.Version,
	})
}

func (h *handler) serveMasterChat(w http.ResponseWriter, r *http.Request) {
	if !h.allowAdvisorRequest(r, "master-chat") {
		writeError(w, r, http.StatusForbidden, "advisor request is not allowed from this request")
		return
	}
	req, err := decodeAdvisorRequest(w, r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid advisor request")
		return
	}
	if req.Mode == "" {
		req.Mode = advisor.ModeChat
	}
	if req.Mode != advisor.ModeChat && req.Mode != advisor.ModeReview {
		writeError(w, r, http.StatusBadRequest, "mode must be chat or review")
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		writeError(w, r, http.StatusBadRequest, "question is required")
		return
	}
	h.serveAdvisorStream(w, r, req, false)
}

func (h *handler) serveReviewAnalysis(w http.ResponseWriter, r *http.Request) {
	if !h.allowAdvisorRequest(r, "review-analysis") {
		writeError(w, r, http.StatusForbidden, "review analysis is not allowed from this request")
		return
	}
	req, err := decodeAdvisorRequest(w, r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid review analysis request")
		return
	}
	req.Mode = advisor.ModeReview
	if strings.TrimSpace(req.Question) == "" {
		req.Question = defaultReviewQuestion(req.AsOf, req.Code)
	}
	if strings.TrimSpace(req.AsOf) == "" {
		writeError(w, r, http.StatusBadRequest, "as_of is required")
		return
	}
	h.serveAdvisorStream(w, r, req, true)
}

func (h *handler) serveAdvisorStream(w http.ResponseWriter, r *http.Request, req advisor.Request, forceReview bool) {
	if h.llm == nil {
		writeError(w, r, http.StatusServiceUnavailable, "llm configuration unavailable")
		return
	}
	if h.review == nil {
		writeError(w, r, http.StatusServiceUnavailable, "review evidence unavailable")
		return
	}
	service, err := h.newAdvisor()
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "llm configuration unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), advisorStreamTimeout)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(advisorStreamTimeout))

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	encoder := json.NewEncoder(w)
	emit := func(event advisor.StreamEvent) error {
		if err := encoder.Encode(event); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	if forceReview {
		err = service.ReviewAnalysis(ctx, req, emit)
	} else {
		err = service.Chat(ctx, req, emit)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		h.log.Warn("Advisor stream ended with an error", "error", err)
	}
}

func (h *handler) newAdvisor() (*advisor.Service, error) {
	settings := h.llm.Snapshot()
	var client *llm.Client
	if settings.Enabled && settings.Configured() {
		created, err := llm.New(llm.Config{
			BaseURL:         settings.BaseURL,
			APIKey:          settings.APIKey,
			Model:           settings.Model,
			Timeout:         config.LLMTimeout(settings),
			MaxOutputTokens: config.MaxOutputTokens,
		}, nil)
		if err != nil {
			return nil, err
		}
		client = created
	}
	effective := h.config
	effective.LLM = settings
	return advisor.New(effective, client, h.review, h.experiments), nil
}

func decodeAdvisorRequest(w http.ResponseWriter, r *http.Request) (advisor.Request, error) {
	var wire advisorHTTPRequest
	if r.URL.RawQuery != "" {
		return advisor.Request{}, errors.New("query parameters are not supported")
	}
	r.Body = http.MaxBytesReader(w, r.Body, advisorBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return advisor.Request{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return advisor.Request{}, errors.New("multiple request documents")
	}
	if len(wire.History) > 0 && len(wire.Messages) > 0 {
		return advisor.Request{}, errors.New("send history or messages, not both")
	}
	role := strings.TrimSpace(wire.Role)
	if role == "" {
		role = strings.TrimSpace(wire.RoleID)
	}
	if role != "" {
		if _, ok := advisor.RoleByID(role); !ok {
			return advisor.Request{}, advisor.ErrUnknownRole
		}
	}
	question := strings.TrimSpace(wire.Question)
	history := append([]advisor.Turn(nil), wire.History...)
	if question == "" && len(wire.Messages) > 0 {
		index := lastUserMessage(wire.Messages)
		if index < 0 {
			return advisor.Request{}, errors.New("the last message must be a user question")
		}
		question = strings.TrimSpace(wire.Messages[index].Content)
		history = append(history, wire.Messages[:index]...)
	} else if len(wire.Messages) > 0 {
		history = append(history, wire.Messages...)
		if len(history) > 0 {
			last := history[len(history)-1]
			if strings.EqualFold(strings.TrimSpace(last.Role), "user") && strings.TrimSpace(last.Content) == question {
				history = history[:len(history)-1]
			}
		}
	}
	if utf8.RuneCountInString(question) > advisorMaxQuestion {
		return advisor.Request{}, errors.New("question is too long")
	}
	if len(history) > advisorMaxHistory {
		return advisor.Request{}, errors.New("history is too long")
	}
	for _, turn := range history {
		if utf8.RuneCountInString(turn.Content) > advisorMaxHistoryItem {
			return advisor.Request{}, errors.New("history turn is too long")
		}
	}
	if wire.AsOf != "" && !validReviewDate(wire.AsOf) {
		return advisor.Request{}, errors.New("as_of is invalid")
	}
	if wire.Code != "" && !config.ValidCode(wire.Code) {
		return advisor.Request{}, errors.New("code is invalid")
	}
	if wire.Revision != "" && !validRevision(wire.Revision) {
		return advisor.Request{}, errors.New("revision is invalid")
	}
	return advisor.Request{
		Mode:     strings.TrimSpace(wire.Mode),
		Role:     role,
		Question: question,
		AsOf:     strings.TrimSpace(wire.AsOf),
		Code:     strings.ToLower(strings.TrimSpace(wire.Code)),
		Revision: strings.TrimSpace(wire.Revision),
		History:  history,
	}, nil
}

func lastUserMessage(messages []advisor.Turn) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(messages[i].Role), "user") && strings.TrimSpace(messages[i].Content) != "" {
			return i
		}
	}
	return -1
}

func (h *handler) allowAdvisorRequest(r *http.Request, action string) bool {
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

func defaultReviewQuestion(asOf, code string) string {
	if code != "" {
		return "请复盘 " + asOf + " 的 " + code + " 推荐结果，解释是否入场、T+1/T+3/T+5 检查点和关键风险证据。"
	}
	return "请复盘 " + asOf + " 的报告推荐结果，解释整体命中、未入场、缺数据和主要风险证据。"
}

func validReviewDate(value string) bool {
	date, err := time.Parse("2006-01-02", value)
	return err == nil && date.Format("2006-01-02") == value
}
