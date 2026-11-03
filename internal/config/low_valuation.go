package config

import "errors"

// LowValuation controls the independent full-market financial valuation
// screen. It consumes full-market daily closes and Sina TTM fundamentals; it
// does not reuse the configured stock pool, first-stage candidates, trend
// ranking or trading simulation.
type LowValuation struct {
	Enabled     bool `yaml:"enabled" json:"enabled"`
	PushEnabled bool `yaml:"push_enabled" json:"push_enabled"`
	TopN        int  `yaml:"top_n" json:"top_n"`
}

func DefaultLowValuation() LowValuation {
	return LowValuation{Enabled: false, PushEnabled: false, TopN: 20}
}

func (l LowValuation) Validate() error {
	if l.PushEnabled && !l.Enabled {
		return errors.New("low_valuation.push_enabled requires low_valuation.enabled")
	}
	if l.TopN < 1 || l.TopN > 50 {
		return errors.New("low_valuation.top_n must be between 1 and 50")
	}
	return nil
}
