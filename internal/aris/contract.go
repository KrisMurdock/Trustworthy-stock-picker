// Package aris governs offline, preregistered research runs. It never opens a
// production database and cannot promote a proxy backtest to independent OOS.
package aris

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
	"io"
	"math"
	"regexp"
	"strings"
	"time"
)

const MaxDocument = 256 << 20

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func ValidID(s string) bool { return validID.MatchString(s) }
func Hash(b []byte) string  { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func Digest(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		return ""
	}
	return Hash(b)
}

// Decode rejects duplicate members as well as unknown fields and trailing JSON.
// Numbers have the concrete destination type; non-finite values cannot decode.
func Decode(raw []byte, v any) error { return decodeLimit(raw, v, MaxDocument) }
func decodeLimit(raw []byte, v any, limit int) error {
	if len(raw) > limit {
		return errors.New("document exceeds limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if e := checkJSON(d, 0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func checkJSON(d *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting limit")
	}
	t, e := d.Token()
	if e != nil {
		return e
	}
	delimiter, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return errors.New("duplicate JSON key")
			}
			seen[s] = true
			if e = checkJSON(d, depth+1); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e = checkJSON(d, depth+1); e != nil {
				return e
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, e = d.Token()
	return e
}

type Request struct {
	Version  int    `json:"schema_version"`
	RunID    string `json:"run_id"`
	Strategy string `json:"strategy"`
}
type Budget struct {
	Candidates  int    `json:"max_candidates"`
	Attempts    int    `json:"max_attempts_per_candidate"`
	ReviewCalls int    `json:"max_review_calls"`
	Timeout     string `json:"timeout"`
}
type Constraints struct {
	Drawdown       *float64  `json:"max_drawdown"`
	TailLoss       *float64  `json:"absolute_tail_loss"`
	MinTrades      int       `json:"min_closed_trades"`
	MinDays        int       `json:"min_signal_days"`
	MinPF          float64   `json:"min_profit_factor"`
	MinExpectancy  float64   `json:"min_net_expectancy"`
	StressSlippage []float64 `json:"stress_slippage"`
}
type ReviewPolicy struct {
	Mode         string `json:"mode"`
	Backend      string `json:"backend,omitempty"`
	Model        string `json:"model,omitempty"`
	Family       string `json:"family,omitempty"`
	AuthorFamily string `json:"author_family,omitempty"`
	CrossFamily  bool   `json:"require_cross_family"`
}
type Contract struct {
	Version     int               `json:"schema_version"`
	ID          string            `json:"id"`
	HumanHash   string            `json:"human_contract_sha256"`
	Build       string            `json:"build_sha256"`
	Baseline    string            `json:"baseline"`
	Candidates  []string          `json:"candidates"`
	InputHashes map[string]string `json:"input_sha256"`
	Constraints Constraints       `json:"constraints"`
	Budget      Budget            `json:"budget"`
	Review      ReviewPolicy      `json:"review"`
	Inference   Inference         `json:"inference"`
}

// Bundle is the portable frozen-input protocol. A candidate's complete config,
// event set and price sequence are included in its hash, not just threshold knobs.
type Bundle struct {
	Request       Request                 `json:"request"`
	Contract      Contract                `json:"contract"`
	HumanContract string                  `json:"human_contract"`
	Inputs        map[string]maexec.Input `json:"inputs,omitempty"`
	Shared        *SharedInput            `json:"shared,omitempty"`
}

func (b Bundle) validate(build string) error {
	c := b.Contract
	if (b.Request.Version != 1 && b.Request.Version != 2) || !ValidID(b.Request.RunID) || b.Request.Strategy != "ma-proxy" || c.Version != 1 || !ValidID(c.ID) || !ValidID(c.Baseline) || strings.TrimSpace(b.HumanContract) == "" || c.HumanHash != Hash([]byte(b.HumanContract)) || c.Build != build {
		return errors.New("request, contract, human hash or build mismatch")
	}
	if len(c.Candidates) < 1 || c.Budget.Candidates < 1 || c.Budget.Candidates > 100 || len(c.Candidates) > c.Budget.Candidates || c.Budget.Attempts < 1 || c.Budget.Attempts > 10 || c.Budget.ReviewCalls < 0 || c.Budget.ReviewCalls > 10 {
		return errors.New("invalid or exceeded attempt budget")
	}
	timeout, e := time.ParseDuration(c.Budget.Timeout)
	if e != nil || timeout <= 0 || timeout > 24*time.Hour {
		return errors.New("timeout must be positive and at most 24h")
	}
	if c.Review.Mode != "off" && c.Review.Mode != "advisory" {
		return errors.New("invalid review mode")
	}
	if c.Review.Mode == "off" && c.Review.CrossFamily {
		return errors.New("cross-family review cannot be disabled")
	}
	if c.Review.Mode == "advisory" && (c.Review.Backend != "http" && c.Review.Backend != "exec") {
		return errors.New("unsupported review backend")
	}
	ids := append([]string{c.Baseline}, c.Candidates...)
	if len(b.Inputs) != len(ids) || len(c.InputHashes) != len(ids) {
		return errors.New("input set differs from registration")
	}
	seen := map[string]bool{}
	var sessions string
	var market string
	for _, id := range ids {
		if !ValidID(id) || seen[id] {
			return errors.New("invalid or duplicate candidate")
		}
		seen[id] = true
		in, ok := b.Inputs[id]
		h := Digest(in)
		if !ok || h == "" || c.InputHashes[id] != h {
			return fmt.Errorf("frozen input mismatch: %s", id)
		}
		ds := Digest(in.Sessions)
		if sessions != "" && sessions != ds {
			return errors.New("candidates must share evaluation sessions")
		}
		sessions = ds
		common := in
		common.Signals = nil
		common.Config.Target = 0 // Registered target and signal filters may differ.
		mh := Digest(common)
		if market != "" && market != mh {
			return errors.New("candidates must share frozen prices, provenance and portfolio constraints")
		}
		market = mh
	}
	r := c.Constraints
	for _, p := range []*float64{r.Drawdown, r.TailLoss} {
		if p != nil && (!finite(*p) || *p <= 0) {
			return errors.New("invalid risk budget")
		}
	}
	if r.Drawdown != nil && *r.Drawdown > 1 {
		return errors.New("drawdown must be a fraction")
	}
	if !finite(r.MinPF) || r.MinPF < 0 || !finite(r.MinExpectancy) || r.MinTrades < 0 || r.MinDays < 0 {
		return errors.New("invalid evidence threshold")
	}
	for _, slip := range r.StressSlippage {
		if !finite(slip) || slip < 0 || slip >= 1 {
			return errors.New("invalid stress slippage")
		}
	}
	if len(r.StressSlippage) > 16 {
		return errors.New("at most 16 stress scenarios")
	}
	if c.Inference != (Inference{}) && !c.Inference.valid() {
		return errors.New("invalid block inference registration")
	}
	return nil
}

// Validate checks the registration without executing a strategy or writing state.
func (b Bundle) Validate(build string) error { return b.validate(build) }
func finite(x float64) bool                  { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func (c Constraints) missing() bool {
	return c.Drawdown == nil || c.TailLoss == nil || len(c.StressSlippage) == 0 || c.MinTrades < 1 || c.MinDays < 1 || c.MinPF < 1 || c.MinExpectancy < 0
}
