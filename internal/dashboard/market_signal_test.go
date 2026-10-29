package dashboard

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/marketforecast"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func seedMarketSignalHistory(t *testing.T, root, asOf string) {
	t.Helper()
	db, err := storage.InitContext(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	through, _ := time.ParseInLocation("2006-01-02", asOf, time.UTC)
	days := []time.Time{}
	for day := through.AddDate(0, 0, -330); !day.After(through); day = day.AddDate(0, 0, 1) {
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			days = append(days, day)
		}
	}
	at := through.Add(10 * time.Hour)
	for offset, index := range marketforecast.DefaultIndices() {
		close := 3000.0 + float64(offset)*500
		bars := make([]storage.ContextBar, 0, len(days))
		for i, day := range days {
			close *= 1 + 0.0007 + math.Sin(float64(i+offset)/9)*0.001
			bars = append(bars, storage.ContextBar{
				Code: index.Code, Date: day.Format("2006-01-02"), Open: close * 0.998,
				Close: close, High: close * 1.003, Low: close * 0.997, Volume: 1000,
			})
		}
		if err := db.SaveInstrument(context.Background(), storage.ContextInstrument{
			Code: index.Code, Name: index.Name, Kind: "index", Category: "index", Source: "tencent", UpdatedAt: at,
		}, bars); err != nil {
			t.Fatal(err)
		}
		if err := db.SaveMinutes(context.Background(), index.Code, sessionMinutes(bars, offset)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveSyncStatus(context.Background(), storage.ContextSyncStatus{
		AsOf: asOf, StartedAt: at.Add(-time.Minute), FinishedAt: at, Status: "complete", Total: 2, Succeeded: 2,
	}); err != nil {
		t.Fatal(err)
	}
}

func sessionMinutes(bars []storage.ContextBar, offset int) []market.MinuteBar {
	out := make([]market.MinuteBar, 0, len(bars)*48)
	for index := 1; index < len(bars); index++ {
		day, _ := time.ParseInLocation("2006-01-02", bars[index].Date, market.Shanghai)
		prevClose := bars[index-1].Close
		morningClose := prevClose * (1 + math.Sin(float64(index+offset)/5)*0.002)
		close := morningClose * (1 + math.Cos(float64(index+offset)/7)*0.002)
		for _, start := range []int{570, 780} {
			for minute := start + 5; minute <= start+120; minute += 5 {
				end := time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, market.Shanghai)
				progress := float64(minute-start) / 120
				price := prevClose + (morningClose-prevClose)*progress
				if start == 780 {
					price = morningClose + (close-morningClose)*progress
				}
				out = append(out, market.MinuteBar{End: end, Open: price * 0.9995, High: price * 1.0005, Low: price * 0.999, Close: price, Volume: 1000, Final: true})
			}
		}
	}
	return out
}

func TestMarketSignalAPIReadsIndexForecast(t *testing.T) {
	root := t.TempDir()
	asOf := "2026-10-13"
	seedMarketSignalHistory(t, root, asOf)
	c := config.Defaults()
	c.DataDir = root
	c.RequireCIFS = false
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	h := newHandler(c, &testReader{}, nil)

	w := request(t, h, http.MethodGet, "/api/market-signal?as_of="+asOf)
	if w.Code != http.StatusOK {
		t.Fatalf("market signal: %d %s", w.Code, w.Body)
	}
	var report marketforecast.Report
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != marketforecast.SchemaVersion || report.AsOf != asOf || report.Status != "ok" || len(report.Signals) != 2 {
		t.Fatalf("unexpected report: %s", w.Body)
	}
	for _, signal := range report.Signals {
		if signal.Status != "ok" || signal.TargetDate != "2026-10-14" || signal.Direction == "" || len(signal.Weekdays) != 5 {
			t.Fatalf("unexpected signal: %+v", signal)
		}
		if signal.Validation.Samples != marketforecast.MinValidationSample || signal.Training.Samples == 0 {
			t.Fatalf("unexpected evidence: %+v", signal)
		}
		if len(signal.Sessions) != 2 {
			t.Fatalf("session signals missing: %+v", signal)
		}
		for _, session := range signal.Sessions {
			if session.Status != "ok" || session.Validation.Samples != marketforecast.SessionMinValidationSample || len(session.Weekdays) != 5 {
				t.Fatalf("unexpected session signal: %+v", session)
			}
		}
	}
	if w := request(t, h, http.MethodGet, "/api/market-signal"); w.Code != http.StatusOK {
		t.Fatalf("default as-of: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{
		"/api/market-signal?as_of=bad", "/api/market-signal?as_of=2026-10-13&as_of=2026-10-12", "/api/market-signal?x=1",
	} {
		if w := request(t, h, http.MethodGet, path); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid query accepted: %s %d", path, w.Code)
		}
	}
	if w := request(t, h, http.MethodPost, "/api/market-signal"); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("write method accepted: %d", w.Code)
	}
}

func TestMarketSignalAPIMissingDatabase(t *testing.T) {
	c := config.Defaults()
	c.DataDir = t.TempDir()
	c.RequireCIFS = false
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	h := newHandler(c, &testReader{}, nil)
	if w := request(t, h, http.MethodGet, "/api/market-signal"); w.Code != http.StatusNotFound {
		t.Fatalf("missing context database: %d %s", w.Code, w.Body)
	}
}
