package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"stock-watcher/internal/bottomstudy"
	"stock-watcher/internal/config"
	"stock-watcher/internal/experiment"
	"stock-watcher/internal/market"
	"testing"
	"time"
)

func snapshotFixture(t *testing.T) (string, string) {
	t.Helper()
	c := config.Defaults()
	c.Calendar.ValidFrom, c.Calendar.ValidThrough, c.Calendar.Holidays = "2026-01-01", "2026-12-31", nil
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

func TestOptimizationRequiresExplicitSplit(t *testing.T) {
	path, through := snapshotFixture(t)
	var stdout, stderr bytes.Buffer
	base := []string{"-snapshot", path, "-from", "2026-01-01", "-through", through}
	if err := run(context.Background(), append(base, "-optimize"), &stdout, &stderr); err == nil {
		t.Fatal("optimization accepted missing training boundary")
	}
	if err := run(context.Background(), append(base, "-train-through", "2026-03-01"), &stdout, &stderr); err == nil {
		t.Fatal("descriptive mode silently accepted optimization option")
	}
	if stdout.Len() != 0 {
		t.Fatal("invalid grid emitted partial output")
	}
}

func TestContextComparisonCLIAndFailures(t *testing.T) {
	path, through := snapshotFixture(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s experiment.Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	c := bottomstudy.MarketContext{Version: 1, StockSnapshotID: s.ID, Basis: "current_only", ObservedAt: "2026-10-16T00:00:00Z", From: "2026-01-01", Through: through}
	for _, code := range []string{"sh000001", "sz399001", "sz399006", "BK1"} {
		kind := "market"
		if code == "BK1" {
			kind = "sector"
		}
		c.Indices = append(c.Indices, bottomstudy.ContextIndex{Code: code, Name: code, Kind: kind, Source: "fixture", Bars: s.Adjusted[0].Bars})
	}
	c.Memberships = []bottomstudy.Membership{{StockCode: s.Stocks[0].Code, SectorCode: "BK1", SectorName: "BK1", ObservedAt: c.ObservedAt}}
	contextPath := filepath.Join(t.TempDir(), "context.json")
	contextJSON, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contextPath, contextJSON, 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"-snapshot", path, "-from", "2026-01-01", "-through", through, "-compare-context", contextPath}
	args := append(append([]string(nil), base...), "-train-through", "2026-03-31")
	var a, b, stderr bytes.Buffer
	for _, out := range []*bytes.Buffer{&a, &b} {
		if err := run(context.Background(), args, out, &stderr); err != nil {
			t.Fatal(err)
		}
	}
	var got struct {
		ContextSHA string `json:"context_file_sha256"`
		StockSHA   string `json:"stock_snapshot_file_sha256"`
		bottomstudy.ComparisonReport
	}
	if json.Unmarshal(a.Bytes(), &got) != nil || a.String() != b.String() || got.Engine != bottomstudy.ComparisonEngine || len(got.Pairs) != 220 || len(got.ContextSHA) != 64 || len(got.StockSHA) != 64 {
		t.Fatal("invalid comparison result or unstable output")
	}
	var speedOut bytes.Buffer
	speedArgs := append(append([]string(nil), args...), "-optimize-speed")
	if err := run(context.Background(), speedArgs, &speedOut, &stderr); err != nil {
		t.Fatal(err)
	}
	var speed bottomstudy.SpeedReport
	if json.Unmarshal(speedOut.Bytes(), &speed) != nil || speed.Engine != bottomstudy.SpeedEngine || len(speed.Pairs) != 240 || len(speed.Winners) != 0 {
		t.Fatal("invalid empty speed study")
	}
	for _, bad := range [][]string{
		{"-snapshot", path, "-from", "2026-01-01", "-through", through, "-optimize-speed"},
		{"-snapshot", path, "-from", "2026-01-01", "-through", through, "-archive-config", "missing.yaml"},
		append(append([]string(nil), speedArgs...), "-optimize"),
		append(append([]string(nil), base...), "-optimize-speed"),
	} {
		var out bytes.Buffer
		if err := run(context.Background(), bad, &out, &stderr); err == nil || out.Len() != 0 {
			t.Fatal("invalid speed configuration emitted success")
		}
	}
	for _, bad := range [][]string{base, append(append([]string(nil), args...), "-optimize")} {
		var out bytes.Buffer
		if err := run(context.Background(), bad, &out, &stderr); err == nil || out.Len() > 0 {
			t.Fatal("invalid options emitted success")
		}
	}
	for _, bad := range [][]byte{[]byte("{"), append(contextJSON, []byte(" {}")...), []byte(`{"schema_version":1,"unknown":true}`)} {
		if err := os.WriteFile(contextPath, bad, 0600); err != nil {
			t.Fatal(err)
		}
		for _, arguments := range [][]string{args, speedArgs} {
			var out bytes.Buffer
			if err := run(context.Background(), arguments, &out, &stderr); err == nil || out.Len() > 0 {
				t.Fatal("invalid context emitted success")
			}
		}
	}
	if err := os.Remove(contextPath); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), args, &out, &stderr); err == nil || out.Len() > 0 {
		t.Fatal("missing file emitted success")
	}
}
