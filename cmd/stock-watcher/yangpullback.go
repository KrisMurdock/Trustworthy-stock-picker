package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/yangpullback"
)

func runYangPullbackStudy(ctx context.Context, c config.Config, asof string, bootstrap int, split float64) error {
	db, err := storage.OpenMarketView(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer db.Close()
	if !db.HasDatabase() {
		return errors.New("study-yang-pullback requires an initialized market database")
	}
	if asof == "" {
		dashboard, err := db.LoadDashboard(ctx, c.Stocks, "qfq")
		if err != nil {
			return err
		}
		asof = dashboard.AsOf
	}
	if asof == "" {
		return errors.New("no saved market data for yang-pullback study")
	}
	adjusted, raw, err := db.LoadHistoriesPair(ctx, c.Stocks, asof, "qfq", true)
	if err != nil {
		return err
	}
	snapshot, err := experiment.NewSnapshot(c, asof, adjusted, raw)
	if err != nil {
		return err
	}
	from, through := asof, snapshot.AsOf
	for _, history := range adjusted {
		if len(history.Bars) > 0 && history.Bars[0].Date < from {
			from = history.Bars[0].Date
		}
	}
	days, calendarErr := db.CalendarDays(ctx, from, through)
	if calendarErr != nil {
		return fmt.Errorf("yang-pullback study requires complete exchange calendar coverage: %w", calendarErr)
	}
	frozen := c
	frozen.Calendar.ValidFrom = from
	frozen.Calendar.ValidThrough = through
	frozen.Calendar.Holidays = nil
	frozen.Calendar.TradingDays = days
	frozen.Calendar.Source = "market_db"
	frozen.Calendar.Digest = ""
	cal := calendar.New(frozen)
	opts := yangpullback.DefaultEvalOptions()
	opts.Options.AsOf = asof
	opts.Bootstrap = bootstrap
	opts.Split = split
	evaluation, err := yangpullback.Evaluate(c, cal, snapshot.Adjusted, snapshot.Raw, asof, opts)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(evaluation)
}
