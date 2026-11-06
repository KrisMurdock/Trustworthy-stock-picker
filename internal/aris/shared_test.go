package aris

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
)

func TestSharedWire(t *testing.T) {
	snap := maexec.Snapshot{Version: 1, Calendar: []string{"2026-01-02", "2026-01-05"}, Stocks: map[string][]maexec.Price{}}
	raw, _ := json.Marshal(snap)
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	z.Write(raw)
	z.Close()
	b := Bundle{Request: Request{Version: 2}, Shared: &SharedInput{Version: 1, Snapshot: compressed.Bytes(), SHA256: Hash(compressed.Bytes()), Sessions: snap.Calendar, Config: maexec.DefaultConfig(), Signals: map[string][]maexec.Signal{"baseline": {}, "candidate": {}}, LimitMode: "historical_board"}}
	encoded, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Bundle
	if err = Decode(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Inputs) != 2 {
		t.Fatal("missing candidates")
	}
	roundtrip, _ := json.Marshal(decoded)
	if !bytes.Equal(encoded, roundtrip) {
		t.Fatal("wire must contain one snapshot, not expanded inputs")
	}
	if Digest(decoded.Inputs["baseline"]) != Digest(decoded.Inputs["candidate"]) {
		t.Fatal("shared input differs")
	}
	b.Shared.SHA256 = "wrong"
	encoded, _ = json.Marshal(b)
	if Decode(encoded, &decoded) == nil {
		t.Fatal("accepted corrupted snapshot")
	}
	b.Shared.SHA256 = Hash(compressed.Bytes())
	b.Request.Version = 1
	encoded, _ = json.Marshal(b)
	if Decode(encoded, &decoded) == nil {
		t.Fatal("v1 accepted shared protocol")
	}
	b.Request.Version = 2
	encoded, _ = json.Marshal(b)
	var wire map[string]json.RawMessage
	json.Unmarshal(encoded, &wire)
	wire["inputs"] = json.RawMessage(`{}`)
	encoded, _ = json.Marshal(wire)
	if Decode(encoded, &decoded) == nil {
		t.Fatal("accepted mixed protocols")
	}
}

func TestSharedRunAndReplay(t *testing.T) {
	b := configured()
	b.Request.Version = 2
	snap := maexec.Snapshot{Version: 1, Calendar: b.Inputs["base"].Sessions, Stocks: map[string][]maexec.Price{}}
	raw, _ := json.Marshal(snap)
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	z.Write(raw)
	z.Close()
	b.Shared = &SharedInput{Version: 1, Snapshot: compressed.Bytes(), SHA256: Hash(compressed.Bytes()), Sessions: snap.Calendar, Config: maexec.DefaultConfig(), Signals: map[string][]maexec.Signal{"base": {}, "candidate": {}}, LimitMode: "historical_board"}
	raw, _ = json.Marshal(b)
	if err := Decode(raw, &b); err != nil {
		t.Fatal(err)
	}
	for id, in := range b.Inputs {
		b.Contract.InputHashes[id] = Digest(in)
	}
	a, err := New(t.TempDir(), "test-build", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	state, err := a.Run(context.Background(), b)
	if err != nil || state.Status != "rejected" {
		t.Fatal(state, err)
	}
	if err = a.Replay(context.Background(), b.Request.RunID); err != nil {
		t.Fatal(err)
	}
}
