package aris

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
)

// SharedInput is v2's self-contained, bounded shared snapshot. No external paths
// or mutable references are resolved. Expanded inputs share one immutable bar map.
type SharedInput struct {
	Version   int                        `json:"schema_version"`
	Snapshot  []byte                     `json:"snapshot_gzip"`
	SHA256    string                     `json:"snapshot_sha256"`
	Sessions  []string                   `json:"sessions"`
	Config    maexec.Config              `json:"config"`
	Signals   map[string][]maexec.Signal `json:"signals"`
	LimitMode string                     `json:"limit_mode"`
}

const MaxSharedSnapshot = 1 << 30

type bundleWire Bundle

func (b Bundle) MarshalJSON() ([]byte, error) {
	w := bundleWire(b)
	if b.Request.Version == 2 {
		w.Inputs = nil
	}
	return json.Marshal(w)
}
func (b *Bundle) UnmarshalJSON(raw []byte) error {
	var w bundleWire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&w); err != nil {
		return err
	}
	if w.Request.Version != 2 {
		if w.Shared != nil {
			return errors.New("shared input requires request version 2")
		}
		*b = Bundle(w)
		return nil
	}
	if w.Inputs != nil || w.Shared == nil {
		return errors.New("v2 requires shared input only")
	}
	s := w.Shared
	if s.Version != 1 || len(s.Snapshot) == 0 || len(s.Snapshot) > 64<<20 || Hash(s.Snapshot) != s.SHA256 || s.LimitMode != "historical_board" {
		return errors.New("invalid shared snapshot identity or mode")
	}
	z, err := gzip.NewReader(bytes.NewReader(s.Snapshot))
	if err != nil {
		return err
	}
	defer z.Close()
	data, err := io.ReadAll(io.LimitReader(z, MaxSharedSnapshot+1))
	if err != nil {
		return err
	}
	if len(data) > MaxSharedSnapshot {
		return errors.New("shared snapshot exceeds expansion limit")
	}
	var snap maexec.Snapshot
	// Use the same strict JSON protocol as v1, with an explicit v2 expansion bound.
	if err = decodeLimit(data, &snap, MaxSharedSnapshot); err != nil {
		return err
	}
	union := []maexec.Signal{}
	seen := map[string]maexec.Signal{}
	for _, signals := range s.Signals {
		for _, sig := range signals {
			k := sig.Code + "/" + sig.Date
			if prior, ok := seen[k]; ok {
				if prior != sig {
					return errors.New("conflicting shared signal")
				}
			} else {
				seen[k] = sig
				union = append(union, sig)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	common, _, err := maexec.Prepare(ctx, snap, s.Sessions, union, s.Config, s.LimitMode)
	if err != nil {
		return err
	}
	w.Inputs = map[string]maexec.Input{}
	for id, signals := range s.Signals {
		in := common
		in.Signals = signals
		w.Inputs[id] = in
	}
	*b = Bundle(w)
	return nil
}
