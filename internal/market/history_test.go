package market

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"stock-watcher/internal/config"
)

func historicalRows(start, end string) [][]string {
	first, _ := time.Parse("2006-01-02", start)
	last, _ := time.Parse("2006-01-02", end)
	var rows [][]string
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			rows = append(rows, []string{d.Format("2006-01-02"), "10", "10", "11", "9", "100"})
		}
	}
	return rows
}

func TestFetchRetainsTenYearsAcrossProviderPages(t *testing.T) {
	for _, adjust := range []string{"qfq", "hfq", "none"} {
		t.Run(adjust, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				parts := strings.Split(r.URL.Query().Get("param"), ",")
				count, _ := strconv.Atoi(parts[4])
				if count != 640 {
					t.Errorf("unbounded provider count: %d", count)
				}
				rows := historicalRows("2016-01-01", parts[3])
				if len(rows) > count {
					rows = rows[len(rows)-count:]
				}
				field := "day"
				if adjust != "none" {
					field = adjust + "day"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"sz000001": map[string]any{field: rows}}})
			}))
			defer srv.Close()
			client := NewClient(time.Second)
			client.Endpoint = srv.URL
			defer client.Close()
			// The start is a Sunday; retain the previous trading day as coverage evidence.
			history, err := client.Fetch(context.Background(), config.Stock{Code: "sz000001", Name: "测试"}, "2016-10-16", "2026-10-16", adjust)
			if err != nil {
				t.Fatal(err)
			}
			if calls < 4 || len(history.Bars) < 2500 || history.Bars[0].Date != "2016-10-14" || history.Bars[len(history.Bars)-1].Date != "2026-10-16" {
				t.Fatalf("incomplete decade: pages=%d bars=%d first=%s last=%s", calls, len(history.Bars), history.Bars[0].Date, history.Bars[len(history.Bars)-1].Date)
			}
			if err := history.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFetchRejectsBrokenHistoricalPages(t *testing.T) {
	for _, kind := range []string{"forbidden", "unavailable", "truncated", "overlap changed", "adjustment changed", "timeout", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				parts := strings.Split(r.URL.Query().Get("param"), ",")
				field := "qfqday"
				rows := historicalRows("2016-01-01", parts[3])
				if len(rows) > 640 {
					rows = rows[len(rows)-640:]
				}
				if calls > 1 {
					switch kind {
					case "forbidden":
						w.WriteHeader(403)
						return
					case "unavailable":
						w.WriteHeader(503)
						return
					case "truncated":
						rows = rows[len(rows)-1:]
					case "overlap changed":
						rows[len(rows)-1][2] = "10.5"
					case "adjustment changed":
						field = "day"
					case "timeout":
						<-r.Context().Done()
						return
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"sz000001": map[string]any{field: rows}}})
				if kind == "canceled" {
					cancel()
				}
			}))
			defer srv.Close()
			client := NewClient(100 * time.Millisecond)
			client.Endpoint = srv.URL
			defer client.Close()
			_, err := client.Fetch(ctx, config.Stock{Code: "sz000001", Name: "测试"}, "2016-10-13", "2026-10-13", "qfq")
			if err == nil {
				t.Fatal("partial history accepted")
			}
			if kind == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if kind == "forbidden" || kind == "unavailable" || kind == "timeout" {
				var failure *Failure
				if !errors.As(err, &failure) || failure.Retryable() != (kind != "forbidden") {
					t.Fatalf("lost failure classification: %v", err)
				}
			}
		})
	}
}

// Deterministically cancel after response headers, while reading the body.
type cancelBodyTransport struct {
	cancel context.CancelFunc
	closed bool
}

func (t *cancelBodyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: t, Header: make(http.Header)}, nil
}
func (t *cancelBodyTransport) Read([]byte) (int, error) { t.cancel(); return 0, context.Canceled }
func (t *cancelBodyTransport) Close() error             { t.closed = true; return nil }
func TestFetchWindowPreservesBodyCancellationAndClosesResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &cancelBodyTransport{cancel: cancel}
	client := NewClient(time.Second)
	client.HTTP.Transport = transport
	defer client.Close()
	_, err := client.FetchWindow(ctx, config.Stock{Code: "sz000001", Name: "测试"}, "2026-10-12", "2026-10-13", "qfq", 640)
	if !errors.Is(err, context.Canceled) || !transport.closed {
		t.Fatalf("cancel=%v body_closed=%v", err, transport.closed)
	}
}
