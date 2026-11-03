package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchStockBasicsFiltersNonAShareAndSorts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req tushareFundamentalsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.API != "stock_basic" || req.Fields != "ts_code,name,industry" || req.Params["list_status"] != "L" {
			t.Errorf("unexpected stock_basic request: %+v", req)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"fields": []string{"ts_code", "name", "industry"},
			"items": [][]any{
				{"600000.SH", "浦发银行", "银行"},
				{"000001.SZ", "平安银行", "银行"},
				{"830799.BJ", "艾融软件", "软件服务"},
				{"900901.SH", "云赛B股", "综合"},
			},
		}}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer srv.Close()
	c := NewTushareClient(time.Second, 0, "test-token")
	c.Endpoint = srv.URL
	defer c.Close()
	rows, err := c.FetchStockBasics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Code != "sh600000" || rows[1].Code != "sz000001" || rows[0].Industry != "银行" {
		t.Fatalf("stock basics = %+v", rows)
	}
}
