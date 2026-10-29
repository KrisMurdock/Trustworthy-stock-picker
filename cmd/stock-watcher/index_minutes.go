package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/marketforecast"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type indexMinuteSyncResult struct {
	Command      string `json:"command"`
	AsOf         string `json:"as_of"`
	From         string `json:"from"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	Bars         int    `json:"bars"`
	First        string `json:"first,omitempty"`
	Last         string `json:"last,omitempty"`
	CoverageDays int    `json:"coverage_days"`
	Status       string `json:"status"`
}

func runSyncIndexMinutes(ctx context.Context, c config.Config, asOf, from string, log *logging.Logger) error {
	cal := calendar.New(c)
	if asOf == "" {
		completed, err := cal.Completed(time.Now())
		if err != nil {
			return err
		}
		asOf = completed
	} else {
		trading, err := cal.Trading(asOf)
		if err != nil || !trading {
			return errors.New("index minute sync as-of must be a configured trading day")
		}
		completed, err := cal.Completed(time.Now())
		if err != nil {
			return err
		}
		if asOf > completed {
			return errors.New("index minute sync as-of must be a completed trading day")
		}
	}
	if from == "" {
		days, err := cal.TradingDays(cal.ValidFrom(), asOf)
		if err != nil || len(days) == 0 {
			return errors.New("cannot determine index minute sync range")
		}
		first := 0
		if len(days) > 100 {
			first = len(days) - 100
		}
		from = days[first]
	} else {
		days, err := cal.TradingDays(from, asOf)
		if err != nil || len(days) == 0 {
			return errors.New("index minute sync range must use configured trading days")
		}
		from = days[0]
	}
	db, err := storage.InitContext(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer db.Close()
	instruments, err := db.LoadInstruments(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(instruments))
	for _, item := range instruments {
		known[item.Code] = true
	}
	client := market.NewSinaKLineClient(config.Duration(c.Timeout), config.Duration(c.Market.RequestInterval))
	client.Endpoint = c.Market.SinaDailyEndpoint
	defer client.Close()
	results := make([]indexMinuteSyncResult, 0, len(marketforecast.DefaultIndices()))
	for _, index := range marketforecast.DefaultIndices() {
		if !known[index.Code] {
			if err := db.SaveInstrument(ctx, storage.ContextInstrument{
				Code: index.Code, Name: index.Name, Kind: "index", Category: "index", Source: "sina", UpdatedAt: time.Now().In(market.Shanghai),
			}, nil); err != nil {
				return err
			}
		}
		archive, err := client.FetchMinuteRange(ctx, config.Stock{Code: index.Code, Name: index.Name}, from, asOf)
		if err != nil {
			return fmt.Errorf("fetch %s index minutes: %w", index.Code, err)
		}
		if len(archive.Bars) == 0 {
			return fmt.Errorf("no %s index minutes in %s..%s", index.Code, from, asOf)
		}
		if err := db.SaveMinutes(ctx, index.Code, archive.Bars); err != nil {
			return err
		}
		days := map[string]int{}
		for _, bar := range archive.Bars {
			days[bar.End.In(market.Shanghai).Format("2006-01-02")]++
		}
		completeDays := 0
		for _, count := range days {
			if count == 48 {
				completeDays++
			}
		}
		results = append(results, indexMinuteSyncResult{
			Command: "sync-index-minutes", AsOf: asOf, From: from, Code: index.Code, Name: index.Name,
			Bars: len(archive.Bars), First: archive.Bars[0].End.Format("2006-01-02 15:04"), Last: archive.Bars[len(archive.Bars)-1].End.Format("2006-01-02 15:04"),
			CoverageDays: completeDays, Status: "ok",
		})
	}
	if log != nil {
		log.Info("Index minute synchronization completed", "as_of", asOf, "from", from, "indices", len(results))
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"command": "sync-index-minutes", "status": "ok", "as_of": asOf, "from": from, "results": results})
}
