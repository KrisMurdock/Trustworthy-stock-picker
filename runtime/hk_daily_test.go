package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestRunSyncsHKDailyIndependentlyWithLocalProviders(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/sina":
			http.Error(w, "fixture unavailable", http.StatusForbidden)
		case "/universe":
			_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{
				"total": 1, "diff": []any{map[string]string{"f12": "00700", "f14": "腾讯控股"}},
			}})
		case "/appstock/app/fqkline/get", "/appstock/app/hkfqkline/get":
			parts := strings.Split(request.URL.Query().Get("param"), ",")
			if len(parts) != 6 || (parts[0] != "hkHSI" && parts[0] != "hk00700") {
				http.Error(w, "invalid fixture request", http.StatusBadRequest)
				return
			}
			from, fromErr := time.Parse("2006-01-02", parts[2])
			through, throughErr := time.Parse("2006-01-02", parts[3])
			if fromErr != nil || throughErr != nil {
				http.Error(w, "invalid fixture range", http.StatusBadRequest)
				return
			}
			var rows [][]string
			for day := through; !day.Before(from) && len(rows) < 10; day = day.AddDate(0, 0, -1) {
				if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
					row := []string{day.Format("2006-01-02"), "10", "11", "12", "9", "1000"}
					rows = append([][]string{row}, rows...)
				}
			}
			field := "day"
			if parts[5] == "qfq" {
				field = "qfqday"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				parts[0]: map[string]any{field: rows},
			}})
		default:
			http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
		}
	}))
	defer provider.Close()

	configuration := runtimeTestConfig(t)
	configuration.HKConnect.SyncEnabled = true
	t.Setenv("STOCK_WATCHER_HK_SYNC_ENABLED", "true")
	configuration.HKConnect.Enabled = false
	configuration.HKConnect.PushEnabled = false
	configuration.Predictor.Enabled = false
	configuration.Predictor.PushEnabled = false
	configuration.Market.Providers = []string{"tencent"}
	configuration.Market.RequestInterval = "350ms"
	configuration.Market.TencentEndpoint = provider.URL + "/a-stock"
	configuration.Market.HKEndpoint = provider.URL
	configuration.Market.HKEndpointFallback = provider.URL
	configuration.Market.HKUniverseEndpoint = provider.URL + "/sina"
	configuration.Market.HKUniverseFallback = provider.URL + "/universe"
	configuration.Timeout = "1s"
	rt, err := New(Options{Config: &configuration})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	now := time.Now().In(market.Shanghai)
	beforeSchedule := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, market.Shanghai)
	rt.hkDaily.Now = func() time.Time { return beforeSchedule }
	date := beforeSchedule.AddDate(0, 0, -1)
	for date.Weekday() == time.Saturday || date.Weekday() == time.Sunday {
		date = date.AddDate(0, 0, -1)
	}
	asOf := date.Format("2006-01-02")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()
	defer cancel()
	deadline := time.Now().Add(20 * time.Second)
	var status string
	var rawBars, adjustedBars int
	for time.Now().Before(deadline) {
		database, openErr := sql.Open("sqlite", "file:"+filepath.Join(configuration.DataDir, "hk-market.db")+"?mode=ro")
		if openErr == nil {
			_ = database.QueryRow(`SELECT status FROM sync_run WHERE as_of=? ORDER BY started_at DESC LIMIT 1`, asOf).Scan(&status)
			_ = database.QueryRow(`SELECT count(*) FROM daily_bar WHERE code='hk00700' AND adjust='none'`).Scan(&rawBars)
			_ = database.QueryRow(`SELECT count(*) FROM daily_bar WHERE code='hk00700' AND adjust='qfq'`).Scan(&adjustedBars)
			_ = database.Close()
		}
		if status == "complete" && rawBars > 0 && adjustedBars > 0 {
			break
		}
		select {
		case runErr := <-done:
			t.Fatalf("runtime stopped before HK synchronization: %v", runErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
	if status != "complete" || rawBars == 0 || adjustedBars == 0 {
		t.Fatalf("HK sync = status %q, raw %d, qfq %d", status, rawBars, adjustedBars)
	}
	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("runtime shutdown returned %v", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime did not stop")
	}
}
