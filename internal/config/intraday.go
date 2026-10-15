package config

import (
	"errors"
	"net"
	"net/url"
	"time"
)

// Intraday is independent of daily publication and its coverage gate.
type Intraday struct {
	Enabled         bool   `yaml:"enabled"`
	PollInterval    string `yaml:"poll_interval"`
	RequestInterval string `yaml:"request_interval"`
	Workers         int    `yaml:"workers"`
	CalibrationAt   string `yaml:"calibration_at"`
	TencentEndpoint string `yaml:"tencent_endpoint"`
}

func DefaultIntraday() Intraday {
	return Intraday{Enabled: true, PollInterval: "1m", RequestInterval: "350ms", Workers: 3,
		CalibrationAt: "15:10", TencentEndpoint: "https://proxy.finance.qq.com/ifzqgtimg/appstock/app/kline/mkline"}
}

func (c Intraday) Validate() error {
	if !c.Enabled {
		return nil
	}
	for _, item := range []struct {
		value    string
		min, max time.Duration
	}{{c.PollInterval, 30 * time.Second, 5 * time.Minute}, {c.RequestInterval, 350 * time.Millisecond, 10 * time.Second}} {
		d, err := time.ParseDuration(item.value)
		if err != nil || d < item.min || d > item.max {
			return errors.New("invalid intraday poll_interval (30s..5m) or request_interval (350ms..10s)")
		}
	}
	if c.Workers < 1 || c.Workers > 8 {
		return errors.New("intraday.workers must be between 1 and 8")
	}
	at, err := time.Parse("15:04", c.CalibrationAt)
	if err != nil || at.Format("15:04") != c.CalibrationAt || c.CalibrationAt < "15:05" || c.CalibrationAt > "23:00" {
		return errors.New("intraday.calibration_at must be between 15:05 and 23:00")
	}
	u, err := url.Parse(c.TencentEndpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()))) {
		return errors.New("intraday.tencent_endpoint requires HTTPS or loopback HTTP without credentials or query parameters")
	}
	return nil
}
