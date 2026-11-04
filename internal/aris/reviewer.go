package aris

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed prompts/review.md
var reviewPrompt string

type Finding struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Evidence string `json:"evidence"`
	Clause   string `json:"contract_clause"`
	Test     string `json:"suggested_test"`
}
type ReviewResult struct {
	Version      int           `json:"schema_version"`
	Verdict      string        `json:"verdict"`
	Findings     []Finding     `json:"findings"`
	Model        string        `json:"model"`
	Family       string        `json:"model_family"`
	Independent  bool          `json:"independent"`
	Duration     time.Duration `json:"duration_ns"`
	ResponseHash string        `json:"response_sha256"`
}

// No free-form human document, source diff, paths, environment or credentials are
// sent. A hash-bound numeric summary prevents prompt injection from those inputs.
type ReviewInput struct {
	Version    int          `json:"schema_version"`
	Prompt     string       `json:"prompt"`
	BundleHash string       `json:"bundle_sha256"`
	Build      string       `json:"build_sha256"`
	Model      string       `json:"model"`
	Gates      []GateResult `json:"gates"`
}
type Reviewer interface {
	Review(context.Context, ReviewInput) (ReviewResult, error)
}

// HTTPReviewer uses a configured HTTPS structured-review service. It provides no
// tools or local file access. Identity must match the administrator's fixed
// model/family mapping, not a family invented by model-generated text.
type HTTPReviewer struct {
	endpoint, token, model, family string
	client                         *http.Client
}

func NewHTTPReviewer(endpoint, token, model, family string) (*HTTPReviewer, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !ValidID(model) || !ValidID(family) {
		return nil, errors.New("review requires a configured HTTPS URL and model identity")
	}
	return &HTTPReviewer{endpoint: endpoint, token: token, model: model, family: family, client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("review redirects forbidden") }}}, nil
}
func (r *HTTPReviewer) Review(ctx context.Context, in ReviewInput) (ReviewResult, error) {
	var out ReviewResult
	if in.Model != r.model {
		return out, errors.New("review model differs from configured identity")
	}
	raw, e := json.Marshal(in)
	if e != nil || len(raw) > 1<<20 {
		return out, errors.New("review input too large")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(raw))
	if e != nil {
		return out, e
	}
	req.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	start := time.Now()
	resp, e := r.client.Do(req)
	if e != nil {
		return out, errors.New("review transport failed")
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	out.ResponseHash = Hash(body)
	if e != nil || len(body) > 1<<20 || resp.StatusCode != http.StatusOK {
		return out, errors.New("review response unavailable or oversized")
	}
	if e = Decode(body, &out); e != nil {
		return ReviewResult{ResponseHash: Hash(body)}, errors.New("review response is not valid structured JSON")
	}
	out.ResponseHash = Hash(body)
	out.Duration = time.Since(start)
	out.Independent = false
	if out.Model != r.model || out.Family != r.family {
		return out, errors.New("review identity mismatch")
	}
	return out, validateReview(out)
}
func validateReview(r ReviewResult) error {
	if r.Version != 1 || !ValidID(r.Model) || !ValidID(r.Family) || len(r.Findings) > 100 {
		return errors.New("invalid review identity or schema")
	}
	blocking := false
	for _, f := range r.Findings {
		if f.Title == "" || f.Evidence == "" || f.Clause == "" || f.Test == "" || len(f.Title)+len(f.Evidence)+len(f.Clause)+len(f.Test) > 16000 {
			return errors.New("incomplete review finding")
		}
		switch f.Severity {
		case "P0", "P1":
			blocking = true
		case "P2", "P3":
		default:
			return errors.New("invalid finding severity")
		}
	}
	switch r.Verdict {
	case "veto":
		if !blocking {
			return errors.New("veto requires P0/P1 evidence")
		}
	case "pass":
		if len(r.Findings) != 0 {
			return errors.New("pass cannot contain unresolved findings")
		}
	case "concern":
	default:
		return errors.New("invalid verdict")
	}
	return nil
}
func (a *App) review(ctx context.Context, dir string, b Bundle, s *State) (string, error) {
	p := b.Contract.Review
	if p.Backend == "exec" {
		return "", errors.New("exec reviewer unavailable: no enforced sandbox configured")
	}
	if a.Reviewer == nil || !ValidID(p.Model) || !ValidID(p.Family) || !ValidID(p.AuthorFamily) {
		return "", errors.New("review configuration missing")
	}
	in := ReviewInput{Version: 1, Prompt: reviewPrompt, BundleHash: s.BundleHash, Build: s.Build, Model: p.Model, Gates: s.Gates}
	// A fixed whitelist is built above. No user free text or credentials are used.
	key := Digest(struct {
		Input  ReviewInput
		Policy ReviewPolicy
	}{in, p})
	path := filepath.Join(dir, "review-"+key+".json")
	var result ReviewResult
	if raw, e := Read(path, 1<<20); e == nil {
		if e = Decode(raw, &result); e != nil {
			return "", e
		}
		if e = validateReview(result); e != nil {
			return "", e
		}
	} else if !os.IsNotExist(e) {
		return "", e
	} else {
		if s.ReviewCalls >= b.Contract.Budget.ReviewCalls {
			return "", errors.New("review call budget exhausted")
		}
		s.ReviewCalls++
		if e = a.save(dir, *s); e != nil {
			return "", e
		}
		var reviewErr error
		result, reviewErr = a.Reviewer.Review(ctx, in)
		if reviewErr != nil {
			s.Review = &ReviewResult{Version: 1, Verdict: "concern", ResponseHash: result.ResponseHash}
			return "", reviewErr
		}
		if e = validateReview(result); e != nil {
			return "", e
		}
		if e = atomic(path, result); e != nil {
			return "", e
		}
	}
	if result.Model != p.Model || result.Family != p.Family {
		return "", errors.New("review identity differs from registration")
	}
	result.Independent = result.Family != p.AuthorFamily
	s.Review = &result
	if p.CrossFamily && !result.Independent {
		return "concern", nil
	}
	return result.Verdict, nil
}

// PublicReason excludes endpoint details and transport credentials from errors.
func PublicReason(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if strings.Contains(strings.ToLower(s), "authorization") {
		return "request failed"
	}
	return s
}
