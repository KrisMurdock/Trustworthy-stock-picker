package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func lastJSONLine(t *testing.T, output string) string {
	t.Helper()
	output = strings.TrimSpace(output)
	if index := strings.LastIndex(output, "\n"); index >= 0 {
		output = strings.TrimSpace(output[index+1:])
	}
	return output
}

func TestSyncHangSengContextWritesDailyHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"hkHSI":{"qfqday":[["2026-10-19","25800.1","25920.2","26000.3","25700.4","1000"],["2026-10-20","25910.2","26010.4","26100.5","25800.6","1200"]]}}}`))
	}))
	defer server.Close()
	data := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	useBuiltinConfig(t, data, "sz000001")
	t.Setenv("STOCK_WATCHER_CONTEXT_HANG_SENG_ENDPOINT", server.URL)
	out, err := captureStdout(t, func() error {
		return run([]string{"sync-hang-seng-context", "-as-of", "2026-10-20"})
	})
	if err != nil {
		t.Fatalf("sync-hang-seng-context: %v", err)
	}
	var result struct {
		Status string `json:"status"`
		Bars   int    `json:"bars"`
		Last   string `json:"last"`
	}
	if err := json.Unmarshal([]byte(lastJSONLine(t, out)), &result); err != nil {
		t.Fatalf("invalid output: %v %s", err, out)
	}
	if result.Status != "ok" || result.Bars != 2 || result.Last != "2026-10-20" {
		t.Fatalf("unexpected output: %+v", result)
	}
	db, err := storage.OpenContextView(data, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bars, err := db.LoadHangSengBarsThrough(context.Background(), "2026-10-20")
	if err != nil || len(bars) != 2 || bars[1].Close != 26010.4 {
		t.Fatalf("load hang seng: %v %+v", err, bars)
	}
}

func TestSyncKoreaMinutesWritesForwardCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/minute")
		if code != market.KoreaKOSPI && code != market.KoreaKOSDAQ {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`[{"localDateTime":"20261020090000","currentPrice":2600.1,"openPrice":2599.5,"highPrice":2601.2,"lowPrice":2598.8,"accumulatedTradingVolume":100},{"localDateTime":"20261020090100","currentPrice":2602.2,"openPrice":2600.1,"highPrice":2603.4,"lowPrice":2599.9,"accumulatedTradingVolume":120}]`))
	}))
	defer server.Close()
	data := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	useBuiltinConfig(t, data, "sz000001")
	t.Setenv("STOCK_WATCHER_CONTEXT_KOREA_MINUTE_ENDPOINT", server.URL)
	out, err := captureStdout(t, func() error {
		return run([]string{"sync-korea-minutes", "-as-of", "2026-10-20"})
	})
	if err != nil {
		t.Fatalf("sync-korea-minutes: %v", err)
	}
	var result struct {
		Status  string `json:"status"`
		Results []struct {
			Code string `json:"code"`
			Bars int    `json:"bars"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(lastJSONLine(t, out)), &result); err != nil {
		t.Fatalf("invalid output: %v %s", err, out)
	}
	if result.Status != "ok" || len(result.Results) != 2 || result.Results[0].Bars != 2 {
		t.Fatalf("unexpected output: %+v", result)
	}
	db, err := storage.OpenContextView(data, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bars, err := db.LoadKoreaMinutesThrough(context.Background(), []string{market.KoreaKOSPI}, "2026-10-20")
	if err != nil || len(bars[market.KoreaKOSPI]) != 2 || bars[market.KoreaKOSPI][1].Close != 2602.2 {
		t.Fatalf("load korea minutes: %v %+v", err, bars)
	}
}
