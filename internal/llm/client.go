// Package llm provides a small OpenAI-compatible streaming chat client.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultTimeout is used when Config.Timeout is not positive.
	DefaultTimeout = 60 * time.Second

	// DefaultMaxOutputTokens is used when no positive output limit is set.
	DefaultMaxOutputTokens = 1024

	maxStreamBytes     int64 = 2 * 1024 * 1024
	maxStreamLineBytes       = 1024 * 1024
	maxErrorBodyBytes  int64 = 4 * 1024
)

var (
	// ErrInvalidBaseURL indicates that Config.BaseURL is not an allowed endpoint.
	ErrInvalidBaseURL = errors.New("llm: invalid base URL")
	// ErrMissingModel indicates that Config.Model is empty.
	ErrMissingModel = errors.New("llm: missing model")
	// ErrMissingAPIKey indicates that Config.APIKey is empty.
	ErrMissingAPIKey = errors.New("llm: missing API key")
	// ErrRedirectNotAllowed indicates that the upstream attempted a redirect.
	ErrRedirectNotAllowed = errors.New("llm: redirects are not allowed")
	// ErrAuthentication classifies a 401 or 403 response.
	ErrAuthentication = errors.New("llm: authentication failed")
	// ErrRetryable classifies a transient upstream response.
	ErrRetryable = errors.New("llm: retryable upstream error")
	// ErrRequestRejected classifies a non-retryable non-2xx response.
	ErrRequestRejected = errors.New("llm: request rejected")
	// ErrRequest indicates that the HTTP request could not be completed.
	ErrRequest = errors.New("llm: HTTP request failed")
	// ErrStreamRead indicates that the response stream could not be read.
	ErrStreamRead = errors.New("llm: failed to read response stream")
	// ErrMalformedStream indicates invalid SSE JSON or an unexpected chunk shape.
	ErrMalformedStream = errors.New("llm: malformed response stream")
	// ErrUpstream indicates an error object embedded in a successful response.
	ErrUpstream = errors.New("llm: upstream returned an error")
	// ErrStreamTooLarge indicates that a response body exceeded 2 MiB.
	ErrStreamTooLarge = errors.New("llm: response stream exceeds 2 MiB")
	// ErrLineTooLarge indicates that one SSE line exceeded 1 MiB.
	ErrLineTooLarge = errors.New("llm: response stream line exceeds 1 MiB")
	// ErrIncompleteStream indicates EOF before the [DONE] marker.
	ErrIncompleteStream = errors.New("llm: response stream ended without [DONE]")
)

// Config configures a Client. BaseURL must be HTTPS, or HTTP only for a
// loopback host. Model is required. APIKey is required when Chat or Test is
// called and is never included in error messages.
type Config struct {
	BaseURL         string
	APIKey          string
	Model           string
	Timeout         time.Duration
	MaxOutputTokens int
}

// Normalize returns a copy of c with defaults for optional limits. Required
// values are deliberately left empty so New can report a specific error.
func (c Config) Normalize() Config {
	c.BaseURL = strings.TrimSpace(c.BaseURL)
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.Model = strings.TrimSpace(c.Model)
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.MaxOutputTokens <= 0 {
		c.MaxOutputTokens = DefaultMaxOutputTokens
	}
	return c
}

// Message is one chat message.
type Message struct {
	Role    string
	Content string
}

// ChatRequest is a provider-neutral chat request. A positive MaxOutputTokens
// overrides the client default for this request.
type ChatRequest struct {
	System          string
	Messages        []Message
	Temperature     float64
	MaxOutputTokens int
}

// Usage contains the token counts reported by the provider. Providers that do
// not report usage in a stream leave these values at zero.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// Result is the accumulated assistant response.
type Result struct {
	Text         string
	Usage        Usage
	FinishReason string
}

// HTTPError describes a non-2xx response without exposing its body.
type HTTPError struct {
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("llm: upstream returned HTTP %d (%s)", e.StatusCode, e.kind())
}

func (e *HTTPError) kind() string {
	switch {
	case e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden:
		return "authentication"
	case e.StatusCode == http.StatusRequestTimeout || e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500:
		return "retryable"
	default:
		return "request"
	}
}

// Unwrap exposes the classification as a sentinel error.
func (e *HTTPError) Unwrap() error {
	switch e.kind() {
	case "authentication":
		return ErrAuthentication
	case "retryable":
		return ErrRetryable
	default:
		return ErrRequestRejected
	}
}

// Client is safe for concurrent use. Its fields are immutable after New.
type Client struct {
	endpoint        string
	apiKey          string
	model           string
	timeout         time.Duration
	maxOutputTokens int
	httpClient      *http.Client
}

// New validates cfg and creates a reusable client. A nil http.Client gets a
// default client; an injected client is shallow-copied, never mutated. A
// redirect check is always installed on the copy. cfg.Timeout bounds every
// Chat and Test call through its request context, while an injected client's
// own Timeout remains an additional upper bound.
func New(cfg Config, client *http.Client) (*Client, error) {
	cfg = cfg.Normalize()
	if cfg.Model == "" {
		return nil, ErrMissingModel
	}

	endpoint, err := chatCompletionsURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}

	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	} else {
		clientCopy := *client
		client = &clientCopy
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return ErrRedirectNotAllowed
	}

	return &Client{
		endpoint:        endpoint,
		apiKey:          cfg.APIKey,
		model:           cfg.Model,
		timeout:         cfg.Timeout,
		maxOutputTokens: cfg.MaxOutputTokens,
		httpClient:      client,
	}, nil
}

// Chat sends a streaming chat request and invokes onDelta for each non-empty
// content fragment. If onDelta returns an error, Chat stops immediately and
// returns that error unchanged. A stream must end with a [DONE] event; EOF
// without it returns ErrIncompleteStream.
func (c *Client) Chat(ctx context.Context, req ChatRequest, onDelta func(string) error) (Result, error) {
	if c.apiKey == "" {
		return Result{}, ErrMissingAPIKey
	}
	if ctx == nil {
		ctx = context.Background()
	}

	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = c.maxOutputTokens
	}
	body, err := json.Marshal(chatPayload{
		Model:       c.model,
		Messages:    makeWireMessages(req),
		Temperature: req.Temperature,
		MaxTokens:   maxTokens,
		Stream:      true,
	})
	if err != nil {
		return Result{}, fmt.Errorf("%w: encode request", ErrRequest)
	}

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("%w: build request", ErrRequest)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return Result{}, classifyRequestError(callCtx, err, false)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		return Result{}, &HTTPError{StatusCode: resp.StatusCode}
	}

	return readStream(callCtx, resp.Body, onDelta)
}

// Test performs a minimal fixed-prompt request to validate connectivity. It
// accepts no caller-supplied data and therefore cannot send user content.
func (c *Client) Test(ctx context.Context) error {
	_, err := c.Chat(ctx, ChatRequest{
		Messages: []Message{{
			Role:    "user",
			Content: "Reply with OK.",
		}},
		MaxOutputTokens: 1,
	}, nil)
	return err
}

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatPayload struct {
	Model       string        `json:"model"`
	Messages    []wireMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
	Stream      bool          `json:"stream"`
}

func makeWireMessages(req ChatRequest) []wireMessage {
	messages := make([]wireMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		messages = append(messages, wireMessage{Role: "system", Content: req.System})
	}
	for _, message := range req.Messages {
		messages = append(messages, wireMessage{Role: message.Role, Content: message.Content})
	}
	return messages
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func readStream(ctx context.Context, body io.Reader, onDelta func(string) error) (Result, error) {
	var result Result
	var text strings.Builder
	var dataLines []string
	done := false

	consumeEvent := func(payload string) error {
		payload = strings.TrimSpace(payload)
		if payload == "" {
			return nil
		}
		if payload == "[DONE]" {
			done = true
			return nil
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return ErrMalformedStream
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return ErrUpstream
		}

		for _, choice := range chunk.Choices {
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				result.FinishReason = *choice.FinishReason
			}
			if choice.Delta.Content == "" {
				continue
			}
			text.WriteString(choice.Delta.Content)
			if onDelta != nil {
				if err := onDelta(choice.Delta.Content); err != nil {
					return err
				}
			}
		}
		if chunk.Usage != nil {
			result.Usage = Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
			}
		}
		return nil
	}

	limited := &streamLimitReader{r: body, remaining: maxStreamBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLineBytes+2)

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > maxStreamLineBytes {
			return Result{}, ErrLineTooLarge
		}
		if line == "" {
			if err := consumeEvent(strings.Join(dataLines, "\n")); err != nil {
				return Result{}, err
			}
			dataLines = dataLines[:0]
			if done {
				break
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}

		field, value, hasColon := strings.Cut(line, ":")
		if hasColon && strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		if field == "data" {
			dataLines = append(dataLines, value)
		}
	}

	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, ctxErr
		}
		if errors.Is(err, ErrStreamTooLarge) {
			return Result{}, ErrStreamTooLarge
		}
		if errors.Is(err, bufio.ErrTooLong) {
			return Result{}, ErrLineTooLarge
		}
		return Result{}, classifyRequestError(ctx, err, true)
	}

	if !done && len(dataLines) > 0 {
		if err := consumeEvent(strings.Join(dataLines, "\n")); err != nil {
			return Result{}, err
		}
	}
	if !done {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, ctxErr
		}
		return Result{}, ErrIncompleteStream
	}

	result.Text = text.String()
	return result, nil
}

type streamLimitReader struct {
	r         io.Reader
	remaining int64
}

func (r *streamLimitReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, ErrStreamTooLarge
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.r.Read(p)
	r.remaining -= int64(n)
	if r.remaining <= 0 {
		return n, ErrStreamTooLarge
	}
	return n, err
}

func classifyRequestError(ctx context.Context, err error, reading bool) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return context.DeadlineExceeded
	}
	if errors.Is(err, ErrRedirectNotAllowed) {
		return ErrRedirectNotAllowed
	}
	if reading {
		return fmt.Errorf("%w: %v", ErrStreamRead, err)
	}
	return fmt.Errorf("%w: %v", ErrRequest, err)
}

func chatCompletionsURL(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("%w: empty URL", ErrInvalidBaseURL)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: malformed URL", ErrInvalidBaseURL)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", fmt.Errorf("%w: unsupported scheme", ErrInvalidBaseURL)
	}
	if parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("%w: missing host", ErrInvalidBaseURL)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w: userinfo is not allowed", ErrInvalidBaseURL)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(raw, "#") {
		return "", fmt.Errorf("%w: query and fragment are not allowed", ErrInvalidBaseURL)
	}
	if scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return "", fmt.Errorf("%w: HTTP is only allowed for loopback hosts", ErrInvalidBaseURL)
	}

	parsed.Scheme = scheme
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/chat/completions"
	parsed.RawPath = ""
	return parsed.String(), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4[0] == 127
	}
	return ip.Equal(net.IPv6loopback)
}
