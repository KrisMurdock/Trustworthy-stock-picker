package logging

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTextLocationRedactionAndRotation(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	l, err := New(Options{Dir: root, MaxBytes: 400, MaxDays: 1, Backups: 2, Color: "off", Stdout: &stdout})
	if err != nil {
		t.Fatal(err)
	}
	l.Info("Business event", "nested", map[string]any{"password": "hidden", "ok": 1}, "webhook", "hidden")
	first, err := os.ReadFile(filepath.Join(root, "stock-watcher.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, stdout.Bytes()) || strings.Contains(string(first), "hidden") || !strings.Contains(string(first), "logging_test.go:") {
		t.Fatalf("unexpected log %s", first)
	}
	for i := 0; i < 10; i++ {
		l.Info("Business event", "value", strings.Repeat("x", 90))
	}
	l.Close()
	files, _ := filepath.Glob(filepath.Join(root, "*.gz"))
	if len(files) != 2 {
		t.Fatalf("retention=%d", len(files))
	}
	for _, p := range files {
		f, _ := os.Open(p)
		z, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(z)
		z.Close()
		f.Close()
		if err != nil || bytes.Contains(data, []byte("\x1b")) {
			t.Fatal("invalid archive")
		}
	}
	old := filepath.Join(root, "stock-watcher-20000101.log.gz")
	os.WriteFile(old, []byte("old"), 0600)
	os.Chtimes(old, time.Now().AddDate(0, 0, -2), time.Now().AddDate(0, 0, -2))
	l, err = New(Options{Dir: root, MaxBytes: 400, MaxDays: 1, Backups: 2, Color: "off", Stdout: &stdout})
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("expired log not pruned")
	}
}
func TestJSONNeverUsesColor(t *testing.T) {
	var out bytes.Buffer
	l, err := New(Options{Dir: t.TempDir(), MaxBytes: 1024, MaxDays: 1, Backups: 1, JSON: true, Color: "force", Stdout: &out})
	if err != nil {
		t.Fatal(err)
	}
	l.Info("Structured event", "token", "private")
	l.Close()
	var v map[string]any
	if json.Unmarshal(out.Bytes(), &v) != nil || v["token"] != "[REDACTED]" || !strings.Contains(v["msg"].(string), "[INFO]") {
		t.Fatal("invalid JSON log")
	}
}
