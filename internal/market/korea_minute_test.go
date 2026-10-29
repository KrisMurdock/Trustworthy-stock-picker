package market

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNaverKoreaMinuteClientParsesFinalBars(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/KOSPI/minute" || r.URL.Query().Get("startDateTime") == "" {
			http.Error(w, "bad minute request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`[{"localDateTime":"20261020090000","currentPrice":2600.1,"openPrice":2599.5,"highPrice":2601.2,"lowPrice":2598.8,"accumulatedTradingVolume":100},{"localDateTime":"20261020090100","currentPrice":2602.2,"openPrice":2600.1,"highPrice":2603.4,"lowPrice":2599.9,"accumulatedTradingVolume":120}]`))
	}))
	defer server.Close()
	client := NewNaverKoreaMinuteClient(5*time.Second, 0)
	client.Endpoint = server.URL
	client.Now = func() time.Time {
		return time.Date(2026, 10, 20, 16, 0, 0, 0, Korea)
	}
	from := time.Date(2026, 10, 13, 0, 0, 0, 0, Korea)
	through := time.Date(2026, 10, 20, 16, 0, 0, 0, Korea)
	bars, err := client.FetchMinutes(context.Background(), KoreaKOSPI, from, through)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 || bars[1].End.Format("15:04") != "09:01" || bars[1].Close != 2602.2 || !bars[1].Final {
		t.Fatalf("unexpected korea minutes: %+v", bars)
	}
}

func TestNaverKoreaMinuteClientRejectsIncompleteLatestBar(t *testing.T) {
	raw := []byte(`[{"localDateTime":"20261020090000","currentPrice":2600.1,"openPrice":2599.5,"highPrice":2601.2,"lowPrice":2598.8,"accumulatedTradingVolume":100}]`)
	observed := time.Date(2026, 10, 20, 9, 0, 30, 0, Korea)
	if _, err := parseNaverKoreaMinutes(raw, time.Date(2026, 10, 20, 0, 0, 0, 0, Korea), time.Date(2026, 10, 20, 16, 0, 0, 0, Korea), observed); err == nil {
		t.Fatal("incomplete latest minute was accepted")
	}
}
