package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"stock-watcher/internal/config"
	"strings"
	"testing"
	"time"
)

const response = `{"code":0,"data":{"sz000001":{"qfqday":[["2026-10-13","10","11","12","9","100"],["2026-10-14","11","12","13","10","200"]],"qt":{"sz000001":["51","平安银行"]}}}}`

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
