package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func TestZoneSummarySkipsEmptyZones(t *testing.T) {
	zones := []strategy.TrappedZone{
		{Label: "浅度", Low: 10, High: 10.5, SharePct: 60},
		{Label: "中度", SharePct: 0},
		{Label: "深度", Low: 12, High: 13, SharePct: 40},
	}
	if got := zoneSummary(zones); got != "浅度60%/10.00-10.50,深度40%/12.00-13.00" {
		t.Fatalf("zoneSummary = %q", got)
	}
	if got := zoneName(""); got != "无上方套牢" {
		t.Fatalf("empty zone name = %q", got)
	}
}

func TestBuildMetricsPoolRejectsUnknownSource(t *testing.T) {
	if _, closePool, err := buildMetricsPool("unknown", 1, time.Second); err == nil {
		closePool()
		t.Fatal("unknown metric source was accepted")
	}
}

func TestWriteJSONAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "result.json")
	if err := writeJSONAtomic(path, map[string]int{"count": 3}); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]int
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded["count"] != 3 {
		t.Fatalf("decoded result = %+v, %v", decoded, err)
	}
	if strings.Contains(string(payload), "token") {
		t.Fatal("unexpected credential-like field in result")
	}
}
