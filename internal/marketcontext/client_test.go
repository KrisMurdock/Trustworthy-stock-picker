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

func TestDiscoverAndFetchMarketContext(t *testing.T) {
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
			if r.URL.Query().Get("secid") != "90.BK1036" {
				t.Errorf("unexpected secid %s", r.URL.Query().Get("secid"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{
				"code": "BK1036", "market": 90, "name": "半导体",
				"klines": []string{
					"2026-10-16,100.0,101.0,102.0,99.0,1000,200000,1.0",
					"2026-10-19,101.0,103.0,104.0,100.0,1200,240000,1.98",
					"2026-10-20,103.0,105.0,106.0,102.0,1300,260000,1.94",
				},
			}})
		case "/tencent":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"sh000001": map[string]any{
					"day": [][]any{
						{"2026-10-16", "3700", "3710", "3720", "3690", "1000"},
						{"2026-10-19", "3710", "3730", "3740", "3700", "1200"},
						{"2026-10-20", "3730", "3750", "3760", "3720", "1300"},
					},
					"qt": map[string][]any{"sh000001": {"", "上证指数"}},
				},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := config.DefaultMarketContext()
	cfg.EastmoneyListEndpoint = server.URL + "/list"
	cfg.EastmoneyKlineEndpoint = server.URL + "/kline"
	cfg.TencentEndpoint = server.URL + "/tencent"
	client := NewClient(cfg, 2*time.Second)
	client.Pacer = &pacer{interval: time.Millisecond}
	defer client.Close()

	boards, err := client.DiscoverBoards(context.Background())
	if err != nil || len(boards) != 2 || boards[0].Category != "industry" || boards[1].Category != "concept" {
		t.Fatalf("discover boards: %v %+v", err, boards)
	}
	bars, name, err := client.FetchBoard(context.Background(), boards[0], "2026-10-19", "")
	if err != nil || name != "半导体" || len(bars) != 2 || bars[1].Close != 103 || bars[1].Date != "2026-10-19" {
		t.Fatalf("board bars: %v %q %+v", err, name, bars)
	}
	if klineCalls.Load() != 1 {
		t.Fatalf("kline calls = %d", klineCalls.Load())
	}
	indexBars, name, err := client.FetchIndex(context.Background(), "sh000001", "2026-10-19")
	if err != nil || name != "上证指数" || len(indexBars) != 2 || indexBars[1].Close != 3730 {
		t.Fatalf("index bars: %v %q %+v", err, name, indexBars)
	}
}

func TestContextParsersRejectInvalidRows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{
			"code": "BK1036", "market": 90, "name": "半导体",
			"klines": []string{"2026-10-19,101.0,103.0,100.0,100.0,1200,240000,1.98"},
		}})
	}))
	defer server.Close()
	cfg := config.DefaultMarketContext()
	cfg.EastmoneyListEndpoint = server.URL
	cfg.EastmoneyKlineEndpoint = server.URL + "/invalid"
	cfg.TencentEndpoint = server.URL
	client := NewClient(cfg, time.Second)
	client.Pacer = &pacer{interval: time.Millisecond}
	defer client.Close()
	if _, _, err := client.FetchBoard(context.Background(), Board{Code: "BK1036", Name: "半导体"}, "2026-10-19", ""); err == nil {
		t.Fatal("invalid OHLC row was accepted")
	}
}

func TestBuildSnapshotAndSummary(t *testing.T) {
	at := time.Date(2026, 10, 19, 19, 0, 0, 0, time.UTC)
	instruments := []storage.ContextInstrument{
		{Code: "sh000001", Name: "上证指数", Kind: "index", Category: "index", Source: "tencent", UpdatedAt: at},
		{Code: "BK1036", Name: "半导体", Kind: "board", Category: "industry,concept", Source: "eastmoney", UpdatedAt: at},
		{Code: "BK1137", Name: "存储芯片", Kind: "board", Category: "concept", Source: "eastmoney", UpdatedAt: at},
	}
	bars := map[string][]storage.ContextBar{"sh000001": {}, "BK1036": {}, "BK1137": {}}
	for i, close := range []float64{10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30} {
		date := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02")
		bars["BK1036"] = append(bars["BK1036"], storage.ContextBar{Code: "BK1036", Date: date, Open: close - .5, Close: close, High: close + 1, Low: close - 1, Volume: 100})
	}
	bars["BK1137"] = []storage.ContextBar{{Code: "BK1137", Date: "2026-10-16", Open: 30, Close: 31, High: 32, Low: 29, Volume: 100}}
	bars["sh000001"] = []storage.ContextBar{{Code: "sh000001", Date: "2026-10-19", Open: 3700, Close: 3730, High: 3740, Low: 3690, Volume: 100}}
	snapshot := BuildSnapshot("2026-10-19", instruments, bars, storage.ContextSyncStatus{AsOf: "2026-10-19", Status: "complete", FinishedAt: at})
	if snapshot.Total != 3 || len(snapshot.Indices) != 1 || len(snapshot.Boards) != 2 || snapshot.Summary.Industry != 1 || snapshot.Summary.Concept != 2 {
		t.Fatalf("snapshot summary: %+v", snapshot)
	}
	if snapshot.Boards[0].Code != "BK1036" || snapshot.Boards[0].MA5 == 0 || snapshot.Boards[0].Stale {
		t.Fatalf("board projection: %+v", snapshot.Boards[0])
	}
	if snapshot.Boards[1].Code != "BK1137" || !snapshot.Boards[1].Stale || snapshot.Complete != 2 {
		t.Fatalf("stale projection: %+v", snapshot.Boards[1])
	}
}
