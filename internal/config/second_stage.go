package config

import "errors"

// SecondStage configures the Top-N reranker over the first-stage candidate
// pool. It does not create an executable recommendation or bypass risk gates.
type SecondStage struct {
	Enabled     bool `yaml:"enabled" json:"enabled"`
	PushEnabled bool `yaml:"push_enabled" json:"push_enabled"`
	TopN        int  `yaml:"top_n" json:"top_n"`
}

func DefaultSecondStage() SecondStage {
	return SecondStage{Enabled: false, PushEnabled: false, TopN: 10}
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
	return nil
}
