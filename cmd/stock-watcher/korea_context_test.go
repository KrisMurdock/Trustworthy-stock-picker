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

func TestSyncKoreaContextWritesDailyHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") == "" {
			http.Error(w, "symbol required", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(" [['날짜', '시가', '고가', '저가', '종가', '거래량', '외국인소진율'],\n[\"20261019\", 2600, 2620, 2590, 2610, 1000, 0],\n[\"20261020\", 2611, 2630, 2600, 2625, 1200, 0],\n]\n"))
	}))
	defer server.Close()
	data := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	useBuiltinConfig(t, data, "sz000001")
	t.Setenv("STOCK_WATCHER_CONTEXT_KOREA_ENDPOINT", server.URL)
	out, err := captureStdout(t, func() error {
		return run([]string{"sync-korea-context", "-as-of", "2026-10-20", "-from", "2026-10-01"})
	})
	if err != nil {
		t.Fatalf("sync-korea-context: %v", err)
	}
	var result struct {
		Status  string `json:"status"`
		Results []struct {
			Code string `json:"code"`
			Bars int    `json:"bars"`
		} `json:"results"`
	}
	out = strings.TrimSpace(out)
	if index := strings.LastIndex(out, "\n"); index >= 0 {
		out = strings.TrimSpace(out[index+1:])
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid output: %v %s", err, out)
	}
	if result.Status != "ok" || len(result.Results) != 2 || result.Results[0].Bars != 2 || result.Results[1].Bars != 2 {
		t.Fatalf("unexpected output: %s", out)
	}
	db, err := storage.OpenContextView(data, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	loaded, err := db.LoadKoreaBarsThrough(context.Background(), []string{market.KoreaKOSPI}, "2026-10-20")
	if err != nil || len(loaded[market.KoreaKOSPI]) != 2 || loaded[market.KoreaKOSPI][1].Close != 2625 {
		t.Fatalf("load korea history: %v %+v", err, loaded)
	}
}
