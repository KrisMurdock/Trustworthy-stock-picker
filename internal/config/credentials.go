package config

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

type feishuCredentials struct {
	Webhook string `yaml:"webhook"`
	Secret  string `yaml:"secret,omitempty"`
}

func loadFeishuCredentials(baseDir, path string) (feishuCredentials, error) {
	var credentials feishuCredentials
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return credentials, errors.New("cannot open Feishu credentials file")
	}
	defer f.Close()
	d := yaml.NewDecoder(io.LimitReader(f, 64<<10))
	d.KnownFields(true)
	if err := d.Decode(&credentials); err != nil {
		return feishuCredentials{}, errors.New("invalid Feishu credentials file: expected webhook and optional secret")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return feishuCredentials{}, errors.New("Feishu credentials file must contain one YAML document")
	}
	return credentials, nil
}
