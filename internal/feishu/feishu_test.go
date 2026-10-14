package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"stock-watcher/internal/config"
	"stock-watcher/internal/notify"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/strategy"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOfficialSignatureAlgorithm(t *testing.T) {
	// Fixture computed independently with HMAC-SHA256(key=timestamp+newline+secret, empty message).
	if got := Sign(1599360473, "test-secret"); got != "wSds2BzzFIIGf/WrhUO+NI1q/9j+FRJd3JNHKAq0NZY=" {
		t.Fatalf("signature %s", got)
	}
}

func TestRankingCardReportsNetCostsWatchOnlyAndReferencePrices(t *testing.T) {
	r := selection.Report{Version: 2, AsOf: "2026-10-13", Target: "2026-10-14", Strategy: "short_horizon_v1", PoolSize: 104, Research: &strategy.Research{Parameters: config.DefaultStrategy()}, Ranking: []strategy.Candidate{{Rank: 1, Code: "sz000001", Name: "测试", Score: 40, Reasons: []string{"strong_close"}, Risks: []string{"insufficient_or_unprofitable_evidence"}, Plan: &strategy.Plan{EntryPrice: 10, Target25: 10.3, Target30: 10.35, StopPrice: 9.7}}}}
	data, err := json.Marshal(Card(r))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"净收益口径", "仅为观察排名", "0.0100%", "参考买入", "以实际买入", "不等于未来获利概率", "收盘接近日内高位", "历史样本或收益未达门槛"} {
		if !strings.Contains(string(data), text) {
			t.Fatalf("card missing %s", text)
		}
	}
	if strings.Contains(string(data), "分数 ≥ 0") {
		t.Fatal("legacy threshold in strategy card")
	}
}

func TestRankingCardShowsSelectedStocksOutsideWatchTopFive(t *testing.T) {
	r := selection.Report{Version: 2, SelectionEnabled: true, Research: &strategy.Research{Parameters: config.DefaultStrategy()}}
	for i := 0; i < 6; i++ {
		r.Ranking = append(r.Ranking, strategy.Candidate{Rank: i + 1, Code: "sz00000" + strconv.Itoa(i), Name: "测试", Score: float64(90 - i), Recommended: i == 5})
	}
	r.Selected = []selection.Candidate{{Code: r.Ranking[5].Code, Ranking: &r.Ranking[5]}}
	data, _ := json.Marshal(Card(r))
	if !strings.Contains(string(data), "sz000005") || !strings.Contains(string(data), "规则通过") {
		t.Fatal("watch-only leaders hid the selected stock")
	}
}
func TestDeliveryAcknowledgementAndPermissions(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
		ok, retry  bool
	}{{"success", `{"code":0}`, 200, true, false}, {"legacy", `{"StatusCode":0}`, 200, true, false}, {"unknown_result", `{}`, 200, false, true}, {"bad_signature", `{"code":19021}`, 200, false, false}, {"permission", `{}`, 403, false, false}, {"limit", `{"code":11232}`, 200, false, true}, {"server", `{}`, 503, false, true}} {
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
			err := c.Send(context.Background(), notify.Message{Version: 1, EventID: "event", Title: "title", Sections: []notify.Section{{Lines: []string{"line"}}}})
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

func TestResponseClassificationAndRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", time.Second)
	defer c.Close()
	err := c.Send(context.Background(), notify.Message{Version: 1, EventID: "event", Sections: []notify.Section{{Lines: []string{"line"}}}})
	var failure *notify.DeliveryError
	if !errors.As(err, &failure) || failure.Kind != notify.Retryable || failure.RetryAfter != 17*time.Second {
		t.Fatalf("unexpected 429 failure: %#v", err)
	}
}

func TestUnexpectedSuccessStatusIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", time.Second)
	defer c.Close()
	err := c.Send(context.Background(), notify.Message{Version: 1, EventID: "event", Sections: []notify.Section{{Lines: []string{"line"}}}})
	var failure *notify.DeliveryError
	if !errors.As(err, &failure) || failure.Kind != notify.Unknown || failure.Status != http.StatusAccepted {
		t.Fatalf("unexpected 202 failure: %#v", err)
	}
}

func TestOversizedCardIsTrimmedBeforeRequest(t *testing.T) {
	var calls int
	var received int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		received = len(data)
		w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", time.Second)
	defer c.Close()
	err := c.Send(context.Background(), notify.Message{
		Version:  1,
		EventID:  "event",
		Sections: []notify.Section{{Lines: []string{strings.Repeat("长", 30000), "事件 ID：event"}}},
	})
	if err != nil || calls != 1 || received > maxPayloadBytes {
		t.Fatalf("oversized payload was not trimmed: err=%v calls=%d bytes=%d", err, calls, received)
	}
}

func TestCardRendersMessageWithoutSelectionDependency(t *testing.T) {
	payload := Card(notify.Message{
		Version:  1,
		EventID:  "event-1",
		Title:    "title",
		Sections: []notify.Section{{Heading: "heading", Lines: []string{"first", "second"}}},
	})
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"\"title\":{\"content\":\"title\"", "heading", "first", "second"} {
		if !strings.Contains(text, want) {
			t.Fatalf("card does not contain %q: %s", want, text)
		}
	}
}
