package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBaoStockTradingCalendarProtocol(t *testing.T) {
	requests := 0
	address := baoTestServer(t, func(kind string, parts []string) (string, string) {
		if kind == "00" {
			return "01", "0\x01success\x01login\x01anonymous\x0120261019"
		}
		requests++
		if kind != "33" || len(parts) != 6 || parts[0] != "query_trade_dates" ||
			parts[1] != "anonymous" || parts[2] != "1" || parts[3] != "2000" {
			t.Errorf("unexpected calendar request: %s %v", kind, parts)
			return "close", ""
		}
		records := [][]string{
			{"2026-01-01", "0"},
			{"2026-01-02", "1"},
			{"2026-01-03", "0"},
			{"2026-01-04", "0"},
			{"2026-01-05", "1"},
		}
		data, _ := json.Marshal(map[string]any{"record": records})
		return "34", strings.Join([]string{
			"0", "success", parts[0], parts[1], parts[2], parts[3], string(data),
			parts[4], parts[5], "calendar_date,is_trading_day",
		}, baoSeparator) + "\n"
	})
	client := NewBaoStockClient(time.Second, 0)
	client.Address = address
	defer client.Close()
	days, err := client.TradingDays(context.Background(), "2026-01-01", "2026-01-05")
	if err != nil || requests != 1 || strings.Join(days, ",") != "2026-01-02,2026-01-05" {
		t.Fatalf("wrong calendar: calls=%d days=%v err=%v", requests, days, err)
	}
}

func TestBaoStockTradingCalendarRejectsIncompleteWindow(t *testing.T) {
	address := baoTestServer(t, func(kind string, parts []string) (string, string) {
		if kind == "00" {
			return "01", "0\x01success\x01login\x01anonymous\x0120261019"
		}
		data, _ := json.Marshal(map[string]any{"record": [][]string{{"2026-01-01", "0"}, {"2026-01-02", "1"}}})
		return "34", strings.Join([]string{
			"0", "success", parts[0], parts[1], parts[2], parts[3], string(data),
			parts[4], parts[5], "calendar_date,is_trading_day",
		}, baoSeparator) + "\n"
	})
	client := NewBaoStockClient(time.Second, 0)
	client.Address = address
	defer client.Close()
	if _, err := client.TradingDays(context.Background(), "2026-01-01", "2026-01-05"); err == nil {
		t.Fatal("truncated BaoStock calendar accepted")
	}
}

func TestBaoStockCalendarAcceptsSpacesAfterFieldDelimiter(t *testing.T) {
	address := baoTestServer(t, func(kind string, parts []string) (string, string) {
		if kind == "00" {
			return "01", "0\x01success\x01login\x01anonymous"
		}
		data, _ := json.Marshal(map[string]any{"record": [][]string{{"2026-10-20", "1"}}})
		return "34", strings.Join([]string{
			"0", "success", parts[0], parts[1], parts[2], parts[3], string(data),
			parts[4], parts[5], "calendar_date, is_trading_day",
		}, baoSeparator) + "\n"
	})
	client := NewBaoStockClient(time.Second, 0)
	client.Address = address
	defer client.Close()

	for name, fetch := range map[string]func(context.Context, string, string) ([]string, error){
		"FetchTradingDays": client.FetchTradingDays,
		"TradingDays":      client.TradingDays,
	} {
		t.Run(name, func(t *testing.T) {
			days, err := fetch(context.Background(), "2026-10-20", "2026-10-20")
			if err != nil || strings.Join(days, ",") != "2026-10-20" {
				t.Fatalf("days=%v err=%v", days, err)
			}
		})
	}
}

func TestTushareTradingCalendarProtocol(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			API    string            `json:"api_name"`
			Token  string            `json:"token"`
			Params map[string]string `json:"params"`
			Fields string            `json:"fields"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.API != "trade_cal" || request.Token != "test-token" ||
			request.Params["exchange"] != "SSE" || request.Params["start_date"] != "20260101" ||
			request.Params["end_date"] != "20260105" || request.Fields != "cal_date,is_open" {
			t.Error("invalid Tushare calendar request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"fields": []string{"is_open", "cal_date"},
				"items": [][]any{
					{"0", "20260101"},
					{"1", "20260102"},
					{"0", "20260103"},
					{"0", "20260104"},
					{"1", "20260105"},
				},
			},
		})
	}))
	defer server.Close()
	client := NewTushareClient(time.Second, 0, "test-token")
	client.Endpoint = server.URL
	defer client.Close()
	days, err := client.TradingDays(context.Background(), "2026-01-01", "2026-01-05")
	if err != nil || calls != 1 || strings.Join(days, ",") != "2026-01-02,2026-01-05" {
		t.Fatalf("wrong calendar: calls=%d days=%v err=%v", calls, days, err)
	}
}

func TestTushareTradingCalendarRejectsIncompleteAndMissingFields(t *testing.T) {
	for _, mode := range []string{"incomplete", "missing_field"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fields := []string{"cal_date", "is_open"}
				items := [][]any{{"20260101", "0"}, {"20260102", "1"}}
				if mode == "missing_field" {
					fields = []string{"cal_date"}
					items = [][]any{{"20260101"}, {"20260102"}, {"20260103"}, {"20260104"}, {"20260105"}}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"fields": fields, "items": items}})
			}))
			defer server.Close()
			client := NewTushareClient(time.Second, 0, "test-token")
			client.Endpoint = server.URL
			defer client.Close()
			_, err := client.TradingDays(context.Background(), "2026-01-01", "2026-01-05")
			if err == nil || strings.Contains(err.Error(), "test-token") {
				t.Fatalf("invalid Tushare calendar accepted or leaked token: %v", err)
			}
		})
	}
}
