package config

import "errors"

// FirstStage configures the recall-first candidate screen. It is a separate
// calculation layer and never changes the formal strategy recommendation.
type FirstStage struct {
	Enabled     bool `yaml:"enabled" json:"enabled"`
	PushEnabled bool `yaml:"push_enabled" json:"push_enabled"`
	Budget      int  `yaml:"budget" json:"budget"`
}

func DefaultFirstStage() FirstStage {
	return FirstStage{Enabled: false, PushEnabled: false, Budget: 145}
}

func (f FirstStage) Validate() error {
	if f.PushEnabled && !f.Enabled {
		return errors.New("first_stage.push_enabled requires first_stage.enabled")
	}
	if !f.Enabled {
		return nil
	}
	if f.Budget < 1 || f.Budget > 500 {
		return errors.New("first_stage.budget must be between 1 and 500")
	}
	return nil
}
