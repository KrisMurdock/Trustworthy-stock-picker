// Package secondstage ranks the recall-first candidate pool and returns the
// Top-N candidates for the next trading day. Production combines the
// first-stage pool percentile with any number of offline model percentiles
// through a weighted blend plan; the embedded tree prediction is retained as
// an auxiliary field.
package secondstage

import (
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
)

const (
	ReportSchemaVersion = 1
	ModelSchemaVersion  = 1
	FeatureVersionV1    = 1
	FeatureVersionV2    = 2
	FeatureVersion      = FeatureVersionV1

	RankerBlendWeighted = "blend_weighted_v1"
	DefaultBlendID      = "blend_50_50"
	ModelJEV            = "jev"

	StatusOK       = "ok"
	StatusEmpty    = "empty"
	StatusFailed   = "failed"
	ModelStatusOK  = "ok"
	ModelStatusNil = "missing"
	ModelStatusBad = "invalid"

	JEVStatusOK   = ModelStatusOK
	JEVStatusNone = ModelStatusNil
	JEVStatusBad  = ModelStatusBad
)

type Options struct {
	AsOf        string
	Target      string
	TopN        int
	PushEnabled bool
	Blend       BlendPlan
	Models      []ModelInput
	Now         time.Time
}

type BlendPlan struct {
	ID               string        `json:"id"`
	FirstStageWeight float64       `json:"first_stage_weight"`
	Models           []BlendWeight `json:"models"`
}

type BlendWeight struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
}

func DefaultBlendPlan() BlendPlan {
	return BlendPlan{
		ID:               DefaultBlendID,
		FirstStageWeight: 0.5,
		Models:           []BlendWeight{{Name: ModelJEV, Weight: 0.5}},
	}
}

type ModelInput struct {
	Name   string                `json:"name"`
	Status string                `json:"status"`
	Scores map[string]ModelScore `json:"scores,omitempty"`
}

type ModelSource struct {
	Name string
	Path string
}

type ModelScore struct {
	Adjacent15  float64 `json:"adjacent15"`
	Full15Top12 float64 `json:"full15_top12"`
}

type JEVInput = ModelInput
type JEVScore = ModelScore

type JEVDocument struct {
	SchemaVersion int                 `json:"schema_version"`
	AsOf          string              `json:"as_of"`
	Scores        map[string]JEVScore `json:"scores"`
	Model         map[string]any      `json:"model,omitempty"`
	PromptVersion string              `json:"prompt_version,omitempty"`
}

type Candidate struct {
	Rank            int     `json:"rank"`
	Code            string  `json:"code"`
	Name            string  `json:"name"`
	Sector          string  `json:"sector,omitempty"`
	FirstStageRank  int     `json:"first_stage_rank"`
	FirstStageBlend float64 `json:"first_stage_blend"`
	JEVAdjacent15   float64 `json:"jev_adjacent15"`
	JEVFull15Top12  float64 `json:"jev_full15_top12"`
	JEVPresent      bool    `json:"jev_present"`
	// FirstStagePercentile and the per-model percentiles are the normalized
	// inputs to the configured blend. JEVPercentile is retained for existing
	// report consumers and mirrors Models["jev"].Percentile when present.
	FirstStagePercentile float64                    `json:"first_stage_percentile"`
	JEVPercentile        float64                    `json:"jev_percentile"`
	Models               map[string]ModelSignal     `json:"models,omitempty"`
	BlendScore           float64                    `json:"blend_score"`
	PredictedMaxHigh     float64                    `json:"predicted_max_high_return_pct"`
	Features             firststage.FeatureSnapshot `json:"features"`
	Reasons              []string                   `json:"reasons,omitempty"`
}

type ModelSignal struct {
	Adjacent15  float64 `json:"adjacent15"`
	Full15Top12 float64 `json:"full15_top12"`
	Percentile  float64 `json:"percentile"`
	Present     bool    `json:"present"`
}

type ModelStatus struct {
	Name    string  `json:"name"`
	Weight  float64 `json:"weight"`
	Status  string  `json:"status"`
	Covered int     `json:"covered"`
}

type Report struct {
	Version        int           `json:"schema_version"`
	Status         string        `json:"status"`
	AsOf           string        `json:"as_of"`
	Target         string        `json:"target_trading_date"`
	GeneratedAt    time.Time     `json:"generated_at"`
	Ranker         string        `json:"ranker,omitempty"`
	ModelID        string        `json:"model_id,omitempty"`
	ModelSHA256    string        `json:"model_sha256,omitempty"`
	TrainedFrom    string        `json:"trained_from,omitempty"`
	TrainedThrough string        `json:"trained_through,omitempty"`
	FeatureVersion int           `json:"feature_version"`
	TopN           int           `json:"top_n"`
	PushEnabled    bool          `json:"push_enabled"`
	InputCount     int           `json:"input_count"`
	JEVStatus      string        `json:"jev_status"`
	Blend          BlendPlan     `json:"blend"`
	Models         []ModelStatus `json:"models,omitempty"`
	Degraded       bool          `json:"degraded"`
	Candidates     []Candidate   `json:"candidates"`
	Excluded       []Excluded    `json:"excluded,omitempty"`
	Rules          []string      `json:"rules"`
	Reason         string        `json:"reason,omitempty"`
}

type Excluded struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}
