package main

import (
	"context"
	"errors"
	"time"

	"stock-watcher/internal/calendar"
	"stock-watcher/internal/config"
	"stock-watcher/internal/experiment"
	"stock-watcher/internal/market"
	"stock-watcher/internal/storage"
)

// ensureSnapshotCalendar attaches a provider-backed or cached exchange calendar
// to a newly created experiment snapshot. Configured calendars are accepted only
// when they already cover the whole saved window; they are always labelled.
func ensureSnapshotCalendar(ctx context.Context, c config.Config, snapshot experiment.Snapshot) (experiment.Snapshot, error) {
	from, through, ok := experiment.SnapshotRange(snapshot)
	if !ok {
		return snapshot, errors.New("experiment snapshot has no saved market window")
	}
	if snapshot.Calendar.Version != 0 && snapshot.Calendar.From <= from && snapshot.Calendar.Through >= through && len(snapshot.Calendar.Days) > 0 {
		return snapshot, nil
	}
	cache, err := storage.OpenCalendar(c.DataDir, c.RequireCIFS)
	if err != nil {
		return snapshot, err
	}
	defer cache.Close()
	if frozen, freezeErr := cache.Freeze(ctx, from, through); freezeErr == nil {
		return experiment.WithTradingCalendar(snapshot, frozen)
	} else {
		var coverageErr *calendar.CoverageError
		if !errors.As(freezeErr, &coverageErr) {
			return snapshot, freezeErr
		}
	}
	history, err := fetchTradingCalendar(ctx, c, from, through)
	if err != nil {
		return snapshot, err
	}
	if err := cache.Save(ctx, history); err != nil {
		return snapshot, err
	}
	return experiment.WithTradingCalendar(snapshot, history)
}

func fetchTradingCalendar(ctx context.Context, c config.Config, from, through string) (calendar.History, error) {
	timeout, interval := config.Duration(c.Timeout), config.Duration(c.Market.RequestInterval)
	providers := []calendar.Provider{}
	baostock := market.NewBaoStockClient(timeout, interval)
	baostock.Address = c.Market.BaostockAddress
	providers = append(providers, calendar.Provider{Name: calendar.SourceBaoStock, Source: baostock})
	if c.Market.TushareToken != "" {
		tushare := market.NewTushareClient(timeout, interval, c.Market.TushareToken)
		tushare.Endpoint = c.Market.TushareEndpoint
		providers = append(providers, calendar.Provider{Name: calendar.SourceTushare, Source: tushare})
	}
	sources := calendar.Sources{Providers: providers, Now: time.Now}
	return sources.Fetch(ctx, from, through)
}
