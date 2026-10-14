package config

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Stock struct {
	Code   string `yaml:"code" json:"code"`
	Name   string `yaml:"name,omitempty" json:"name,omitempty"`
	Sector string `yaml:"sector,omitempty" json:"sector,omitempty"`
}

type Config struct {
	Version      int     `yaml:"schema_version"`
	DataDir      string  `yaml:"data_dir"`
	RequireCIFS  bool    `yaml:"require_cifs"`
	PollInterval string  `yaml:"poll_interval"`
	Timeout      string  `yaml:"request_timeout"`
	PoolFile     string  `yaml:"pool_file,omitempty"`
	Stocks       []Stock `yaml:"stocks"`
	Daily        struct {
		RunAt       string `yaml:"run_at"`
		HistoryDays int    `yaml:"history_days"`
		Adjust      string `yaml:"adjust"`
	} `yaml:"daily"`
	Selection struct {
		Enabled        bool    `yaml:"enabled"`
		MinScore       int     `yaml:"min_score"`
		TopN           int     `yaml:"top_n"`
		MinVolumeRatio float64 `yaml:"min_volume_ratio"`
		RSIMin         float64 `yaml:"rsi_min"`
		RSIMax         float64 `yaml:"rsi_max"`
		MaxDayChange   float64 `yaml:"max_day_change_pct"`
		MinDayChange   float64 `yaml:"min_day_change_pct"`
	} `yaml:"selection"`
	Feishu struct {
		Enabled bool   `yaml:"enabled"`
		Webhook string `yaml:"webhook,omitempty"`
		Secret  string `yaml:"secret,omitempty"`
	} `yaml:"feishu"`
	Calendar struct {
		ValidFrom    string   `yaml:"valid_from"`
		ValidThrough string   `yaml:"valid_through"`
		Holidays     []string `yaml:"holidays"`
	} `yaml:"calendar"`
}

var stockCode = regexp.MustCompile(`^(sh(60|68)[0-9]{4}|sz(00|30)[0-9]{4}|bj(4|8)[0-9]{5}|bj92[0-9]{4})$`)

func ValidCode(s string) bool { return stockCode.MatchString(s) }

func Defaults() Config {
	c := Config{Version: 1, DataDir: "data", RequireCIFS: true, PollInterval: "5m", Timeout: "10s"}
	c.Daily.RunAt = "18:05"
	c.Daily.HistoryDays = 365
	c.Daily.Adjust = "qfq"
	c.Selection.MinScore = 70
	c.Selection.TopN = 5
	c.Selection.MinVolumeRatio = 1.2
	c.Selection.RSIMin = 50
	c.Selection.RSIMax = 80
	c.Selection.MinDayChange = -3
	c.Selection.MaxDayChange = 7
	return c
}

// Load resolves paths against the config file. Environment values override secrets.
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, errors.New("cannot open configuration file")
	}
	defer f.Close()
	c := Defaults()
	c.Version = 0 // Files must explicitly declare the schema; defaults are for constructors.
	d := yaml.NewDecoder(io.LimitReader(f, 1<<20))
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return Config{}, errors.New("invalid configuration: unknown field or malformed YAML")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return Config{}, errors.New("configuration must contain one YAML document")
	}
	if c.PoolFile != "" {
		if len(c.Stocks) > 0 {
			return Config{}, errors.New("set pool_file or stocks, not both")
		}
		poolPath := c.PoolFile
		if !filepath.IsAbs(poolPath) {
			poolPath = filepath.Join(filepath.Dir(path), poolPath)
		}
		f, err := os.Open(poolPath)
		if err != nil {
			return Config{}, errors.New("cannot open stock pool file")
		}
		defer f.Close()
		var pool struct {
			Version int     `yaml:"schema_version"`
			Stocks  []Stock `yaml:"stocks"`
		}
		decoder := yaml.NewDecoder(io.LimitReader(f, 1<<20))
		decoder.KnownFields(true)
		if decoder.Decode(&pool) != nil || pool.Version != 1 {
			return Config{}, errors.New("invalid stock pool schema")
		}
		if decoder.Decode(&extra) != io.EOF {
			return Config{}, errors.New("stock pool must contain one document")
		}
		c.Stocks = pool.Stocks
	}
	if v, ok := os.LookupEnv("STOCK_WATCHER_DATA_DIR"); ok {
		c.DataDir = v
	}
	if v, ok := os.LookupEnv("FEISHU_WEBHOOK"); ok {
		c.Feishu.Webhook = v
	}
	if v, ok := os.LookupEnv("FEISHU_SECRET"); ok {
		c.Feishu.Secret = v
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return Config{}, errors.New("data_dir is required")
	}
	if !filepath.IsAbs(c.DataDir) {
		c.DataDir = filepath.Join(filepath.Dir(path), c.DataDir)
	}
	c.DataDir, err = filepath.Abs(c.DataDir)
	if err != nil {
		return Config{}, errors.New("invalid data directory")
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return errors.New("schema_version must be 1")
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return errors.New("data_dir is required")
	}
	for _, field := range []struct {
		name, value string
		min, max    time.Duration
	}{
		{"poll_interval", c.PollInterval, time.Second, time.Hour},
		{"request_timeout", c.Timeout, time.Second, time.Minute},
	} {
		d, err := time.ParseDuration(field.value)
		if err != nil || d < field.min || d > field.max {
			return fmt.Errorf("invalid %s duration", field.name)
		}
	}
	if len(c.Stocks) == 0 || len(c.Stocks) > 500 {
		return errors.New("stocks must contain 1 to 500 A-share stocks")
	}
	stocks := map[string]bool{}
	for _, s := range c.Stocks {
		if !ValidCode(s.Code) || stocks[s.Code] {
			return errors.New("stocks contain an invalid or duplicate A-share code")
		}
		if len(s.Name) > 128 || len(s.Sector) > 128 {
			return errors.New("stock labels are too long")
		}
		stocks[s.Code] = true
	}
	clock, err := time.Parse("15:04", c.Daily.RunAt)
	if err != nil || clock.Hour() < 16 {
		return errors.New("daily.run_at must be HH:MM at or after 16:00")
	}
	if c.Daily.HistoryDays < 90 || c.Daily.HistoryDays > 730 {
		return errors.New("daily.history_days must be 90 to 730 calendar days")
	}
	switch c.Daily.Adjust {
	case "none", "qfq", "hfq":
	default:
		return errors.New("daily.adjust must be none, qfq or hfq")
	}
	if c.Selection.MinScore < 1 || c.Selection.MinScore > 100 || c.Selection.TopN < 1 || c.Selection.TopN > 20 {
		return errors.New("invalid selection score or top_n")
	}
	for _, v := range []float64{c.Selection.MinVolumeRatio, c.Selection.RSIMin, c.Selection.RSIMax, c.Selection.MinDayChange, c.Selection.MaxDayChange} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("selection values must be finite")
		}
	}
	if c.Selection.MinVolumeRatio < 1 || c.Selection.MinVolumeRatio > 10 || c.Selection.RSIMin < 0 || c.Selection.RSIMax > 100 || c.Selection.RSIMin > c.Selection.RSIMax || c.Selection.MinDayChange > c.Selection.MaxDayChange {
		return errors.New("invalid selection volume, RSI or daily change range")
	}
	if c.Feishu.Enabled {
		if !c.Selection.Enabled {
			return errors.New("Feishu recommendations require enabled selection")
		}
		u, err := url.Parse(c.Feishu.Webhook)
		if err != nil || u.Scheme != "https" || u.Host != "open.feishu.cn" || !strings.HasPrefix(u.Path, "/open-apis/bot/v2/hook/") || len(strings.TrimPrefix(u.Path, "/open-apis/bot/v2/hook/")) < 8 || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("enabled Feishu requires a valid HTTPS custom bot webhook")
		}
	}
	start, e1 := time.Parse("2006-01-02", c.Calendar.ValidFrom)
	end, e2 := time.Parse("2006-01-02", c.Calendar.ValidThrough)
	if e1 != nil || e2 != nil || end.Before(start) {
		return errors.New("calendar requires valid_from and valid_through dates")
	}
	dates := map[string]bool{}
	for _, day := range c.Calendar.Holidays {
		d, err := time.Parse("2006-01-02", day)
		if err != nil || d.Before(start) || d.After(end) || dates[day] {
			return errors.New("calendar contains invalid or duplicate holidays")
		}
		dates[day] = true
	}
	return nil
}
func Duration(s string) time.Duration { d, _ := time.ParseDuration(s); return d }
