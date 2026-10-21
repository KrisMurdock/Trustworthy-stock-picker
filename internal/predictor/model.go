package predictor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

type Hyperparameters struct {
	Trees        int     `json:"trees"`
	MaxDepth     int     `json:"max_depth"`
	MinLeaf      int     `json:"min_leaf"`
	LearningRate float64 `json:"learning_rate"`
	L2           float64 `json:"l2"`
	Seed         int64   `json:"seed"`
	Bins         int     `json:"bins"`
}

// PredictionCalibration shrinks the raw boosted-tree point estimates after
// clipping. It is recorded in the artifact so inference and evaluation use the
// same scales; nil calibration means 1.0 for backward compatibility.
type PredictionCalibration struct {
	High  float64 `json:"high"`
	Low   float64 `json:"low"`
	Close float64 `json:"close"`
}

func DefaultPredictionCalibration() PredictionCalibration {
	return PredictionCalibration{High: 1, Low: 1, Close: 1}
}

func (c PredictionCalibration) normalized() PredictionCalibration {
	defaults := DefaultPredictionCalibration()
	if c.High <= 0 || math.IsNaN(c.High) || math.IsInf(c.High, 0) {
		c.High = defaults.High
	}
	if c.Low <= 0 || math.IsNaN(c.Low) || math.IsInf(c.Low, 0) {
		c.Low = defaults.Low
	}
	if c.Close <= 0 || math.IsNaN(c.Close) || math.IsInf(c.Close, 0) {
		c.Close = defaults.Close
	}
	return c
}

func (c PredictionCalibration) valid() bool {
	values := []float64{c.High, c.Low, c.Close}
	for _, value := range values {
		if value <= 0 || value > 2 || math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

type Node struct {
	Feature     int     `json:"feature"`
	Threshold   float64 `json:"threshold"`
	Left        int     `json:"left"`
	Right       int     `json:"right"`
	Leaf        bool    `json:"leaf"`
	Value       float64 `json:"value"`
	MissingLeft bool    `json:"missing_left"`
}

type Tree struct {
	Nodes []Node `json:"nodes"`
}

type Artifact struct {
	SchemaVersion   int                    `json:"schema_version"`
	Info            ModelInfo              `json:"info"`
	Hyperparameters Hyperparameters        `json:"hyperparameters"`
	Base            map[Target]float64     `json:"base"`
	Trees           map[Target][]Tree      `json:"trees"`
	LabelClip       map[Target]float64     `json:"label_clip"`
	Calibration     *PredictionCalibration `json:"calibration,omitempty"`
}

type Model struct {
	Artifact Artifact
}

func NewModelID(created time.Time, checksum string) string {
	short := checksum
	if len(short) > 10 {
		short = short[:10]
	}
	return created.UTC().Format("20060102-150405") + "-" + short
}

func MarshalArtifact(a Artifact) ([]byte, error) {
	a.Info.Checksum = ""
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return nil, err
	}
	checksum := sha256.Sum256(data)
	a.Info.Checksum = hex.EncodeToString(checksum[:])
	return json.MarshalIndent(a, "", "  ")
}

// SealArtifact assigns a stable model ID and checksum. The checksum covers the
// compact canonical JSON with the checksum field empty.
func SealArtifact(a *Artifact) ([]byte, error) {
	if a == nil {
		return nil, errors.New("predictor artifact is nil")
	}
	a.Info.Checksum = ""
	a.Info.ModelID = ""
	first, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	firstSum := sha256.Sum256(first)
	a.Info.ModelID = NewModelID(a.Info.CreatedAt, hex.EncodeToString(firstSum[:]))
	second, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	secondSum := sha256.Sum256(second)
	a.Info.Checksum = hex.EncodeToString(secondSum[:])
	return json.MarshalIndent(a, "", "  ")
}

// ResealArtifact refreshes the checksum after metadata changes while keeping
// the already assigned model ID stable.
func ResealArtifact(a *Artifact) ([]byte, error) {
	if a == nil {
		return nil, errors.New("predictor artifact is nil")
	}
	if a.Info.ModelID == "" {
		return nil, errors.New("predictor artifact has no model ID")
	}
	a.Info.Checksum = ""
	canonical, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	a.Info.Checksum = hex.EncodeToString(sum[:])
	return json.MarshalIndent(a, "", "  ")
}

func LoadModel(data []byte) (*Model, error) {
	var a Artifact
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&a); err != nil {
		return nil, errors.New("invalid predictor model artifact")
	}
	if a.SchemaVersion != ArtifactSchemaVersion || a.Info.SchemaVersion != ArtifactSchemaVersion {
		return nil, errors.New("unsupported predictor model schema")
	}
	if a.Info.FeatureVersion != FeatureVersion || len(a.Info.FeatureNames) == 0 || a.Info.ModelID == "" {
		return nil, errors.New("invalid predictor model metadata")
	}
	if len(a.Base) != len(Targets) || len(a.Trees) != len(Targets) || len(a.LabelClip) != len(Targets) {
		return nil, errors.New("predictor model does not cover every target")
	}
	if a.Calibration != nil && !a.Calibration.valid() {
		return nil, errors.New("invalid predictor calibration")
	}
	for _, target := range Targets {
		if _, ok := a.Base[target]; !ok {
			return nil, errors.New("predictor model is missing a target base")
		}
		if _, ok := a.Trees[target]; !ok {
			return nil, errors.New("predictor model is missing a target ensemble")
		}
		for _, tree := range a.Trees[target] {
			if err := validateTree(tree); err != nil {
				return nil, err
			}
		}
	}
	expected := a.Info.Checksum
	a.Info.Checksum = ""
	canonical, err := json.Marshal(a)
	if err != nil {
		return nil, errors.New("cannot canonicalize predictor model")
	}
	checksum := sha256.Sum256(canonical)
	if expected == "" || expected != hex.EncodeToString(checksum[:]) {
		return nil, errors.New("predictor model checksum mismatch")
	}
	a.Info.Checksum = expected
	return &Model{Artifact: a}, nil
}

func validateTree(tree Tree) error {
	if len(tree.Nodes) == 0 || len(tree.Nodes) > 4096 {
		return errors.New("invalid predictor tree size")
	}
	for i, node := range tree.Nodes {
		if node.Leaf {
			if math.IsNaN(node.Value) || math.IsInf(node.Value, 0) {
				return errors.New("invalid predictor leaf value")
			}
			continue
		}
		if node.Feature < 0 || node.Left < 0 || node.Right < 0 || node.Left >= len(tree.Nodes) || node.Right >= len(tree.Nodes) ||
			node.Left == i || node.Right == i || math.IsNaN(node.Threshold) || math.IsInf(node.Threshold, 0) {
			return errors.New("invalid predictor tree node")
		}
	}
	return nil
}

func (m *Model) Predict(values []float64) (map[Target]float64, error) {
	if m == nil {
		return nil, errors.New("predictor model is not loaded")
	}
	if len(values) != len(m.Artifact.Info.FeatureNames) {
		return nil, fmt.Errorf("predictor feature count mismatch: got %d want %d", len(values), len(m.Artifact.Info.FeatureNames))
	}
	out := make(map[Target]float64, len(Targets))
	calibration := DefaultPredictionCalibration()
	if m.Artifact.Calibration != nil {
		calibration = m.Artifact.Calibration.normalized()
	}
	for _, target := range Targets {
		sum := m.Artifact.Base[target]
		for _, tree := range m.Artifact.Trees[target] {
			sum += m.Artifact.Hyperparameters.LearningRate * predictTree(tree, values)
		}
		if clip := m.Artifact.LabelClip[target]; clip > 0 {
			sum = math.Max(-clip, math.Min(clip, sum))
		}
		if math.IsNaN(sum) || math.IsInf(sum, 0) {
			return nil, errors.New("predictor produced a non-finite value")
		}
		switch target {
		case TargetHigh:
			sum *= calibration.High
		case TargetLow:
			sum *= calibration.Low
		case TargetClose:
			sum *= calibration.Close
		}
		out[target] = sum
	}
	return out, nil
}

func predictTree(tree Tree, values []float64) float64 {
	index := 0
	for {
		node := tree.Nodes[index]
		if node.Leaf {
			return node.Value
		}
		value := values[node.Feature]
		if math.IsNaN(value) {
			if node.MissingLeft {
				index = node.Left
			} else {
				index = node.Right
			}
			continue
		}
		if value <= node.Threshold {
			index = node.Left
		} else {
			index = node.Right
		}
	}
}

// FeatureImportance returns a deterministic node-count heuristic for audit.
func (m *Model) FeatureImportance() map[string]int {
	out := map[string]int{}
	if m == nil {
		return out
	}
	for _, target := range Targets {
		for _, tree := range m.Artifact.Trees[target] {
			for _, node := range tree.Nodes {
				if !node.Leaf && node.Feature >= 0 && node.Feature < len(m.Artifact.Info.FeatureNames) {
					out[m.Artifact.Info.FeatureNames[node.Feature]]++
				}
			}
		}
	}
	return out
}

func sortedTargets() []Target {
	out := append([]Target(nil), Targets...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
