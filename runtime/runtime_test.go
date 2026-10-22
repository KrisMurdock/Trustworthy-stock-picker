package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/dayrange"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/predictor"
)

func runtimeTestConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	t.Setenv("STOCK_WATCHER_DATA_DIR", root)
	t.Setenv("STOCK_WATCHER_REQUIRE_CIFS", "false")
	t.Setenv("STOCK_WATCHER_POOL_FILE", "")
	t.Setenv("STOCK_WATCHER_FEISHU_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_WEB_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_INTRADAY_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_CALENDAR_VALID_FROM", "2020-01-01")
	t.Setenv("STOCK_WATCHER_CALENDAR_VALID_THROUGH", "2099-12-31")
	t.Setenv("STOCK_WATCHER_CALENDAR_HOLIDAYS", "")
	t.Setenv("STOCK_WATCHER_POLL_INTERVAL", "5m")

	cfg := DefaultConfig()
	cfg.DataDir = root
	cfg.RequireCIFS = false
	cfg.PoolFile = ""
	cfg.Stocks = []Stock{{Code: "sz000001", Name: "test", ListedFrom: "2000-01-01"}}
	cfg.Web.Enabled = false
	cfg.Feishu.Enabled = false
	cfg.Intraday.Enabled = false
	if path, err := Init(&cfg); err != nil {
		t.Fatal(err)
	} else if path == "" {
		t.Fatal("init returned an empty market database path")
	}
	return cfg
}

func TestNewOpensResourcesAndServesDashboardHandler(t *testing.T) {
	cfg := runtimeTestConfig(t)
	rt, err := New(Options{Version: "test", Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Config().DataDir != cfg.DataDir || len(rt.Config().Stocks) != 1 {
		t.Fatalf("unexpected runtime config: %+v", rt.Config())
	}
	handler := rt.DashboardHandler()
	if handler == nil {
		t.Fatal("dashboard handler is nil")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected health status: %d %s", recorder.Code, recorder.Body.String())
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}
	if err := rt.Run(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("run after close returned %v", err)
	}
}

func TestDayRangeForecastsMapsPointsAndIntervals(t *testing.T) {
	now := time.Date(2026, 10, 22, 18, 5, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	records := []dayrange.PredictionRecord{
		{Prediction: dayrange.Prediction{
			Code: "sz000001", TargetDate: "2026-10-23", ModelID: "model-1", Source: "saved", CreatedAt: now.Add(-time.Minute),
			ReferencePrice: 10, PointHigh: 10.6, PointLow: 9.5,
			IntervalHigh: &dayrange.Interval{Lower: 10.2, Upper: 10.9, NominalCoverage: 0.8},
			IntervalLow:  &dayrange.Interval{Lower: 9.2, Upper: 9.8, NominalCoverage: 0.7},
		}},
		{Prediction: dayrange.Prediction{
			Code: "sz000001", TargetDate: "2026-10-23", ModelID: "model-2", Source: "saved", CreatedAt: now,
			ReferencePrice: 10.1, PointHigh: 10.7, PointLow: 9.6,
			IntervalHigh: &dayrange.Interval{Lower: 10.3, Upper: 11, NominalCoverage: 0.85},
			IntervalLow:  &dayrange.Interval{Lower: 9.1, Upper: 9.7, NominalCoverage: 0.75},
		}},
		{Prediction: dayrange.Prediction{
			Code: "sz000002", TargetDate: "2026-10-23", ModelID: "model-1", Source: "saved", CreatedAt: now,
			ReferencePrice: 20, PointHigh: 21, PointLow: 19,
		}},
		{Prediction: dayrange.Prediction{
			Code: "sh600000", TargetDate: "2026-10-23", PointHigh: 99, PointLow: 88,
		}},
	}

	got := dayRangeForecasts(records, []string{"sz000001", "sz000002", "sz000003"})
	if len(got) != 2 {
		t.Fatalf("forecasts = %+v", got)
	}
	first := got["sz000001"]
	if first.ModelID != "model-2" || first.PointHigh != 10.7 || first.PointLow != 9.6 ||
		first.HighLower != 10.3 || first.HighUpper != 11 || first.HighCoverage != 0.85 ||
		first.LowLower != 9.1 || first.LowUpper != 9.7 || first.LowCoverage != 0.75 {
		t.Fatalf("first forecast = %+v", first)
	}
	second := got["sz000002"]
	if second.PointHigh != 21 || second.PointLow != 19 || second.HighLower != 0 || second.LowUpper != 0 {
		t.Fatalf("missing intervals must stay zero: %+v", second)
	}
	if _, ok := got["sh600000"]; ok {
		t.Fatal("unrequested code was not filtered")
	}
	if missing := missingDayRangeForecasts(got, []string{"sz000001", "sz000002", "sz000003", "sz000003"}); missing != 1 {
		t.Fatalf("missing forecasts = %d, want 1", missing)
	}
}

func TestNewWiresDayRangeForecastLoader(t *testing.T) {
	cfg := runtimeTestConfig(t)
	cfg.DayRange.Enabled = true
	rt, err := New(Options{Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.dayRange == nil || rt.monitor.DayRangeForecasts == nil {
		t.Fatal("dayrange forecast loader was not wired into the monitor")
	}
}

func TestNewRegistersWechatSecretWithLogger(t *testing.T) {
	cfg := runtimeTestConfig(t)
	cfg.FirstStage.Enabled = true
	cfg.SecondStage.Enabled = true
	cfg.SecondStage.PushEnabled = true
	cfg.Wechat.Enabled = true
	cfg.Wechat.AppID = "wx-runtime-app"
	cfg.Wechat.AppSecret = "runtime-wechat-secret-sentinel"

	var stdout bytes.Buffer
	log, err := NewLogger(LoggerOptions{
		Dir:      t.TempDir(),
		MaxBytes: 1 << 20,
		MaxDays:  1,
		Backups:  1,
		Stdout:   &stdout,
		Color:    "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	rt, err := New(Options{Config: &cfg, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	log.Info("secret registration probe", "value", cfg.Wechat.AppSecret)
	if strings.Contains(stdout.String(), cfg.Wechat.AppSecret) {
		t.Fatal("runtime logger did not redact the WeChat AppSecret")
	}
}

func TestInitCreatesLocalDataDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	t.Setenv("STOCK_WATCHER_DATA_DIR", root)
	t.Setenv("STOCK_WATCHER_REQUIRE_CIFS", "false")
	t.Setenv("STOCK_WATCHER_POOL_FILE", "")
	t.Setenv("STOCK_WATCHER_FEISHU_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_WEB_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_INTRADAY_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_CALENDAR_VALID_FROM", "2020-01-01")
	t.Setenv("STOCK_WATCHER_CALENDAR_VALID_THROUGH", "2099-12-31")

	cfg := DefaultConfig()
	cfg.DataDir = root
	cfg.PoolFile = ""
	cfg.Stocks = []Stock{{Code: "sz000001", Name: "test"}}
	path, err := Init(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(root, "market.db") {
		t.Fatalf("unexpected market database path: %s", path)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("local data directory was not created: %v", err)
	}
}

func TestCloseCancelsRunningRuntime(t *testing.T) {
	cfg := runtimeTestConfig(t)
	rt, err := New(Options{Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	stopped := make(chan struct{})
	rt.runMonitor = func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}
	runDone := make(chan error, 1)
	go func() { runDone <- rt.Run(context.Background()) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime did not start")
	}
	if err := rt.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("concurrent run returned %v", err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- rt.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("close did not return")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("canceled run returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not stop after close")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("close returned before monitor cleanup")
	}
}

func TestRunRejectsOccupiedPortBeforeMonitorStarts(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg := runtimeTestConfig(t)
	t.Setenv("STOCK_WATCHER_WEB_ENABLED", "true")
	cfg.Web.Enabled = true
	cfg.Web.Listen = listener.Addr().String()
	rt, err := New(Options{Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	started := false
	rt.runMonitor = func(context.Context) error {
		started = true
		return nil
	}
	err = rt.Run(context.Background())
	if err == nil || started {
		t.Fatalf("occupied port must fail before monitor starts: %v, started=%v", err, started)
	}
}

func TestRuntimeIncludesMarketContextInDefaultMonitors(t *testing.T) {
	cfg := runtimeTestConfig(t)
	rt, err := New(Options{Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	if rt.contextDB == nil || rt.contextSyncer == nil || rt.contextClient == nil || rt.monitor.MarketContextSync == nil {
		rt.Close()
		t.Fatal("default runtime did not wire market context synchronization into the daily sync")
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "context.db")); err != nil {
		rt.Close()
		t.Fatalf("context database missing: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.MarketContext.Enabled = false
	disabled, err := New(Options{Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer disabled.Close()
	if disabled.contextSyncer != nil || disabled.contextDB != nil || disabled.monitor.MarketContextSync != nil {
		t.Fatal("disabled market context was still started")
	}
}

func TestRuntimeWiresPredictorIntoDashboardAndRunLoop(t *testing.T) {
	cfg := runtimeTestConfig(t)
	cfg.Predictor.Enabled = true
	cfg.Predictor.PushEnabled = true
	rt, err := New(Options{Version: "test", Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	if rt.predictor == nil {
		t.Fatal("predictor monitor was not constructed")
	}
	r := httptest.NewRequest(http.MethodGet, "/api/predictor", nil)
	r.RemoteAddr = "127.0.0.1:45678"
	w := httptest.NewRecorder()
	rt.DashboardHandler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("predictor API unavailable: %d %s", w.Code, w.Body.String())
	}
	var view predictor.View
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Enabled || view.WriteMode != cfg.Predictor.WriteMode {
		t.Fatalf("unexpected predictor view: %+v", view)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if rt.predictor != nil {
		t.Fatal("predictor was not released")
	}
}

func TestRuntimeWithoutPredictorKeepsDashboardReadOnly(t *testing.T) {
	cfg := runtimeTestConfig(t)
	cfg.Predictor.Enabled = false
	cfg.Predictor.PushEnabled = false
	rt, err := New(Options{Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.predictor != nil {
		t.Fatal("disabled predictor was constructed")
	}
	w := httptest.NewRecorder()
	rt.DashboardHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/predictor", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled predictor API status: %d", w.Code)
	}
}
