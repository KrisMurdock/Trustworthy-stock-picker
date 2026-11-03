package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/allmarket"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/lowvaluation"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/trappedscan"
)

type syncAllOutput struct {
	allmarket.Result
	LowValuation      *lowValuationSummary `json:"low_valuation,omitempty"`
	LowValuationError string               `json:"low_valuation_error,omitempty"`
	ValueTrapped      *valueTrappedSummary `json:"value_trapped,omitempty"`
	ValueTrappedError string               `json:"value_trapped_error,omitempty"`
}

func runSyncAll(ctx context.Context, c config.Config, log *logging.Logger, asOf string, full bool, workers int, timeout time.Duration, codes string) error {
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
	universeClient := market.NewUniverseClient(config.Duration(c.Timeout), config.Duration(c.Market.RequestInterval), c.Market.UniverseEndpoint, c.Market.UniverseCountEndpoint)
	defer universeClient.Close()
	sources, err := market.NewSources(c, log)
	if err != nil {
		return err
	}
	defer sources.Close()
	syncer := allmarket.New(c, db, sources, universeClient, log)
	result, err := syncer.Run(ctx, allmarket.Options{AsOf: asOf, Full: full, Workers: workers, Codes: parseAllMarketCodes(codes)})
	if err != nil {
		return err
	}
	output := syncAllOutput{Result: result}
	if result.Status != "complete" {
		if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
			return err
		}
		return errors.New("all-market synchronization is incomplete; rerun sync-all to resume")
	}
	if c.LowValuation.Enabled && strings.TrimSpace(codes) == "" {
		now := time.Now()
		var trappedScanner *trappedscan.Scanner
		var metrics *market.SohuMetricsClient
		var scanTrapped trappedScanFunc
		if c.ValueTrapped.Enabled {
			metrics = market.NewSohuMetricsClient(20*time.Second, 350*time.Millisecond)
			trappedScanner = &trappedscan.Scanner{Store: db, Metrics: metrics, Log: log, Now: func() time.Time { return now }}
			scanTrapped = func(scanCtx context.Context, options trappedscan.Options) (trappedscan.Result, error) {
				return trappedScanner.Scan(scanCtx, options)
			}
		}
		recommendation, valueRecommendation, scanErr, valueErr := generateMarketScreens(ctx, c, log, db, workers, now,
			func(scanCtx context.Context) (lowvaluation.Recommendation, error) {
				return scanLowValuationCandidatesWithDB(scanCtx, c, log, db, result.AsOf, workers, now)
			}, scanTrapped)
		if metrics != nil {
			metrics.Close()
		}
		if scanErr != nil {
			output.LowValuationError = scanErr.Error()
			log.Warn("Low valuation recommendation generation failed", "as_of", result.AsOf, "reason", scanErr.Error())
		} else {
			summary := lowValuationSummaryOf(recommendation)
			output.LowValuation = &summary
			log.Info("Low valuation recommendation generated", "as_of", recommendation.AsOf, "target", recommendation.Target,
				"id", recommendation.ID, "priced", recommendation.Priced, "valued", recommendation.Valued,
				"eligible", recommendation.Eligible, "selected", recommendation.Selected)
			if c.ValueTrapped.Enabled && valueErr != nil {
				output.ValueTrappedError = valueErr.Error()
				log.Warn("Value-trapped recommendation generation failed", "as_of", result.AsOf, "reason", valueErr.Error())
			} else if c.ValueTrapped.Enabled {
				valueSummary := valueTrappedSummaryOf(valueRecommendation)
				output.ValueTrapped = &valueSummary
			}
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		return err
	}
	return nil
}

func parseAllMarketCodes(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, part := range strings.Split(csv, ",") {
		code := strings.TrimSpace(part)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
}
