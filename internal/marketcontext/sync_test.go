package marketcontext

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func TestForDateDiscoversAndSynchronizesEveryBoard(t *testing.T) {
	var klineCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list":
			fs := r.URL.Query().Get("fs")
			code, name := "BK1036", "半导体"
			if strings.Contains(fs, "t:3") {
				code, name = "BK1137", "存储芯片"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"total": 1, "diff": []map[string]string{{"f12": code, "f14": name}}}})
		case "/kline":
			klineCalls.Add(1)
			code := strings.TrimPrefix(r.URL.Query().Get("secid"), "90.")
			_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{
				"code": code, "market": 90, "name": map[string]string{"BK1036": "半导体", "BK1137": "存储芯片"}[code],
				"klines": []string{"2026-10-16,100.0,101.0,102.0,99.0,1000,200000,1.0", "2026-10-19,101.0,103.0,104.0,100.0,1200,240000,1.98"},
			}})
		case "/tencent":
			param := r.URL.Query().Get("param")
			code := strings.Split(param, ",")[0]
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				code: map[string]any{
					"day": [][]any{{"2026-10-16", "100", "101", "102", "99", "1000"}, {"2026-10-19", "101", "103", "104", "100", "1200"}},
					"qt":  map[string][]any{code: {"", "测试指数"}},
				},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.MarketContext = config.DefaultMarketContext()
	cfg.MarketContext.Workers = 2
	cfg.MarketContext.EastmoneyListEndpoint = server.URL + "/list"
	cfg.MarketContext.EastmoneyKlineEndpoint = server.URL + "/kline"
	cfg.MarketContext.TencentEndpoint = server.URL + "/tencent"
	db, err := storage.InitContext(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := NewClient(cfg.MarketContext, 2*time.Second)
	client.Pacer = &pacer{interval: time.Millisecond}
	defer client.Close()
	syncer := NewSyncer(cfg, db, client, nil)
	result, err := syncer.ForDate(context.Background(), "2026-10-19")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "complete" || result.Total != 7 || result.Succeeded != 7 || result.Boards != 2 || result.Indices != 5 || klineCalls.Load() != 2 {
		t.Fatalf("unexpected sync result %+v calls=%d", result, klineCalls.Load())
	}
	instruments, err := db.LoadInstruments(context.Background())
	if err != nil || len(instruments) != 7 {
		t.Fatalf("instruments: %v %+v", err, instruments)
	}
	last, err := db.LastBarDates(context.Background())
	if err != nil || len(last) != 7 || last["BK1036"] != "2026-10-19" || last["sh000001"] != "2026-10-19" {
		t.Fatalf("last dates: %v %+v", err, last)
	}
	status, err := db.LoadSyncStatus(context.Background())
	if err != nil || status.Status != "complete" || status.AsOf != "2026-10-19" {
		t.Fatalf("sync status: %v %+v", err, status)
	}

	result, err = syncer.ForDate(context.Background(), "2026-10-19")
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 7 || result.Succeeded != 7 || klineCalls.Load() != 2 {
		t.Fatalf("second sync did not resume: %+v calls=%d", result, klineCalls.Load())
	}
}

func TestForDateReportsPartialBoardFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list":
			fs := r.URL.Query().Get("fs")
			code, name := "BK1036", "半导体"
			if strings.Contains(fs, "t:3") {
				code, name = "BK1137", "存储芯片"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"total": 1, "diff": []map[string]string{{"f12": code, "f14": name}}}})
		case "/kline":
			if strings.Contains(r.URL.Query().Get("secid"), "BK1137") {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"code": "BK1036", "market": 90, "name": "半导体", "klines": []string{"2026-10-19,101.0,103.0,104.0,100.0,1200,240000,1.98"}}})
		case "/tencent":
			param := r.URL.Query().Get("param")
			code := strings.Split(param, ",")[0]
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{code: map[string]any{"day": [][]any{{"2026-10-19", "101", "103", "104", "100", "1200"}}, "qt": map[string][]any{code: {"", "测试指数"}}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.MarketContext = config.DefaultMarketContext()
	cfg.MarketContext.Workers = 2
	cfg.MarketContext.EastmoneyListEndpoint = server.URL + "/list"
	cfg.MarketContext.EastmoneyKlineEndpoint = server.URL + "/kline"
	cfg.MarketContext.TencentEndpoint = server.URL + "/tencent"
	db, err := storage.InitContext(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := NewClient(cfg.MarketContext, 2*time.Second)
	client.Pacer = &pacer{interval: time.Millisecond}
	defer client.Close()
	result, err := NewSyncer(cfg, db, client, nil).ForDate(context.Background(), "2026-10-19")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "partial" || result.Failed != 1 || len(result.Errors) != 1 || result.Errors[0].Code != "BK1137" {
		t.Fatalf("partial result: %+v", result)
	}
	status, err := db.LoadSyncStatus(context.Background())
	if err != nil || status.Status != "partial" || status.Failed != 1 {
		t.Fatalf("partial status: %v %+v", err, status)
	}
}
