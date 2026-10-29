package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/marketforecast"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func seedCLIMarketSignal(t *testing.T, data, asOf string) {
	t.Helper()
	db, err := storage.InitContext(data, false)
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
			close *= 1 + 0.0006 + math.Sin(float64(i+offset)/8)*0.0012
			bars = append(bars, storage.ContextBar{
				Code: index.Code, Date: day.Format("2006-01-02"), Open: close, Close: close,
				High: close * 1.002, Low: close * 0.998, Volume: 1000,
			})
		}
		if err := db.SaveInstrument(context.Background(), storage.ContextInstrument{
			Code: index.Code, Name: index.Name, Kind: "index", Category: "index", Source: "tencent", UpdatedAt: at,
		}, bars); err != nil {
			t.Fatal(err)
		}
		if err := db.SaveMinutes(context.Background(), index.Code, cliSessionMinutes(bars, offset)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveSyncStatus(context.Background(), storage.ContextSyncStatus{
		AsOf: asOf, StartedAt: at.Add(-time.Minute), FinishedAt: at, Status: "complete", Total: 2, Succeeded: 2,
	}); err != nil {
		t.Fatal(err)
	}
}

func cliSessionMinutes(bars []storage.ContextBar, offset int) []market.MinuteBar {
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

func TestMarketSignalCLIIsDeterministicAndReadOnly(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	useBuiltinConfig(t, data, "sz000001")
	asOf := "2026-10-13"
	seedCLIMarketSignal(t, data, asOf)
	before, err := os.ReadFile(filepath.Join(data, "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(before)
	args := []string{"market-signal", "-as-of", asOf}
	first, err := captureStdout(t, func() error { return run(args) })
	if err != nil {
		t.Fatalf("market-signal: %v", err)
	}
	second, err := captureStdout(t, func() error { return run(args) })
	if err != nil {
		t.Fatalf("market-signal repeat: %v", err)
	}
	if first != second {
		t.Fatal("market-signal output is not deterministic")
	}
	var report marketforecast.Report
	if err := json.Unmarshal([]byte(first), &report); err != nil {
		t.Fatalf("invalid market-signal JSON: %v", err)
	}
	if report.SchemaVersion != marketforecast.SchemaVersion || report.Status != "ok" || len(report.Signals) != 2 {
		t.Fatalf("unexpected market-signal output: %s", first)
	}
	for _, signal := range report.Signals {
		if len(signal.Sessions) != 2 {
			t.Fatalf("session signals missing from CLI output: %+v", signal)
		}
	}
	after, err := os.ReadFile(filepath.Join(data, "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(after) != want {
		t.Fatal("market-signal modified context.db")
	}
}

func TestMarketSignalCLIRequiresContext(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	useBuiltinConfig(t, data, "sz000001")
	if err := run([]string{"market-signal"}); err == nil {
		t.Fatal("market-signal accepted missing context database")
	}
}
