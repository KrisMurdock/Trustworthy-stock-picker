package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"stock-watcher/internal/config"
	"stock-watcher/internal/experiment"
	"stock-watcher/internal/market"
	"testing"
	"time"
)

func snapshotFixture(t *testing.T) (string, string) {
	t.Helper()
	c := config.Defaults()
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "样本"}}
	h := market.History{Version: 1, Code: "sz000001", Name: "样本", Source: "tencent", Adjust: "qfq", DataField: "qfqday", VolumeUnit: "provider_raw", FetchedAt: time.Now()}
	d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for len(h.Bars) < 100 {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			h.Bars = append(h.Bars, market.Bar{Date: d.Format(time.DateOnly), Open: 100, Close: 100, High: 101, Low: 99, Volume: 100})
		}
		d = d.AddDate(0, 0, 1)
	}
	h.AsOf = h.Bars[len(h.Bars)-1].Date
	raw := h
	raw.Adjust, raw.DataField = "none", "day"
	s, err := experiment.NewSnapshot(c, h.AsOf, []market.History{h}, []market.History{raw})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path, h.AsOf
}

func TestOfflineRunAndEmptySignals(t *testing.T) {
	path, through := snapshotFixture(t)
	args := []string{"-snapshot", path, "-from", "2026-01-01", "-through", through}
	var a, b, stderr bytes.Buffer
	for _, out := range []*bytes.Buffer{&a, &b} {
		if err := run(context.Background(), args, out, &stderr); err != nil {
			t.Fatal(err)
		}
	}
	if a.String() != b.String() {
		t.Fatal("same snapshot and options must produce byte-identical report")
	}
	var got output
	if err := json.Unmarshal(a.Bytes(), &got); err != nil || got.Stocks != 1 || got.SignalDays != 0 || len(got.Summary) != 12 || got.SnapshotSHA == "" {
		t.Fatalf("invalid empty-state report: %+v %v", got, err)
	}
}

func TestMissingInvalidTamperedAndCanceledInput(t *testing.T) {
	path, through := snapshotFixture(t)
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{nil, {"-snapshot", path}, {"-snapshot", path + "-missing", "-from", "2026-01-01", "-through", through}, {"-snapshot", path, "-from", "invalid", "-through", through}} {
		if err := run(context.Background(), args, &stdout, &stderr); err == nil {
			t.Fatal("accepted missing or invalid input", args)
		}
	}
	args := []string{"-snapshot", path, "-from", "2026-01-01", "-through", through}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, args, &stdout, &stderr); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, data := range []string{"{", `{"schema_version":1,"id":"wrong"}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := run(context.Background(), args, &stdout, &stderr); err == nil {
			t.Fatal("accepted corrupt snapshot")
		}
	}
	if stdout.Len() != 0 {
		t.Fatal("error emitted a partial report")
	}
}
