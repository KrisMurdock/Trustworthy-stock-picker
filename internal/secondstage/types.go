// Package secondstage ranks the recall-first candidate pool and returns the
// Top-N candidates for the next trading day. It consumes an offline JEV
// artifact as a teacher feature and runs only deterministic Go tree inference
// in production.
package secondstage

import (
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
)

const (
	ReportSchemaVersion = 1
	ModelSchemaVersion  = 1
	FeatureVersion      = 1

	StatusOK      = "ok"
	StatusEmpty   = "empty"
	StatusFailed  = "failed"
	JEVStatusOK   = "ok"
	JEVStatusNone = "missing"
	JEVStatusBad  = "invalid"
)

type Options struct {
	AsOf    string
	Target  string
	TopN    int
	JEVPath string
	JEV     *JEVInput
	Now     time.Time
}

type JEVInput struct {
	Status string              `json:"status"`
	Scores map[string]JEVScore `json:"scores,omitempty"`
}

type JEVScore struct {
	Adjacent15  float64 `json:"adjacent15"`
	Full15Top12 float64 `json:"full15_top12"`
}

type JEVDocument struct {
	SchemaVersion int                 `json:"schema_version"`
	AsOf          string              `json:"as_of"`
	Scores        map[string]JEVScore `json:"scores"`
	Model         map[string]any      `json:"model,omitempty"`
	PromptVersion string              `json:"prompt_version,omitempty"`
}

type Candidate struct {
	Rank             int                        `json:"rank"`
	Code             string                     `json:"code"`
	Name             string                     `json:"name"`
	Sector           string                     `json:"sector,omitempty"`
	FirstStageRank   int                        `json:"first_stage_rank"`
	FirstStageBlend  float64                    `json:"first_stage_blend"`
	JEVAdjacent15    float64                    `json:"jev_adjacent15"`
	JEVFull15Top12   float64                    `json:"jev_full15_top12"`
	JEVPresent       bool                       `json:"jev_present"`
	PredictedMaxHigh float64                    `json:"predicted_max_high_return_pct"`
	Features         firststage.FeatureSnapshot `json:"features"`
	Reasons          []string                   `json:"reasons,omitempty"`
}

type Report struct {
	Version        int         `json:"schema_version"`
	Status         string      `json:"status"`
	AsOf           string      `json:"as_of"`
	Target         string      `json:"target_trading_date"`
	GeneratedAt    time.Time   `json:"generated_at"`
	ModelID        string      `json:"model_id,omitempty"`
	ModelSHA256    string      `json:"model_sha256,omitempty"`
	TrainedFrom    string      `json:"trained_from,omitempty"`
	TrainedThrough string      `json:"trained_through,omitempty"`
	FeatureVersion int         `json:"feature_version"`
	TopN           int         `json:"top_n"`
	InputCount     int         `json:"input_count"`
	JEVStatus      string      `json:"jev_status"`
	Degraded       bool        `json:"degraded"`
	Candidates     []Candidate `json:"candidates"`
	Excluded       []Excluded  `json:"excluded,omitempty"`
	Rules          []string    `json:"rules"`
	Reason         string      `json:"reason,omitempty"`
}

type Excluded struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}
