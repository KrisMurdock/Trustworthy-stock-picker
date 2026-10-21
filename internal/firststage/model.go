package firststage

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
)

const (
	ModelSchemaVersion = 1
	ModelID            = "logistic_z"
	ModelTarget        = "future_three_day_max_high_touch_1pct"
)

//go:embed model.json
var modelArtifact []byte

const modelArtifactSHA256 = "41242068967f52ec11cd6936f24c3dbbb066d09980ae345aad2d0f1cd2356316"

var modelFeatures = []string{
	"z_atr14_pct",
	"z_low20_distance",
	"z_return5",
	"z_rsi14",
	"z_touch_rate20",
	"z_base_score",
	"z_volume_ratio5",
	"z_high20_distance",
}

type Model struct {
	SchemaVersion int
	ID            string
	Target        string
	Features      []string
	Means         []float64
	Scales        []float64
	Coefficients  []float64
	Intercept     float64
	BaseWeight    float64
	ModelWeight   float64
	TrainRows     int
	TrainFrom     string
	TrainThrough  string
	SHA256        string
}

type modelDocument struct {
	SchemaVersion int       `json:"schema_version"`
	Model         string    `json:"model"`
	Target        string    `json:"target"`
	Features      []string  `json:"features"`
	Means         []float64 `json:"means"`
	Scales        []float64 `json:"scales"`
	Coefficients  []float64 `json:"coefficients"`
	Intercept     float64   `json:"intercept"`
	TrainRows     int       `json:"train_rows"`
	TrainFrom     string    `json:"train_from"`
	TrainThrough  string    `json:"train_through"`
	Blend         struct {
		BaseWeight  float64 `json:"base_weight"`
		ModelWeight float64 `json:"model_weight"`
		Rank        string  `json:"rank"`
	} `json:"blend"`
}

var (
	defaultOnce  sync.Once
	defaultModel Model
	defaultErr   error
)

func DefaultModel() (Model, error) {
	defaultOnce.Do(func() {
		if err := verifyChecksum(modelArtifact, modelArtifactSHA256); err != nil {
			defaultErr = err
			return
		}
		defaultModel, defaultErr = parseModel(modelArtifact)
		if defaultErr == nil {
			defaultModel.SHA256 = modelArtifactSHA256
		}
	})
	return defaultModel, defaultErr
}

func verifyChecksum(data []byte, want string) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("first-stage model checksum mismatch: got %s", got)
	}
	return nil
}

func parseModel(data []byte) (Model, error) {
	if len(data) == 0 {
		return Model{}, errors.New("first-stage model is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var doc modelDocument
	if err := decoder.Decode(&doc); err != nil {
		return Model{}, errors.New("first-stage model is invalid JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Model{}, errors.New("first-stage model must contain one document")
	}
	if doc.SchemaVersion != ModelSchemaVersion || doc.Model != ModelID || doc.Target != ModelTarget {
		return Model{}, errors.New("unsupported first-stage model contract")
	}
	if len(doc.Features) != len(modelFeatures) {
		return Model{}, errors.New("first-stage model feature count is invalid")
	}
	for i, feature := range modelFeatures {
		if doc.Features[i] != feature {
			return Model{}, errors.New("first-stage model feature order is invalid")
		}
	}
	if len(doc.Means) != len(modelFeatures) || len(doc.Scales) != len(modelFeatures) || len(doc.Coefficients) != len(modelFeatures) {
		return Model{}, errors.New("first-stage model parameter length is invalid")
	}
	for i := range modelFeatures {
		if !finite(doc.Means[i]) || !finite(doc.Scales[i]) || doc.Scales[i] <= 0 || !finite(doc.Coefficients[i]) {
			return Model{}, errors.New("first-stage model parameters must be finite")
		}
	}
	if !finite(doc.Intercept) || doc.TrainRows < 1 || doc.TrainFrom == "" || doc.TrainThrough == "" {
		return Model{}, errors.New("first-stage model metadata is invalid")
	}
	if !finite(doc.Blend.BaseWeight) || !finite(doc.Blend.ModelWeight) || doc.Blend.BaseWeight <= 0 || doc.Blend.ModelWeight <= 0 || math.Abs(doc.Blend.BaseWeight+doc.Blend.ModelWeight-1) > 1e-9 || doc.Blend.Rank != "within_day" {
		return Model{}, errors.New("first-stage model blend is invalid")
	}
	return Model{
		SchemaVersion: doc.SchemaVersion,
		ID:            doc.Model,
		Target:        doc.Target,
		Features:      append([]string(nil), doc.Features...),
		Means:         append([]float64(nil), doc.Means...),
		Scales:        append([]float64(nil), doc.Scales...),
		Coefficients:  append([]float64(nil), doc.Coefficients...),
		Intercept:     doc.Intercept,
		BaseWeight:    doc.Blend.BaseWeight,
		ModelWeight:   doc.Blend.ModelWeight,
		TrainRows:     doc.TrainRows,
		TrainFrom:     doc.TrainFrom,
		TrainThrough:  doc.TrainThrough,
	}, nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
