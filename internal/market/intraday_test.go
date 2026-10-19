package market

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"stock-watcher/internal/config"
	"testing"
	"time"
)

func minuteAt(s string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04:05", s, Shanghai)
	return t
}
func minuteFixture(end string) MinuteBar {
	return MinuteBar{End: minuteAt("2026-10-15 " + end + ":00"), Open: 10, High: 12, Low: 9, Close: 11, Volume: 100, Final: true}
}

func TestMinuteProtocolAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rows   [][]any
		status int
		valid  bool
	}{
		{"forming", [][]any{{"202610151435", "10", "11", "12", "9", "100"}, {"202610151440", "11", "12", "13", "10", "200"}}, 200, true},
		{"duplicate", [][]any{{"202610151435", 10, 11, 12, 9, 100}, {"202610151435", 10, 11, 12, 9, 100}}, 200, false},
		{"lunch", [][]any{{"202610151200", 10, 11, 12, 9, 100}}, 200, false},
		{"future", [][]any{{"202610151450", 10, 11, 12, 9, 100}}, 200, false},
		{"bad_ohlc", [][]any{{"202610151435", 10, 11, 8, 9, 100}}, 200, false},
		{"null_volume", [][]any{{"202610151435", 10, 11, 12, 9, nil}}, 200, false},
		{"empty", nil, 200, false}, {"permission", nil, 403, false}, {"rate", nil, 429, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("param") != "sz000001,m5,,64" {
					t.Error("unexpected minute query")
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"sz000001": map[string]any{"m5": tc.rows}}})
			}))
			defer server.Close()
			c := NewMinuteClient(config.Defaults())
			defer c.Close()
			c.Endpoint = server.URL
			c.Now = func() time.Time { return minuteAt("2026-10-15 14:37:00") }
			h, err := c.FetchMinutes(context.Background(), config.Stock{Code: "sz000001"}, 64)
			if (err == nil) != tc.valid {
				t.Fatalf("history=%+v err=%v", h, err)
			}
			if tc.valid && (!h.Bars[0].Final || h.Bars[1].Final) {
				t.Fatal("unclosed candle finalized")
			}
			if tc.status != 200 {
				var f *Failure
				if !errors.As(err, &f) || f.Status != tc.status {
					t.Fatalf("lost HTTP classification: %v", err)
				}
			}
		})
	}
}

func TestMinutePairRequiresOneSnapshot(t *testing.T) {
	base := MinuteHistory{Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: minuteAt("2026-10-15 15:10:00")}
	for i := 0; i < 48; i++ {
		minute := 575 + i*5
		if i >= 24 {
			minute = 785 + (i-24)*5
		}
		base.Bars = append(base.Bars, MinuteBar{End: time.Date(2026, 10, 15, minute/60, minute%60, 0, 0, Shanghai), Open: 10, High: 12, Low: 9, Close: 11, Volume: 100, Final: true})
	}
	derived := base
	derived.Period = 30
	derived.Bars = AggregateMinutes(base.Bars, 30, base.FetchedAt)
	if err := ValidateMinutePair(base, derived); err != nil {
		t.Fatal(err)
	}
	if err := base.ValidateBase(); err != nil {
		t.Fatal(err)
	}
	if err := derived.Validate(); err != nil {
		t.Fatal(err)
	}
	other := derived
	other.FetchedAt = other.FetchedAt.Add(time.Minute)
	if err := ValidateMinutePair(base, other); err == nil {
		t.Fatal("different FetchedAt accepted")
	}
	other = derived
	other.Bars = append([]MinuteBar(nil), derived.Bars...)
	other.Bars[0].Close += 0.1
	if err := ValidateMinutePair(base, other); err == nil {
		t.Fatal("relabelled aggregate accepted")
	}
	base.Period = 30
	if err := base.ValidateBase(); err == nil {
		t.Fatal("aggregated series accepted as native base")
	}
}

func TestMinuteCancellationAndTimeout(t *testing.T) {
	started := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { started <- struct{}{}; <-r.Context().Done() }))
	defer server.Close()
	c := NewMinuteClient(config.Defaults())
	defer c.Close()
	c.Endpoint = server.URL
	c.HTTP.Timeout = 40 * time.Millisecond
	if _, err := c.FetchMinutes(context.Background(), config.Stock{Code: "sz000001"}, 64); err == nil {
		t.Fatal("timeout accepted")
	}
	<-started
	c.HTTP.Timeout = time.Second
	c.pacer.interval = 0
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.FetchMinutes(ctx, config.Stock{Code: "sz000001"}, 64); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked")
	}
}

func TestMinuteAggregationSessionsGapsAndProvisional(t *testing.T) {
	bars := []MinuteBar{}
	for _, start := range []int{570, 780} {
		for i := 5; i <= 120; i += 5 {
			b := minuteFixture("09:35")
			b.End = time.Date(2026, 10, 15, (start+i)/60, (start+i)%60, 0, 0, Shanghai)
			b.Close = float64(i) + 10
			b.High = b.Close + 1
			bars = append(bars, b)
		}
	}
	for period, count := range map[int]int{5: 48, 15: 16, 30: 8, 60: 4} {
		got := AggregateMinutes(bars, period, minuteAt("2026-10-15 15:10:00"))
		if len(got) != count {
			t.Fatalf("period%d count%d", period, len(got))
		}
		if got[0].Volume != float64(period/5)*100 || !got[len(got)-1].Final {
			t.Fatal("aggregation lost volume or final status")
		}
		if period == 60 && (got[1].End.Hour() != 11 || got[1].End.Minute() != 30 || got[2].End.Hour() != 14) {
			t.Fatal("aggregation crossed lunch")
		}
	}
	missing := append(append([]MinuteBar{}, bars[:2]...), bars[3:]...)
	if got := AggregateMinutes(missing, 15, minuteAt("2026-10-15 15:10:00")); len(got) != 15 {
		t.Fatal("gap was fabricated")
	}
	partial := append([]MinuteBar{}, bars[:2]...)
	partial[1].Final = false
	got := AggregateMinutes(partial, 15, minuteAt("2026-10-15 09:37:00"))
	if len(got) != 1 || got[0].Final || got[0].End.Minute() != 45 || got[0].Volume != 200 {
		t.Fatalf("bad forming group: %+v", got)
	}
	if got := AggregateMinutes(partial, 15, minuteAt("2026-10-15 10:00:00")); len(got) != 0 {
		t.Fatal("expired incomplete group published")
	}
	if got := AggregateMinutes(partial[1:], 15, minuteAt("2026-10-15 09:37:00")); len(got) != 0 {
		t.Fatal("missing group open fabricated")
	}
	if got := AggregateMinutes(bars, 1, time.Now()); got != nil {
		t.Fatal("one-minute period accepted")
	}
}
