package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"go.yaml.in/yaml/v3"
)

// LLMUpdate is the allow-listed set of values the web UI may change. Nil means
// unchanged; environment-locked fields are ignored by Apply.
type LLMUpdate struct {
	BaseURL *string `json:"base_url,omitempty"`
	APIKey  *string `json:"api_key,omitempty"`
	Model   *string `json:"model,omitempty"`
	Timeout *string `json:"timeout,omitempty"`
}

// LLMRuntime owns the effective LLM configuration for one process. The initial
// value is the already-resolved environment/file/default configuration; web
// updates only write the file and never override locked fields.
type LLMRuntime struct {
	mu   sync.RWMutex
	cfg  LLM
	path string
}

func NewLLMRuntime(cfg LLM) *LLMRuntime {
	return &LLMRuntime{cfg: cloneLLM(cfg), path: cfg.ConfigFile}
}

func (r *LLMRuntime) Snapshot() LLM {
	if r == nil {
		return DefaultLLM()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneLLM(r.cfg)
}

// Apply validates and persists only unlocked fields. A failed write leaves the
// in-memory effective configuration unchanged.
func (r *LLMRuntime) Apply(update LLMUpdate) (LLM, error) {
	if r == nil {
		return LLM{}, errors.New("llm runtime is unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	next := cloneLLM(r.cfg)
	applyString := func(field string, value *string, target *string) {
		if value != nil && !next.IsLocked(field) {
			*target = *value
			if next.Sources == nil {
				next.Sources = map[string]string{}
			}
			next.Sources[field] = "file"
		}
	}
	applyString("base_url", update.BaseURL, &next.BaseURL)
	applyString("api_key", update.APIKey, &next.APIKey)
	applyString("model", update.Model, &next.Model)
	applyString("timeout", update.Timeout, &next.Timeout)
	if err := next.Validate(); err != nil {
		return LLM{}, err
	}
	if err := writeLLMFile(r.path, next); err != nil {
		return LLM{}, err
	}
	r.cfg = next
	return cloneLLM(next), nil
}

type llmDiskFile struct {
	SchemaVersion int     `yaml:"schema_version"`
	BaseURL       *string `yaml:"base_url,omitempty"`
	APIKey        *string `yaml:"api_key,omitempty"`
	Model         *string `yaml:"model,omitempty"`
	Timeout       *string `yaml:"timeout,omitempty"`
}

func writeLLMFile(path string, cfg LLM) error {
	if path == "" {
		return errors.New("llm config file path is empty")
	}
	document := llmDiskFile{SchemaVersion: 1}
	if !cfg.IsLocked("base_url") {
		document.BaseURL = &cfg.BaseURL
	}
	if !cfg.IsLocked("api_key") {
		document.APIKey = &cfg.APIKey
	}
	if !cfg.IsLocked("model") {
		document.Model = &cfg.Model
	}
	if !cfg.IsLocked("timeout") {
		document.Timeout = &cfg.Timeout
	}
	data, err := yaml.Marshal(document)
	if err != nil {
		return errors.New("cannot encode llm config file")
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".llm-config-*")
	if err != nil {
		return fmt.Errorf("cannot create llm config file: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return errors.New("cannot secure llm config file")
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return errors.New("cannot write llm config file")
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return errors.New("cannot sync llm config file")
	}
	if err := f.Close(); err != nil {
		return errors.New("cannot close llm config file")
	}
	if err := os.Rename(name, path); err != nil {
		return errors.New("cannot replace llm config file")
	}
	dirHandle, err := os.Open(dir)
	if err != nil {
		return errors.New("cannot sync llm config directory")
	}
	defer dirHandle.Close()
	if err := dirHandle.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return errors.New("cannot sync llm config directory")
	}
	return nil
}

func cloneLLM(cfg LLM) LLM {
	cfg.Locked = cloneBoolMap(cfg.Locked)
	cfg.Sources = cloneStringMap(cfg.Sources)
	return cfg
}

func cloneBoolMap(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
