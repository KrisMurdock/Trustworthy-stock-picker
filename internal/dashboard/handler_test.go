package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/health"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReportDiagnosticsAreReadOnlyAndAvailableOnBothRoutes(t *testing.T) {
	r := selection.Report{Version: 2, ID: "saved", AsOf: "2026-10-13", Adjust: "qfq", Research: &strategy.Research{Parameters: config.DefaultStrategy(), Holdout: strategy.Evaluation{Method: "frozen_holdout", SignalDates: 40}}, Ranking: []strategy.Candidate{{Code: "sz000001", Rules: []string{"baseline"}, Risks: []string{"insufficient_or_unprofitable_evidence"}}}}
	reader := &testReader{report: r, snapshot: storage.DashboardSnapshot{Version: 1, Report: &r}}
	h := newHandler(config.Defaults(), reader, nil)
	before, _ := json.Marshal(r)
	for _, path := range []string{"/api/dashboard", "/api/reports?as_of=2026-10-13"} {
		w := request(t, h, "GET", path)
		var body map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
			t.Fatalf("read failed: %v", err)
		}
		if path == "/api/dashboard" {
			if err := json.Unmarshal(body["report"], &body); err != nil {
				t.Fatal(err)
			}
		}
		var view reportView
		raw, _ := json.Marshal(body)
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		if view.Diagnostics == nil || view.Diagnostics.Version != 1 || view.Diagnostics.Candidates["sz000001"].Status != "no_signal" || view.Diagnostics.Validation.Status != "no_recommendations" {
			t.Fatalf("missing typed diagnostics: %s", raw)
		}
		stored, _ := json.Marshal(view.Report)
		if string(stored) != string(before) {
			t.Fatal("API changed stored report fields")
		}
	}
	after, _ := json.Marshal(reader.report)
	if string(before) != string(after) {
		t.Fatal("view mutated reader state")
	}
}

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
		{"GET", "/stocks", "text/html", "no-store", 200},
		{"GET", "/chart", "text/html", "no-store", 200},
		{"GET", "/rankings", "text/html", "no-store", 200},
		{"HEAD", "/stocks", "text/html", "no-store", 200},
		{"POST", "/chart", "application/json", "no-store", 405},
		{"GET", "/calculator", "text/html", "no-store", 200},
		{"HEAD", "/calculator", "text/html", "no-store", 200},
		{"POST", "/calculator", "application/json", "no-store", 405},
		{"GET", "/assets/calculator.js", "text/javascript", "no-cache", 200},
		{"GET", "/assets/strategy-model.js", "text/javascript", "no-cache", 200},
		{"GET", "/assets/calculator.css", "text/css", "no-cache", 200},
		{"GET", "/assets/common.js", "text/javascript", "no-cache", 200},
		{"GET", "/assets/overview.js", "text/javascript", "no-cache", 200},
		{"GET", "/assets/stocks.js", "text/javascript", "no-cache", 200},
		{"GET", "/assets/chart-page.js", "text/javascript", "no-cache", 200},
		{"GET", "/assets/rankings.js", "text/javascript", "no-cache", 200},
		{"GET", "/assets/style.css", "text/css", "no-cache", 200},
		{"GET", "/assets/ambient.css", "text/css", "no-cache", 200},
		{"GET", "/assets/scene.js", "text/javascript", "no-cache", 200},
		{"GET", "/assets/vendor/three-bundle.min.js", "text/javascript", "no-cache", 200},
		{"GET", "/assets/models/dome.glb", "model/gltf-binary", "no-cache", 200},
		{"GET", "/assets/scene-poster.svg", "image/svg+xml", "no-cache", 200},
		{"GET", "/assets/fonts/plex-serif-400.woff2", "font/woff2", "no-cache", 200},
		{"GET", "/assets/fonts/plex-serif-600.woff2", "font/woff2", "no-cache", 200},
		{"GET", "/assets/fonts/plex-serif-300.woff2", "application/json", "no-store", 404},
		{"GET", "/assets/models/missing.glb", "application/json", "no-store", 404},
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

func TestPublicReadOnlyModeRejectsInteractiveEndpoints(t *testing.T) {
	c := config.Defaults()
	c.Web.PublicReadOnly = true
	h := NewHandler(c, nil, nil)
	for _, path := range []string{"/api/llm/config", "/api/llm/config/test", "/api/master/chat", "/api/review/analysis", "/api/predictor/config"} {
		w := request(t, h, http.MethodGet, path)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"error":"not found"`) {
			t.Fatalf("interactive route %s was exposed: %d %s", path, w.Code, w.Body)
		}
	}
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/dashboard"},
		{http.MethodPost, "/api/master/chat"},
		{http.MethodPost, "/api/review/analysis"},
		{http.MethodPut, "/api/predictor/config"},
		{http.MethodDelete, "/healthz"},
	} {
		w := request(t, h, tc.method, tc.path)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"error":"not found"`) {
			t.Fatalf("public write %s %s was exposed: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
	}
	if w := request(t, h, http.MethodGet, "/api/dashboard"); w.Code != http.StatusOK {
		t.Fatalf("public dashboard is unavailable: %d %s", w.Code, w.Body)
	}
	if w := request(t, h, http.MethodGet, "/api/master/roles"); w.Code != http.StatusOK {
		t.Fatalf("public role metadata is unavailable: %d %s", w.Code, w.Body)
	}
	w := request(t, h, http.MethodGet, "/review")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `name="stock-watcher-public-read-only" content="true"`) {
		t.Fatalf("public review page marker missing: %d %s", w.Code, w.Body)
	}
	local := request(t, NewHandler(config.Defaults(), nil, nil), http.MethodGet, "/review")
	if strings.Contains(local.Body.String(), "stock-watcher-public-read-only") {
		t.Fatal("local review page was marked public read-only")
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

func (s *testReader) LoadMinutes(ctx context.Context, stock config.Stock, period int) (storage.MinuteSnapshot, error) {
	_, err := s.LoadDashboard(ctx, nil, "")
	return storage.MinuteSnapshot{MinuteHistory: market.MinuteHistory{Version: 1, Code: stock.Code, Period: period, BasePeriod: 5, Adjust: "none", Source: "tencent", Bars: []market.MinuteBar{}}}, err
}
func TestIntradayReadOnlyValidationAndErrors(t *testing.T) {
	c := config.Defaults()
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试"}}
	reader := &testReader{}
	h := newHandler(c, reader, nil)
	for _, query := range []string{"", "?code=sz000001", "?code=sz000001&period=1", "?code=sz000001&period=05", "?code=sz000001&period=5&period=15", "?code=sz000001&period=5&adjust=qfq", "?code=sz000001&period=5&bad=%ZZ"} {
		if w := request(t, h, "GET", "/api/intraday"+query); w.Code != 400 {
			t.Fatal(query, w.Code)
		}
	}
	for _, period := range []string{"5", "15", "30", "60"} {
		w := request(t, h, "GET", "/api/intraday?code=sz000001&period="+period)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), `"base_period_minutes":5`) {
			t.Fatal(w)
		}
	}
	if w := request(t, h, "GET", "/api/intraday?code=sh600519&period=5"); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := request(t, h, "POST", "/api/intraday?code=sz000001&period=5"); w.Code != 405 {
		t.Fatal(w.Code)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{fs.ErrNotExist, 404}, {errors.New("token=private-secret"), 503}} {
		reader.err = tc.err
		w := request(t, h, "GET", "/api/intraday?code=sz000001&period=5")
		if w.Code != tc.status || strings.Contains(w.Body.String(), "private-secret") {
			t.Fatal(w)
		}
	}
	reader.wait = true
	for _, cancelNow := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		if cancelNow {
			cancel()
		}
		r := httptest.NewRequest("GET", "/api/intraday?code=sz000001&period=5", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		cancel()
		want := 504
		if cancelNow {
			want = 503
		}
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestRuntimeStatusReadinessAndUnavailable(t *testing.T) {
	h := &handler{log: logging.Nop(), health: func(context.Context) (health.Status, error) {
		return health.Status{Version: 1, Ready: true, Business: "degraded", Issues: []string{"analysis_not_published"}}, nil
	}}
	for _, path := range []string{"/healthz", "/readyz", "/api/status"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(path, w.Code)
		}
	}
	h.health = func(context.Context) (health.Status, error) { return health.Status{}, context.DeadlineExceeded }
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 {
		t.Fatal("unavailable storage ready")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/status", nil))
	if w.Code != 405 {
		t.Fatal("status accepted write")
	}
}
