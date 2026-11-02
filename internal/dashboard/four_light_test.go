package dashboard

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func TestFourLightSummaryAndHistoryAreReadOnlyViews(t *testing.T) {
	now := time.Date(2026, 10, 13, 18, 40, 0, 0, time.UTC)
	first := storage.FourLightRecord{AsOf: "2026-10-13", Rule: strategy.FourLightRule, Code: "sz000001", Name: "测试银行",
		PreviousTradeDate: "2026-10-12", DataStatus: "ready", State: "all_red", Lifecycle: "first_all_red",
		Trend: "red", Energy: "red", MidTerm: "red", ShortTerm: "red", BarCount: 80, LatestBarDate: "2026-10-13",
		Source: "tencent", DataField: "qfqday", Reason: "fixture", ComputedAt: now, RunID: "run-1"}
	reader := &fakeFourLightReader{summary: storage.FourLightSummary{Run: storage.FourLightRun{ID: "run-1", AsOf: "2026-10-13",
		Rule: strategy.FourLightRule, StartedAt: now, FinishedAt: now, Status: "complete", Total: 4, Ready: 3, AllRed: 1,
		FirstAllRed: 1, Unknown: 1}, First: []storage.FourLightRecord{first}}, records: []storage.FourLightRecord{first}}
	h := newHandlerWithFourLight(config.Defaults(), nil, nil, nil, nil, reader)

	w := request(t, h, http.MethodGet, "/api/four-light?as_of=2026-10-13")
	if w.Code != http.StatusOK {
		t.Fatalf("summary response %d: %s", w.Code, w.Body)
	}
	var summary fourLightSummaryView
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Version != 1 || summary.Rule != strategy.FourLightRule || !summary.Approximation || summary.AlignmentStatus != "unverified" || summary.Run.ID != "run-1" || len(summary.First) != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if reader.summaryAsOf != "2026-10-13" || reader.summaryRule != strategy.FourLightRule {
		t.Fatalf("summary query = %q %q", reader.summaryAsOf, reader.summaryRule)
	}

	w = request(t, h, http.MethodGet, "/api/four-light/history?code=sz000001&from=2026-10-01&through=2026-10-13&rule=four_light_approx_v1")
	if w.Code != http.StatusOK {
		t.Fatalf("history response %d: %s", w.Code, w.Body)
	}
	var history fourLightHistoryView
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if history.Code != "sz000001" || len(history.Records) != 1 || history.Records[0].Lifecycle != "first_all_red" {
		t.Fatalf("history = %+v", history)
	}
	if reader.historyCode != "sz000001" || reader.historyFrom != "2026-10-01" || reader.historyThrough != "2026-10-13" {
		t.Fatalf("history query = %q %q %q", reader.historyCode, reader.historyFrom, reader.historyThrough)
	}
}

func TestFourLightValidationAndUnavailableStates(t *testing.T) {
	reader := &fakeFourLightReader{summaryErr: fs.ErrNotExist, historyErr: fs.ErrNotExist}
	h := newHandlerWithFourLight(config.Defaults(), nil, nil, nil, nil, reader)
	for _, query := range []string{"?as_of=", "?as_of=2026-02-30", "?as_of=2026-10-13&as_of=2026-10-14", "?date=2026-10-13", "?as_of=2026-10-13;x=1", "?as_of=2026-10-13&bad=%ZZ"} {
		if w := request(t, h, http.MethodGet, "/api/four-light"+query); w.Code != http.StatusBadRequest {
			t.Fatalf("summary query %q = %d %s", query, w.Code, w.Body)
		}
	}
	if w := request(t, h, http.MethodGet, "/api/four-light?as_of=2026-10-13"); w.Code != http.StatusNotFound {
		t.Fatalf("missing summary = %d %s", w.Code, w.Body)
	}
	for _, query := range []string{"", "?code=bad&from=2026-10-01&through=2026-10-13", "?code=sz000001&from=bad&through=2026-10-13", "?code=sz000001&from=2026-10-14&through=2026-10-13", "?code=sz000001&from=2020-01-01&through=2026-10-13", "?code=sz000001&from=2026-10-01&through=2026-10-13&rule=other", "?code=sz000001&from=2026-10-01&through=2026-10-13&date=2026-10-13"} {
		if w := request(t, h, http.MethodGet, "/api/four-light/history"+query); w.Code != http.StatusBadRequest {
			t.Fatalf("history query %q = %d %s", query, w.Code, w.Body)
		}
	}
	if w := request(t, h, http.MethodGet, "/api/four-light/history?code=sz000001&from=2026-10-01&through=2026-10-13"); w.Code != http.StatusNotFound {
		t.Fatalf("missing history = %d %s", w.Code, w.Body)
	}
	unavailable := newHandlerWithFourLight(config.Defaults(), nil, nil, nil, nil, nil)
	if w := request(t, unavailable, http.MethodGet, "/api/four-light"); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "unavailable") {
		t.Fatalf("unavailable summary = %d %s", w.Code, w.Body)
	}
	if w := request(t, unavailable, http.MethodGet, "/api/four-light/history?code=sz000001&from=2026-10-01&through=2026-10-13"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable history = %d %s", w.Code, w.Body)
	}
}

type fakeFourLightReader struct {
	summary        storage.FourLightSummary
	records        []storage.FourLightRecord
	summaryErr     error
	historyErr     error
	summaryAsOf    string
	summaryRule    string
	historyCode    string
	historyRule    string
	historyFrom    string
	historyThrough string
}

func (f *fakeFourLightReader) LatestFourLightSummary(_ context.Context, asOf, rule string) (storage.FourLightSummary, error) {
	f.summaryAsOf, f.summaryRule = asOf, rule
	return f.summary, f.summaryErr
}

func (f *fakeFourLightReader) FourLightHistory(_ context.Context, code, rule, from, through string) ([]storage.FourLightRecord, error) {
	f.historyCode, f.historyRule, f.historyFrom, f.historyThrough = code, rule, from, through
	return f.records, f.historyErr
}
