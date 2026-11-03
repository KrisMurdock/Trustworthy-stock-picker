package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func TestHKMinuteCLIFlagsRejectAmbiguity(t *testing.T) {
	for _, args := range [][]string{
		{"sync-hk-minutes", "-from", "2026-10-30"},
		{"sync-hk-minutes", "-through", "2026-10-30"},
		{"sync-hk-minutes", "-as-of", "2026-10-30"},
		{"sync-hk-minutes", "-full"},
		{"sync-hk-minutes", "-timeout", "0s"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("accepted invalid flags %v", args)
		}
	}
}

func TestHKMinuteAndValuationCLIFlagsRemainIndependent(t *testing.T) {
	useBuiltinConfig(t, t.TempDir(), "sz000001")
	t.Setenv("STOCK_WATCHER_WECHAT_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_HK_CONNECT_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_HK_CONNECT_PUSH_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_HK_INTRADAY_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_LOW_VALUATION_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_LOW_VALUATION_PUSH_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_VALUE_TRAPPED_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_VALUE_TRAPPED_PUSH_ENABLED", "false")
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"scan-low-valuation", "-as-of", "2026-10-30", "-workers", "2", "-timeout", "2h"}, "low_valuation is disabled"},
		{[]string{"scan-low-valuation", "-timeout", "30m"}, "sync-all, scan-low-valuation, scan-value-trapped or sync-hk-connect timeout must be between 1h and 72h"},
		{[]string{"scan-value-trapped", "-as-of", "2026-10-30", "-workers", "2", "-timeout", "2h"}, "value_trapped is disabled"},
		{[]string{"scan-value-trapped", "-timeout", "30m"}, "sync-all, scan-low-valuation, scan-value-trapped or sync-hk-connect timeout must be between 1h and 72h"},
		{[]string{"scan-value-trapped", "-from", "2026-10-30"}, "from requires the experiment, sync-hk-minutes, backfill-minutes, sync-index-minutes or sync-korea-context command"},
		{[]string{"sync", "-workers", "2"}, "workers requires the backfill-minutes, sync-all, scan-low-valuation, scan-value-trapped, sync-hk-connect or sync-hk-minutes command"},
		{[]string{"sync-hk-minutes", "-from", "2026-10-30", "-through", "2026-10-30", "-codes", "hk00700", "-workers", "2", "-timeout", "10s"}, "HK minute synchronization is disabled"},
		{[]string{"sync-hk-minutes", "-timeout", "61m"}, "sync-hk-minutes timeout must be greater than zero and at most 1h"},
	} {
		if err := run(test.args); err == nil || err.Error() != test.want {
			t.Fatalf("run(%v) = %v, want %q", test.args, err, test.want)
		}
	}
}

func TestHKMinuteCLIRealProviderFlowAndFailurePreservation(t *testing.T) {
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sina":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case "/universe":
			_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"total": 1, "diff": []any{map[string]any{"f12": "00700", "f14": "腾讯控股"}}}})
		case "/appstock/app/fqkline/get":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"hkHSI": map[string]any{"day": [][]string{{"2026-10-30", "100", "101", "102", "99", "1000"}}}}})
		case "/minutes":
			if r.URL.Query().Get("secid") != "116.00700" || r.URL.Query().Get("klt") != "5" {
				t.Error("invalid upstream minute query")
			}
			if mode.Load() == 1 {
				http.Error(w, "denied", http.StatusForbidden)
				return
			}
			if mode.Load() == 2 {
				<-r.Context().Done()
				return
			}
			rows := []string{}
			for _, session := range [][2]int{{570, 720}, {780, 960}} {
				for minute := session[0] + 5; minute <= session[1]; minute += 5 {
					rows = append(rows, fmt.Sprintf("2026-10-30 %02d:%02d,10,11,12,9,100,1000,1,2,3,4", minute/60, minute%60))
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"code": "00700", "market": 116, "name": "腾讯控股", "klines": rows}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	useBuiltinConfig(t, root, "sz000001")
	t.Setenv("STOCK_WATCHER_HK_INTRADAY_ENABLED", "true")
	t.Setenv("STOCK_WATCHER_HK_CONNECT_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_HK_CONNECT_PUSH_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_MARKET_REQUEST_INTERVAL", "350ms")
	t.Setenv("STOCK_WATCHER_REQUEST_TIMEOUT", "1s")
	t.Setenv("STOCK_WATCHER_MARKET_HK_UNIVERSE_ENDPOINT", server.URL+"/sina")
	t.Setenv("STOCK_WATCHER_MARKET_HK_UNIVERSE_FALLBACK", server.URL+"/universe")
	t.Setenv("STOCK_WATCHER_MARKET_HK_ENDPOINT", server.URL)
	t.Setenv("STOCK_WATCHER_MARKET_HK_ENDPOINT_FALLBACK", server.URL)
	t.Setenv("STOCK_WATCHER_MARKET_EASTMONEY_ENDPOINT", server.URL+"/minutes")
	t.Setenv("STOCK_WATCHER_LOG_DIR", filepath.Join(t.TempDir(), "logs"))
	args := []string{"sync-hk-minutes", "-from", "2026-10-30", "-through", "2026-10-30", "-codes", "hk00700", "-workers", "1", "-timeout", "10s"}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int32{1, 2} {
		mode.Store(status)
		if err := run(args); err == nil {
			t.Fatal("provider failure was reported as success")
		}
		view, err := storage.OpenHKMarketView(root, false)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := view.LoadHKMinutes(context.Background(), "hk00700", 5, config.DefaultHKIntraday())
		view.Close()
		if err != nil || len(snapshot.Bars) != 66 || snapshot.Sync.Status != "failed" {
			t.Fatalf("provider failure lost data: %+v %v", snapshot.Sync, err)
		}
	}
	mode.Store(0)
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	invalid := append([]string(nil), args...)
	for index := range invalid {
		if invalid[index] == "hk00700" {
			invalid[index] = ","
		}
	}
	if err := run(invalid); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("empty code filter accepted: %v", err)
	}
	if _, err := time.Parse("2006-01-02", "2026-10-30"); err != nil {
		t.Fatal(err)
	}
}
