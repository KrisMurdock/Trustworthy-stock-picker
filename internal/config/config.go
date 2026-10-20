package config

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Stock struct {
	Code       string `yaml:"code" json:"code"`
	Name       string `yaml:"name,omitempty" json:"name,omitempty"`
	Sector     string `yaml:"sector,omitempty" json:"sector,omitempty"`
	ListedFrom string `yaml:"listed_from,omitempty" json:"listed_from,omitempty"`
}

type Config struct {
	Analysis       Analysis       `yaml:"analysis"`
	Intraday       Intraday       `yaml:"intraday"`
	Predictor      Predictor      `yaml:"predictor"`
	LLM            LLM            `yaml:"llm"`
	Market         Market         `yaml:"market"`
	MarketContext  MarketContext  `yaml:"market_context"`
	Strategy       Strategy       `yaml:"strategy"`
	StructureGuard StructureGuard `yaml:"structure_guard"`
	Version        int            `yaml:"schema_version"`
	DataDir        string         `yaml:"data_dir"`
	RequireCIFS    bool           `yaml:"require_cifs"`
	PollInterval   string         `yaml:"poll_interval"`
	Timeout        string         `yaml:"request_timeout"`
	PoolFile       string         `yaml:"pool_file,omitempty"`
	Stocks         []Stock        `yaml:"stocks"`
	Web            struct {
		Enabled bool   `yaml:"enabled"`
		Listen  string `yaml:"listen"`
	} `yaml:"web"`
	Daily struct {
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
	Sync struct {
		MinCoveragePct    float64 `yaml:"min_coverage_pct"`
		MinAnalyzablePct  float64 `yaml:"min_analyzable_pct"`
		IncrementalBars   int     `yaml:"incremental_bars"`
		StatusRefreshDays int     `yaml:"status_refresh_days"`
	} `yaml:"sync"`
	Notify struct {
		Channels []NotifyChannel `yaml:"channels"`
	} `yaml:"notify"`
	Feishu struct {
		Enabled         bool   `yaml:"enabled"`
		CredentialsFile string `yaml:"credentials_file,omitempty"`
		Webhook         string `yaml:"webhook,omitempty"`
		Secret          string `yaml:"secret,omitempty"`
	} `yaml:"feishu"`
	Calendar struct {
		ValidFrom    string   `yaml:"valid_from"`
		ValidThrough string   `yaml:"valid_through"`
		Holidays     []string `yaml:"holidays"`
		TradingDays  []string `yaml:"-"`
		Source       string   `yaml:"-"`
		Digest       string   `yaml:"-"`
	} `yaml:"calendar"`
	prepared bool
}

type StructureGuard struct {
	Mode string `yaml:"mode" json:"mode"`
}

type NotifyChannel struct {
	Name            string `yaml:"name"`
	Type            string `yaml:"type"`
	Enabled         bool   `yaml:"enabled"`
	CredentialsFile string `yaml:"credentials_file,omitempty"`
	WebhookEnv      string `yaml:"webhook_env,omitempty"`
	SecretEnv       string `yaml:"secret_env,omitempty"`
	Webhook         string `yaml:"-"`
	Secret          string `yaml:"-"`
}

var stockCode = regexp.MustCompile(`^(sh(60|68)[0-9]{4}|sz(00|30)[0-9]{4})$`)

func ValidCode(s string) bool { return stockCode.MatchString(s) }

func Defaults() Config {
	c := Config{Version: 1, DataDir: "data", RequireCIFS: false, PollInterval: "5m", Timeout: "10s"}
	c.Analysis = Analysis{Engine: "auto", Timeout: "10m", SyncTimeout: "90m"}
	c.LLM = DefaultLLM()
	c.Strategy = DefaultStrategy()
	c.StructureGuard.Mode = "shadow"
	c.Market = DefaultMarket()
	c.MarketContext = DefaultMarketContext()
	c.Intraday = DefaultIntraday()
	c.Predictor = DefaultPredictor()
	c.Web.Enabled = true
	c.Web.Listen = "127.0.0.1:8080"
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
	c.Sync.MinCoveragePct = 95
	c.Sync.MinAnalyzablePct = 90
	c.Sync.IncrementalBars = 5
	c.Sync.StatusRefreshDays = 30
	return c
}

// Load returns the built-in configuration when no path is supplied. A path is
// retained for internal fixtures and offline research tools only; the
// production CLI never requires or reads an application configuration file.
func Load(paths ...string) (Config, error) {
	switch len(paths) {
	case 0:
		return resolve(Builtin(), ".")
	case 1:
		return loadFile(paths[0])
	default:
		return Config{}, errors.New("at most one configuration path is supported")
	}
}

// Prepare finalizes an externally constructed configuration. Values returned by
// Load are already prepared; preparing them again only revalidates and refreshes
// environment-backed secrets.
func Prepare(c Config, baseDir string) (Config, error) {
	if c.prepared {
		return finalize(c, baseDir)
	}
	return resolve(c, baseDir)
}

func loadFile(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, errors.New("cannot open configuration file")
	}
	defer f.Close()
	c := Defaults()
	c.Version = 0          // Files must explicitly declare the schema; defaults are for constructors.
	c.Strategy.Version = 0 // Explicit when the optional strategy is enabled.
	d := yaml.NewDecoder(io.LimitReader(f, 1<<20))
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return Config{}, errors.New("invalid configuration: unknown field or malformed YAML")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return Config{}, errors.New("configuration must contain one YAML document")
	}
	return resolve(c, filepath.Dir(path))
}

func resolve(c Config, baseDir string) (Config, error) {
	if err := applyEnvironment(&c); err != nil {
		return Config{}, err
	}
	if err := loadLLMFile(&c, baseDir); err != nil {
		return Config{}, err
	}
	if c.PoolFile != "" {
		if len(c.Stocks) > 0 {
			return Config{}, errors.New("set pool_file or stocks, not both")
		}
		poolPath := c.PoolFile
		if !filepath.IsAbs(poolPath) {
			poolPath = filepath.Join(baseDir, poolPath)
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
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return Config{}, errors.New("stock pool must contain one document")
		}
		c.Stocks = pool.Stocks
	}
	if c.Feishu.Enabled && c.Feishu.CredentialsFile != "" {
		credentials, err := loadFeishuCredentials(baseDir, c.Feishu.CredentialsFile)
		if err != nil {
			return Config{}, err
		}
		c.Feishu.Webhook, c.Feishu.Secret = credentials.Webhook, credentials.Secret
	}
	if v, ok := os.LookupEnv("FEISHU_WEBHOOK"); ok {
		c.Feishu.Webhook = v
	}
	if v, ok := os.LookupEnv("FEISHU_SECRET"); ok {
		c.Feishu.Secret = v
	}
	for i := range c.Notify.Channels {
		channel := &c.Notify.Channels[i]
		if channel.Enabled && channel.CredentialsFile != "" {
			credentials, err := loadFeishuCredentials(baseDir, channel.CredentialsFile)
			if err != nil {
				return Config{}, err
			}
			channel.Webhook, channel.Secret = credentials.Webhook, credentials.Secret
		}
		if channel.WebhookEnv != "" {
			if v, ok := os.LookupEnv(channel.WebhookEnv); ok {
				channel.Webhook = v
			}
		}
		if channel.SecretEnv != "" {
			if v, ok := os.LookupEnv(channel.SecretEnv); ok {
				channel.Secret = v
			}
		}
	}
	return finalize(c, baseDir)
}

func finalize(c Config, baseDir string) (Config, error) {
	if strings.TrimSpace(c.DataDir) == "" {
		return Config{}, errors.New("data_dir is required")
	}
	if !filepath.IsAbs(c.DataDir) {
		c.DataDir = filepath.Join(baseDir, c.DataDir)
	}
	absolute, err := filepath.Abs(c.DataDir)
	if err != nil {
		return Config{}, errors.New("invalid data directory")
	}
	c.DataDir = absolute
	c.Market.loadCredentials()
	if c.LLM.ConfigFile != "" {
		if !filepath.IsAbs(c.LLM.ConfigFile) {
			c.LLM.ConfigFile = filepath.Join(baseDir, c.LLM.ConfigFile)
		}
		llmPath, err := filepath.Abs(c.LLM.ConfigFile)
		if err != nil {
			return Config{}, errors.New("invalid llm config file path")
		}
		c.LLM.ConfigFile = llmPath
	}
	c.prepared = true
	return c, c.Validate()
}

func (c Config) Validate() error {
	if (c.Analysis.Engine == "short_horizon_v1" && !c.Strategy.Enabled) || (c.Analysis.Engine == "leader_daily_v1" && c.Strategy.Enabled) {
		return errors.New("analysis.engine does not match strategy.enabled")
	}
	if err := c.Analysis.Validate(); err != nil {
		return err
	}
	if err := c.Intraday.Validate(); err != nil {
		return err
	}
	if err := c.Predictor.Validate(); err != nil {
		return err
	}
	if err := c.Market.Validate(); err != nil {
		return err
	}
	if err := c.MarketContext.Validate(); err != nil {
		return err
	}
	if err := c.LLM.Validate(); err != nil {
		return err
	}
	if c.Web.Enabled {
		host, port, err := net.SplitHostPort(c.Web.Listen)
		n, portErr := strconv.Atoi(port)
		if err != nil || portErr != nil || n < 1 || n > 65535 || (host != "" && host != "localhost" && net.ParseIP(host) == nil) {
			return errors.New("web.listen must be an IP address or localhost with a port from 1 to 65535")
		}
	}
	if c.Version != 1 {
		return errors.New("schema_version must be 1")
	}
	if err := c.Strategy.Validate(); err != nil {
		return err
	}
	switch c.StructureGuard.Mode {
	case "", "shadow", "enforce":
	default:
		return errors.New("structure_guard.mode must be shadow or enforce")
	}
	if c.Strategy.Enabled && c.Daily.Adjust != "qfq" {
		return errors.New("strategy requires daily.adjust=qfq")
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
	if len(c.Notify.Channels) > 0 && c.Feishu.Enabled {
		return errors.New("set notify.channels or feishu, not both")
	}
	channelNames := map[string]bool{}
	for _, channel := range c.Notify.Channels {
		if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`).MatchString(channel.Name) || strings.TrimSpace(channel.Type) == "" || channelNames[channel.Name] {
			return errors.New("notify channels contain an invalid or duplicate name")
		}
		if channel.WebhookEnv != "" && !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(channel.WebhookEnv) {
			return errors.New("notify channel webhook_env is invalid")
		}
		if channel.SecretEnv != "" && !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(channel.SecretEnv) {
			return errors.New("notify channel secret_env is invalid")
		}
		channelNames[channel.Name] = true
	}
	stocks := map[string]bool{}
	for _, s := range c.Stocks {
		if !ValidCode(s.Code) || stocks[s.Code] {
			return errors.New("stocks contain an invalid or duplicate A-share code")
		}
		if len(s.Name) > 128 || len(s.Sector) > 128 {
			return errors.New("stock labels are too long")
		}
		if s.ListedFrom != "" {
			if _, err := time.Parse("2006-01-02", s.ListedFrom); err != nil {
				return errors.New("stock listed_from must be a valid date")
			}
		}
		stocks[s.Code] = true
	}
	clock, err := time.Parse("15:04", c.Daily.RunAt)
	if err != nil || clock.Hour() < 16 {
		return errors.New("daily.run_at must be HH:MM at or after 16:00")
	}
	if c.Daily.HistoryDays < 90 || c.Daily.HistoryDays > 7300 {
		return errors.New("daily.history_days must be 90 to 7300 calendar days (effective window is capped at ten calendar years and the listing date)")
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
	if math.IsNaN(c.Sync.MinCoveragePct) || math.IsInf(c.Sync.MinCoveragePct, 0) ||
		c.Sync.MinCoveragePct < 0 || c.Sync.MinCoveragePct > 100 ||
		math.IsNaN(c.Sync.MinAnalyzablePct) || math.IsInf(c.Sync.MinAnalyzablePct, 0) ||
		c.Sync.MinAnalyzablePct < 0 || c.Sync.MinAnalyzablePct > 100 ||
		c.Sync.IncrementalBars < 2 || c.Sync.IncrementalBars > 30 ||
		c.Sync.StatusRefreshDays < 5 || c.Sync.StatusRefreshDays > 250 {
		return errors.New("invalid sync coverage, incremental bars or status refresh days")
	}
	for _, channel := range c.NotificationChannels() {
		if channel.Type != "feishu" {
			return errors.New("unsupported notification channel type")
		}
		u, err := url.Parse(channel.Webhook)
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

func (c Config) NotificationChannels() []NotifyChannel {
	if len(c.Notify.Channels) > 0 {
		out := make([]NotifyChannel, 0, len(c.Notify.Channels))
		for _, channel := range c.Notify.Channels {
			if channel.Enabled {
				out = append(out, channel)
			}
		}
		return out
	}
	if c.Feishu.Enabled {
		return []NotifyChannel{{Name: "feishu-main", Type: "feishu", Enabled: true, Webhook: c.Feishu.Webhook, Secret: c.Feishu.Secret}}
	}
	return nil
}
func Duration(s string) time.Duration { d, _ := time.ParseDuration(s); return d }
