package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

func sinaKLineServer(t *testing.T, rows map[string][]sinaKLineRow) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		symbol := r.URL.Query().Get("symbol")
		values, ok := rows[symbol]
		if !ok {
			http.Error(w, "unknown symbol", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(values); err != nil {
			t.Fatal(err)
		}
	}))
}

func dailyRows(days ...string) []sinaKLineRow {
	rows := make([]sinaKLineRow, 0, len(days))
	for _, day := range days {
		rows = append(rows, sinaKLineRow{Day: day})
	}
	return rows
}

func TestSinaStatusDerivesSuspensionFromCompleteDailyHistory(t *testing.T) {
	server := sinaKLineServer(t, map[string][]sinaKLineRow{
		"sh000001": dailyRows("2026-10-12", "2026-10-13", "2026-10-14", "2026-10-15", "2026-10-16"),
		"sz000001": dailyRows("2026-10-12", "2026-10-13", "2026-10-15", "2026-10-16"),
	})
	defer server.Close()
	client := NewSinaKLineClient(time.Second, 0)
	client.Endpoint = server.URL
	window, err := client.FetchStatus(context.Background(), config.Stock{Code: "sz000001"}, "2026-10-12", "2026-10-16")
	if err != nil {
		t.Fatal(err)
	}
	if !window.Full || window.Source != "sina_daily" || len(window.Statuses) != 5 {
		t.Fatalf("unexpected window: %+v", window)
	}
	states := map[string]SessionState{}
	for _, status := range window.Statuses {
		states[status.Date] = status.State
	}
	if states["2026-10-14"] != SessionSuspended {
		t.Fatalf("missing daily bar was not marked suspended: %+v", states)
	}
	for _, day := range []string{"2026-10-12", "2026-10-13", "2026-10-15", "2026-10-16"} {
		if states[day] != SessionTraded {
			t.Fatalf("traded day %s has state %s", day, states[day])
		}
	}
}

func TestSinaStatusRejectsShortCalendarAndDuplicateHistory(t *testing.T) {
	server := sinaKLineServer(t, map[string][]sinaKLineRow{
		"sh000001": dailyRows("2026-10-12", "2026-10-13"),
		"sz000001": dailyRows("2026-10-12", "2026-10-13"),
	})
	defer server.Close()
	client := NewSinaKLineClient(time.Second, 0)
	client.Endpoint = server.URL
	if _, err := client.FetchStatus(context.Background(), config.Stock{Code: "sz000001"}, "2026-10-12", "2026-10-16"); err == nil {
		t.Fatal("short index calendar was accepted")
	}

	server.Close()
	server = sinaKLineServer(t, map[string][]sinaKLineRow{
		"sh000001": dailyRows("2026-10-12", "2026-10-13"),
		"sz000001": dailyRows("2026-10-12", "2026-10-12"),
	})
	defer server.Close()
	client = NewSinaKLineClient(time.Second, 0)
	client.Endpoint = server.URL
	if _, err := client.FetchStatus(context.Background(), config.Stock{Code: "sz000001"}, "2026-10-12", "2026-10-13"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate daily history was accepted: %v", err)
	}
}

func TestSinaMinuteRangeParsesNativeFiveMinuteBars(t *testing.T) {
	server := sinaKLineServer(t, map[string][]sinaKLineRow{
		"sh600036": {
			{Day: "2026-10-20 09:35:00", Open: "42.00", High: "42.10", Low: "41.90", Close: "42.05", Volume: "120000"},
			{Day: "2026-10-20 09:40:00", Open: "42.05", High: "42.20", Low: "42.00", Close: "42.15", Volume: "90000"},
			{Day: "2026-10-20 09:45:00", Open: "42.15", High: "42.30", Low: "42.10", Close: "42.25", Volume: "80000"},
		},
	})
	defer server.Close()
	client := NewSinaKLineClient(time.Second, 0)
	client.Endpoint = server.URL
	archive, err := client.FetchMinuteRange(context.Background(), config.Stock{Code: "sh600036"}, "2026-10-20", "2026-10-20")
	if err != nil {
		t.Fatal(err)
	}
	if archive.Source != "sina" || archive.VolumeUnit != "shares" || len(archive.Bars) != 3 {
		t.Fatalf("unexpected archive: %+v", archive)
	}
	if archive.Bars[2].End.Format("15:04") != "09:45" || archive.Bars[2].Volume != 80000 {
		t.Fatalf("unexpected minute bar: %+v", archive.Bars[2])
	}
	if err := archive.Validate(); err != nil {
		t.Fatalf("archive validation: %v", err)
	}
}

func TestSinaMinuteRangeRejectsUnorderedBars(t *testing.T) {
	server := sinaKLineServer(t, map[string][]sinaKLineRow{
		"sh600036": {
			{Day: "2026-10-20 09:40:00", Open: "42.05", High: "42.20", Low: "42.00", Close: "42.15", Volume: "90000"},
			{Day: "2026-10-20 09:35:00", Open: "42.00", High: "42.10", Low: "41.90", Close: "42.05", Volume: "120000"},
		},
	})
	defer server.Close()
	client := NewSinaKLineClient(time.Second, 0)
	client.Endpoint = server.URL
	if _, err := client.FetchMinuteRange(context.Background(), config.Stock{Code: "sh600036"}, "2026-10-20", "2026-10-20"); err == nil {
		t.Fatal("unordered minute bars were accepted")
	}
}

func TestSinaMinuteRangeAcceptsIndexCodes(t *testing.T) {
	server := sinaKLineServer(t, map[string][]sinaKLineRow{
		"sh000001": {
			{Day: "2026-10-20 09:35:00", Open: "3900.00", High: "3901.00", Low: "3899.00", Close: "3900.50", Volume: "120000"},
			{Day: "2026-10-20 09:40:00", Open: "3900.50", High: "3902.00", Low: "3900.00", Close: "3901.50", Volume: "90000"},
		},
		"sz399006": {
			{Day: "2026-10-20 09:35:00", Open: "2600.00", High: "2601.00", Low: "2599.00", Close: "2600.50", Volume: "120000"},
		},
	})
	defer server.Close()
	client := NewSinaKLineClient(time.Second, 0)
	client.Endpoint = server.URL
	for _, code := range []string{"sh000001", "sz399006"} {
		archive, err := client.FetchMinuteRange(context.Background(), config.Stock{Code: code}, "2026-10-20", "2026-10-20")
		if err != nil {
			t.Fatalf("%s index minutes: %v", code, err)
		}
		if archive.Code != code || len(archive.Bars) == 0 {
			t.Fatalf("unexpected %s archive: %+v", code, archive)
		}
	}
}

func TestSinaSessionRangeReadsThirtyMinuteBars(t *testing.T) {
	rows := make([]sinaKLineRow, 0, 8)
	for _, minute := range []int{600, 630, 660, 690, 810, 840, 870, 900} {
		end := time.Date(2026, 10, 20, minute/60, minute%60, 0, 0, Shanghai)
		rows = append(rows, sinaKLineRow{Day: end.Format("2006-01-02 15:04:05"), Open: "100", High: "102", Low: "99", Close: "101", Volume: "1000"})
	}
	server := sinaKLineServer(t, map[string][]sinaKLineRow{"sh000001": rows})
	defer server.Close()
	client := NewSinaKLineClient(time.Second, 0)
	client.Endpoint = server.URL
	archive, err := client.FetchSessionRange(context.Background(), config.Stock{Code: "sh000001"}, "2026-10-20", "2026-10-20")
	if err != nil {
		t.Fatal(err)
	}
	if archive.Period != 30 || archive.BasePeriod != 30 || len(archive.Bars) != 8 || !archive.Bars[0].End.Equal(time.Date(2026, 10, 20, 10, 0, 0, 0, Shanghai)) {
		t.Fatalf("unexpected session archive: %+v", archive)
	}
}
