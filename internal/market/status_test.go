package market

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fixedStatusSource struct {
	window StatusWindow
	err    error
}

func (f fixedStatusSource) FetchStatus(context.Context, config.Stock, string, string) (StatusWindow, error) {
	return f.window, f.err
}

type blockingStatusSource struct{}

func (blockingStatusSource) FetchStatus(ctx context.Context, _ config.Stock, _, _ string) (StatusWindow, error) {
	<-ctx.Done()
	return StatusWindow{}, ctx.Err()
}

type fixedCalendarSource struct {
	days []string
	err  error
}

func (f fixedCalendarSource) FetchTradingDays(context.Context, string, string) ([]string, error) {
	return f.days, f.err
}

type blockingCalendarSource struct{}

func (blockingCalendarSource) FetchTradingDays(ctx context.Context, _, _ string) ([]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func statusWindow(source string, values map[string]SessionState) StatusWindow {
	w := StatusWindow{Code: "sz000001", Start: "2026-10-12", End: "2026-10-13", Source: source, FetchedAt: time.Now()}
	dates := []string{"2026-10-12", "2026-10-13"}
	for _, date := range dates {
		if state, ok := values[date]; ok {
			w.Statuses = append(w.Statuses, SessionStatus{Code: w.Code, Date: date, State: state, Source: source, FetchedAt: w.FetchedAt})
		}
	}
	return w
}

func TestFetchStatusMergesFailClosed(t *testing.T) {
	stock := config.Stock{Code: "sz000001", Name: "测试"}
	cases := []struct {
		name    string
		bao     map[string]SessionState
		tushare map[string]SessionState
		baoErr  error
		want12  SessionState
		want13  SessionState
	}{
		{name: "suspension survives an absent secondary row", bao: map[string]SessionState{"2026-10-12": SessionSuspended, "2026-10-13": SessionTraded}, tushare: map[string]SessionState{}, want12: SessionSuspended, want13: SessionTraded},
		{name: "explicit traded loses to explicit suspension", bao: map[string]SessionState{"2026-10-12": SessionTraded}, tushare: map[string]SessionState{"2026-10-12": SessionSuspended}, want12: SessionUnknown},
		{name: "conflicting sources do not invent a suspension", bao: map[string]SessionState{"2026-10-12": SessionSuspended, "2026-10-13": SessionTraded}, tushare: map[string]SessionState{"2026-10-12": SessionTraded, "2026-10-13": SessionSuspended}, want12: SessionUnknown, want13: SessionUnknown},
		{name: "secondary suspension is used when primary fails", baoErr: &Failure{Kind: "network"}, tushare: map[string]SessionState{"2026-10-12": SessionSuspended}, want12: SessionSuspended, want13: SessionUnknown},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s := &Sources{
				statusProviders: []StatusProvider{
					{Name: "baostock_status", Source: fixedStatusSource{window: statusWindow("baostock", tt.bao), err: tt.baoErr}},
					{Name: "tushare_status", Source: fixedStatusSource{window: statusWindow("tushare", tt.tushare)}},
				},
			}
			got, err := s.FetchStatus(context.Background(), stock, "2026-10-12", "2026-10-13")
			if err != nil {
				t.Fatal(err)
			}
			states := map[string]SessionState{}
			for _, status := range got.Statuses {
				states[status.Date] = status.State
			}
			get := func(day string) SessionState {
				if state, ok := states[day]; ok {
					return state
				}
				return SessionUnknown
			}
			want12, want13 := tt.want12, tt.want13
			if want12 == "" {
				want12 = SessionUnknown
			}
			if want13 == "" {
				want13 = SessionUnknown
			}
			if get("2026-10-12") != want12 || get("2026-10-13") != want13 {
				t.Fatalf("states = %+v", states)
			}
		})
	}
}

func TestFetchStatusReportsAllSourcesFailed(t *testing.T) {
	s := &Sources{statusProviders: []StatusProvider{
		{Name: "baostock_status", Source: fixedStatusSource{err: &Failure{Kind: "network"}}},
		{Name: "tushare_status", Source: fixedStatusSource{err: &Failure{Status: 403, Kind: "tushare_api"}}},
	}}
	_, err := s.FetchStatus(context.Background(), config.Stock{Code: "sz000001"}, "2026-10-12", "2026-10-13")
	if err == nil || ProviderFailureSummary(err) == "" {
		t.Fatalf("missing aggregate failure: %v", err)
	}
}

func TestFetchStatusFallsBackAfterAttemptTimeout(t *testing.T) {
	s := &Sources{attemptTimeout: 20 * time.Millisecond, statusProviders: []StatusProvider{
		{Name: "baostock_status", Source: blockingStatusSource{}},
		{Name: "tushare_status", Source: fixedStatusSource{window: statusWindow("tushare", map[string]SessionState{"2026-10-12": SessionSuspended})}},
	}}
	got, err := s.FetchStatus(context.Background(), config.Stock{Code: "sz000001"}, "2026-10-12", "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Statuses) != 1 || got.Statuses[0].Date != "2026-10-12" || got.Statuses[0].State != SessionSuspended {
		t.Fatalf("fallback result = %+v", got)
	}
}

func TestFetchTradingDaysFallsBackAfterTimeoutAndEmptyWindow(t *testing.T) {
	s := &Sources{attemptTimeout: 20 * time.Millisecond, calendarProviders: []CalendarProvider{
		{Name: "baostock_calendar", Source: blockingCalendarSource{}},
		{Name: "empty_calendar", Source: fixedCalendarSource{}},
		{Name: "tushare_calendar", Source: fixedCalendarSource{days: []string{"2026-10-12"}}},
	}}
	days, err := s.FetchTradingDays(context.Background(), "2026-10-12", "2026-10-13")
	if err != nil || len(days) != 1 || days[0] != "2026-10-12" {
		t.Fatalf("calendar fallback = %+v, %v", days, err)
	}
}

func TestMergeStatusWindowMarksOnlyFullAnswers(t *testing.T) {
	full := statusWindow("baostock", map[string]SessionState{"2026-10-12": SessionTraded})
	full.Full = true
	sparse := statusWindow("tushare", map[string]SessionState{"2026-10-13": SessionSuspended})
	s := &Sources{statusProviders: []StatusProvider{
		{Name: "baostock_status", Source: fixedStatusSource{window: full}},
		{Name: "tushare_status", Source: fixedStatusSource{window: sparse}},
	}}
	got, err := s.FetchStatus(context.Background(), config.Stock{Code: "sz000001"}, "2026-10-12", "2026-10-13")
	if err != nil || !got.Full {
		t.Fatalf("merged full = %+v, %v", got, err)
	}
	s.statusProviders = s.statusProviders[1:]
	got, err = s.FetchStatus(context.Background(), config.Stock{Code: "sz000001"}, "2026-10-12", "2026-10-13")
	if err != nil || got.Full {
		t.Fatalf("sparse-only full = %+v, %v", got, err)
	}
	s.statusProviders = []StatusProvider{
		{Name: "baostock_status", Source: fixedStatusSource{err: &Failure{Kind: "network"}}},
		{Name: "tushare_status", Source: fixedStatusSource{window: sparse}},
	}
	got, err = s.FetchStatus(context.Background(), config.Stock{Code: "sz000001"}, "2026-10-12", "2026-10-13")
	if err != nil || got.Full || len(got.Statuses) != 1 || got.Statuses[0].State != SessionSuspended {
		t.Fatalf("primary failure + sparse fallback = %+v, %v", got, err)
	}
}

func TestTushareStatusAndCalendarProtocol(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			API    string            `json:"api_name"`
			Params map[string]string `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		switch req.API {
		case "suspend_d":
			if req.Params["ts_code"] != "688981.SH" || req.Params["start_date"] != "20261012" || req.Params["end_date"] != "20261013" {
				t.Error("unexpected suspend_d params")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"fields": []string{"ts_code", "trade_date", "suspend_type"},
				"items":  [][]any{{"688981.SH", "20261012", "S"}, {"688981.SH", "20261013", "R"}},
			}})
		case "trade_cal":
			if req.Params["exchange"] != "SSE" || req.Params["start_date"] != "20261012" || req.Params["end_date"] != "20261013" {
				t.Error("unexpected trade_cal params")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"fields": []string{"exchange", "cal_date", "is_open"},
				"items":  [][]any{{"SSE", "20261012", "1"}, {"SSE", "20261013", "0"}},
			}})
		default:
			t.Errorf("unexpected API %s", req.API)
		}
	}))
	defer srv.Close()
	c := NewTushareClient(time.Second, 0, "test-token")
	c.Endpoint = srv.URL
	defer c.Close()
	window, err := c.FetchStatus(context.Background(), providerStock, "2026-10-12", "2026-10-13")
	if err != nil || len(window.Statuses) != 2 || window.Statuses[0].State != SessionSuspended || window.Statuses[1].State != SessionTraded || window.Full {
		t.Fatalf("status = %+v, %v", window, err)
	}
	days, err := c.FetchTradingDays(context.Background(), "2026-10-12", "2026-10-13")
	if err != nil || len(days) != 1 || days[0] != "2026-10-12" {
		t.Fatalf("days = %+v, %v", days, err)
	}
}

func TestTushareStatusEmptyAndPermission(t *testing.T) {
	mode := "empty"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "permission" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 2002, "msg": "denied"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": nil})
	}))
	defer srv.Close()
	c := NewTushareClient(time.Second, 0, "test-token")
	c.Endpoint = srv.URL
	defer c.Close()
	window, err := c.FetchStatus(context.Background(), providerStock, "2026-10-12", "2026-10-13")
	if err != nil || len(window.Statuses) != 0 || window.Full {
		t.Fatalf("empty status = %+v, %v", window, err)
	}
	mode = "permission"
	_, err = c.FetchStatus(context.Background(), providerStock, "2026-10-12", "2026-10-13")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Status != http.StatusForbidden {
		t.Fatalf("permission error = %v", err)
	}
}

func TestBaoStockStatusAndCalendarProtocol(t *testing.T) {
	address := baoTestServer(t, func(kind string, parts []string) (string, string) {
		if kind == "00" {
			return "01", "0\x01success\x01login\x01anonymous"
		}
		if kind == "95" {
			if len(parts) != 10 || parts[4] != "sh.688981" || parts[5] != "date,code,tradestatus" || parts[9] != "3" {
				t.Error("unexpected BaoStock status query")
				return "close", ""
			}
			return "96", baoReply(parts, [][]string{
				{"2026-10-12", "sh.688981", "1"},
				{"2026-10-13", "sh.688981", "0"},
			})
		}
		if kind == "33" {
			if len(parts) != 6 || parts[0] != "query_trade_dates" || parts[4] != "2026-10-01" || parts[5] != "2026-10-13" {
				t.Error("unexpected BaoStock calendar query")
				return "close", ""
			}
			data, _ := json.Marshal(map[string]any{"record": [][]string{{"2026-10-12", "1"}, {"2026-10-13", "0"}}})
			return "34", strings.Join([]string{"0", "success", parts[0], parts[1], parts[2], parts[3], string(data), parts[4], parts[5], "calendar_date,is_trading_day"}, baoSeparator) + "\n"
		}
		t.Errorf("unexpected BaoStock request kind %s", kind)
		return "close", ""
	})
	c := NewBaoStockClient(time.Second, 0)
	c.Address = address
	window, err := c.FetchStatus(context.Background(), providerStock, "2026-10-01", "2026-10-13")
	if err != nil || !window.Full || len(window.Statuses) != 2 || window.Statuses[1].State != SessionSuspended {
		t.Fatalf("status = %+v, %v", window, err)
	}
	days, err := c.FetchTradingDays(context.Background(), "2026-10-01", "2026-10-13")
	if err != nil || len(days) != 1 || days[0] != "2026-10-12" {
		t.Fatalf("days = %+v, %v", days, err)
	}
}

func TestBaoStockRejectsEmptyStatusWindow(t *testing.T) {
	address := baoTestServer(t, func(kind string, parts []string) (string, string) {
		if kind == "00" {
			return "01", "0\x01success\x01login\x01anonymous"
		}
		return "96", baoReply(parts, nil)
	})
	c := NewBaoStockClient(time.Second, 0)
	c.Address = address
	_, err := c.FetchStatus(context.Background(), providerStock, "2026-10-01", "2026-10-13")
	var dataErr *DataError
	if !errors.As(err, &dataErr) || dataErr.Reason != "empty_baostock_status" {
		t.Fatalf("empty status error = %v", err)
	}
}
