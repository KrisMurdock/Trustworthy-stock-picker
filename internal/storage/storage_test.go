package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicReadAndExclusiveOwnership(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := Open(root, false); err == nil {
		t.Fatal("accepted second writer")
	}
	if err := s.Probe(); err != nil {
		t.Fatal(err)
	}
	value := struct {
		Name string `json:"name"`
	}{"中文日线"}
	if err := s.Write("daily/2026-10-13/sz000001.json", value); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Name string `json:"name"`
	}
	if err := s.Read("daily/2026-10-13/sz000001.json", &got); err != nil || got != value {
		t.Fatalf("read %v %v", got, err)
	}
	if err := s.Write("../escape.json", value); err == nil {
		t.Fatal("accepted path escape")
	}
	os.Symlink(t.TempDir(), filepath.Join(root, "outside"))
	if err := s.Write("outside/test.json", value); err == nil {
		t.Fatal("followed a symlink")
	}
	os.WriteFile(filepath.Join(root, "broken.json"), []byte(`{"name":`), 0600)
	if err := s.Read("broken.json", &got); err == nil {
		t.Fatal("accepted corrupt state")
	}
	if _, err := Open(root, true); err == nil {
		t.Fatal("accepted local filesystem as CIFS")
	}
}
func TestMissingRootIsNotCreated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-mounted")
	if _, err := Open(root, true); err == nil {
		t.Fatal("accepted missing mount")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("created fallback directory")
	}
}

func TestEnsureRootCreatesOnlyLocalDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "local-data")
	if err := EnsureRoot(root, false); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("local data root not created: %v", err)
	}

	missingMount := filepath.Join(t.TempDir(), "missing-mount")
	if err := EnsureRoot(missingMount, true); err == nil {
		t.Fatal("created a required CIFS mount")
	}
	if _, err := os.Stat(missingMount); !os.IsNotExist(err) {
		t.Fatal("created local fallback for required CIFS")
	}
}

func TestReadOnlyViewWithActiveWriter(t *testing.T) {
	root := t.TempDir()
	writer, err := Open(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := writer.Write("state.json", map[string]int{"schema_version": 1}); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenView(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var state map[string]int
	if err := reader.Read("state.json", &state); err != nil || state["schema_version"] != 1 {
		t.Fatal("concurrent status read failed")
	}
	if err := reader.Write("state.json", map[string]int{}); err == nil {
		t.Fatal("read view wrote without lock")
	}
}
