package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/lowvaluation"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/lowvaluationstore"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type lowValuationSummary struct {
	ID                string  `json:"id"`
	AsOf              string  `json:"as_of"`
	Target            string  `json:"target"`
	UniverseTotal     int     `json:"universe_total"`
	Priced            int     `json:"priced"`
	Valued            int     `json:"valued"`
	Eligible          int     `json:"eligible"`
	Selected          int     `json:"selected"`
	TopN              int     `json:"top_n"`
	SectorCoveragePct float64 `json:"sector_coverage_pct"`
}

func lowValuationSummaryOf(rec lowvaluation.Recommendation) lowValuationSummary {
	return lowValuationSummary{
		ID: rec.ID, AsOf: rec.AsOf, Target: rec.Target,
		UniverseTotal: rec.UniverseTotal, Priced: rec.Priced, Valued: rec.Valued,
		Eligible: rec.Eligible, Selected: rec.Selected, TopN: rec.TopN,
		SectorCoveragePct: rec.SectorCoveragePct,
	}
}

func runScanLowValuation(ctx context.Context, c config.Config, log *logging.Logger, asOf string, workers int, timeout time.Duration) error {
	if !c.LowValuation.Enabled {
		return errors.New("low_valuation is disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := storage.EnsureRoot(c.DataDir, c.RequireCIFS); err != nil {
		return err
	}
	db, err := storage.OpenAllMarket(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer db.Close()
	rec, err := scanLowValuationWithDB(ctx, c, log, db, asOf, workers, time.Now())
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(lowValuationSummaryOf(rec))
}

func scanLowValuationWithDB(ctx context.Context, c config.Config, log *logging.Logger, db *storage.AllMarketDB,
	asOf string, workers int, now time.Time) (lowvaluation.Recommendation, error) {
	if log == nil {
		log = logging.Nop()
	}
	sina := market.NewSinaClient(config.Duration(c.Timeout), config.Duration(c.Market.RequestInterval))
	sina.Endpoint = c.Market.SinaEndpoint
	defer sina.Close()
	sectors := loadLowValuationSectors(ctx, c, log)
	return generateLowValuationRecommendation(ctx, c, log, db, lowvaluationstore.Quotes{DB: db}, sina, sectors, asOf, workers, now)
}

func generateLowValuationRecommendation(ctx context.Context, c config.Config, log *logging.Logger,
	db *storage.AllMarketDB, quotes lowvaluation.QuoteSource, fundamentals lowvaluation.FundamentalSource,
	sectors map[string]string, asOf string, workers int, now time.Time) (lowvaluation.Recommendation, error) {
	if db == nil || quotes == nil || fundamentals == nil {
		return lowvaluation.Recommendation{}, errors.New("low valuation generation dependencies are unavailable")
	}
	cal := calendar.New(c)
	if asOf == "" {
		var err error
		asOf, err = cal.Completed(now)
		if err != nil {
			return lowvaluation.Recommendation{}, err
		}
	}
	target, err := cal.Next(asOf)
	if err != nil {
		return lowvaluation.Recommendation{}, err
	}
	rec, err := lowvaluation.Scan(ctx, quotes, fundamentals, lowvaluation.Options{
		AsOf: asOf, Target: target, TopN: c.LowValuation.TopN, Workers: workers,
		Sectors: sectors, Now: func() time.Time { return now },
		Progress: func(done, total int) {
			log.Info("Low valuation scan progress", "completed", done, "total", total)
		},
	})
	if err != nil {
		return rec, err
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return rec, errors.New("cannot encode low valuation recommendation")
	}
	if err := db.SaveRecommendation(ctx, storage.RecommendationArtifact{
		Market: lowvaluation.Market, AsOf: rec.AsOf, ID: rec.ID, Target: rec.Target,
		BuiltAt: rec.GeneratedAt, Payload: payload,
	}); err != nil {
		return rec, err
	}
	return rec, nil
}

func loadLowValuationSectors(ctx context.Context, c config.Config, log *logging.Logger) map[string]string {
	if log == nil {
		log = logging.Nop()
	}
	if c.Market.TushareToken == "" {
		log.Warn("Low valuation industry snapshot skipped", "reason", "tushare token is not configured")
		return nil
	}
	client := market.NewTushareClient(config.Duration(c.Timeout), config.Duration(c.Market.RequestInterval), c.Market.TushareToken)
	client.Endpoint = c.Market.TushareEndpoint
	defer client.Close()
	basics, err := client.FetchStockBasics(ctx)
	if err != nil {
		log.Warn("Low valuation industry snapshot unavailable", "reason", err.Error())
		return nil
	}
	sectors := make(map[string]string, len(basics))
	for _, basic := range basics {
		if basic.Industry != "" {
			sectors[basic.Code] = basic.Industry
		}
	}
	log.Info("Low valuation industry snapshot loaded", "stocks", len(sectors))
	return sectors
}
