package config

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// MarketContext configures the independent index and sector-board snapshot
// feed. It is deliberately separate from the stock pool: board codes are not
// tradable A-share stock codes and must never reach stock selection.
type MarketContext struct {
	Enabled                bool   `yaml:"enabled"`
	RequestInterval        string `yaml:"request_interval"`
	Workers                int    `yaml:"workers"`
	HistoryLimit           int    `yaml:"history_limit"`
	EastmoneyListEndpoint  string `yaml:"eastmoney_list_endpoint"`
	EastmoneyKlineEndpoint string `yaml:"eastmoney_kline_endpoint"`
	TencentEndpoint        string `yaml:"tencent_endpoint"`
}

func DefaultMarketContext() MarketContext {
	return MarketContext{
		Enabled:                true,
		RequestInterval:        "350ms",
		Workers:                3,
		HistoryLimit:           10000,
		EastmoneyListEndpoint:  "https://push2.eastmoney.com/api/qt/clist/get",
		EastmoneyKlineEndpoint: "http://push2his.eastmoney.com/api/qt/stock/kline/get",
		TencentEndpoint:        "https://proxy.finance.qq.com/ifzqgtimg/appstock/app/newfqkline/get",
	}
}

func (m MarketContext) Validate() error {
	if !m.Enabled {
		return nil
	}
	if !validMarketEndpoint(m.EastmoneyListEndpoint) {
		return errors.New("market_context.eastmoney_list_endpoint requires HTTPS or loopback HTTP, without credentials or query parameters")
	}
	if !validContextKlineEndpoint(m.EastmoneyKlineEndpoint) {
		return errors.New("market_context.eastmoney_kline_endpoint requires HTTPS, loopback HTTP, or the approved public push2his HTTP endpoint")
	}
	if !validMarketEndpoint(m.TencentEndpoint) {
		return errors.New("market_context.tencent_endpoint requires HTTPS or loopback HTTP, without credentials or query parameters")
	}
	interval, err := time.ParseDuration(m.RequestInterval)
	if err != nil || interval < 350*time.Millisecond || interval > time.Minute {
		return errors.New("market_context.request_interval must be between 350ms and 1m")
	}
	if m.Workers < 1 || m.Workers > 8 {
		return errors.New("market_context.workers must be between 1 and 8")
	}
	if m.HistoryLimit < 640 || m.HistoryLimit > 10000 {
		return errors.New("market_context.history_limit must be between 640 and 10000")
	}
	return nil
}

// The Eastmoney history host currently resets its TLS handshake from some
// networks while the exact public read-only HTTP path remains available. Keep
// this exception narrow: no credentials, query parameters, redirects or other
// hosts are accepted. All other feeds still require HTTPS or loopback HTTP.
func validContextKlineEndpoint(endpoint string) bool {
	if validMarketEndpoint(endpoint) {
		return true
	}
	u, err := url.Parse(endpoint)
	return err == nil && u.Scheme == "http" && strings.EqualFold(u.Hostname(), "push2his.eastmoney.com") &&
		u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path == "/api/qt/stock/kline/get"
}
