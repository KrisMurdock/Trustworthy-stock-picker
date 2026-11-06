package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/aris"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
)

func TestCLISealRunAndInspect(t *testing.T) {
	root := t.TempDir()
	in := maexec.Input{Version: 3, Model: "adjusted_return_proxy", PriceBasis: "qfq_proxy", Config: maexec.DefaultConfig(), Sessions: []string{"2026-01-01"}, Bars: map[string]map[string]maexec.Bar{}, Signals: []maexec.Signal{}, Provenance: maexec.Provenance{Calendar: "fixture", Rules: "assumed", Assumptions: "proxy"}}
	b := aris.Bundle{Request: aris.Request{Version: 1, RunID: "cli-test", Strategy: "ma-proxy"}, HumanContract: "fixture", Contract: aris.Contract{Version: 1, ID: "fixture", Baseline: "base", Candidates: []string{"candidate"}, Budget: aris.Budget{Candidates: 1, Attempts: 1, Timeout: "1m"}, Review: aris.ReviewPolicy{Mode: "off"}}, Inputs: map[string]maexec.Input{"base": in, "candidate": in}}
	raw, _ := json.Marshal(b)
	path := filepath.Join(root, "request.json")
	os.WriteFile(path, raw, 0600)
	var out, stderr bytes.Buffer
	call := func(args ...string) int {
		out.Reset()
		stderr.Reset()
		return execute(context.Background(), args, &out, &stderr)
	}
	if n := call("seal", "--request", path); n != 0 {
		t.Fatalf("seal %d %s", n, stderr.String())
	}
	if e := aris.Decode(out.Bytes(), &b); e != nil || b.Contract.Build == "" {
		t.Fatal(e)
	}
	os.WriteFile(path, out.Bytes(), 0600)
	if n := call("run", "--request", path, "--state-dir", root, "--log-dir", filepath.Join(root, "logs"), "--json"); n != 3 {
		t.Fatalf("run %d %s", n, stderr.String())
	}
	var s aris.State
	if e := aris.Decode(out.Bytes(), &s); e != nil || s.Status != "needs_human" {
		t.Fatalf("%s %v", out.String(), e)
	}
	if n := call("verify", "--run", "cli-test", "--state-dir", root, "--log-dir", filepath.Join(root, "logs"), "--json"); n != 0 {
		t.Fatalf("verify %d %s", n, stderr.String())
	}
	if n := call("run", "--request", path, "--state-dir", root, "--log-dir", filepath.Join(root, "logs"), "--json"); n != 1 {
		t.Fatal("duplicate accepted")
	}
	if n := call("simulate", "--input", filepath.Join(root, "missing")); n != 1 {
		t.Fatal(n)
	}
	if n := call("version"); n != 0 || !bytes.Contains(out.Bytes(), []byte("build_sha256")) {
		t.Fatal(out.String())
	}
}

func TestDiscoveryCLIRejectsMissingInputAndReportsStatus(t *testing.T) {
	root := t.TempDir()
	var out, stderr bytes.Buffer
	for _, cmd := range []string{"discover", "discover-resume", "discover-status", "discover-cancel", "discover-verify"} {
		out.Reset()
		stderr.Reset()
		status := execute(context.Background(), []string{cmd, "--state-dir", root, "--log-dir", filepath.Join(root, "logs"), "--json"}, &out, &stderr)
		if status != 1 || bytes.Contains(stderr.Bytes(), []byte("unknown command")) {
			t.Fatalf("%s: %d %s", cmd, status, stderr.String())
		}
	}
}
