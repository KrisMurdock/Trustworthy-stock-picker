package monitor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"stock-watcher/internal/config"
	"stock-watcher/internal/feishu"
	"stock-watcher/internal/market"
	"stock-watcher/internal/notify"
	"stock-watcher/internal/selection"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the real HTTP parsers, ranking, files, card sender and ledger together.
// Endpoints are loopback-only fixtures; no real market or bot is contacted.
func TestStartupHTTPPipeline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   notify.Status
	}{
		{"delivered", http.StatusOK, notify.Delivered},
		{"permission denied", http.StatusForbidden, notify.Blocked},
		{"temporary failure", http.StatusServiceUnavailable, notify.Pending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := fixture(t)
			m.Config.Stocks = append(m.Config.Stocks, config.Stock{Code: "sh600000", Name: "第二只"})
			m.Config.Strategy.Enabled = true
			m.Now = func() time.Time { return time.Date(2026, 10, 13, 17, 0, 0, 0, market.Shanghai) }
			var downloads, posts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/market":
					parts := strings.Split(r.URL.Query().Get("param"), ",")
					if len(parts) != 6 || parts[3] != "2026-10-13" {
						t.Error("wrong requested trading date")
						w.WriteHeader(400)
						return
					}
					adjust := parts[5]
					field := "qfqday"
					if adjust == "" {
						adjust = "none"
						field = "day"
					}
					history, err := (&fakeSource{}).Fetch(r.Context(), config.Stock{Code: parts[0], Name: "测试股票"}, parts[2], parts[3], adjust)
					if err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					rows := make([][]any, 0, len(history.Bars))
					for _, bar := range history.Bars {
						rows = append(rows, []any{bar.Date, bar.Open, bar.Close, bar.High, bar.Low, bar.Volume})
					}
					downloads.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{parts[0]: map[string]any{field: rows}}})
				case "/bot":
					posts.Add(1)
					if r.Method != http.MethodPost || downloads.Load() != 4 {
						t.Error("bot called before full adjusted and raw synchronization")
					}
					var report selection.Report
					if err := m.Store.Read("reports/2026-10-13.json", &report); err != nil || report.PoolSize != 2 || report.Research == nil {
						t.Error("analysis not persisted before notification")
					}
					body, err := io.ReadAll(r.Body)
					if err != nil || !json.Valid(body) || !strings.Contains(string(body), "2026-10-14") || !strings.Contains(string(body), "interactive") {
						t.Error("invalid Feishu card payload")
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"code":0}`))
				default:
					t.Error("unexpected endpoint")
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			source := market.NewClient(time.Second)
			source.Endpoint = srv.URL + "/market"
			defer source.Close()
			channel := feishu.NewNamedClient("fake-main", srv.URL+"/bot", "", time.Second)
			defer channel.Close()
			m.Source = source
			m.Channels = []notify.Channel{channel}
			if err := m.prepareStartup(context.Background()); err != nil {
				t.Fatal(err)
			}
			err := m.Cycle(context.Background())
			if (err == nil) != (tc.status == 200) {
				t.Fatalf("unexpected delivery error: %v", err)
			}
			state, err := m.Status()
			if err != nil {
				t.Fatal(err)
			}
			if channelState(t, state, "fake-main").Status != tc.want {
				t.Fatalf("expected %s", tc.want)
			}
			if err := m.Cycle(context.Background()); err != nil {
				t.Fatal(err)
			}
			if downloads.Load() != 4 || posts.Load() != 1 {
				t.Fatalf("unexpected repetition: downloads=%d posts=%d", downloads.Load(), posts.Load())
			}
		})
	}
}
