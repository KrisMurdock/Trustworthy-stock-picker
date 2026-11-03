package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func TestHKMinuteAPIReadOnlyPeriodsAndFailures(t *testing.T) {
	c := config.Defaults()
	c.DataDir = t.TempDir()
	c.RequireCIFS = false
	db, err := storage.OpenHKMarket(c.DataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stock := config.Stock{Code: "hk00700", Name: "腾讯控股"}
	observed := time.Date(2026, 11, 2, 16, 20, 0, 0, market.Shanghai)
	history := market.HKMinuteHistory{Version: 1, Code: stock.Code, Name: stock.Name, Source: "eastmoney_hk", Period: 5, BasePeriod: 5,
		Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: observed}
	for _, session := range [][2]int{{570, 720}, {780, 960}} {
		for minute := session[0] + 5; minute <= session[1]; minute += 5 {
			history.Bars = append(history.Bars, market.MinuteBar{
				End:  time.Date(2026, 11, 2, minute/60, minute%60, 0, 0, market.Shanghai),
				Open: 10, High: 12, Low: 9, Close: 11, Volume: 100, Final: true,
			})
		}
	}
	if err := db.SaveHKMinutes(context.Background(), stock, "2026-11-02", "2026-11-02", observed, &history, "ok", "", 0); err != nil {
		t.Fatal(err)
	}
	handler := newHandler(c, nil, nil)
	for period, count := range map[int]int{5: 66, 15: 22, 30: 11, 60: 6} {
		request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/hk-intraday?code=hk00700&period=%d", period), nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var snapshot storage.HKMinuteSnapshot
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &snapshot) != nil || len(snapshot.Bars) != count || snapshot.Sync.Status != "ok" {
			t.Fatalf("period %d: status %d body %s", period, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("missing security headers: %v", response.Header())
		}
	}
	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/api/hk-intraday?code=hk00700&period=1", 400},
		{http.MethodGet, "/api/hk-intraday?code=hk00700&period=05", 400},
		{http.MethodGet, "/api/hk-intraday?code=sz000001&period=5", 400},
		{http.MethodGet, "/api/hk-intraday?code=hk00700&period=5&extra=1", 400},
		{http.MethodGet, "/api/hk-intraday?code=hk00700&code=hk00005&period=5", 400},
		{http.MethodGet, "/api/hk-intraday?code=hk00005&period=5", 404},
		{http.MethodPost, "/api/hk-intraday?code=hk00700&period=5", 405},
		{http.MethodHead, "/api/hk-intraday?code=hk00700&period=5", 200},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.status || test.method == http.MethodHead && response.Body.Len() != 0 {
			t.Fatalf("%s %s: status %d, body %s", test.method, test.path, response.Code, response.Body.String())
		}
	}
	for name, calendar := range map[string]config.HKIntraday{
		"missing coverage":        {ValidFrom: "2026-01-01", ValidThrough: ""},
		"outside coverage":        {ValidFrom: "2026-11-03", ValidThrough: "2026-12-31", EarlyCloses: []string{"2026-12-24", "2026-12-31"}},
		"conflicting early close": {ValidFrom: "2026-01-01", ValidThrough: "2026-12-31", EarlyCloses: []string{"2026-11-02"}},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := c
			candidate.HKIntraday = calendar
			for _, period := range []int{5, 60} {
				response := httptest.NewRecorder()
				path := fmt.Sprintf("/api/hk-intraday?code=hk00700&period=%d", period)
				newHandler(candidate, nil, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				if response.Code != http.StatusServiceUnavailable {
					t.Fatalf("invalid calendar for period %d produced status %d: %s", period, response.Code, response.Body.String())
				}
			}
		})
	}
	c.DataDir += "/missing"
	response := httptest.NewRecorder()
	newHandler(c, nil, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/hk-intraday?code=hk00700&period=5", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing storage: %d %s", response.Code, response.Body.String())
	}
}
