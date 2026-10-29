package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/marketforecast"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func runMarketSignal(ctx context.Context, c config.Config, asOf string) error {
	db, err := storage.OpenContextView(c.DataDir, c.RequireCIFS)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.New("market context is not synchronized; run stock-watcher sync-context")
		}
		return err
	}
	defer db.Close()
	if asOf == "" {
		status, err := db.LoadSyncStatus(ctx)
		if errors.Is(err, fs.ErrNotExist) || (err == nil && status.AsOf == "") {
			return errors.New("market context is not synchronized; run stock-watcher sync-context")
		}
		if err != nil {
			return err
		}
		asOf = status.AsOf
	}
	indices := marketforecast.DefaultIndices()
	codes := make([]string, 0, len(indices))
	for _, index := range indices {
		codes = append(codes, index.Code)
	}
	stored, err := db.LoadBarsThrough(ctx, codes, asOf)
	if err != nil {
		return err
	}
	minutes, err := db.LoadMinutesThrough(ctx, codes, asOf)
	if err != nil {
		return err
	}
	sessionMinutes, err := db.LoadSessionMinutesThrough(ctx, codes, asOf)
	if err != nil {
		return err
	}
	koreaBars, err := db.LoadKoreaBarsThrough(ctx, []string{market.KoreaKOSPI, market.KoreaKOSDAQ}, asOf)
	if err != nil {
		return err
	}
	series := make([]marketforecast.Series, 0, len(indices))
	for _, index := range indices {
		bars := make([]marketforecast.Bar, 0, len(stored[index.Code]))
		for _, bar := range stored[index.Code] {
			bars = append(bars, marketforecast.Bar{Date: bar.Date, Open: bar.Open, Close: bar.Close, High: bar.High, Low: bar.Low})
		}
		fiveMinutes := make([]marketforecast.MinuteBar, 0, len(minutes[index.Code]))
		for _, bar := range minutes[index.Code] {
			fiveMinutes = append(fiveMinutes, marketforecast.MinuteBar{
				End: bar.End, Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume,
			})
		}
		thirtyMinutes := make([]marketforecast.MinuteBar, 0, len(sessionMinutes[index.Code]))
		for _, bar := range sessionMinutes[index.Code] {
			thirtyMinutes = append(thirtyMinutes, marketforecast.MinuteBar{
				End: bar.End, Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume,
			})
		}
		series = append(series, marketforecast.Series{Index: index, Bars: bars, Minutes: fiveMinutes, SessionMinutes: thirtyMinutes})
	}
	korea := make([]marketforecast.KoreaSeries, 0, 2)
	for _, code := range []string{market.KoreaKOSPI, market.KoreaKOSDAQ} {
		bars := make([]marketforecast.Bar, 0, len(koreaBars[code]))
		for _, bar := range koreaBars[code] {
			bars = append(bars, marketforecast.Bar{Date: bar.Date, Open: bar.Open, Close: bar.Close, High: bar.High, Low: bar.Low})
		}
		korea = append(korea, marketforecast.KoreaSeries{Code: code, Bars: bars})
	}
	report, err := marketforecast.Build(series, marketforecast.Options{AsOf: asOf, NextTradingDay: calendar.New(c).Next, Korea: korea})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
