package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLLMEnvironmentOverridesFileAndLocksFields(t *testing.T) {
	root := t.TempDir()
	llmPath := filepath.Join(root, "llm.config")
	file := `schema_version: 1
base_url: https://file.example/v1
api_key: file-key
model: file-model
timeout: 45s
`
	if err := os.WriteFile(llmPath, []byte(file), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	text := valid + "llm:\n  config_file: llm.config\n  write_mode: off\n"
	if err := os.WriteFile(configPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STOCK_WATCHER_LLM_BASE_URL", "https://env.example/v1")
	t.Setenv("STOCK_WATCHER_LLM_API_KEY", "env-key")
	t.Setenv("STOCK_WATCHER_LLM_MODEL", "env-model")
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LLM.BaseURL != "https://env.example/v1" || loaded.LLM.APIKey != "env-key" || loaded.LLM.Model != "env-model" || loaded.LLM.Timeout != "45s" {
		t.Fatalf("field precedence is wrong: %+v", loaded.LLM)
	}
	if !loaded.LLM.Locked["base_url"] || !loaded.LLM.Locked["api_key"] || !loaded.LLM.Locked["model"] || loaded.LLM.Locked["timeout"] {
		t.Fatalf("lock state is wrong: %+v", loaded.LLM.Locked)
	}
	if loaded.LLM.Sources["base_url"] != "env" || loaded.LLM.Sources["timeout"] != "file" {
		t.Fatalf("source metadata is wrong: %+v", loaded.LLM.Sources)
	}
	if loaded.LLM.ConfigFile != llmPath || !filepath.IsAbs(loaded.LLM.ConfigFile) {
		t.Fatalf("config path was not resolved: %q", loaded.LLM.ConfigFile)
	}
}

func TestLLMRuntimeApplyPersistsUnlockedFieldsAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llm.config")
	settings := DefaultLLM()
	settings.ConfigFile = path
	settings.BaseURL = "https://old.example/v1"
	settings.APIKey = "old-key"
	settings.Model = "old-model"
	settings.Timeout = "30s"
	settings.Locked = map[string]bool{"api_key": true}
	settings.Sources = map[string]string{"api_key": "env"}
	runtime := NewLLMRuntime(settings)
	base := "https://new.example/v1"
	model := "new-model"
	timeout := "90s"
	key := "must-not-be-written"
	updated, err := runtime.Apply(LLMUpdate{BaseURL: &base, APIKey: &key, Model: &model, Timeout: &timeout})
	if err != nil {
		t.Fatal(err)
	}
	if updated.BaseURL != base || updated.Model != model || updated.Timeout != timeout || updated.APIKey != "old-key" {
		t.Fatalf("unexpected effective configuration: %+v", updated)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions = %o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"schema_version: 1", "base_url: https://new.example/v1", "model: new-model", "timeout: 90s"} {
		if !strings.Contains(text, want) {
			t.Fatalf("persisted config missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "must-not-be-written") || strings.Contains(text, "old-key") || strings.Contains(text, "api_key") {
		t.Fatalf("locked API key was persisted: %s", text)
	}
}

func TestLLMRuntimeFailedApplyKeepsPreviousState(t *testing.T) {
	settings := DefaultLLM()
	settings.ConfigFile = filepath.Join(t.TempDir(), "missing", "llm.config")
	settings.BaseURL = "https://old.example/v1"
	runtime := NewLLMRuntime(settings)
	next := "https://new.example/v1"
	if _, err := runtime.Apply(LLMUpdate{BaseURL: &next}); err == nil {
		t.Fatal("write to a missing directory succeeded")
	}
	snapshot := runtime.Snapshot()
	if snapshot.BaseURL != "https://old.example/v1" {
		t.Fatalf("failed write changed in-memory state: %+v", snapshot)
	}
}

func TestLLMValidation(t *testing.T) {
	base := DefaultLLM()
	base.ConfigFile = filepath.Join(t.TempDir(), "llm.config")
	cases := []struct {
		name   string
		mutate func(*LLM)
		ok     bool
	}{
		{"defaults", func(*LLM) {}, true},
		{"loopback_http", func(c *LLM) { c.BaseURL = "http://127.0.0.1:8080/v1" }, true},
		{"remote_http", func(c *LLM) { c.BaseURL = "http://api.example/v1" }, false},
		{"userinfo", func(c *LLM) { c.BaseURL = "https://user:secret@api.example/v1" }, false},
		{"query", func(c *LLM) { c.BaseURL = "https://api.example/v1?key=secret" }, false},
		{"fragment", func(c *LLM) { c.BaseURL = "https://api.example/v1#secret" }, false},
		{"bad_timeout", func(c *LLM) { c.Timeout = "500ms" }, false},
		{"long_timeout", func(c *LLM) { c.Timeout = "5m1s" }, false},
		{"bad_write_mode", func(c *LLM) { c.WriteMode = "remote" }, false},
		{"empty_model", func(c *LLM) { c.Model = "" }, false},
		{"disabled_ignores_invalid", func(c *LLM) { c.Enabled = false; c.BaseURL = ""; c.Model = ""; c.Timeout = "bad" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := base
			tc.mutate(&settings)
			if err := settings.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate() error = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}
