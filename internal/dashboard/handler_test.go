package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/storage"
	"strings"
	"testing"
	"time"
)

type testReader struct {
	snapshot storage.DashboardSnapshot
	report   selection.Report
	err      error
	wait     bool
	deadline time.Duration
	date     string
}

func (s *testReader) LoadDashboard(ctx context.Context, _ []config.Stock, _ string) (storage.DashboardSnapshot, error) {
	if deadline, ok := ctx.Deadline(); ok {
		s.deadline = time.Until(deadline)
	}
	if s.wait {
		<-ctx.Done()
		return storage.DashboardSnapshot{}, ctx.Err()
	}
	return s.snapshot, s.err
}

func (s *testReader) LoadDashboardHistory(ctx context.Context, stock config.Stock, adjust string) (market.History, error) {
	_, err := s.LoadDashboard(ctx, nil, adjust)
	return market.History{Version: 1, Code: stock.Code, Adjust: adjust, Bars: []market.Bar{{Date: "2026-10-13", Close: 10}}}, err
}

func (s *testReader) LoadDashboardReport(_ context.Context, date string) (selection.Report, error) {
	s.date = date
	return s.report, s.err
}

func request(t *testing.T, handler http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestDashboardEmptyDatabaseAndReadOnlySurface(t *testing.T) {
	c := config.Defaults()
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试银行"}, {Code: "sh600519", Name: "测试消费"}}
	c.DataDir, c.Feishu.Webhook, c.Feishu.Secret = "/private/database", "https://secret.example/webhook", "private-signing-secret"
	h := NewHandler(c, nil, nil)
	for _, identity := range []string{"", "Bearer ordinary-user", "Bearer administrator"} {
		r := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
		r.Header.Set("Authorization", identity)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("public local dashboard rejected identity: %d %s", w.Code, w.Body)
		}
		var snapshot storage.DashboardSnapshot
		if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil || len(snapshot.Stocks) != 2 || snapshot.Summary.Missing != 2 || snapshot.Report != nil {
			t.Fatalf("empty dashboard lost pool: %+v %v", snapshot, err)
		}
		for _, sensitive := range []string{c.DataDir, c.Feishu.Webhook, c.Feishu.Secret, "Authorization", "delivery"} {
			if strings.Contains(w.Body.String(), sensitive) {
				t.Fatalf("private configuration leaked: %s", sensitive)
			}
		}
	}
	if w := request(t, h, http.MethodGet, "/api/reports?as_of=2026-10-13"); w.Code != http.StatusNotFound {
		t.Fatalf("missing report: %d", w.Code)
	}
}

func TestDashboardRoutingAndSecurityHeaders(t *testing.T) {
	h := NewHandler(config.Defaults(), nil, nil)
	for _, tc := range []struct {
		method, path, contentType, cache string
		status                           int
	}{
		{"GET", "/", "text/html", "no-store", 200},
		{"GET", "/calculator", "text/html", "no-store", 200},
		{"HEAD", "/calculator", "text/html", "no-store", 200},
		{"POST", "/calculator", "application/json", "no-store", 405},
		{"GET", "/assets/calculator.js", "text/javascript", "no-cache", 200},
		{"GET", "/assets/strategy-model.js", "text/javascript", "no-cache", 200},
		{"GET", "/assets/calculator.css", "text/css", "no-cache", 200},
		{"GET", "/assets/app.js", "text/javascript", "no-cache", 200},
		{"GET", "/assets/style.css", "text/css", "no-cache", 200},
		{"GET", "/healthz", "application/json", "no-store", 200},
		{"GET", "/api/dashboard", "application/json", "no-store", 200},
		{"HEAD", "/", "text/html", "no-store", 200},
		{"HEAD", "/api/dashboard", "application/json", "no-store", 200},
		{"POST", "/api/dashboard", "application/json", "no-store", 405},
		{"DELETE", "/healthz", "application/json", "no-store", 405},
		{"GET", "/config.yaml", "application/json", "no-store", 404},
		{"GET", "/state/delivery.json", "application/json", "no-store", 404},
		{"GET", "/assets/../config.yaml", "application/json", "no-store", 404},
		{"GET", "/assets/%2e%2e/config.yaml", "application/json", "no-store", 404},
		{"GET", "/assets/", "application/json", "no-store", 404},
		{"GET", "/assets/index.html", "application/json", "no-store", 404},
		{"GET", "/missing-route", "application/json", "no-store", 404},
		{"GET", "/api/dashboard?as_of=2026-10-13", "application/json", "no-store", 400},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w := request(t, h, tc.method, tc.path)
			if w.Code != tc.status || !strings.HasPrefix(w.Header().Get("Content-Type"), tc.contentType) || w.Header().Get("Cache-Control") != tc.cache {
				t.Fatalf("response %d, headers %+v, body %s", w.Code, w.Header(), w.Body)
			}
			if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") || strings.Contains(w.Header().Get("Content-Security-Policy"), "unsafe-inline") {
				t.Fatalf("missing security headers: %+v", w.Header())
			}
			if tc.method == "HEAD" && w.Body.Len() != 0 {
				t.Fatalf("HEAD returned a body: %s", w.Body)
			}
			if tc.status == 405 && w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("missing allowed methods")
			}
		})
	}
}

func TestHistoricalReportValidationAndVersions(t *testing.T) {
	reader := &testReader{}
	h := newHandler(config.Defaults(), reader, nil)
	for _, query := range []string{"", "?as_of=", "?as_of=2026-02-30", "?as_of=2026-1-01", "?as_of=2026-10-13&as_of=2026-10-14", "?as_of=2026-10-13&other=1", "?as_of=2026-10-13;private=value", "?as_of=2026-10-13&bad=%ZZ", "?as_of=../../private"} {
		if w := request(t, h, "GET", "/api/reports"+query); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid date accepted: %q, %d", query, w.Code)
		}
	}
	for _, version := range []int{1, 2} {
		reader.report = selection.Report{Version: version, ID: "persisted", AsOf: "2026-10-13", Adjust: "qfq", Selected: []selection.Candidate{{Code: "sh600519", Close: 1400}}}
		w := request(t, h, "GET", "/api/reports?as_of=2026-10-13")
		var report selection.Report
		if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || w.Code != 200 || report.Version != version || report.Selected[0].Close != 1400 || reader.date != report.AsOf {
			t.Fatalf("persisted v%d report changed: %s %v", version, w.Body, err)
		}
	}
	reader.err = fs.ErrNotExist
	if w := request(t, h, "GET", "/api/reports?as_of=2026-10-13"); w.Code != http.StatusNotFound {
		t.Fatalf("missing report: %d", w.Code)
	}
}

func TestDatabaseFailuresAreGenericAndRequestsAreBounded(t *testing.T) {
	reader := &testReader{err: errors.New("/private/market.db token=secret-value")}
	h := newHandler(config.Defaults(), reader, nil)
	for _, path := range []string{"/api/dashboard", "/api/reports?as_of=2026-10-13"} {
		w := request(t, h, "GET", path)
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"error"`) || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "secret-value") {
			t.Fatalf("database failure leaked details: %d %s", w.Code, w.Body)
		}
	}
	if reader.deadline <= 0 || reader.deadline > requestTimeout {
		t.Fatalf("request deadline not bounded: %s", reader.deadline)
	}
	reader.wait = true
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		if canceled {
			cancel()
		}
		r := httptest.NewRequest("GET", "/api/dashboard", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		cancel()
		want := http.StatusGatewayTimeout
		if canceled {
			want = http.StatusServiceUnavailable
		}
		if w.Code != want {
			t.Fatalf("canceled=%v: got %d want %d", canceled, w.Code, want)
		}
	}
	if w := request(t, h, "GET", "/healthz"); w.Code != http.StatusOK {
		t.Fatal("liveness depends on database availability")
	}
}

func TestHistoryReadOnlyPoolScopeAndErrors(t *testing.T) {
	c := config.Defaults()
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试银行"}}
	reader := &testReader{}
	h := newHandler(c, reader, nil)
	for _, query := range []string{"", "?code=", "?code=sz000001&code=sh600519", "?code=%ZZ", "?code=sz000001&adjust=", "?code=sz000001&adjust=none&adjust=qfq", "?code=sz000001&adjust=hfq", "?code=sz000001&other=none"} {
		if w := request(t, h, "GET", "/api/history"+query); w.Code != 400 {
			t.Fatalf("invalid query accepted: %s %d", query, w.Code)
		}
	}
	if w := request(t, h, "GET", "/api/history?code=sh600519"); w.Code != 404 {
		t.Fatalf("pool boundary: %d", w.Code)
	}
	for _, identity := range []string{"", "Bearer ordinary-user", "Bearer administrator"} {
		r := httptest.NewRequest("GET", "/api/history?code=sz000001", nil)
		r.Header.Set("Authorization", identity)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var history market.History
		if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil || w.Code != 200 || history.Code != "sz000001" || history.Bars[0].Close != 10 {
			t.Fatalf("history: %s %v", w.Body, err)
		}
	}
	if reader.deadline <= 0 || reader.deadline > requestTimeout {
		t.Fatal("unbounded history request")
	}
	if w := request(t, h, "POST", "/api/history?code=sz000001"); w.Code != 405 {
		t.Fatal("history allows mutation")
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{fs.ErrNotExist, 404}, {errors.New("private credentials"), 503}, {context.DeadlineExceeded, 504}} {
		reader.err = tc.err
		w := request(t, h, "GET", "/api/history?code=sz000001")
		if w.Code != tc.status || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("history failure: %d %s", w.Code, w.Body)
		}
	}
	reader.wait = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/history?code=sz000001", nil).WithContext(ctx))
	if w.Code != 504 {
		t.Fatalf("history cancellation: %d", w.Code)
	}
}

func TestHistoryPriceBasis(t *testing.T) {
	for _, configured := range []string{"qfq", "hfq", "none"} {
		c := config.Defaults()
		c.Daily.Adjust = configured
		c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试银行"}}
		h := newHandler(c, &testReader{}, nil)
		for _, basis := range []string{"", "qfq", "hfq", "none", "raw"} {
			query := "/api/history?code=sz000001"
			if basis != "" {
				query += "&adjust=" + basis
			}
			w := request(t, h, "GET", query)
			if basis != "" && basis != configured && basis != "none" {
				if w.Code != 400 {
					t.Fatalf("unsupported basis %s with %s: %d", basis, configured, w.Code)
				}
				continue
			}
			want := basis
			if want == "" {
				want = configured
			}
			var history market.History
			if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil || w.Code != 200 || history.Adjust != want {
				t.Fatalf("basis %s with %s: %d %s %v", basis, configured, w.Code, w.Body, err)
			}
		}
	}
}
