package secondstage

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
	"strings"
	"sync"
)

const modelArtifactSHA256 = "21cb6c2a19706381cdb0ce2598ceda743b81227b22391cb5f92ba429d9d4e960"

//go:embed model.json
var modelArtifact []byte

type Model struct {
	SchemaVersion  int
	ModelID        string
	Target         string
	FeatureVersion int
	FeatureNames   []string
	Base           float64
	LearningRate   float64
	LabelClipLow   float64
	LabelClipHigh  float64
	Trees          []Tree
	TrainRows      int
	TrainFrom      string
	TrainThrough   string
	SHA256         string
}

type Tree struct {
	Nodes []Node `json:"nodes"`
}

type Node struct {
	Leaf        bool    `json:"leaf"`
	Value       float64 `json:"value,omitempty"`
	Feature     int     `json:"feature,omitempty"`
	Threshold   float64 `json:"threshold,omitempty"`
	Left        int     `json:"left,omitempty"`
	Right       int     `json:"right,omitempty"`
	MissingLeft bool    `json:"missing_left,omitempty"`
}

type modelDocument struct {
	SchemaVersion  int      `json:"schema_version"`
	ModelID        string   `json:"model_id"`
	Target         string   `json:"target"`
	FeatureVersion int      `json:"feature_version"`
	FeatureNames   []string `json:"feature_names"`
	Base           float64  `json:"base"`
	LearningRate   float64  `json:"learning_rate"`
	LabelClipLow   float64  `json:"label_clip_low"`
	LabelClipHigh  float64  `json:"label_clip_high"`
	Trees          []Tree   `json:"trees"`
	TrainRows      int      `json:"train_rows"`
	TrainFrom      string   `json:"train_from"`
	TrainThrough   string   `json:"train_through"`
}

var (
	defaultOnce  sync.Once
	defaultModel Model
	defaultErr   error
)

func DefaultModel() (Model, error) {
	defaultOnce.Do(func() {
		defaultModel, defaultErr = parseModel(modelArtifact, modelArtifactSHA256)
	})
	return defaultModel, defaultErr
}

func parseModel(data []byte, wantSHA string) (Model, error) {
	if len(data) == 0 {
		return Model{}, errors.New("second-stage model is empty")
	}
	if wantSHA != "" {
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != wantSHA {
			return Model{}, fmt.Errorf("second-stage model checksum mismatch: got %s", got)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var doc modelDocument
	if err := decoder.Decode(&doc); err != nil {
		return Model{}, errors.New("second-stage model is invalid JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Model{}, errors.New("second-stage model must contain one document")
	}
	if doc.SchemaVersion != ModelSchemaVersion || strings.TrimSpace(doc.ModelID) == "" || doc.Target != "future_three_day_max_high_return_pct" {
		return Model{}, errors.New("unsupported second-stage model contract")
	}
	expectedFeatures, ok := featureNamesForVersion(doc.FeatureVersion)
	if !ok || len(doc.FeatureNames) != len(expectedFeatures) {
		return Model{}, errors.New("invalid second-stage feature contract")
	}
	for index, name := range expectedFeatures {
		if doc.FeatureNames[index] != name {
			return Model{}, errors.New("second-stage feature order is invalid")
		}
	}
	if !finite(doc.Base) || !finite(doc.LearningRate) || doc.LearningRate <= 0 || len(doc.Trees) == 0 || doc.TrainRows < 1 || doc.TrainFrom == "" || doc.TrainThrough == "" {
		return Model{}, errors.New("invalid second-stage model metadata")
	}
	if !finite(doc.LabelClipLow) || !finite(doc.LabelClipHigh) || doc.LabelClipLow >= doc.LabelClipHigh {
		return Model{}, errors.New("invalid second-stage label clip")
	}
	for _, tree := range doc.Trees {
		if err := validateTree(tree, len(expectedFeatures)); err != nil {
			return Model{}, err
		}
	}
	return Model{
		SchemaVersion:  doc.SchemaVersion,
		ModelID:        doc.ModelID,
		Target:         doc.Target,
		FeatureVersion: doc.FeatureVersion,
		FeatureNames:   append([]string(nil), doc.FeatureNames...),
		Base:           doc.Base,
		LearningRate:   doc.LearningRate,
		LabelClipLow:   doc.LabelClipLow,
		LabelClipHigh:  doc.LabelClipHigh,
		Trees:          doc.Trees,
		TrainRows:      doc.TrainRows,
		TrainFrom:      doc.TrainFrom,
		TrainThrough:   doc.TrainThrough,
		SHA256:         wantSHA,
	}, nil
}

func validateTree(tree Tree, featureCount int) error {
	if len(tree.Nodes) == 0 || len(tree.Nodes) > 4096 {
		return errors.New("invalid second-stage tree size")
	}
	for index, node := range tree.Nodes {
		if node.Leaf {
			if !finite(node.Value) {
				return errors.New("invalid second-stage leaf value")
			}
			continue
		}
		if node.Feature < 0 || node.Feature >= featureCount || node.Left < 0 || node.Right < 0 || node.Left >= len(tree.Nodes) || node.Right >= len(tree.Nodes) || node.Left == index || node.Right == index || !finite(node.Threshold) {
			return errors.New("invalid second-stage tree node")
		}
	}
	state := make([]uint8, len(tree.Nodes))
	var walk func(int) error
	walk = func(index int) error {
		switch state[index] {
		case 1:
			return errors.New("second-stage tree contains a cycle")
		case 2:
			return nil
		}
		state[index] = 1
		node := tree.Nodes[index]
		if !node.Leaf {
			if err := walk(node.Left); err != nil {
				return err
			}
			if err := walk(node.Right); err != nil {
				return err
			}
		}
		state[index] = 2
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	for _, value := range state {
		if value != 2 {
			return errors.New("second-stage tree contains an unreachable node")
		}
	}
	return nil
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

func (m Model) Predict(values []float64) (float64, error) {
	if len(values) != len(m.FeatureNames) {
		return 0, errors.New("second-stage feature count mismatch")
	}
	value := m.Base
	for _, tree := range m.Trees {
		value += m.LearningRate * predictTree(tree, values)
	}
	if !finite(value) {
		return 0, errors.New("second-stage model produced a non-finite value")
	}
	return math.Max(m.LabelClipLow, math.Min(m.LabelClipHigh, value)), nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
