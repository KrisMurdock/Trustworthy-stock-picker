package aris

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
)

type App struct {
	root     string
	build    string
	log      *logging.Logger
	Reviewer Reviewer
}

func New(root, build string, log *logging.Logger) (*App, error) {
	if build == "" {
		return nil, errors.New("build identity required")
	}
	abs, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	if abs == "/" {
		return nil, errors.New("invalid state root")
	}
	if e = SafePath(abs); e != nil {
		return nil, e
	}
	if e = SafePath(filepath.Join(abs, "runs")); e != nil {
		return nil, e
	}
	// Check the nearest existing ancestor before creating any directory. A
	// rejected remote mount must not receive even an empty research directory.
	if e = localPath(filepath.Join(abs, "runs")); e != nil {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Join(abs, "runs"), 0700); e != nil {
		return nil, e
	}
	if log == nil {
		log = logging.Nop()
	}
	return &App{root: abs, build: build, log: log}, nil
}
func localPath(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		var st syscall.Statfs_t
		e := syscall.Statfs(p, &st)
		if os.IsNotExist(e) && p != "/" {
			continue
		}
		if e != nil {
			return e
		}
		if uint64(st.Type) == 0xff534d42 || uint64(st.Type) == 0xfe534d42 || uint64(st.Type) == 0x6969 {
			return errors.New("state root must be local, not CIFS/NFS")
		}
		return nil
	}
}
func (a *App) Close() error { return nil }

// SafePath rejects links including ancestor links before opening local artifacts.
// The directory must be owned by the invoking user; HTTP callers cannot supply it.
func SafePath(path string) error {
	abs, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	for p := abs; p != "/"; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink paths are not allowed")
		}
	}
	return nil
}
func Read(path string, limit int64) ([]byte, error) {
	if e := SafePath(path); e != nil {
		return nil, e
	}
	info, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("regular input file required")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() {
		return nil, errors.New("regular input file required")
	}
	raw, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("document exceeds size limit")
	}
	return raw, nil
}
func atomic(path string, value any) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	if len(raw) > MaxDocument {
		return errors.New("artifact size limit")
	}
	return atomicBytes(path, append(raw, '\n'))
}
func atomicBytes(path string, raw []byte) error {
	if e := SafePath(path); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".pending-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(raw); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (a *App) dir(id string) (string, error) {
	if !ValidID(id) {
		return "", errors.New("invalid run ID")
	}
	p := filepath.Join(a.root, "runs", id)
	return p, SafePath(p)
}
func lock(dir string) (func(), error) {
	p := filepath.Join(dir, ".lock")
	if e := SafePath(p); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("run is locked")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

type Attempt struct {
	ID         string `json:"id"`
	Candidate  string `json:"candidate"`
	Status     string `json:"status"`
	ResultHash string `json:"result_sha256,omitempty"`
	File       string `json:"file,omitempty"`
}
type GateResult struct {
	ID        string `json:"id"`
	Candidate string `json:"candidate,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	Evidence  string `json:"evidence,omitempty"`
}
type State struct {
	Version     int           `json:"schema_version"`
	RunID       string        `json:"run_id"`
	Status      string        `json:"status"`
	BundleHash  string        `json:"bundle_sha256"`
	Build       string        `json:"build_sha256"`
	Created     time.Time     `json:"created_at"`
	Deadline    time.Time     `json:"deadline"`
	Attempts    []Attempt     `json:"attempts"`
	Gates       []GateResult  `json:"gate_results"`
	Review      *ReviewResult `json:"review,omitempty"`
	ReviewCalls int           `json:"review_calls"`
	Reason      string        `json:"reason,omitempty"`
}

func terminal(status string) bool {
	switch status {
	case "accepted", "rejected", "development_passed", "needs_human", "failed", "canceled":
		return true
	}
	return false
}
func (a *App) Status(id string) (State, error) {
	var s State
	dir, e := a.dir(id)
	if e != nil {
		return s, e
	}
	raw, e := Read(filepath.Join(dir, "state.json"), 8<<20)
	if e != nil {
		return s, e
	}
	if e = Decode(raw, &s); e != nil {
		return s, e
	}
	if s.Version != 1 || s.RunID != id || s.BundleHash == "" || s.Build == "" {
		return s, errors.New("invalid state")
	}
	return s, nil
}
func (a *App) save(dir string, s State) error {
	// Audit indexes may contain an uncommitted suffix after a crash. Only the
	// state hash committed by state.json is authoritative, never the last line.
	if e := appendAudit(filepath.Join(dir, "ledger.jsonl"), struct {
		StateHash string    `json:"state_sha256"`
		Attempts  []Attempt `json:"attempts"`
	}{Digest(s), s.Attempts}); e != nil {
		return e
	}
	if e := appendAudit(filepath.Join(dir, "events.jsonl"), struct {
		StateHash string    `json:"state_sha256"`
		Phase     string    `json:"phase"`
		At        time.Time `json:"at"`
	}{Digest(s), s.Status, time.Now().UTC()}); e != nil {
		return e
	}
	if e := atomic(filepath.Join(dir, "state.json"), s); e != nil {
		return e
	}
	a.log.Info("Research phase changed", "run_id", s.RunID, "phase", s.Status)
	return nil
}
func appendAudit(path string, v any) error {
	if e := SafePath(path); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return e
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if st.Size()+int64(len(raw))+1 > 32<<20 {
		return errors.New("audit index size limit")
	}
	if _, e = f.Write(append(raw, '\n')); e != nil {
		return e
	}
	return f.Sync()
}
func (a *App) Verify(id string) error {
	s, e := a.Status(id)
	if e != nil {
		return e
	}
	dir, e := a.dir(id)
	if e != nil {
		return e
	}
	raw, e := Read(filepath.Join(dir, "bundle.json"), MaxDocument)
	if e != nil {
		return e
	}
	var b Bundle
	if e = Decode(raw, &b); e != nil {
		return e
	}
	if Digest(b) != s.BundleHash || s.Build != a.build {
		return errors.New("bundle or build mismatch")
	}
	if e = b.validate(a.build); e != nil {
		return e
	}
	if s.Deadline.Sub(s.Created) <= 0 {
		return errors.New("invalid persisted deadline")
	}
	timeout, _ := time.ParseDuration(b.Contract.Budget.Timeout)
	if !s.Deadline.Equal(s.Created.Add(timeout)) {
		return errors.New("persisted deadline differs from contract")
	}
	counts := map[string]int{}
	completed := map[string]bool{}
	for _, at := range s.Attempts {
		counts[at.Candidate]++
		if _, ok := b.Inputs[at.Candidate]; !ok || counts[at.Candidate] > b.Contract.Budget.Attempts || at.ID != fmt.Sprintf("%s-%d", at.Candidate, counts[at.Candidate]) || completed[at.Candidate] {
			return errors.New("invalid attempt ledger")
		}
		switch at.Status {
		case "started", "interrupted", "failed", "completed":
		default:
			return errors.New("invalid attempt status")
		}
		if at.Status != "completed" {
			continue
		}
		completed[at.Candidate] = true
		if !ValidID(at.Candidate) || at.File != "artifacts/"+at.Candidate+".json" {
			return errors.New("invalid artifact reference")
		}
		r, e := Read(filepath.Join(dir, at.File), MaxDocument)
		if e != nil {
			return e
		}
		if Hash(r) != at.ResultHash {
			return errors.New("result digest mismatch")
		}
		var artifact Artifact
		if e = Decode(r, &artifact); e != nil {
			return e
		}
		if Digest(artifact.Result) != artifact.ReplayHash {
			return errors.New("replay digest mismatch")
		}
	}
	if s.ReviewCalls < 0 || s.ReviewCalls > b.Contract.Budget.ReviewCalls {
		return errors.New("invalid review budget")
	}
	return nil
}
func (a *App) Cancel(id string) error {
	dir, e := a.dir(id)
	if e != nil {
		return e
	}
	s, e := a.Status(id)
	if e != nil {
		return e
	}
	if terminal(s.Status) {
		return errors.New("run is terminal")
	}
	return atomic(filepath.Join(dir, "cancel.json"), struct {
		Requested bool `json:"requested"`
	}{true})
}
func (a *App) report(dir string, s State) error {
	if e := atomic(filepath.Join(dir, "report.json"), s); e != nil {
		return e
	}
	var text strings.Builder
	fmt.Fprintf(&text, "# 自动化策略研究\n\n运行：%s\n\n状态：%s\n\n%s\n\n模型：前复权简化收益、虚拟持仓单位。结果仅属历史开发研究。\n\n", s.RunID, s.Status, s.Reason)
	for _, g := range s.Gates {
		fmt.Fprintf(&text, "- %s %s：%s；%s\n", g.ID, g.Candidate, g.Status, g.Reason)
	}
	return atomicBytes(filepath.Join(dir, "report.md"), []byte(text.String()))
}
