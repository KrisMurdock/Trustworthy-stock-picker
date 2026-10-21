package config

import "errors"

// SecondStage configures the Top-N reranker over the first-stage candidate
// pool. It does not create an executable recommendation or bypass risk gates.
type SecondStage struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	TopN    int  `yaml:"top_n" json:"top_n"`
}

func DefaultSecondStage() SecondStage {
	return SecondStage{Enabled: false, TopN: 10}
}

func (s SecondStage) Validate() error {
	if !s.Enabled {
		return nil
	}
	if s.TopN < 1 || s.TopN > 100 {
		return errors.New("second_stage.top_n must be between 1 and 100")
	}
	return nil
}
