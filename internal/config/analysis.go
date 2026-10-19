package config

import (
	"errors"
	"time"
)

type Analysis struct {
	Engine      string `yaml:"engine"`
	Timeout     string `yaml:"timeout"`
	SyncTimeout string `yaml:"sync_timeout"`
}

func (a Analysis) Validate() error {
	switch a.Engine {
	case "auto", "leader_daily_v1", "short_horizon_v1":
	default:
		return errors.New("unsupported analysis.engine")
	}
	for _, value := range []string{a.Timeout, a.SyncTimeout} {
		d, e := time.ParseDuration(value)
		if e != nil || d < time.Second || d > 24*time.Hour {
			return errors.New("analysis timeouts must be between 1s and 24h")
		}
	}
	return nil
}
