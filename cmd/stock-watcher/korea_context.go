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
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type koreaSyncResult struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Bars  int    `json:"bars"`
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
}

func runSyncKoreaContext(ctx context.Context, c config.Config, asOf, from string, log *logging.Logger) error {
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
			return errors.New("korea context as-of must be a configured trading day")
		}
		completed, err := cal.Completed(time.Now())
		if err != nil {
			return err
		}
		if asOf > completed {
			return errors.New("korea context as-of must be a completed trading day")
		}
	}
	if from == "" {
		start, err := time.Parse("2006-01-02", asOf)
		if err != nil {
			return errors.New("cannot determine korea context range")
		}
		from = start.AddDate(-3, 0, 0).Format("2006-01-02")
	} else {
		start, err := time.Parse("2006-01-02", from)
		if err != nil || start.Format("2006-01-02") != from || from > asOf {
			return errors.New("korea context from must be a valid date not after as-of")
		}
	}
	db, err := storage.InitContext(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer db.Close()
	client := market.NewNaverKoreaClient(config.Duration(c.Timeout), config.Duration(c.MarketContext.RequestInterval))
	client.Endpoint = c.MarketContext.KoreaEndpoint
	defer client.Close()
	results := make([]koreaSyncResult, 0, 2)
	for _, item := range []struct {
		code string
		name string
	}{
		{market.KoreaKOSPI, "韩国综合指数"},
		{market.KoreaKOSDAQ, "韩国创业板指数"},
	} {
		bars, err := client.FetchDaily(ctx, item.code, from, asOf)
		if err != nil {
			return fmt.Errorf("fetch %s daily history: %w", item.code, err)
		}
		if err := db.SaveKoreaBars(ctx, item.code, bars); err != nil {
			return err
		}
		results = append(results, koreaSyncResult{
			Code: item.code, Name: item.name, Bars: len(bars),
			First: bars[0].Date, Last: bars[len(bars)-1].Date,
		})
	}
	if log != nil {
		log.Info("Korean index context synchronization completed", "as_of", asOf, "from", from, "indices", len(results))
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"command": "sync-korea-context", "status": "ok", "as_of": asOf, "from": from, "results": results,
	})
}
