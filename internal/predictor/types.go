// Package predictor owns the experimental five-minute price forecaster. It
// consumes finalized minute bars, trains deterministic tree models, records
// predictions for audit, and optionally publishes one aggregate card per bar.
package predictor

import (
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
)

const (
	ArtifactSchemaVersion   = 2
	PredictionSchemaVersion = 3
	FeatureVersion          = 1
	StatusSchemaVersion     = 1
)

type Target string

const (
	TargetHigh  Target = "high"
	TargetLow   Target = "low"
	TargetClose Target = "close"
)

var Targets = []Target{TargetHigh, TargetLow, TargetClose}

func (t Target) Valid() bool {
	return t == TargetHigh || t == TargetLow || t == TargetClose
}

// Sample is one finalized input bar and the following five-minute bar it
// predicts. Labels are percentage returns relative to InputClose.
type Sample struct {
	Code          string             `json:"code"`
	Date          string             `json:"date"`
	InputEnd      time.Time          `json:"input_end"`
	TargetEnd     time.Time          `json:"target_end"`
	InputOpen     float64            `json:"input_open"`
	InputHigh     float64            `json:"input_high"`
	InputLow      float64            `json:"input_low"`
	InputClose    float64            `json:"input_close"`
	BaselineRange float64            `json:"baseline_range"`
	Features      []float64          `json:"features"`
	Labels        map[Target]float64 `json:"labels"`
}

type FeatureVector struct {
	Names  []string  `json:"names"`
	Values []float64 `json:"values"`
}

// Prediction is the immutable model output for one stock and one target bar.
type Prediction struct {
	Code        string    `json:"code"`
	Date        string    `json:"date"`
	InputEnd    time.Time `json:"input_end"`
	TargetEnd   time.Time `json:"target_end"`
	ModelID     string    `json:"model_id"`
	BasePrice   float64   `json:"base_price"`
	HighPct     float64   `json:"high_pct"`
	LowPct      float64   `json:"low_pct"`
	ClosePct    float64   `json:"close_pct"`
	Trend       string    `json:"trend"`
	Confidence  string    `json:"confidence"`
	TrendScore  float64   `json:"trend_score"`
	InferenceMS float64   `json:"inference_ms,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Actual records the realized target bar. Status is resolved or unresolved.
type Actual struct {
	Code       string    `json:"code"`
	TargetEnd  time.Time `json:"target_end"`
	ModelID    string    `json:"model_id"`
	HighPct    float64   `json:"high_pct"`
	LowPct     float64   `json:"low_pct"`
	ClosePct   float64   `json:"close_pct"`
	Status     string    `json:"status"`
	ResolvedAt time.Time `json:"resolved_at"`
}

type TargetMetrics struct {
	High  float64 `json:"high"`
	Low   float64 `json:"low"`
	Close float64 `json:"close"`
}

type ModelInfo struct {
	SchemaVersion   int                      `json:"schema_version"`
	ModelID         string                   `json:"model_id"`
	CreatedAt       time.Time                `json:"created_at"`
	DataFrom        string                   `json:"data_from"`
	DataTo          string                   `json:"data_to"`
	ValidFor        string                   `json:"valid_for"`
	FeatureVersion  int                      `json:"feature_version"`
	FeatureNames    []string                 `json:"feature_names"`
	Metrics         []modeleval.MetricRecord `json:"metrics"`
	Residuals       TargetMetrics            `json:"residual_bands"`
	Loss            string                   `json:"loss,omitempty"`
	CloseLoss       string                   `json:"close_loss,omitempty"`
	CodeVersion     string                   `json:"code_version"`
	Checksum        string                   `json:"checksum"`
	ArtifactPath    string                   `json:"artifact_path"`
	TrainDurationMS int64                    `json:"train_duration_ms,omitempty"`
}

type LatencyStats struct {
	Count int     `json:"count"`
	AvgMS float64 `json:"avg_ms"`
	P95MS float64 `json:"p95_ms"`
}

type Status struct {
	Version           int       `json:"schema_version"`
	Enabled           bool      `json:"enabled"`
	PushEnabled       bool      `json:"push_enabled"`
	ModelID           string    `json:"model_id,omitempty"`
	ModelValidFor     string    `json:"model_valid_for,omitempty"`
	TrainedAt         time.Time `json:"trained_at,omitempty"`
	LastPredictionAt  time.Time `json:"last_prediction_at,omitempty"`
	LastPredictionEnd time.Time `json:"last_prediction_end,omitempty"`
	LastError         string    `json:"last_error,omitempty"`
	PredictionsToday  int       `json:"predictions_today"`
	PushDelivered     int       `json:"push_delivered"`
	PushBlocked       int       `json:"push_blocked"`
	TrainDurationMS   int64     `json:"train_duration_ms,omitempty"`
	InferenceCount    int       `json:"inference_count"`
	InferenceAvgMS    float64   `json:"inference_avg_ms"`
	InferenceP95MS    float64   `json:"inference_p95_ms"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type View struct {
	Status
	WriteEnabled bool   `json:"write_enabled"`
	WriteMode    string `json:"write_mode"`
}

type Result struct {
	Date        string       `json:"date"`
	TargetEnd   time.Time    `json:"target_end"`
	Predictions []Prediction `json:"predictions"`
	Pushed      bool         `json:"pushed"`
	Skipped     []string     `json:"skipped,omitempty"`
}

type ModelPointer struct {
	SchemaVersion int       `json:"schema_version"`
	ModelID       string    `json:"model_id"`
	Path          string    `json:"path"`
	Checksum      string    `json:"checksum"`
	ValidFor      string    `json:"valid_for"`
	CreatedAt     time.Time `json:"created_at"`
}
