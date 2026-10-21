package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/dayrange"
)

type fakeDayRangeReader struct {
	status     dayrange.Status
	records    []dayrange.PredictionRecord
	evaluation dayrange.Evaluation
	err        error
	wait       bool
	seenDate   string
	seenCode   string
}

func (f *fakeDayRangeReader) result(ctx context.Context) error {
	if !f.wait {
		return f.err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeDayRangeReader) Status(ctx context.Context, _ string) (dayrange.Status, error) {
	if err := f.result(ctx); err != nil {
		return dayrange.Status{}, err
	}
	return f.status, f.err
}

func (f *fakeDayRangeReader) Predictions(ctx context.Context, date, code string) ([]dayrange.PredictionRecord, error) {
	f.seenDate, f.seenCode = date, code
	if err := f.result(ctx); err != nil {
		return nil, err
	}
	return f.records, f.err
}

func (f *fakeDayRangeReader) Evaluation(ctx context.Context, _ string) (dayrange.Evaluation, error) {
	if err := f.result(ctx); err != nil {
		return dayrange.Evaluation{}, err
	}
	return f.evaluation, f.err
}

func dayRangeHandler(fake *fakeDayRangeReader) http.Handler {
	c := config.Defaults()
	c.DayRange.Enabled = true
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试银行"}, {Code: "sh600519", Name: "测试消费"}}
	return newHandlerWithDayRange(c, &testReader{}, nil, nil, fake)
}

func TestDayRangeReadOnlyEndpoints(t *testing.T) {
	now := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	fake := &fakeDayRangeReader{
		status:     dayrange.Status{SchemaVersion: 1, Enabled: true, Available: true, Date: "2026-10-21", PredictionCount: 1, BaselineModelID: dayrange.BaselineModelID},
		records:    []dayrange.PredictionRecord{{Prediction: dayrange.Prediction{PredictionID: "p1", Code: "sz000001", TargetDate: "2026-10-21", Mode: dayrange.ModePremarket, AsOf: now, ModelID: dayrange.BaselineModelID, Source: "baseline"}}},
		evaluation: dayrange.Evaluation{ID: "e1", ModelID: "m1", BaselineID: dayrange.BaselineModelID, AsOf: "2026-10-20", Gate: dayrange.Gate{Passed: false, Reasons: []string{"insufficient_validation"}}},
	}
	h := dayRangeHandler(fake)
	w := request(t, h, http.MethodGet, "/api/day-range?date=2026-10-21&code=sz000001")
	if w.Code != http.StatusOK || fake.seenDate != "2026-10-21" || fake.seenCode != "sz000001" {
		t.Fatalf("day-range request failed: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		SchemaVersion   int                         `json:"schema_version"`
		Date            string                      `json:"date"`
		Available       bool                        `json:"available"`
		Predictions     []dayrange.PredictionRecord `json:"predictions"`
		StockNames      map[string]string           `json:"stock_names"`
		RemainingModel  string                      `json:"remaining_model"`
		RemainingReason string                      `json:"remaining_reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SchemaVersion != 1 || body.Date != "2026-10-21" || !body.Available || len(body.Predictions) != 1 || body.StockNames["sz000001"] != "测试银行" || body.RemainingModel != "unsupported" || body.RemainingReason != "phase_six_not_implemented" {
		t.Fatalf("unexpected day-range body: %s", w.Body.String())
	}
	if w := request(t, h, http.MethodGet, "/api/day-range/status"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"available":true`) {
		t.Fatalf("status failed: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, h, http.MethodGet, "/api/day-range/evaluation?date=2026-10-20"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"evaluation_id":"e1"`) {
		t.Fatalf("evaluation failed: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/day-range", "/assets/day-range.js", "/assets/day-range.css"} {
		w := request(t, h, http.MethodGet, path)
		if w.Code != http.StatusOK || w.Body.Len() == 0 {
			t.Fatalf("asset %s missing: %d", path, w.Code)
		}
		cache := w.Header().Get("Cache-Control")
		if path == "/day-range" && cache != "no-store" || path != "/day-range" && cache != "no-cache" {
			t.Fatalf("asset %s has unexpected cache policy %q", path, cache)
		}
	}
}

func TestDayRangeQueryAndMethodBoundaries(t *testing.T) {
	h := dayRangeHandler(&fakeDayRangeReader{status: dayrange.Status{Enabled: true, Available: true, Date: "2026-10-21"}})
	for _, path := range []string{
		"/api/day-range",
		"/api/day-range?date=2026-02-30",
		"/api/day-range?date=2026-10-21&code=sz999999",
		"/api/day-range?date=2026-10-21&code=sz000001&code=sh600519",
		"/api/day-range?date=2026-10-21&extra=1",
		"/api/day-range/status?date=2026-10-21",
		"/api/day-range/evaluation",
		"/api/day-range/evaluation?date=2026-10-21&extra=1",
	} {
		if w := request(t, h, http.MethodGet, path); w.Code != http.StatusBadRequest {
			t.Fatalf("%s accepted: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/day-range?date=2026-10-21", "/api/day-range/status", "/api/day-range/evaluation?date=2026-10-20"} {
		if w := request(t, h, http.MethodPost, path); w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s accepted: %d", path, w.Code)
		}
	}
}

func TestDayRangeUnavailableAndReadErrors(t *testing.T) {
	c := config.Defaults()
	c.DayRange.Enabled = true
	c.Stocks = []config.Stock{{Code: "sz000001"}}
	h := newHandlerWithDayRange(c, &testReader{}, nil, nil, nil)
	w := request(t, h, http.MethodGet, "/api/day-range?date=2026-10-21")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"available":false`) || !strings.Contains(w.Body.String(), `"predictions":[]`) {
		t.Fatalf("unavailable day-range should be an empty read model: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodGet, "/api/day-range/status")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":true`) || !strings.Contains(w.Body.String(), `dayrange_store_unavailable`) {
		t.Fatalf("unavailable status lost diagnostics: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, h, http.MethodGet, "/api/day-range/evaluation?date=2026-10-20"); w.Code != http.StatusNotFound {
		t.Fatalf("missing evaluation status: %d", w.Code)
	}

	failing := dayRangeHandler(&fakeDayRangeReader{err: errors.New("database busy")})
	if w := request(t, failing, http.MethodGet, "/api/day-range?date=2026-10-21"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("read error status: %d", w.Code)
	}
}

func TestDayRangeReadTimeouts(t *testing.T) {
	h := dayRangeHandler(&fakeDayRangeReader{wait: true})
	for _, path := range []string{"/api/day-range?date=2026-10-21", "/api/day-range/status", "/api/day-range/evaluation?date=2026-10-20"} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
		cancel()
		if w.Code != http.StatusGatewayTimeout || !strings.Contains(w.Body.String(), "day-range request timed out") {
			t.Fatalf("%s timeout status: %d %s", path, w.Code, w.Body.String())
		}
	}
}
