package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const response = `{"code":0,"data":{"sz000001":{"qfqday":[["2026-10-13","10","11","12","9","100"],["2026-10-14","11","12","13","10","200"]],"qt":{"sz000001":["51","平安银行"]}}}}`

func TestCurrentSessionUsesLatestWindowAndFiltersFutureBars(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Query().Get("param"), ",")
		rows := [][]string{{"2026-10-13", "10", "10", "11", "9", "100"}, {"2026-10-14", "10", "10", "11", "9", "100"}}
		if parts[3] == "" {
			rows = append(rows, []string{"2026-10-15", "10", "10", "11", "9", "100"})
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"sz000001": map[string]any{"qfqday": rows}}})
	}))
	defer srv.Close()
	c := NewClient(time.Second)
	c.Endpoint = srv.URL
	c.Now = func() time.Time { return time.Date(2026, 10, 15, 16, 0, 0, 0, Shanghai) }
	defer c.Close()
	for _, end := range []string{"2026-10-15", "2026-10-14"} {
		h, err := c.FetchWindow(context.Background(), config.Stock{Code: "sz000001"}, "2026-10-01", end, "qfq", 2)
		if err != nil || len(h.Bars) != 2 || h.Bars[len(h.Bars)-1].Date != end {
			t.Fatalf("wrong final bar for %s: %+v %v", end, h.Bars, err)
		}
	}
}

func TestParserRejectsWrongAdjustmentAndIncomplete(t *testing.T) {
	b, name, err := Parse("sz000001", []byte(response), "2026-01-01", "2026-10-13", "qfq")
	if err != nil || len(b) != 1 || b[0].Close != 11 || name != "平安银行" {
		t.Fatalf("bars=%+v name=%s err=%v", b, name, err)
	}
	for _, body := range []string{`{"code":0,"data":{"sz000001":{"day":[]}}}`, `{"code":0,"data":{"sh600000":{}}}`, `{"code":1}`, `{}`, `{"code":0,"data":{"sz000001":{"qfqday":[["2026-10-13","NaN","11","12","9","100"]]}}}`} {
		if _, _, err := Parse("sz000001", []byte(body), "2026-01-01", "2026-10-13", "qfq"); err == nil {
			t.Fatal("accepted malformed data")
		}
	}
}
func TestHTTPFailureAndCancellation(t *testing.T) {
	for _, status := range []int{200, 401, 403, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("param") != "sz000001,day,2026-01-01,2026-10-13,640,qfq" {
					t.Error("wrong daily query")
				}
				w.WriteHeader(status)
				w.Write([]byte(response))
			}))
			defer srv.Close()
			c := NewClient(time.Second)
			c.Endpoint = srv.URL
			defer c.Close()
			h, err := c.Fetch(context.Background(), config.Stock{Code: "sz000001"}, "2026-01-01", "2026-10-13", "qfq")
			if status == 200 {
				if err != nil || len(h.Bars) != 1 {
					t.Fatal(err)
				}
				return
			}
			var failure *Failure
			if !errors.As(err, &failure) || failure.Retryable() != (status == 429 || status >= 500) {
				t.Fatalf("wrong failure %v", err)
			}
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	c := NewClient(20 * time.Millisecond)
	c.Endpoint = srv.URL
	defer c.Close()
	start := time.Now()
	_, err := c.Fetch(context.Background(), config.Stock{Code: "sz000001"}, "2026-01-01", "2026-10-13", "qfq")
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("timeout not enforced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.Fetch(ctx, config.Stock{Code: "sz000001"}, "2026-01-01", "2026-10-13", "qfq")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestTencentDayResponseForAdjustedRequest(t *testing.T) {
	body := strings.ReplaceAll(response, "qfqday", "day")
	bars, _, err := Parse("sz000001", []byte(body), "2026-01-01", "2026-10-13", "qfq")
	if err != nil || len(bars) != 1 || adjustmentField(map[string]json.RawMessage{"day": json.RawMessage(`[]`)}, "qfq") != "day" {
		t.Fatal("Tencent base-series response not supported")
	}
}

func TestHistoryValidationRejectsCorruptStoredMetadata(t *testing.T) {
	bars, name, err := Parse("sz000001", []byte(response), "2026-01-01", "2026-10-13", "qfq")
	if err != nil {
		t.Fatal(err)
	}
	h := History{Version: 1, Code: "sz000001", Name: name, Source: "tencent", Adjust: "qfq", DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: "2026-10-13", FetchedAt: time.Now(), Bars: bars}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
	h.VolumeUnit = ""
	if err := h.Validate(); err == nil {
		t.Fatal("missing volume unit accepted")
	}
	h.VolumeUnit = "provider_raw"
	h.Bars = append(h.Bars, h.Bars[0])
	if err := h.Validate(); err == nil {
		t.Fatal("duplicate persisted dates accepted")
	}
}

func TestTencentAppendedLiveBarIsFilteredIntoRequestedWindow(t *testing.T) {
	// Tencent may append the current session to the 640 historical rows requested.
	date, _ := time.Parse("2006-01-02", "2026-10-14")
	rows := [][]string{}
	for len(rows) < 641 {
		if date.Weekday() != time.Saturday && date.Weekday() != time.Sunday {
			rows = append(rows, []string{date.Format("2006-01-02"), "10", "10", "11", "9", "100"})
		}
		date = date.AddDate(0, 0, -1)
	}
	body, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"sz000001": map[string]any{"day": rows}}})
	bars, _, err := Parse("sz000001", body, "2025-10-14", "2026-10-13", "none")
	if err != nil || len(bars) == 0 || len(bars) > 640 || bars[len(bars)-1].Date != "2026-10-13" {
		t.Fatalf("appended current bar blocked or leaked into completed window: bars=%d err=%v", len(bars), err)
	}
	if _, _, err := Parse("sz000001", body, "2020-01-01", "2026-10-14", "none"); err == nil {
		t.Fatal("persisted 640-bar contract was exceeded")
	}
	rows = append(rows, []string{date.Format("2006-01-02"), "10", "10", "11", "9", "100"})
	body, _ = json.Marshal(map[string]any{"code": 0, "data": map[string]any{"sz000001": map[string]any{"day": rows}}})
	if _, _, err := Parse("sz000001", body, "2025-10-14", "2026-10-13", "none"); err == nil {
		t.Fatal("unbounded provider rows accepted")
	}
}

func TestFetchWindowUsesRequestedIncrementalCount(t *testing.T) {
	body := strings.ReplaceAll(response, "qfqday", "day")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("param"); got != "sz000001,day,2026-10-01,2026-10-13,5," {
			t.Fatalf("unexpected incremental query: %s", got)
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()
	c := NewClient(time.Second)
	c.Endpoint = srv.URL
	defer c.Close()
	_, err := c.FetchWindow(context.Background(), config.Stock{Code: "sz000001"}, "2026-10-01", "2026-10-13", "none", 5)
	if err != nil {
		t.Fatal(err)
	}
}

func TestFetchStopsAtKnownListingDateWhenProbeWindowStartsEarlier(t *testing.T) {
	const code = "sh601975"
	const listed = "2019-01-08"
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		parts := strings.Split(r.URL.Query().Get("param"), ",")
		if len(parts) != 6 || parts[2] != listed {
			t.Fatalf("probe start was not clamped to listing date: %q", r.URL.Query().Get("param"))
		}
		body := fmt.Sprintf(`{"code":0,"data":{%q:{"qfqday":[[%q,"10","10","11","9","100"]]}}}`, code, listed)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewClient(time.Second)
	c.Endpoint = srv.URL
	defer c.Close()
	h, err := c.Fetch(context.Background(), config.Stock{Code: code, ListedFrom: listed}, listed, "2026-10-13", "qfq")
	if err != nil || len(h.Bars) != 1 || h.Bars[0].Date != listed || calls.Load() != 1 {
		t.Fatalf("listing boundary was treated as truncation: %+v %v", h.Bars, err)
	}
}
