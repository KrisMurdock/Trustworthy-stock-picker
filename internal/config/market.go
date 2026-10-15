package config

import (
	"errors"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

type Market struct {
	Providers         []string `yaml:"providers"`
	RequestInterval   string   `yaml:"request_interval"`
	TencentEndpoint   string   `yaml:"tencent_endpoint"`
	BaostockAddress   string   `yaml:"baostock_address"`
	TushareEndpoint   string   `yaml:"tushare_endpoint"`
	TushareTokenEnv   string   `yaml:"tushare_token_env"`
	TushareToken      string   `yaml:"-" json:"-"`
	EastmoneyEndpoint string   `yaml:"eastmoney_endpoint"`
}

func DefaultMarket() Market {
	return Market{Providers: []string{"tencent", "baostock", "tushare", "eastmoney"}, RequestInterval: "1s",
		TencentEndpoint: "https://proxy.finance.qq.com/ifzqgtimg/appstock/app/newfqkline/get",
		BaostockAddress: "public-api.baostock.com:10030", TushareEndpoint: "https://api.tushare.pro",
		TushareTokenEnv: "TUSHARE_TOKEN", EastmoneyEndpoint: "https://push2his.eastmoney.com/api/qt/stock/kline/get"}
}

func (m *Market) loadCredentials() { m.TushareToken = strings.TrimSpace(os.Getenv(m.TushareTokenEnv)) }

func (m Market) Validate() error {
	if len(m.Providers) == 0 || len(m.Providers) > 4 {
		return errors.New("market.providers must contain one to four providers")
	}
	interval, err := time.ParseDuration(m.RequestInterval)
	if err != nil || interval < 350*time.Millisecond || interval > time.Minute {
		return errors.New("market.request_interval must be between 350ms and 1m")
	}
	seen := map[string]bool{}
	active := 0
	for _, name := range m.Providers {
		if seen[name] {
			return errors.New("duplicate market provider")
		}
		seen[name] = true
		var endpoint string
		switch name {
		case "tencent":
			endpoint = m.TencentEndpoint
		case "baostock":
			host, port, err := net.SplitHostPort(m.BaostockAddress)
			if err != nil || host == "" || !regexp.MustCompile(`^[0-9]+$`).MatchString(port) {
				return errors.New("invalid market.baostock_address")
			}
			n, err := net.LookupPort("tcp", port)
			if err != nil || n < 1 {
				return errors.New("invalid market.baostock_address")
			}
		case "tushare":
			endpoint = m.TushareEndpoint
			if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(m.TushareTokenEnv) {
				return errors.New("invalid market.tushare_token_env")
			}
		case "eastmoney":
			endpoint = m.EastmoneyEndpoint
		default:
			return errors.New("unsupported market provider; use tencent, baostock, tushare or eastmoney")
		}
		if endpoint != "" {
			u, err := url.Parse(endpoint)
			if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()))) {
				return errors.New("market HTTP endpoints require HTTPS or loopback HTTP, without credentials or query parameters")
			}
		} else if name != "baostock" {
			return errors.New("market HTTP endpoint is required")
		}
		if name != "tushare" || m.TushareToken != "" {
			active++
		}
	}
	if active == 0 {
		return errors.New("no active market provider; configure the Tushare token environment variable or enable another provider")
	}
	return nil
}
