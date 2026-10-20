package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDashboardConfiguration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	base := "schema_version: 1\nstocks:\n  - code: sz000001\ncalendar:\n  valid_from: '2026-01-01'\n  valid_through: '2026-12-31'\n"
	write := func(extra string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(base+extra), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("")
	c, err := Load(path)
	if err != nil || !c.Web.Enabled || c.Web.Listen != "127.0.0.1:8080" {
		t.Fatalf("default dashboard configuration: %+v, %v", c.Web, err)
	}
	if c.Web.PublicReadOnly {
		t.Fatal("public read-only mode must be opt-in")
	}
	for _, address := range []string{"0.0.0.0:8081", "[::1]:8082", "localhost:8083", ":8084"} {
		write("web:\n  listen: '" + address + "'\n")
		c, err = Load(path)
		if err != nil || c.Web.Listen != address {
			t.Fatalf("valid dashboard address rejected: %s, %v", address, err)
		}
	}
	for _, address := range []string{"", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:65536", "http://localhost:8080", "user:secret@localhost:8080"} {
		write("web:\n  listen: '" + address + "'\n")
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "web.listen") || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid dashboard address accepted or disclosed: %v", err)
		}
	}
	write("web:\n  enabled: false\n  listen: ''\n")
	if c, err = Load(path); err != nil || c.Web.Enabled {
		t.Fatalf("cannot disable dashboard: %+v, %v", c.Web, err)
	}
	write("web:\n  listen: '127.0.0.1:8081'\n")
	t.Setenv("STOCK_WATCHER_WEB_ADDR", "127.0.0.1:8088")
	if c, err = Load(path); err != nil || c.Web.Listen != "127.0.0.1:8088" {
		t.Fatalf("environment override ignored: %+v, %v", c.Web, err)
	}
	t.Setenv("STOCK_WATCHER_WEB_ADDR", "")
	if _, err := Load(path); err == nil {
		t.Fatal("explicit empty address was ignored")
	}
	t.Setenv("STOCK_WATCHER_WEB_ADDR", "127.0.0.1:8088")
	t.Setenv("STOCK_WATCHER_WEB_PUBLIC_READ_ONLY", "true")
	if c, err = Load(path); err != nil || !c.Web.PublicReadOnly {
		t.Fatalf("public read-only override ignored: %+v, %v", c, err)
	}
	t.Setenv("STOCK_WATCHER_WEB_PUBLIC_READ_ONLY", "not-a-boolean")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "STOCK_WATCHER_WEB_PUBLIC_READ_ONLY") {
		t.Fatalf("invalid public read-only override accepted: %v", err)
	}
}
