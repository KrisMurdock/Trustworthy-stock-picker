package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

func hkMinuteFixture(day string) []MinuteBar {
	date, _ := time.ParseInLocation("2006-01-02", day, Shanghai)
	bars := make([]MinuteBar, 0, 66)
	for _, session := range [][2]int{{570, 720}, {780, 960}} {
		for minute := session[0] + 5; minute <= session[1]; minute += 5 {
			price := 100 + float64(len(bars))
			bars = append(bars, MinuteBar{
				End:  time.Date(date.Year(), date.Month(), date.Day(), minute/60, minute%60, 0, 0, Shanghai),
				Open: price, High: price + 2, Low: price - 1, Close: price + 1, Volume: float64(len(bars) + 1), Final: true,
			})
		}
	}
	return bars
}

func TestHKMinuteAggregationUsesTradingMinutesAcrossLunch(t *testing.T) {
	bars := hkMinuteFixture("2026-11-02")
	observed := bars[len(bars)-1].End.Add(15 * time.Minute)
	for period, want := range map[int]int{5: 66, 15: 22, 30: 11, 60: 6} {
		got := AggregateHKMinutes(bars, period, observed, nil)
		if len(got) != want {
			t.Fatalf("period %d: got %d bars, want %d", period, len(got), want)
		}
	}
	hourly := AggregateHKMinutes(bars, 60, observed, nil)
	for index, want := range []string{"10:30", "11:30", "13:30", "14:30", "15:30", "16:00"} {
		if got := hourly[index].End.In(Shanghai).Format("15:04"); got != want {
			t.Fatalf("hour %d ends at %s, want %s", index, got, want)
		}
	}
	if hourly[2].Open != bars[24].Open || hourly[2].Close != bars[35].Close || hourly[2].TradingMinutes != 60 {
		t.Fatalf("lunch-spanning candle is incorrect: %+v", hourly[2])
	}
	if hourly[5].TradingMinutes != 30 || !hourly[5].Final {
		t.Fatalf("last candle must be the final 30 trading minutes: %+v", hourly[5])
	}
}

func TestHKMinuteAggregationHandlesEarlyCloseGapsAndFormingBars(t *testing.T) {
	bars := hkMinuteFixture("2026-12-24")
	early := map[string]bool{"2026-12-24": true}
	hourly := AggregateHKMinutes(bars[:30], 60, bars[29].End.Add(15*time.Minute), early)
	if len(hourly) != 3 || hourly[2].End.Format("15:04") != "12:00" || hourly[2].TradingMinutes != 30 || !hourly[2].Final {
		t.Fatalf("early close was not finalized correctly: %+v", hourly)
	}
	unknown := AggregateHKMinutes(bars[:30], 60, bars[29].End.Add(5*time.Hour), nil)
	if len(unknown) != 2 {
		t.Fatalf("unknown early close incorrectly finalized: %+v", unknown)
	}
	withGap := append(append([]MinuteBar(nil), bars[:26]...), bars[27:]...)
	hourly = AggregateHKMinutes(withGap, 60, bars[65].End.Add(15*time.Minute), nil)
	for _, bar := range hourly {
		if bar.End.Format("15:04") == "13:30" {
			t.Fatal("an incomplete historical group was fabricated")
		}
	}
	observed := time.Date(2026, 12, 24, 13, 17, 0, 0, Shanghai)
	hourly = AggregateHKMinutes(bars[:33], 60, observed, nil)
	last := hourly[len(hourly)-1]
	if last.End.Format("15:04") != "13:30" || last.Final || last.TradingMinutes != 45 {
		t.Fatalf("forming candle: %+v", last)
	}
}

func TestHKMinuteClientValidatesProtocol(t *testing.T) {
	validRows := []string{
		"2026-11-02 11:55,100,101,102,99,12,1000,1,2,3,4",
		"2026-11-02 12:00,101,102,103,100,13,1000,1,2,3,4",
		"2026-11-02 13:05,102,103,104,101,14,1000,1,2,3,4",
	}
	tests := []struct {
		name   string
		code   string
		market int
		rows   []string
		status int
		valid  bool
	}{
		{"valid", "00700", 116, validRows, 200, true},
		{"wrong code", "00701", 116, validRows, 200, false},
		{"wrong market", "00700", 1, validRows, 200, false},
		{"empty", "00700", 116, nil, 200, false},
		{"duplicate", "00700", 116, []string{validRows[0], validRows[0]}, 200, false},
		{"lunch", "00700", 116, []string{"2026-11-02 12:05,100,101,102,99,12,1000,1,2,3,4"}, 200, false},
		{"wrong day", "00700", 116, []string{"2026-10-30 10:00,100,101,102,99,12,1000,1,2,3,4"}, 200, false},
		{"invalid ohlc", "00700", 116, []string{"2026-11-02 10:00,100,101,99,98,12,1000,1,2,3,4"}, 200, false},
		{"invalid volume", "00700", 116, []string{"2026-11-02 10:00,100,101,102,99,-1,1000,1,2,3,4"}, 200, false},
		{"forbidden", "00700", 116, nil, 403, false},
		{"rate limited", "00700", 116, nil, 429, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query()
				if query.Get("secid") != "116.00700" || query.Get("klt") != "5" || query.Get("fqt") != "0" || query.Get("beg") != "20261102" || query.Get("end") != "20261102" {
					t.Errorf("unexpected query: %s", r.URL.RawQuery)
				}
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"code": test.code, "market": test.market, "name": "腾讯控股", "klines": test.rows}})
			}))
			defer server.Close()
			client := NewHKMinuteClient(time.Second, time.Millisecond, server.URL)
			defer client.Close()
			client.Now = func() time.Time { return time.Date(2026, 11, 2, 13, 10, 0, 0, Shanghai) }
			history, err := client.FetchMinutes(context.Background(), config.Stock{Code: "hk00700"}, "2026-11-02", "2026-11-02")
			if (err == nil) != test.valid {
				t.Fatalf("history=%+v err=%v", history, err)
			}
			if test.valid && (history.Source != "eastmoney_hk" || len(history.Bars) != 3 || !history.Bars[2].Final) {
				t.Fatalf("unexpected history: %+v", history)
			}
			if test.status != 200 {
				var failure *Failure
				if !errors.As(err, &failure) || failure.Status != test.status {
					t.Fatalf("HTTP classification lost: %v", err)
				}
			}
		})
	}
}

func TestHKMinuteClientBoundsAndCancellation(t *testing.T) {
	started := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewHKMinuteClient(30*time.Millisecond, time.Millisecond, server.URL)
	defer client.Close()
	client.Now = func() time.Time { return time.Date(2026, 11, 2, 16, 15, 0, 0, Shanghai) }
	stock := config.Stock{Code: "hk00700"}
	if _, err := client.FetchMinutes(context.Background(), stock, "2026-01-01", "2026-11-02"); err == nil {
		t.Fatal("unbounded request accepted")
	}
	if _, err := client.FetchMinutes(context.Background(), config.Stock{Code: "sz000001"}, "2026-11-02", "2026-11-02"); err == nil {
		t.Fatal("non-HK code accepted")
	}
	if _, err := client.FetchMinutes(context.Background(), stock, "2026-11-02", "2026-11-02"); err == nil {
		t.Fatal("timeout accepted")
	}
	<-started
	client.HTTP.Timeout = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.FetchMinutes(ctx, stock, "2026-11-02", "2026-11-02")
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lost cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked")
	}
}

func TestHKMinuteValidationRejectsFutureAndBadMetadata(t *testing.T) {
	bars := hkMinuteFixture("2026-11-02")
	history := HKMinuteHistory{Version: 1, Code: "hk00700", Source: "eastmoney_hk", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: bars[len(bars)-1].End.Add(15 * time.Minute), Bars: bars}
	if err := history.ValidateBase(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*HKMinuteHistory){
		"foreign market": func(h *HKMinuteHistory) { h.Code = "sz000001" },
		"wrong source":   func(h *HKMinuteHistory) { h.Source = "tencent" },
		"wrong period":   func(h *HKMinuteHistory) { h.Period = 30 },
		"future":         func(h *HKMinuteHistory) { h.FetchedAt = bars[0].End },
		"empty":          func(h *HKMinuteHistory) { h.Bars = nil },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := history
			mutate(&candidate)
			if err := candidate.ValidateBase(); err == nil {
				t.Fatal("invalid history accepted")
			}
		})
	}
	if slot, ok := HKMinuteSlot(bars[29].End); !ok || slot != 29 {
		t.Fatal(fmt.Sprintf("invalid noon slot %d/%t", slot, ok))
	}
	if _, ok := HKMinuteSlot(bars[29].End.Add(5 * time.Minute)); ok {
		t.Fatal("lunch break accepted")
	}
	if !strings.Contains(fmt.Sprint(history.Bars[0].End), "2026-11-02") {
		t.Fatal("fixture date mismatch")
	}
}
