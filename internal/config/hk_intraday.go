package config

import (
	"errors"
	"time"
)

type HKIntraday struct {
	Enabled         bool     `yaml:"enabled"`
	PollInterval    string   `yaml:"poll_interval"`
	RequestInterval string   `yaml:"request_interval"`
	CycleTimeout    string   `yaml:"cycle_timeout"`
	Workers         int      `yaml:"workers"`
	ValidFrom       string   `yaml:"valid_from"`
	ValidThrough    string   `yaml:"valid_through"`
	EarlyCloses     []string `yaml:"early_closes"`
}

func DefaultHKIntraday() HKIntraday {
	return HKIntraday{Enabled: false, PollInterval: "5m", RequestInterval: "350ms", CycleTimeout: "15m", Workers: 3,
		ValidFrom: "2026-01-01", ValidThrough: "2026-12-31", EarlyCloses: []string{"2026-02-16", "2026-12-24", "2026-12-31"}}
}

func (c HKIntraday) Validate() error {
	if !c.Enabled {
		return nil
	}
	for _, item := range []struct {
		value    string
		min, max time.Duration
	}{
		{c.PollInterval, 5 * time.Minute, 30 * time.Minute},
		{c.RequestInterval, 350 * time.Millisecond, 10 * time.Second},
		{c.CycleTimeout, 5 * time.Minute, time.Hour},
	} {
		duration, err := time.ParseDuration(item.value)
		if err != nil || duration < item.min || duration > item.max {
			return errors.New("invalid hk_intraday poll interval, request interval or cycle timeout")
		}
	}
	if c.Workers < 1 || c.Workers > 8 {
		return errors.New("hk_intraday.workers must be between 1 and 8")
	}
	return c.ValidateCalendar()
}

func (c HKIntraday) ValidateCalendar() error {
	from, fromErr := time.Parse("2006-01-02", c.ValidFrom)
	through, throughErr := time.Parse("2006-01-02", c.ValidThrough)
	if fromErr != nil || throughErr != nil || from.Format("2006-01-02") != c.ValidFrom || through.Format("2006-01-02") != c.ValidThrough || from.After(through) {
		return errors.New("hk_intraday calendar coverage is invalid")
	}
	seen := map[string]bool{}
	for _, day := range c.EarlyCloses {
		parsed, err := time.Parse("2006-01-02", day)
		if err != nil || parsed.Format("2006-01-02") != day || day < c.ValidFrom || day > c.ValidThrough || seen[day] {
			return errors.New("invalid or duplicate hk_intraday early close")
		}
		seen[day] = true
	}
	return nil
}

func (c HKIntraday) EarlyCloseMap() map[string]bool {
	out := make(map[string]bool, len(c.EarlyCloses))
	for _, day := range c.EarlyCloses {
		out[day] = true
	}
	return out
}
