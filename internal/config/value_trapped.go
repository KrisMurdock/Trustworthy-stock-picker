package config

import (
	"errors"
	"math"
)

// ValueTrapped controls the independent full-market intersection screen. It
// requires the low-valuation upstream scan and only evaluates trapped supply
// for stocks already below the financial research lower bound.
type ValueTrapped struct {
	Enabled         bool    `yaml:"enabled" json:"enabled"`
	PushEnabled     bool    `yaml:"push_enabled" json:"push_enabled"`
	TopN            int     `yaml:"top_n" json:"top_n"`
	MaxTrappedRatio float64 `yaml:"max_trapped_ratio" json:"max_trapped_ratio"`
}

func DefaultValueTrapped() ValueTrapped {
	return ValueTrapped{Enabled: false, PushEnabled: false, TopN: 20, MaxTrappedRatio: 0.30}
}

func (v ValueTrapped) Validate(lowValuationEnabled bool) error {
	if v.Enabled && !lowValuationEnabled {
		return errors.New("value_trapped.enabled requires low_valuation.enabled")
	}
	if v.PushEnabled && !v.Enabled {
		return errors.New("value_trapped.push_enabled requires value_trapped.enabled")
	}
	if v.TopN < 1 || v.TopN > 50 {
		return errors.New("value_trapped.top_n must be between 1 and 50")
	}
	if math.IsNaN(v.MaxTrappedRatio) || math.IsInf(v.MaxTrappedRatio, 0) || v.MaxTrappedRatio < 0.05 || v.MaxTrappedRatio > 1 {
		return errors.New("value_trapped.max_trapped_ratio must be between 0.05 and 1")
	}
	return nil
}
