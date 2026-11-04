package aris

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPAllowlistAndLifecycle(t *testing.T) {
	a, _ := New(t.TempDir(), "test-build", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, e := NewHandler(ctx, a, map[string]Bundle{"approved": bundle()})
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	server := httptest.NewServer(h)
	defer server.Close()
	request := func(method, path, body, origin string) *httptest.ResponseRecorder {
		r, e := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		r.Host = "127.0.0.1:9090"
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		response, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		data, e := io.ReadAll(response.Body)
		if e != nil {
			t.Fatal(e)
		}
		w.Code = response.StatusCode
		for k, vs := range response.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.Body.Write(data)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cache header")
		}
		return w
	}
	if w := request("POST", "/v1/runs", `{"schema_version":1,"bundle":"../../secret"}`, ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/v1/runs", `{"schema_version":1,"bundle":"approved"}`, "https://evil.invalid"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/v1/runs", `{"schema_version":1,"bundle":"approved","command":"sh"}`, ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/v1/runs", strings.Repeat(" ", 1<<20)+`{}`, ""); w.Code != 400 {
		t.Fatal("oversized request accepted", w.Code)
	}
	if w := request("POST", "/v1/runs", `{"schema_version":1,"bundle":"approved"}`, ""); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		w := request("GET", "/v1/runs/test", "", "")
		if w.Code == 200 && strings.Contains(w.Body.String(), `"status":"needs_human"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run not finished %s", w.Body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w := request("GET", "/v1/runs/test/report", "", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/v1/runs/test/resume", `{}`, ""); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/v1/runs", `{"schema_version":1,"bundle":"approved"}`, ""); w.Code != 202 {
		t.Fatal("idempotency", w.Code)
	}
}
