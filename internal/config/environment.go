package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

func applyEnvironment(c *Config) error {
	for name, target := range map[string]*string{
		"STOCK_WATCHER_DATA_DIR":                  &c.DataDir,
		"STOCK_WATCHER_WEB_ADDR":                  &c.Web.Listen,
		"STOCK_WATCHER_POOL_FILE":                 &c.PoolFile,
		"STOCK_WATCHER_FEISHU_CREDENTIALS_FILE":   &c.Feishu.CredentialsFile,
		"STOCK_WATCHER_POLL_INTERVAL":             &c.PollInterval,
		"STOCK_WATCHER_REQUEST_TIMEOUT":           &c.Timeout,
		"STOCK_WATCHER_CALENDAR_VALID_FROM":       &c.Calendar.ValidFrom,
		"STOCK_WATCHER_CALENDAR_VALID_THROUGH":    &c.Calendar.ValidThrough,
		"STOCK_WATCHER_MARKET_REQUEST_INTERVAL":   &c.Market.RequestInterval,
		"STOCK_WATCHER_MARKET_ATTEMPT_TIMEOUT":    &c.Market.AttemptTimeout,
		"STOCK_WATCHER_MARKET_FAILURE_COOLDOWN":   &c.Market.FailureCooldown,
		"STOCK_WATCHER_MARKET_TENCENT_ENDPOINT":   &c.Market.TencentEndpoint,
		"STOCK_WATCHER_MARKET_BAOSTOCK_ADDRESS":   &c.Market.BaostockAddress,
		"STOCK_WATCHER_MARKET_TUSHARE_ENDPOINT":   &c.Market.TushareEndpoint,
		"STOCK_WATCHER_MARKET_EASTMONEY_ENDPOINT": &c.Market.EastmoneyEndpoint,
		"STOCK_WATCHER_MARKET_SINA_ENDPOINT":      &c.Market.SinaEndpoint,
		"STOCK_WATCHER_MARKET_LISTING_ENDPOINT":   &c.Market.ListingEndpoint,
		"STOCK_WATCHER_INTRADAY_POLL_INTERVAL":    &c.Intraday.PollInterval,
		"STOCK_WATCHER_INTRADAY_REQUEST_INTERVAL": &c.Intraday.RequestInterval,
		"STOCK_WATCHER_INTRADAY_CALIBRATION_AT":   &c.Intraday.CalibrationAt,
		"STOCK_WATCHER_INTRADAY_TENCENT_ENDPOINT": &c.Intraday.TencentEndpoint,
	} {
		if value, ok := os.LookupEnv(name); ok {
			*target = value
		}
	}
	for name, target := range map[string]*bool{
		"STOCK_WATCHER_REQUIRE_CIFS":     &c.RequireCIFS,
		"STOCK_WATCHER_WEB_ENABLED":      &c.Web.Enabled,
		"STOCK_WATCHER_FEISHU_ENABLED":   &c.Feishu.Enabled,
		"STOCK_WATCHER_INTRADAY_ENABLED": &c.Intraday.Enabled,
		"STOCK_WATCHER_STRATEGY_ENABLED": &c.Strategy.Enabled,
	} {
		if value, ok := os.LookupEnv(name); ok {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return errors.New("invalid boolean environment variable: " + name)
			}
			*target = parsed
		}
	}
	if value, ok := os.LookupEnv("STOCK_WATCHER_CALENDAR_HOLIDAYS"); ok {
		c.Calendar.Holidays = splitList(value)
	}
	if value, ok := os.LookupEnv("STOCK_WATCHER_MARKET_PROVIDERS"); ok {
		c.Market.Providers = splitList(value)
	}
	if value, ok := os.LookupEnv("STOCK_WATCHER_DAILY_HISTORY_DAYS"); ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return errors.New("invalid integer environment variable: STOCK_WATCHER_DAILY_HISTORY_DAYS")
		}
		c.Daily.HistoryDays = parsed
	}
	if value, ok := os.LookupEnv("STOCK_WATCHER_MARKET_FAILURE_THRESHOLD"); ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return errors.New("invalid integer environment variable: STOCK_WATCHER_MARKET_FAILURE_THRESHOLD")
		}
		c.Market.FailureThreshold = parsed
	}
	if value, ok := os.LookupEnv("STOCK_WATCHER_INTRADAY_WORKERS"); ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return errors.New("invalid integer environment variable: STOCK_WATCHER_INTRADAY_WORKERS")
		}
		c.Intraday.Workers = parsed
	}
	return nil
}

func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
