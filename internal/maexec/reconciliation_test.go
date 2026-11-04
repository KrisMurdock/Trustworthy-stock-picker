package maexec

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Opt-in only: the frozen market data is private/local and is never committed.
// The ordinary suite uses small synthetic fixtures and needs no shared drive.
func TestFrozenReferenceReconciliation(t *testing.T) {
	root := os.Getenv("ARIS_REFERENCE_ROOT")
	if root == "" {
		t.Skip("set ARIS_REFERENCE_ROOT to frozen proxy-baseline directory")
	}
	f, e := os.Open(filepath.Join(root, "run-01/prices.json.gz"))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		t.Fatal(e)
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != "4450054aa56683993238d038445f25ff891e15516698cd1b50354b8f7e09517d" {
		t.Fatal("reference snapshot differs from approved frozen baseline")
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		t.Fatal(e)
	}
	z, e := gzip.NewReader(f)
	if e != nil {
		t.Fatal(e)
	}
	defer z.Close()
	var snap Snapshot
	if e = json.NewDecoder(z).Decode(&snap); e != nil {
		t.Fatal(e)
	}
	files, e := filepath.Glob(filepath.Join(root, "run-02/*.json"))
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, file := range files {
		if !strings.Contains(filepath.Base(file), "_slip") && !strings.HasSuffix(file, "winner-five-percent-guard.json") {
			continue
		}
		count++
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, e := os.ReadFile(file)
			if e != nil {
				t.Fatal(e)
			}
			var ref Result
			if e = json.Unmarshal(data, &ref); e != nil {
				t.Fatal(e)
			}
			sessions := []string{}
			for _, n := range ref.NAV {
				sessions = append(sessions, n.Date)
			}
			signals := []Signal{}
			for _, d := range ref.Decisions {
				signals = append(signals, d.Signal)
			}
			mode := "board"
			if strings.Contains(file, "five-percent") {
				mode = "five_percent"
			}
			in, _, e := Prepare(context.Background(), snap, sessions, signals, ref.Config, mode)
			if e != nil {
				t.Fatal(e)
			}
			got, e := Simulate(context.Background(), in)
			if e != nil {
				t.Fatal(e)
			}
			// Compare all accounting fields, not just summary returns. Metadata and
			// explanatory prose are deliberately excluded from the numerical contract.
			for key, pair := range map[string][2]any{"orders": {ref.Orders, got.Orders}, "trades": {ref.Trades, got.Trades}, "decisions": {ref.Decisions, got.Decisions}, "equity": {ref.NAV, got.NAV}, "positions": {ref.Open, got.Open}, "metrics": {ref.Metrics, got.Metrics}} {
				a, _ := json.Marshal(pair[0])
				b, _ := json.Marshal(pair[1])
				var x, y any
				json.Unmarshal(a, &x)
				json.Unmarshal(b, &y)
				compareJSON(t, key, x, y)
			}
		})
	}
	if count != 41 {
		t.Fatalf("expected 41 frozen scenarios, found %d", count)
	}
}
func compareJSON(t *testing.T, path string, a, b any) {
	t.Helper()
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		if !ok || math.Abs(x-y) > 1e-6+math.Abs(x)*1e-10 {
			t.Fatalf("%s: want %.12g, got %v", path, x, b)
		}
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			t.Fatalf("%s: array mismatch %d / %v", path, len(x), b)
		}
		for i := range x {
			compareJSON(t, fmt.Sprintf("%s[%d]", path, i), x[i], y[i])
		}
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			t.Fatalf("%s: object mismatch", path)
		}
		for k, v := range x {
			compareJSON(t, path+"."+k, v, y[k])
		}
	default:
		if a != b {
			t.Fatalf("%s: want %v, got %v", path, a, b)
		}
	}
}
