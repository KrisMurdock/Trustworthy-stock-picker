package market

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNaverKoreaDailyParsing(t *testing.T) {
	body := "\n[['날짜', '시가', '고가', '저가', '종가', '거래량', '외국인소진율'],\n\n\t\t\n" +
		"[\"20261016\", 2600.0, 2620.0, 2590.0, 2610.0, 1000, 0.0],\n" +
		"[\"20261019\", 2611.0, 2630.0, 2600.0, 2625.0, 1200, 0.0],\n]\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") != KoreaKOSPI || r.URL.Query().Get("timeframe") != "day" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client := NewNaverKoreaClient(time.Second, 0)
	client.Endpoint = server.URL
	bars, err := client.FetchDaily(context.Background(), KoreaKOSPI, "2026-10-16", "2026-10-19")
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 || bars[0].Date != "2026-10-16" || bars[1].Close != 2625 || bars[1].Volume != 1200 {
		t.Fatalf("unexpected korea bars: %+v", bars)
	}
}

func TestNaverKoreaDailyRejectsBadRowsAndCodes(t *testing.T) {
	client := NewNaverKoreaClient(time.Second, 0)
	if _, err := client.FetchDaily(context.Background(), "NIKKEI", "2026-10-16", "2026-10-19"); err == nil {
		t.Fatal("unknown korea code was accepted")
	}
	if _, err := parseNaverKoreaDaily([]byte("[['날짜'],['20261016', 1, 1, 1, 0, 1, 0]]"), "2026-10-16", "2026-10-19"); err == nil {
		t.Fatal("impossible korea close was accepted")
	}
}
