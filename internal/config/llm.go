package config

import (
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// LLM contains the OpenAI-compatible endpoint and the local configuration
// boundary. Locked is runtime-only and records fields supplied by the
// deployment environment, which the web UI must never overwrite.
type LLM struct {
	Enabled    bool              `yaml:"enabled" json:"enabled"`
	BaseURL    string            `yaml:"base_url" json:"base_url,omitempty"`
	APIKey     string            `yaml:"api_key" json:"-"`
	Model      string            `yaml:"model" json:"model,omitempty"`
	Timeout    string            `yaml:"timeout" json:"timeout,omitempty"`
	ConfigFile string            `yaml:"config_file,omitempty" json:"-"`
	WriteMode  string            `yaml:"write_mode,omitempty" json:"write_mode,omitempty"`
	Locked     map[string]bool   `yaml:"-" json:"-"`
	Sources    map[string]string `yaml:"-" json:"-"`
}

func DefaultLLM() LLM {
	return LLM{Enabled: true, BaseURL: "https://api.openai.com/v1", Model: "gpt-4.1-mini", Timeout: "60s", ConfigFile: "llm.config", WriteMode: "loopback", Sources: map[string]string{"base_url": "default", "api_key": "default", "model": "default", "timeout": "default"}}
}

func (l LLM) Configured() bool {
	return strings.TrimSpace(l.APIKey) != "" && strings.TrimSpace(l.BaseURL) != "" && strings.TrimSpace(l.Model) != ""
}

func (l LLM) IsLocked(field string) bool {
	return l.Locked != nil && l.Locked[field]
}

func (l LLM) Validate() error {
	if !l.Enabled {
		return nil
	}
	if strings.TrimSpace(l.ConfigFile) == "" {
		return errors.New("llm.config_file is required")
	}
	if !filepath.IsAbs(l.ConfigFile) {
		return errors.New("llm.config_file must be resolved to an absolute path")
	}
	if err := validateLLMBaseURL(l.BaseURL); err != nil {
		return err
	}
	if strings.TrimSpace(l.Model) == "" {
		return errors.New("llm.model is required")
	}
	duration, err := time.ParseDuration(l.Timeout)
	if err != nil || duration < time.Second || duration > 5*time.Minute {
		return errors.New("llm.timeout must be between 1s and 5m")
	}
	switch l.WriteMode {
	case "off", "loopback", "all":
	default:
		return errors.New("llm.write_mode must be off, loopback or all")
	}
	return nil
}

type llmFile struct {
	SchemaVersion int     `yaml:"schema_version"`
	BaseURL       *string `yaml:"base_url"`
	APIKey        *string `yaml:"api_key"`
	Model         *string `yaml:"model"`
	Timeout       *string `yaml:"timeout"`
}

func loadLLMFile(c *Config, baseDir string) error {
	if strings.TrimSpace(c.LLM.ConfigFile) == "" {
		return nil
	}
	path := c.LLM.ConfigFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return errors.New("invalid llm config file path")
	}
	c.LLM.ConfigFile = absolute
	file, err := os.Open(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot open llm config file")
	}
	defer file.Close()
	var document llmFile
	decoder := yaml.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		return errors.New("invalid llm config file")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("llm config file must contain one YAML document")
	}
	if document.SchemaVersion != 1 {
		return errors.New("llm config schema_version must be 1")
	}
	if document.BaseURL != nil && !c.LLM.IsLocked("base_url") {
		c.LLM.BaseURL = *document.BaseURL
		c.LLM.Sources["base_url"] = "file"
	}
	if document.APIKey != nil && !c.LLM.IsLocked("api_key") {
		c.LLM.APIKey = *document.APIKey
		c.LLM.Sources["api_key"] = "file"
	}
	if document.Model != nil && !c.LLM.IsLocked("model") {
		c.LLM.Model = *document.Model
		c.LLM.Sources["model"] = "file"
	}
	if document.Timeout != nil && !c.LLM.IsLocked("timeout") {
		c.LLM.Timeout = *document.Timeout
		c.LLM.Sources["timeout"] = "file"
	}
	return nil
}

func validateLLMBaseURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("llm.base_url is invalid")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return errors.New("llm.base_url must use HTTPS or loopback HTTP")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(raw, "#") || parsed.Fragment != "" {
		return errors.New("llm.base_url must not contain credentials, query or fragment")
	}
	if scheme == "http" && !isLLMLoopback(parsed.Hostname()) {
		return errors.New("llm.base_url HTTP is only allowed for loopback hosts")
	}
	return nil
}

func isLLMLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4[0] == 127
	}
	return ip.Equal(net.IPv6loopback)
}

// LLMTimeout returns the parsed request timeout. Validate must be called first.
func LLMTimeout(l LLM) time.Duration {
	duration, _ := time.ParseDuration(l.Timeout)
	return duration
}

// MaxOutputTokens is fixed by the first advisor contract rather than exposed as
// a user-editable setting. Invalid deployment overrides are rejected by tests.
const MaxOutputTokens = 2048
