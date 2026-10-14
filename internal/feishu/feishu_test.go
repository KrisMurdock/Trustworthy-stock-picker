package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"stock-watcher/internal/selection"
	"strconv"
	"testing"
	"time"
)

func TestOfficialSignatureAlgorithm(t *testing.T) {
	// Fixture computed independently with HMAC-SHA256(key=timestamp+newline+secret, empty message).
	if got := Sign(1599360473, "test-secret"); got != "wSds2BzzFIIGf/WrhUO+NI1q/9j+FRJd3JNHKAq0NZY=" {
		t.Fatalf("signature %s", got)
	}
}
func TestDeliveryAcknowledgementAndPermissions(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
		ok, retry  bool
	}{{"success", `{"code":0}`, 200, true, false}, {"legacy", `{"StatusCode":0}`, 200, true, false}, {"unsigned_result", `{}`, 200, false, false}, {"bad_signature", `{"code":19021}`, 200, false, false}, {"permission", `{}`, 403, false, false}, {"limit", `{"code":11232}`, 200, false, true}, {"server", `{}`, 503, false, true}} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p map[string]any
				json.NewDecoder(r.Body).Decode(&p)
				ts, _ := strconv.ParseInt(p["timestamp"].(string), 10, 64)
				if p["sign"] != Sign(ts, "test-secret") {
					t.Error("incorrect signature payload")
				}
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c := NewClient(srv.URL, "test-secret", time.Second)
			defer c.Close()
			err := c.Send(context.Background(), selection.Report{AsOf: "2026-10-13", Target: "2026-10-14"})
			if (err == nil) != tt.ok {
				t.Fatalf("error=%v", err)
			}
			if err != nil {
				var f *Failure
				if !errors.As(err, &f) || f.Retryable() != tt.retry {
					t.Fatalf("retry mismatch %v", err)
				}
			}
		})
	}
}
