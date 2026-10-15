package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"stock-watcher/internal/config"
)

var providerStock = config.Stock{Code: "sh688981", Name: "中芯国际"}

func TestTushareAdjustmentsAndFields(t *testing.T) {
	for _, adjust := range []string{"none", "qfq", "hfq"} {
		t.Run(adjust, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req struct {
					API    string            `json:"api_name"`
					Token  string            `json:"token"`
					Params map[string]string `json:"params"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil || req.Token != "test-token" || req.Params["ts_code"] != "688981.SH" || r.Method != "POST" || r.URL.RawQuery != "" {
					t.Error("invalid REST request")
				}
				fields := []string{"vol", "close", "trade_date", "open", "ts_code", "low", "high"}
				items := [][]any{{200, 12, "20261013", 11, "688981.SH", 10, 13}, {100, 10, "20261012", 9, "688981.SH", 8, 11}}
				if req.API == "adj_factor" {
					fields = []string{"trade_date", "ts_code", "adj_factor"}
					items = [][]any{{"20261012", "688981.SH", 2}, {"20261013", "688981.SH", 4}}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"fields": fields, "items": items}})
			}))
			defer srv.Close()
			c := NewTushareClient(time.Second, 0, "test-token")
			c.Endpoint = srv.URL
			defer c.Close()
			h, err := c.Fetch(context.Background(), providerStock, "2026-10-12", "2026-10-13", adjust)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]float64{"none": 10, "qfq": 5, "hfq": 20}[adjust]
			if h.Source != "tushare" || len(h.Bars) != 2 || h.Bars[0].Close != want || h.Bars[0].Volume != 100 || ProviderVolumeShares(h.Source, h.Code, 100) != 10000 {
				t.Fatalf("invalid adjusted history: %+v", h)
			}
			if (adjust == "none" && calls != 1) || (adjust != "none" && calls != 2) {
				t.Fatalf("unexpected calls: %d", calls)
			}
		})
	}
}

func TestTushareRejectsPermissionMissingFactorAndNull(t *testing.T) {
	for _, mode := range []string{"permission", "invalid_token", "rate", "missing_factor", "null_price", "wrong_code", "duplicate_date"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				_ = json.NewDecoder(r.Body).Decode(&req)
				if mode == "permission" || mode == "invalid_token" || mode == "rate" {
					code := 2002
					if mode == "invalid_token" {
						code = 40101
					}
					if mode == "rate" {
						code = -2001
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": "test-token secret"})
					return
				}
				fields := []string{"ts_code", "trade_date", "open", "high", "low", "close", "vol"}
				row := []any{"688981.SH", "20261013", 10, 11, 9, 10, 100}
				if mode == "null_price" {
					row[2] = nil
				}
				if mode == "wrong_code" {
					row[0] = "000001.SZ"
				}
				rows := [][]any{row}
				if mode == "duplicate_date" {
					rows = append(rows, row)
				}
				if req["api_name"] == "adj_factor" {
					fields = []string{"ts_code", "trade_date", "adj_factor"}
					rows = nil
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"fields": fields, "items": rows}})
			}))
			defer srv.Close()
			c := NewTushareClient(time.Second, 0, "test-token")
			c.Endpoint = srv.URL
			defer c.Close()
			adjust := "none"
			if mode == "missing_factor" {
				adjust = "qfq"
			}
			_, err := c.Fetch(context.Background(), providerStock, "2026-10-12", "2026-10-13", adjust)
			if err == nil || strings.Contains(err.Error(), "test-token") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("accepted invalid data or leaked credential: %v", err)
			}
			if mode == "rate" && IsIsolatable(err) {
				t.Fatal("rate limit must remain retryable")
			}
			if (mode == "permission" || mode == "invalid_token") && !IsIsolatable(err) {
				t.Fatal("permission error must be permanent")
			}
		})
	}
}

func TestTushareYearChunksKeepOneAdjustmentAnchor(t *testing.T) {
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			API    string            `json:"api_name"`
			Params map[string]string `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		start, end := req.Params["start_date"], req.Params["end_date"]
		seen[req.API+":"+end] = true
		date, factor := "20251231", 2.0
		if start > date {
			date = "20260102"
			factor = 4
		}
		fields := []string{"ts_code", "trade_date", "open", "high", "low", "close", "vol"}
		rows := [][]any{{"688981.SH", date, 10, 11, 9, 10, 100}}
		if req.API == "adj_factor" {
			fields = []string{"ts_code", "trade_date", "adj_factor"}
			rows = [][]any{{"688981.SH", date, factor}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"fields": fields, "items": rows}})
	}))
	defer srv.Close()
	c := NewTushareClient(time.Second, 0, "test-token")
	c.Endpoint = srv.URL
	defer c.Close()
	h, err := c.Fetch(context.Background(), providerStock, "2025-12-31", "2026-01-02", "qfq")
	if err != nil || len(seen) != 4 || len(h.Bars) != 2 || h.Bars[0].Close != 5 || h.Bars[1].Close != 10 {
		t.Fatalf("incorrect cross-year adjustment: %+v %v", h, err)
	}
}

func TestEastmoneyProtocolAndValidation(t *testing.T) {
	for _, mode := range []string{"none", "qfq", "hfq", "null", "wrong_code", "wrong_market", "duplicate", "bad_price", "missing_rc"} {
		t.Run(mode, func(t *testing.T) {
			adjust := mode
			if adjust != "qfq" && adjust != "hfq" {
				adjust = "none"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get("secid") != "1.688981" || q.Get("klt") != "101" || q.Get("fqt") != map[string]string{"none": "0", "qfq": "1", "hfq": "2"}[adjust] || q.Get("end") != "20261013" {
					t.Error("bad Eastmoney parameters")
				}
				code, exchange := "688981", 1
				if mode == "wrong_code" {
					code = "000001"
				}
				if mode == "wrong_market" {
					exchange = 0
				}
				lines := []string{"2026-10-12,10,11,12,9,100,1000,1,1,1,1", "2026-10-13,11,12,13,10,200,2000,1,1,1,1"}
				if mode == "duplicate" {
					lines = append(lines, lines[0])
				}
				if mode == "bad_price" {
					lines[0] = strings.Replace(lines[0], ",10,", ",NaN,", 1)
				}
				response := map[string]any{"rc": 0, "data": map[string]any{"code": code, "market": exchange, "name": "中芯国际", "klines": lines}}
				if mode == "null" {
					response["data"] = nil
				}
				if mode == "missing_rc" {
					delete(response, "rc")
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer srv.Close()
			c := NewEastmoneyClient(time.Second, 0)
			c.Endpoint = srv.URL
			defer c.Close()
			h, err := c.Fetch(context.Background(), providerStock, "2026-10-12", "2026-10-13", adjust)
			ok := mode == "none" || mode == "qfq" || mode == "hfq"
			if (err == nil) != ok {
				t.Fatalf("unexpected result: %v", err)
			}
			if ok && (h.Source != "eastmoney" || h.Bars[1].Volume != 200 || ProviderVolumeShares(h.Source, h.Code, 200) != 20000) {
				t.Fatalf("bad history: %+v", h)
			}
		})
	}
}

func TestProviderHTTPTimeoutCancellationAndRedirect(t *testing.T) {
	for _, kind := range []string{"eastmoney", "tushare"} {
		for _, mode := range []string{"timeout", "cancel", "redirect", "403", "503"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				entered := make(chan struct{}, 1)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					entered <- struct{}{}
					switch mode {
					case "timeout", "cancel":
						select {
						case <-r.Context().Done():
						case <-time.After(time.Second):
						}
					case "redirect":
						http.Redirect(w, r, "http://127.0.0.1:1/credential-leak", 302)
					case "403":
						w.WriteHeader(403)
					case "503":
						w.WriteHeader(503)
					}
				}))
				defer srv.Close()
				var source Source
				if kind == "eastmoney" {
					c := NewEastmoneyClient(80*time.Millisecond, 0)
					c.Endpoint = srv.URL
					defer c.Close()
					source = c
				} else {
					c := NewTushareClient(80*time.Millisecond, 0, "test-token")
					c.Endpoint = srv.URL
					defer c.Close()
					source = c
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "cancel" {
					go func() { <-entered; cancel() }()
				}
				_, err := source.Fetch(ctx, providerStock, "2026-10-12", "2026-10-13", "none")
				if err == nil {
					t.Fatal("accepted failing HTTP call")
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
				if (mode == "timeout" || mode == "503") && IsIsolatable(err) {
					t.Fatalf("transient error isolated: %v", err)
				}
				if (mode == "403" || mode == "redirect") && !IsIsolatable(err) {
					t.Fatalf("permanent error retryable: %v", err)
				}
			})
		}
	}
}

type sourceFunc func(context.Context, config.Stock, string, string, string) (History, error)

func (f sourceFunc) Fetch(ctx context.Context, s config.Stock, a, b, c string) (History, error) {
	return f(ctx, s, a, b, c)
}

func TestFallbackFailureClassificationAndCancellation(t *testing.T) {
	for _, mode := range []string{"success", "invalid", "transient", "cancel", "storage"} {
		t.Run(mode, func(t *testing.T) {
			var second atomic.Int32
			first := sourceFunc(func(context.Context, config.Stock, string, string, string) (History, error) {
				switch mode {
				case "transient":
					return History{}, &Failure{Kind: "network"}
				case "cancel":
					return History{}, context.Canceled
				case "storage":
					return History{}, errors.New("shared disk unavailable")
				}
				return History{}, &DataError{Reason: "stale"}
			})
			last := sourceFunc(func(context.Context, config.Stock, string, string, string) (History, error) {
				second.Add(1)
				if mode != "success" {
					return History{}, &DataError{Reason: "empty"}
				}
				return History{Version: 1, Code: providerStock.Code, Name: providerStock.Name, Source: "eastmoney", Adjust: "none", DataField: "day", VolumeUnit: "provider_raw", AsOf: "2026-10-13", FetchedAt: time.Now(), Bars: []Bar{{Date: "2026-10-13", Open: 10, Close: 10, High: 11, Low: 9, Volume: 1}}}, nil
			})
			s := &Sources{Providers: []Provider{{"tencent", first}, {"eastmoney", last}}}
			h, err := s.Fetch(context.Background(), providerStock, "2026-10-13", "2026-10-13", "none")
			if mode == "success" {
				if err != nil || h.Source != "eastmoney" {
					t.Fatalf("fallback failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("failure lost")
			}
			if mode == "invalid" && !IsIsolatable(err) {
				t.Fatal("all-invalid must isolate")
			}
			if mode == "transient" && IsIsolatable(err) {
				t.Fatal("transient failure must abort publication")
			}
			if (mode == "cancel" || mode == "storage") && second.Load() != 0 {
				t.Fatal("retried after cancellation/storage failure")
			}
		})
	}
}

func TestProviderVolumeUnits(t *testing.T) {
	for _, code := range []string{"sz000001", "sh600000", "sh688981"} {
		for _, source := range []string{"tencent", "baostock", "tushare", "eastmoney"} {
			want := 10000.0
			if source == "baostock" || (source == "tencent" && code == "sh688981") {
				want = 100
			}
			if got := ProviderVolumeShares(source, code, 100); got != want {
				t.Fatal(fmt.Sprintf("%s %s = %v", source, code, got))
			}
		}
	}
}
