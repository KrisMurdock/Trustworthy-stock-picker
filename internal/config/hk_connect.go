package config

import "errors"

// HKConnect controls the independent Stock Connect daily rule report. It is
// deliberately separate from the A-share first-stage and second-stage models.
type HKConnect struct {
	Enabled     bool `yaml:"enabled" json:"enabled"`
	PushEnabled bool `yaml:"push_enabled" json:"push_enabled"`
	SyncEnabled bool `yaml:"sync_enabled" json:"sync_enabled"`
	TopN        int  `yaml:"top_n" json:"top_n"`
}

func DefaultHKConnect() HKConnect {
	return HKConnect{Enabled: false, PushEnabled: false, TopN: 10}
}

func (h HKConnect) Validate() error {
	if h.PushEnabled && !h.Enabled {
		return errors.New("hk_connect.push_enabled requires hk_connect.enabled")
	}
	if h.TopN < 1 || h.TopN > 20 {
		return errors.New("hk_connect.top_n must be between 1 and 20")
	}
	return nil
}
