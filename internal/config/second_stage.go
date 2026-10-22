package config

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SecondStage configures the Top-N reranker over the first-stage candidate
// pool. It does not create an executable recommendation or bypass risk gates.
type SecondStage struct {
	Enabled          bool          `yaml:"enabled" json:"enabled"`
	PushEnabled      bool          `yaml:"push_enabled" json:"push_enabled"`
	TopN             int           `yaml:"top_n" json:"top_n"`
	BlendID          string        `yaml:"blend_id" json:"blend_id"`
	FirstStageWeight float64       `yaml:"first_stage_weight" json:"first_stage_weight"`
	Models           []ModelWeight `yaml:"models" json:"models"`
	GroupSize        int           `yaml:"group_size" json:"group_size"`
	Calibration      string        `yaml:"calibration" json:"calibration"`
	CalibrationParam float64       `yaml:"calibration_param" json:"calibration_param"`
	RiskEta          float64       `yaml:"risk_eta" json:"risk_eta"`
}

type ModelWeight struct {
	Name   string  `yaml:"name" json:"name"`
	Weight float64 `yaml:"weight" json:"weight"`
}

func DefaultSecondStage() SecondStage {
	return SecondStage{
		Enabled:          false,
		PushEnabled:      false,
		TopN:             10,
		BlendID:          "blend_50_50",
		FirstStageWeight: 0.5,
		Models:           []ModelWeight{{Name: "jev", Weight: 0.5}},
		GroupSize:        15,
		Calibration:      "none",
	}
}

func (s SecondStage) Validate() error {
	if s.PushEnabled && !s.Enabled {
		return errors.New("second_stage.push_enabled requires second_stage.enabled")
	}
	if !s.Enabled {
		return nil
	}
	if s.TopN < 1 || s.TopN > 100 {
		return errors.New("second_stage.top_n must be between 1 and 100")
	}
	if !validModelName(s.BlendID) {
		return errors.New("second_stage.blend_id is invalid")
	}
	if !finiteWeight(s.FirstStageWeight) || s.FirstStageWeight < 0 || s.FirstStageWeight > 1 {
		return errors.New("second_stage.first_stage_weight must be between 0 and 1")
	}
	if s.GroupSize < 1 || s.GroupSize > 100 {
		return errors.New("second_stage.group_size must be between 1 and 100")
	}
	if s.Calibration != "none" && s.Calibration != "local" {
		return errors.New("second_stage.calibration must be none or local")
	}
	if !finiteWeight(s.CalibrationParam) || s.CalibrationParam < 0 || s.CalibrationParam > 1 {
		return errors.New("second_stage.calibration_param must be between 0 and 1")
	}
	if !finiteWeight(s.RiskEta) || s.RiskEta < 0 || s.RiskEta > 1 {
		return errors.New("second_stage.risk_eta must be between 0 and 1")
	}
	if len(s.Models) == 0 {
		return errors.New("second_stage.models must not be empty")
	}
	seen := map[string]bool{}
	total := s.FirstStageWeight
	for _, model := range s.Models {
		if !validModelName(model.Name) {
			return fmt.Errorf("second_stage model name %q is invalid", model.Name)
		}
		if seen[model.Name] {
			return fmt.Errorf("second_stage model %q is duplicated", model.Name)
		}
		seen[model.Name] = true
		if !finiteWeight(model.Weight) || model.Weight <= 0 || model.Weight > 1 {
			return fmt.Errorf("second_stage model %q weight must be greater than 0 and at most 1", model.Name)
		}
		total += model.Weight
	}
	if math.Abs(total-1) > 1e-9 {
		return fmt.Errorf("second_stage blend weights must sum to 1, got %.6f", total)
	}
	return nil
}

func finiteWeight(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validModelName(name string) bool {
	if name == "" || len(name) > 64 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return !strings.Contains(name, "..")
}

func parseModelWeights(raw string) ([]ModelWeight, error) {
	parts := strings.Split(strings.TrimSpace(raw), ",")
	models := make([]ModelWeight, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("second_stage.models contains an empty entry")
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("second_stage model %q must use name=weight", part)
		}
		weight, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return nil, fmt.Errorf("second_stage model %q has invalid weight", name)
		}
		models = append(models, ModelWeight{Name: strings.TrimSpace(name), Weight: weight})
	}
	return models, nil
}
