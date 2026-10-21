package dayrange

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
)

func PredictionID(p Prediction) string {
	raw, _ := json.Marshal([]any{p.Code, p.TargetDate, string(p.Mode), p.AsOf.UTC().Format(time.RFC3339Nano), p.ModelID})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type ModelStatus string

const (
	ModelStatusInsufficient ModelStatus = "insufficient_data"
	ModelStatusResearch     ModelStatus = "research"
	ModelStatusValidated    ModelStatus = "validated"
	ModelStatusRetired      ModelStatus = "retired"
	ModelStatusInvalid      ModelStatus = "invalid"
)

type ModelRun struct {
	ModelID         string                   `json:"model_id"`
	Kind            string                   `json:"kind"`
	Status          ModelStatus              `json:"status"`
	FeatureVersion  int                      `json:"feature_version"`
	CodeVersion     string                   `json:"code_version"`
	CreatedAt       time.Time                `json:"created_at"`
	TrainFrom       string                   `json:"train_from,omitempty"`
	TrainTo         string                   `json:"train_to,omitempty"`
	CalibrationFrom string                   `json:"calibration_from,omitempty"`
	CalibrationTo   string                   `json:"calibration_to,omitempty"`
	ValidationFrom  string                   `json:"validation_from,omitempty"`
	ValidationTo    string                   `json:"validation_to,omitempty"`
	ValidFor        string                   `json:"valid_for,omitempty"`
	Metrics         []modeleval.MetricRecord `json:"metrics,omitempty"`
	Checksum        string                   `json:"checksum,omitempty"`
	Artifact        []byte                   `json:"-"`
	Reason          string                   `json:"reason,omitempty"`
}

type Prediction struct {
	PredictionID    string     `json:"prediction_id"`
	Code            string     `json:"code"`
	TargetDate      string     `json:"target_date"`
	Mode            Mode       `json:"mode"`
	AsOf            time.Time  `json:"as_of"`
	InputThrough    string     `json:"input_through"`
	ModelID         string     `json:"model_id"`
	Source          string     `json:"source"`
	FeatureVersion  int        `json:"feature_version"`
	ReferencePrice  float64    `json:"reference_price"`
	PointHigh       float64    `json:"point_high"`
	PointLow        float64    `json:"point_low"`
	QuantilesHigh   []Quantile `json:"quantiles_high"`
	QuantilesLow    []Quantile `json:"quantiles_low"`
	IntervalHigh    *Interval  `json:"interval_high"`
	IntervalLow     *Interval  `json:"interval_low"`
	InputSnapshotID string     `json:"input_snapshot_id"`
	BaselineModelID string     `json:"baseline_model_id"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
}

type ActualRecord struct {
	Code            string    `json:"code"`
	TargetDate      string    `json:"target_date"`
	Actual          *Extremes `json:"actual,omitempty"`
	Source          string    `json:"source"`
	ObservedThrough string    `json:"observed_through"`
	ResolvedAt      time.Time `json:"resolved_at"`
	Status          string    `json:"status"`
	Reason          string    `json:"reason,omitempty"`
}

type PredictionRecord struct {
	Prediction
	Actual *ActualRecord `json:"actual,omitempty"`
}

type Evaluation struct {
	ID             string    `json:"evaluation_id"`
	ModelID        string    `json:"model_id"`
	BaselineID     string    `json:"baseline_id"`
	Mode           Mode      `json:"mode"`
	AsOf           string    `json:"as_of"`
	ValidationFrom string    `json:"validation_from"`
	ValidationTo   string    `json:"validation_to"`
	MetricsVersion int       `json:"metrics_version"`
	Candidate      Report    `json:"candidate"`
	Baseline       Report    `json:"baseline"`
	Gate           Gate      `json:"gate"`
	CreatedAt      time.Time `json:"created_at"`
}

type AuditRecord struct {
	ID         string    `json:"audit_id"`
	SnapshotID string    `json:"snapshot_id"`
	AsOf       string    `json:"as_of"`
	CreatedAt  time.Time `json:"created_at"`
	Audit      Audit     `json:"audit"`
}
