package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fundamentals"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type fairValueResearchReport struct {
	Command        string                      `json:"command"`
	AsOf           string                      `json:"as_of"`
	Code           string                      `json:"code"`
	VisibilityMode fundamentals.VisibilityMode `json:"visibility_mode"`
	Price          float64                     `json:"price"`
	PriceDate      string                      `json:"price_date,omitempty"`
	Normalized     fundamentals.Normalized     `json:"normalized"`
	Estimate       fairvalue.EstimateV3        `json:"estimate"`
}

func runFairValueResearch(ctx context.Context, c config.Config, asOf, code, visibility string) error {
	if code == "" {
		return errors.New("fair-value requires -code")
	}
	mode, err := fundamentals.ParseVisibilityMode(visibility)
	if err != nil {
		return err
	}
	stock, ok := configuredStock(c, code)
	if !ok {
		return errors.New("fair-value code is not in the configured pool")
	}
	if asOf == "" {
		var err error
		asOf, err = calendar.New(c).Completed(time.Now())
		if err != nil {
			return err
		}
	}
	fundamentalsDB, err := storage.OpenFundamentalsView(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer fundamentalsDB.Close()
	series, err := fundamentalsDB.LoadSeries(ctx, code)
	if err != nil {
		return err
	}
	normalized := fundamentals.NormalizeWithMode(series, asOf, mode)

	price := 0.0
	priceDate := ""
	marketDB, err := storage.OpenMarketView(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer marketDB.Close()
	if history, loadErr := marketDB.LoadLatestHistory(ctx, code, "none", asOf); loadErr == nil && len(history.Bars) > 0 {
		last := history.Bars[len(history.Bars)-1]
		if last.Date == asOf {
			price = last.Close
			priceDate = last.Date
		}
	}
	estimate := fairvalue.CalculateV3(stock, price, asOf, normalized)
	return json.NewEncoder(os.Stdout).Encode(fairValueResearchReport{
		Command: "fair-value", AsOf: asOf, Code: code, VisibilityMode: mode, Price: price, PriceDate: priceDate,
		Normalized: normalized, Estimate: estimate,
	})
}

func configuredStock(c config.Config, code string) (config.Stock, bool) {
	for _, stock := range c.Stocks {
		if stock.Code == code {
			return stock, true
		}
	}
	return config.Stock{}, false
}
