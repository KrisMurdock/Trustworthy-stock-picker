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
