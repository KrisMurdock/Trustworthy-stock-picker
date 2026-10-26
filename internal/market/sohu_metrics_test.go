package market

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

func TestSohuDailyMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("code") != "cn_600000" || query.Get("start") != "20231026" || query.Get("end") != "20261026" ||
			query.Get("period") != "d" || query.Get("order") != "D" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`callback([{"status":0,"code":"cn_600000","hq":[["2026-10-26","9.48","9.62","0.09","0.94%","9.45","9.68","793760","75799.98","0.24%","58.00"],["2026-10-23","9.69","9.53","-0.09","-0.94%","9.52","9.73","0","0","0.00%"],["2023-10-26","6.93","6.96","0.03","0.43%","6.89","7.02","247395","17236.90","0.08%"],["2024-01-02","6.90","6.93","0.03","0.43%","6.89","7.02","247395","17236.90","-"]]}])`))
	}))
	defer server.Close()

	client := NewSohuMetricsClient(time.Second, 0)
	client.Endpoint = server.URL
	history, err := client.FetchDailyMetrics(context.Background(), config.Stock{Code: "sh600000"}, "2023-10-26", "2026-10-26")
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Metrics) != 2 || history.Source != "sohu" {
		t.Fatalf("unexpected history: %+v", history)
	}
	if history.Metrics[0].Date != "2023-10-26" || history.Metrics[1].Date != "2026-10-26" {
		t.Fatalf("metrics are not ascending: %+v", history.Metrics)
	}
	if history.Metrics[1].AmountCNY != 757999800 || history.Metrics[1].TurnoverRatePct != 0.24 {
		t.Fatalf("unexpected latest metric: %+v", history.Metrics[1])
	}
}

func TestSohuDailyMetricsRejectsMismatchedStock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`callback([{"status":0,"code":"cn_600519","hq":[["2026-10-26","1","1","0","0%","1","1","1","1","1%"]]}])`))
	}))
	defer server.Close()

	client := NewSohuMetricsClient(time.Second, 0)
	client.Endpoint = server.URL
	_, err := client.FetchDailyMetrics(context.Background(), config.Stock{Code: "sh600000"}, "2023-10-26", "2026-10-26")
	if err == nil {
		t.Fatal("mismatched stock was accepted")
	}
}
