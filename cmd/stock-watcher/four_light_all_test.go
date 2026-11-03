package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func TestFourLightAllCLIRequiresExistingAllMarketDatabase(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	useBuiltinConfig(t, data, "sz000001")
	_, err := captureStdout(t, func() error { return run([]string{"four-light-all", "-as-of", "2026-10-13"}) })
	if err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("missing all-market database error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(data, "all-market.db")); !os.IsNotExist(statErr) {
		t.Fatalf("signal command created an empty all-market database: %v", statErr)
	}
}

func TestFourLightAllCLIWritesWholeMarketArchive(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	useBuiltinConfig(t, data, "sz000001")
	db, err := storage.OpenAllMarket(data, false)
	if err != nil {
		t.Fatal(err)
	}
	stock := config.Stock{Code: "sz000001", Name: "测试银行", ListedFrom: "1991-04-03"}
	history := market.History{Version: 1, Code: stock.Code, Name: stock.Name, Source: "tencent", Adjust: "qfq",
		DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: "2026-10-13", FetchedAt: time.Now().In(market.Shanghai)}
	end, _ := time.ParseInLocation("2006-01-02", "2026-10-13", market.Shanghai)
	dates := []string{}
	for day := end; len(dates) < 80; day = day.AddDate(0, 0, -1) {
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			dates = append(dates, day.Format("2006-01-02"))
		}
	}
	for index := len(dates) - 1; index >= 0; index-- {
		close := 10 + float64(len(dates)-1-index)*0.1
		volume := 1000.0
		if index == 0 {
			volume = 2000
		}
		history.Bars = append(history.Bars, market.Bar{Date: dates[index],
			Open: close - 0.05, High: close + 0.1, Low: close - 0.1, Close: close, Volume: volume})
	}
	if err := db.SaveHistory(context.Background(), stock, history, true); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return run([]string{"four-light-all", "-as-of", "2026-10-13", "-timeout", "1m"}) })
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	out = lines[len(lines)-1]
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil || result["status"] != "complete" || result["total"] != float64(1) {
		t.Fatalf("result = %s, %v", out, err)
	}
	view, err := storage.OpenAllMarketView(data, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	summary, err := view.LatestFourLightSummary(context.Background(), "2026-10-13", "four_light_approx_v1", 1, storage.DefaultFourLightPageSize)
	if err != nil || summary.Run.Status != "complete" || summary.Run.AllRed != 1 || len(summary.First) != 1 {
		t.Fatalf("summary = %+v, %v", summary, err)
	}
}

func TestFourLightAllCLIFlagsRemainIndependentFromHKSync(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"four-light-all", "-timeout", "59s"}, "between 1m and 72h"},
		{[]string{"four-light-all", "-timeout", "73h"}, "between 1m and 72h"},
		{[]string{"four-light-all", "-full"}, "full is supported by sync-all and sync-hk-connect"},
		{[]string{"four-light-all", "-workers", "4"}, "workers requires"},
		{[]string{"four-light-all", "-codes", "sz000001"}, "codes requires"},
		{[]string{"sync-hk-minutes", "-timeout", "61m"}, "at most 1h"},
		{[]string{"sync-hk-connect", "-timeout", "59m"}, "between 1h and 72h"},
		{[]string{"sync-all", "-timeout", "59m"}, "between 1h and 72h"},
	} {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			if err := run(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run(%v) = %v, want %q", tc.args, err, tc.want)
			}
		})
	}
}
