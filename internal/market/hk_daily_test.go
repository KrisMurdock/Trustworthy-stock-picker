package market

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

type hkDailyFixture struct {
	hsiDays     []string
	qfqUsesDay  bool
	failFirst   int
	mutate      func(call int, code, adjust string, bars []Bar) []Bar
	requestSeen atomic.Int64
}

func weekdaysBetween(start, through string, limit int) []Bar {
	first, _ := time.Parse("2006-01-02", start)
	last, _ := time.Parse("2006-01-02", through)
	out := []Bar{}
	for day := last; !day.Before(first) && len(out) < limit; day = day.AddDate(0, 0, -1) {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		out = append(out, Bar{Date: day.Format("2006-01-02"), Open: 100, Close: 101, High: 102, Low: 99, Volume: 1000})
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (f *hkDailyFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Query().Get("param"), ",")
		if len(parts) != 6 {
			t.Errorf("invalid Tencent HK parameter: %q", r.URL.Query().Get("param"))
			http.Error(w, "bad parameter", http.StatusBadRequest)
			return
		}
		code, start, through, adjust := parts[0], parts[2], parts[3], parts[5]
		call := int(f.requestSeen.Add(1))
		if call <= f.failFirst {
			http.Error(w, "temporary upstream failure", http.StatusInternalServerError)
			return
		}
		count := 1000
		fmt.Sscanf(parts[4], "%d", &count)
		field := "day"
		if strings.Contains(r.URL.Path, "hkfqkline") && !f.qfqUsesDay {
			field = "qfqday"
		}
		var bars []Bar
		if code == "hkHSI" {
			for _, date := range f.hsiDays {
				if date >= start && date <= through {
					bars = append(bars, Bar{Date: date, Open: 20000, Close: 20100, High: 20200, Low: 19900, Volume: 1})
				}
			}
		} else {
			bars = weekdaysBetween(start, through, count)
		}
		if f.mutate != nil {
			bars = f.mutate(call, code, adjust, bars)
		}
		rows := make([]string, 0, len(bars))
		for _, bar := range bars {
			rows = append(rows, fmt.Sprintf(`["%s","%.3f","%.3f","%.3f","%.3f","%.3f"]`, bar.Date, bar.Open, bar.Close, bar.High, bar.Low, bar.Volume))
		}
		fmt.Fprintf(w, `{"code":0,"msg":"","data":{%q:{%q:[%s]}}}`, code, field, strings.Join(rows, ","))
	}))
}

func TestHKDailyClientPagesRawHistoryAndRefreshesQFQ(t *testing.T) {
	fixture := &hkDailyFixture{}
	server := fixture.server(t)
	defer server.Close()
	client := NewHKDailyClient(2*time.Second, time.Millisecond, server.URL)
	stock := config.Stock{Code: "hk00700", Name: "腾讯控股"}

	raw, err := client.Fetch(context.Background(), stock, "2016-01-01", "2026-10-30", "none")
	if err != nil {
		t.Fatal(err)
	}
	if raw.Source != "tencent_hk" || raw.DataField != "day" || len(raw.Bars) < 2500 {
		t.Fatalf("unexpected raw history: source=%s field=%s bars=%d", raw.Source, raw.DataField, len(raw.Bars))
	}
	if raw.Bars[0].Date < "2016-01-01" || raw.Bars[len(raw.Bars)-1].Date != "2026-10-30" {
		t.Fatalf("raw history bounds: %s..%s", raw.Bars[0].Date, raw.Bars[len(raw.Bars)-1].Date)
	}
	if fixture.requestSeen.Load() < 3 {
		t.Fatalf("expected paginated raw requests, got %d", fixture.requestSeen.Load())
	}

	before := fixture.requestSeen.Load()
	qfq, err := client.Fetch(context.Background(), stock, "2025-10-30", "2026-10-30", "qfq")
	if err != nil {
		t.Fatal(err)
	}
	if qfq.DataField != "qfqday" || len(qfq.Bars) < 250 || fixture.requestSeen.Load() <= before {
		t.Fatalf("unexpected qfq history: field=%s bars=%d requests=%d", qfq.DataField, len(qfq.Bars), fixture.requestSeen.Load())
	}
}

func TestHKDailyClientRetriesRetryableUpstreamFailures(t *testing.T) {
	fixture := &hkDailyFixture{failFirst: 2}
	server := fixture.server(t)
	defer server.Close()
	client := NewHKDailyClient(2*time.Second, time.Millisecond, server.URL)
	client.backoff = time.Millisecond
	history, err := client.Fetch(context.Background(), config.Stock{Code: "hk00700", Name: "腾讯控股"}, "2026-10-01", "2026-10-30", "none")
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Bars) == 0 || fixture.requestSeen.Load() < 3 {
		t.Fatalf("retry did not recover: bars=%d requests=%d", len(history.Bars), fixture.requestSeen.Load())
	}
}

func TestHKDailyClientUsesFallbackEndpointAfterRetryableFailure(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary upstream failure", http.StatusNotImplemented)
	}))
	defer primary.Close()
	fixture := &hkDailyFixture{}
	fallback := fixture.server(t)
	defer fallback.Close()
	client := NewHKDailyClient(2*time.Second, time.Millisecond, primary.URL, fallback.URL)
	client.backoff = time.Millisecond
	history, err := client.Fetch(context.Background(), config.Stock{Code: "hk00700", Name: "腾讯控股"}, "2026-10-01", "2026-10-30", "none")
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Bars) == 0 {
		t.Fatal("fallback endpoint returned no history")
	}
}

func TestHKDailyClientAcceptsDayForQFQWithoutAdjustmentFactors(t *testing.T) {
	fixture := &hkDailyFixture{qfqUsesDay: true}
	server := fixture.server(t)
	defer server.Close()
	client := NewHKDailyClient(2*time.Second, time.Millisecond, server.URL)
	qfq, err := client.Fetch(context.Background(), config.Stock{Code: "hk00013", Name: "和黄医药"}, "2025-10-30", "2026-10-30", "qfq")
	if err != nil {
		t.Fatal(err)
	}
	if qfq.Adjust != "qfq" || qfq.DataField != "day" || len(qfq.Bars) < 250 {
		t.Fatalf("unexpected QFQ fallback: adjust=%s field=%s bars=%d", qfq.Adjust, qfq.DataField, len(qfq.Bars))
	}
}

func TestHKDailyClientAcceptsOneOverflowRowFromQFQEndpoint(t *testing.T) {
	fixture := &hkDailyFixture{qfqUsesDay: true, mutate: func(_ int, code, adjust string, bars []Bar) []Bar {
		if code == "hk00013" && adjust == "qfq" {
			extra := Bar{Date: "2022-11-01", Open: 100, Close: 101, High: 102, Low: 99, Volume: 1000}
			return append([]Bar{extra}, bars...)
		}
		return bars
	}}
	server := fixture.server(t)
	defer server.Close()
	client := NewHKDailyClient(2*time.Second, time.Millisecond, server.URL)
	page, err := client.fetchPageOnce(context.Background(), server.URL, "hk00013", "2022-11-02", "2026-11-02", hkDailyPageSize, "qfq")
	if err != nil {
		t.Fatal(err)
	}
	if page.Field != "day" || len(page.Bars) != hkDailyPageSize {
		t.Fatalf("unexpected overflow page: field=%s bars=%d", page.Field, len(page.Bars))
	}
}

func TestHKDailyClientRejectsChangedPaginationBoundary(t *testing.T) {
	fixture := &hkDailyFixture{mutate: func(call int, code, adjust string, bars []Bar) []Bar {
		if call == 2 && code == "hk00700" && adjust == "none" && len(bars) > 0 {
			bars[len(bars)-1].Open += 1
		}
		return bars
	}}
	server := fixture.server(t)
	defer server.Close()
	client := NewHKDailyClient(2*time.Second, time.Millisecond, server.URL)
	_, err := client.Fetch(context.Background(), config.Stock{Code: "hk00700", Name: "腾讯控股"}, "2016-01-01", "2026-10-30", "none")
	if err == nil || !strings.Contains(err.Error(), "hk_daily_page_overlap_mismatch") {
		t.Fatalf("changed pagination boundary not rejected: %v", err)
	}
}

func TestHKDailyClientRejectsInvalidOHLC(t *testing.T) {
	fixture := &hkDailyFixture{mutate: func(_ int, code, adjust string, bars []Bar) []Bar {
		if code != "hkHSI" && len(bars) > 0 {
			bars[len(bars)-1].High = bars[len(bars)-1].Open - 1
		}
		return bars
	}}
	server := fixture.server(t)
	defer server.Close()
	client := NewHKDailyClient(2*time.Second, time.Millisecond, server.URL)
	_, err := client.Fetch(context.Background(), config.Stock{Code: "hk00700", Name: "腾讯控股"}, "2025-10-30", "2026-10-30", "qfq")
	if err == nil || !strings.Contains(err.Error(), "invalid_hk_daily_row") {
		t.Fatalf("invalid OHLC not rejected: %v", err)
	}
}

func TestHKDailyClientIgnoresNegativeQFQPricesBeforeRequestedWindow(t *testing.T) {
	fixture := &hkDailyFixture{mutate: func(_ int, code, adjust string, bars []Bar) []Bar {
		if code == "hk00700" && adjust == "qfq" {
			outside := Bar{Date: "2025-10-28", Open: -10, Close: -9, High: -8, Low: -11, Volume: 1000}
			return append([]Bar{outside}, bars...)
		}
		return bars
	}}
	server := fixture.server(t)
	defer server.Close()
	client := NewHKDailyClient(2*time.Second, time.Millisecond, server.URL)
	qfq, err := client.Fetch(context.Background(), config.Stock{Code: "hk00700", Name: "腾讯控股"}, "2025-10-30", "2026-10-30", "qfq")
	if err != nil {
		t.Fatal(err)
	}
	if len(qfq.Bars) == 0 || qfq.Bars[0].Date != "2025-10-30" {
		t.Fatalf("unexpected QFQ window: %+v", qfq.Bars)
	}
	for _, bar := range qfq.Bars {
		if bar.Date < "2025-10-30" {
			t.Fatalf("out-of-window bar was retained: %+v", bar)
		}
	}
}

func TestHKDailyClientDerivesTradingDaysFromHangSengIndex(t *testing.T) {
	fixture := &hkDailyFixture{hsiDays: []string{"2026-10-28", "2026-10-30", "2026-11-02"}}
	server := fixture.server(t)
	defer server.Close()
	client := NewHKDailyClient(2*time.Second, time.Millisecond, server.URL)
	days, err := client.TradingDays(context.Background(), "2026-10-27", "2026-11-02")
	if err != nil {
		t.Fatal(err)
	}
	if !sort.StringsAreSorted(days) || len(days) != 3 || days[2] != "2026-11-02" {
		t.Fatalf("unexpected HK sessions: %v", days)
	}
}
