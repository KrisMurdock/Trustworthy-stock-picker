package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/predictor"
)

type fakePredictor struct {
	view       predictor.View
	history    []predictor.Prediction
	historyErr error
	viewErr    error
	pushes     []bool
}

func (f *fakePredictor) View(context.Context) (predictor.View, error) {
	return f.view, f.viewErr
}

func (f *fakePredictor) History(context.Context, string) ([]predictor.Prediction, error) {
	return f.history, f.historyErr
}

func (f *fakePredictor) SetPushEnabled(_ context.Context, enabled bool) error {
	f.pushes = append(f.pushes, enabled)
	f.view.PushEnabled = enabled
	return nil
}

func predictorRequest(t *testing.T, h http.Handler, method, path string, body any, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(payload))
	r.RemoteAddr = "127.0.0.1:43210"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://example.com")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-Stock-Watcher-Action", predictorConfigAction)
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func predictorHandler(t *testing.T, controller PredictorController) http.Handler {
	t.Helper()
	c := config.Defaults()
	c.Predictor.WriteMode = "loopback"
	return newHandlerWithPredictor(c, &testReader{}, nil, controller)
}

func TestPredictionsEndpointValidatesDateAndReturnsHistory(t *testing.T) {
	at := time.Date(2026, 10, 20, 9, 35, 0, 0, time.UTC)
	fake := &fakePredictor{history: []predictor.Prediction{{Code: "sz000001", Date: "2026-10-20", InputEnd: at, TargetEnd: at.Add(5 * time.Minute), ModelID: "m1", BasePrice: 10, HighPct: 0.01, LowPct: -0.01, ClosePct: 0.002}}}
	h := predictorHandler(t, fake)

	w := predictorRequest(t, h, http.MethodGet, "/api/predictions?date=2026-10-20", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Date        string                 `json:"date"`
		Predictions []predictor.Prediction `json:"predictions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Date != "2026-10-20" || len(body.Predictions) != 1 || body.Predictions[0].Code != "sz000001" {
		t.Fatalf("unexpected predictions body: %s", w.Body.String())
	}

	for _, path := range []string{
		"/api/predictions",
		"/api/predictions?date=2026-10-20&extra=1",
		"/api/predictions?date=2026-02-30",
		"/api/predictions?date=20-10-2026",
	} {
		if got := predictorRequest(t, h, http.MethodGet, path, nil, nil); got.Code != http.StatusBadRequest {
			t.Fatalf("%s accepted: %d", path, got.Code)
		}
	}
	if got := predictorRequest(t, h, http.MethodPost, "/api/predictions?date=2026-10-20", nil, nil); got.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST accepted: %d", got.Code)
	}
}

func TestPredictionsEndpointRequiresRunningPredictor(t *testing.T) {
	h := predictorHandler(t, nil)
	if w := predictorRequest(t, h, http.MethodGet, "/api/predictions?date=2026-10-20", nil, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status without predictor: %d", w.Code)
	}
	if w := predictorRequest(t, h, http.MethodGet, "/api/predictor", nil, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status without predictor: %d", w.Code)
	}
}

func TestPredictorViewAndPushToggleProtection(t *testing.T) {
	fake := &fakePredictor{view: predictor.View{Status: predictor.Status{Version: 1, Enabled: true, PushEnabled: true, ModelID: "m1"}}}
	h := predictorHandler(t, fake)

	w := predictorRequest(t, h, http.MethodGet, "/api/predictor", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected view status: %d %s", w.Code, w.Body.String())
	}
	var view predictor.View
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.WriteEnabled || view.WriteMode != "loopback" || !view.PushEnabled {
		t.Fatalf("unexpected view: %+v", view)
	}

	if got := predictorRequest(t, h, http.MethodPut, "/api/predictor/config", map[string]any{"push_enabled": false}, nil); got.Code != http.StatusOK {
		t.Fatalf("valid toggle rejected: %d %s", got.Code, got.Body.String())
	}
	if len(fake.pushes) != 1 || fake.pushes[0] {
		t.Fatalf("toggle not applied: %+v", fake.pushes)
	}

	cases := []struct {
		name   string
		mutate func(*http.Request)
		body   any
		status int
	}{
		{"wrong action", func(r *http.Request) { r.Header.Set("X-Stock-Watcher-Action", "llm-config") }, map[string]any{"push_enabled": true}, http.StatusForbidden},
		{"remote", func(r *http.Request) { r.RemoteAddr = "203.0.113.9:1234" }, map[string]any{"push_enabled": true}, http.StatusForbidden},
		{"cross site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, map[string]any{"push_enabled": true}, http.StatusForbidden},
		{"foreign origin", func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }, map[string]any{"push_enabled": true}, http.StatusForbidden},
		{"missing field", nil, map[string]any{}, http.StatusBadRequest},
		{"unknown field", nil, map[string]any{"push_enabled": true, "enabled": false}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if got := predictorRequest(t, h, http.MethodPut, "/api/predictor/config", tc.body, tc.mutate); got.Code != tc.status {
			t.Fatalf("%s: got %d want %d", tc.name, got.Code, tc.status)
		}
	}
	if len(fake.pushes) != 1 {
		t.Fatalf("rejected request changed state: %+v", fake.pushes)
	}
}

func TestPredictorWriteModeOffAndReadErrors(t *testing.T) {
	fake := &fakePredictor{view: predictor.View{Status: predictor.Status{Version: 1, Enabled: true}}}
	c := config.Defaults()
	c.Predictor.WriteMode = "off"
	h := newHandlerWithPredictor(c, &testReader{}, nil, fake)
	w := predictorRequest(t, h, http.MethodPut, "/api/predictor/config", map[string]any{"push_enabled": true}, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("off mode accepted write: %d", w.Code)
	}
	view := predictorRequest(t, h, http.MethodGet, "/api/predictor", nil, nil)
	var got predictor.View
	_ = json.Unmarshal(view.Body.Bytes(), &got)
	if got.WriteEnabled {
		t.Fatal("off mode reported writable")
	}

	fake.viewErr = errors.New("boom")
	if w := predictorRequest(t, h, http.MethodGet, "/api/predictor", nil, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("view error status: %d", w.Code)
	}
	fake.historyErr = errors.New("boom")
	if w := predictorRequest(t, h, http.MethodGet, "/api/predictions?date=2026-10-20", nil, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("history error status: %d", w.Code)
	}
}
