// Package storage owns all durable market data. Linux CIFS is required by default.
package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const cifsMagic = 0xff534d42
const smb2Magic = 0xfe534d42 // Linux SMB2/SMB3 filesystem, including this Windows mount.
const maxFileBytes = 8 << 20

type Store struct {
	root        string
	requireCIFS bool
	lock        *os.File
}

func Open(root string, requireCIFS bool) (*Store, error) {
	s, err := OpenView(root, requireCIFS)
	if err != nil {
		return nil, err
	}
	root = s.root
	f, err := os.OpenFile(filepath.Join(root, ".stock-watcher.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("storage lock is not writable")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another stock-watcher process owns the data directory")
	}
	s.lock = f
	return s, nil
}

// OpenView allows atomic read-only status queries while the single writer runs.
func OpenView(root string, requireCIFS bool) (*Store, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	s := &Store{root: root, requireCIFS: requireCIFS}
	// Never create a missing data root. This is what prevents local fallback.
	if err := s.Check(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error {
	if s.lock == nil {
		return nil
	}
	err := syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	closeErr := s.lock.Close()
	s.lock = nil
	return errors.Join(err, closeErr)
}
func (s *Store) Check() error {
	info, err := os.Stat(s.root)
	if err != nil || !info.IsDir() {
		return errors.New("data root is unavailable; no local fallback")
	}
	// Lookup a child to activate systemd autofs. Stat(root) alone can report the
	// dormant autofs filesystem even when the SSH tunnel and share are ready.
	if _, err := os.Stat(filepath.Join(s.root, ".stock-watcher.lock")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("remote storage is unavailable")
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.root, &st); err != nil {
		return errors.New("cannot inspect data filesystem")
	}
	if s.requireCIFS && uint64(st.Type) != cifsMagic && uint64(st.Type) != smb2Magic {
		return errors.New("data root is not mounted CIFS; no local fallback")
	}
	return nil
}

func (s *Store) Probe() error {
	if err := s.Check(); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.root, ".stock-watcher-probe-*")
	if err != nil {
		return errors.New("data root is not writable")
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString("stock-watcher storage probe\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (s *Store) path(name string, create bool) (string, error) {
	if filepath.IsAbs(name) || name == "." || name != filepath.Clean(name) || strings.HasPrefix(name, "..") {
		return "", errors.New("invalid storage relative path")
	}
	if err := s.Check(); err != nil {
		return "", err
	}
	parts := strings.Split(name, string(filepath.Separator))
	cur := s.root
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			if create && i < len(parts)-1 {
				if err := os.Mkdir(cur, 0700); err != nil {
					return "", err
				}
				continue
			}
			if i == len(parts)-1 {
				return cur, nil
			}
			return "", fs.ErrNotExist
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symlinks within the data directory are prohibited")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", errors.New("storage parent is not a directory")
		}
	}
	return cur, nil
}

func (s *Store) Write(name string, value any) error {
	if s.lock == nil {
		return errors.New("storage view cannot write without exclusive ownership")
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxFileBytes {
		return errors.New("storage document exceeds size limit")
	}
	data = append(data, '\n')
	path, err := s.path(name, true)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".stock-watcher-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = s.Check(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	// CIFS servers may not implement directory fsync; file fsync remains mandatory.
	if err = dir.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}
	return nil
}

func (s *Store) Read(name string, value any) error {
	path, err := s.path(name, false)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxFileBytes {
		return errors.New("storage document exceeds size limit")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return errors.New("invalid stored JSON; refusing to discard state")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("invalid trailing stored JSON")
	}
	return nil
}
