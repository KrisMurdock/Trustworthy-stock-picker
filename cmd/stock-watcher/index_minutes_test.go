package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func TestSyncIndexMinutesWritesContextMinuteCache(t *testing.T) {
	fiveMinuteRows := make([]map[string]string, 0, 48)
	for _, start := range []int{570, 780} {
		for minute := start + 5; minute <= start+120; minute += 5 {
			fiveMinuteRows = append(fiveMinuteRows, map[string]string{
				"day":  fmt.Sprintf("2026-10-20 %02d:%02d:00", minute/60, minute%60),
				"open": "100", "high": "101", "low": "99", "close": "100.5", "volume": "1000",
			})
		}
	}
	thirtyMinuteRows := make([]map[string]string, 0, 8)
	for _, minute := range []int{600, 630, 660, 690, 810, 840, 870, 900} {
		thirtyMinuteRows = append(thirtyMinuteRows, map[string]string{
			"day":  fmt.Sprintf("2026-10-20 %02d:%02d:00", minute/60, minute%60),
			"open": "100", "high": "101", "low": "99", "close": "100.5", "volume": "1000",
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") == "" {
			http.Error(w, "symbol required", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("scale") == "30" {
			_ = json.NewEncoder(w).Encode(thirtyMinuteRows)
			return
		}
		_ = json.NewEncoder(w).Encode(fiveMinuteRows)
	}))
	defer server.Close()

	data := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	useBuiltinConfig(t, data, "sz000001")
	t.Setenv("STOCK_WATCHER_MARKET_SINA_DAILY_ENDPOINT", server.URL)
	out, err := captureStdout(t, func() error {
		return run([]string{"sync-index-minutes", "-as-of", "2026-10-20", "-from", "2026-10-20"})
	})
	if err != nil {
		t.Fatalf("sync-index-minutes: %v", err)
	}
	var result struct {
		Status  string `json:"status"`
		Results []struct {
			Code         string `json:"code"`
			Bars         int    `json:"bars"`
			CoverageDays int    `json:"coverage_days"`
			SessionBars  int    `json:"session_bars"`
			SessionDays  int    `json:"session_days"`
		} `json:"results"`
	}
	out = strings.TrimSpace(out)
	if index := strings.LastIndex(out, "\n"); index >= 0 {
		out = strings.TrimSpace(out[index+1:])
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid sync output: %v %s", err, out)
	}
	if result.Status != "ok" || len(result.Results) != 2 || result.Results[0].Bars != 48 || result.Results[0].CoverageDays != 1 || result.Results[0].SessionBars != 8 || result.Results[0].SessionDays != 1 {
		t.Fatalf("unexpected sync output: %s", out)
	}
	db, err := storage.OpenContextView(data, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	loaded, err := db.LoadMinutesThrough(context.Background(), []string{"sh000001", "sz399006"}, "2026-10-20")
	if err != nil || len(loaded["sh000001"]) != 48 || len(loaded["sz399006"]) != 48 {
		t.Fatalf("load synced minutes: %v %+v", err, loaded)
	}
	sessions, err := db.LoadSessionMinutesThrough(context.Background(), []string{"sh000001", "sz399006"}, "2026-10-20")
	if err != nil || len(sessions["sh000001"]) != 8 || len(sessions["sz399006"]) != 8 {
		t.Fatalf("load synced session minutes: %v %+v", err, sessions)
	}
}
