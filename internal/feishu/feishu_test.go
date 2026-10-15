package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"stock-watcher/internal/notify"
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

func TestStructuredCardRendersFieldsAndEventID(t *testing.T) {
	payload := Card(notify.Message{
		Version: 2,
		EventID: "event-v2",
		Title:   "title",
		Sections: []notify.Section{
			{Heading: "摘要", Fields: []notify.Field{{Label: "日期", Value: "2026-10-13"}, {Label: "状态", Value: "有推荐"}}},
			{Heading: "第 1 名 · sz000001 测试", Fields: []notify.Field{{Label: "机会分", Value: "82.5"}, {Label: "风险", Value: "上影线较长"}}},
		},
	})
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	card, ok := envelope["card"].(map[string]any)
	if !ok {
		t.Fatalf("card missing from payload: %s", data)
	}
	elements, ok := card["elements"].([]any)
	if !ok {
		t.Fatalf("elements missing from card: %s", data)
	}
	fieldCount := 0
	foundEventID := false
	for _, rawElement := range elements {
		element, ok := rawElement.(map[string]any)
		if !ok {
			continue
		}
		fields, _ := element["fields"].([]any)
		for _, rawField := range fields {
			field, _ := rawField.(map[string]any)
			text, _ := field["text"].(map[string]any)
			if field["is_short"] != true || text["tag"] != "lark_md" {
				t.Fatalf("field is not rendered as a short lark markdown field: %+v", field)
			}
			fieldCount++
		}
		notes, _ := element["elements"].([]any)
		for _, rawNote := range notes {
			note, _ := rawNote.(map[string]any)
			if note["tag"] == "plain_text" && strings.Contains(note["content"].(string), "event-v2") {
				foundEventID = true
			}
		}
	}
	if fieldCount != 4 || !foundEventID {
		t.Fatalf("structured card missing fields or event id: fields=%d event=%v payload=%s", fieldCount, foundEventID, data)
	}
}

func TestVersionOneCardKeepsLegacyMarkdownRendering(t *testing.T) {
	payload := Card(notify.Message{
		Version:  1,
		EventID:  "event-v1",
		Title:    "title",
		Sections: []notify.Section{{Heading: "heading", Lines: []string{"first", "second", "事件 ID：event-v1"}}},
	})
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"lark_md", "heading", "first", "second", "event-v1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("legacy card missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "\"fields\"") {
		t.Fatalf("version one card changed layout: %s", text)
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

func TestOversizedStructuredCardDropsOptionalSectionsAndPreservesSummary(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		received, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("{\"code\":0}"))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", time.Second)
	defer c.Close()
	err := c.Send(context.Background(), notify.Message{
		Version: 2,
		EventID: "event-structured",
		Title:   "title",
		Sections: []notify.Section{
			{Heading: "摘要", Fields: []notify.Field{{Label: "状态", Value: "保留摘要"}}},
			{Heading: "首选股票", Optional: true, Fields: []notify.Field{{Label: "名称", Value: "保留候选"}}},
			{Heading: "长尾股票", Optional: true, Fields: []notify.Field{{Label: "详情", Value: strings.Repeat("长", 30000)}}},
		},
	})
	if err != nil {
		t.Fatalf("oversized structured card was not trimmed: %v", err)
	}
	if len(received) == 0 || len(received) > maxPayloadBytes {
		t.Fatalf("unexpected structured payload size %d", len(received))
	}
	text := string(received)
	for _, want := range []string{"保留摘要", "保留候选", "event-structured"} {
		if !strings.Contains(text, want) {
			t.Fatalf("trimmed card lost %q: %s", want, text)
		}
	}
	if strings.Contains(text, "长尾股票") || strings.Contains(text, strings.Repeat("长", 100)) {
		t.Fatalf("trimmed card retained oversized optional section: size=%d", len(received))
	}
}

func TestOversizedMandatoryStructuredCardFailsBeforeRequest(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte("{\"code\":0}"))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", time.Second)
	defer c.Close()
	err := c.Send(context.Background(), notify.Message{
		Version: 2,
		EventID: "core-event",
		Title:   "title",
		Sections: []notify.Section{{
			Heading: "摘要",
			Fields:  []notify.Field{{Label: "必要字段", Value: strings.Repeat("x", maxPayloadBytes)}},
		}},
	})
	var failure *notify.DeliveryError
	if !errors.As(err, &failure) || failure.Kind != notify.Permanent || failure.Reason != "payload_limit" || calls != 0 {
		t.Fatalf("mandatory oversized card should fail before request: err=%v calls=%d", err, calls)
	}
}

func TestWebhookTimeoutIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("{\"code\":0}"))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", 10*time.Millisecond)
	defer c.Close()
	err := c.Send(context.Background(), notify.Message{
		Version:  2,
		EventID:  "timeout-event",
		Sections: []notify.Section{{Fields: []notify.Field{{Label: "状态", Value: "测试"}}}},
	})
	var failure *notify.DeliveryError
	if !errors.As(err, &failure) || failure.Kind != notify.Retryable {
		t.Fatalf("timeout should be retryable, got %v", err)
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
