package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/lowvaluation"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/trappedscan"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/valuetrapped"
)

type valueTrappedSummary struct {
	ID                   string  `json:"id"`
	AsOf                 string  `json:"as_of"`
	Target               string  `json:"target"`
	UniverseTotal        int     `json:"universe_total"`
	Priced               int     `json:"priced"`
	Valued               int     `json:"valued"`
	LowValuationEligible int     `json:"low_valuation_eligible"`
	TrappedValid         int     `json:"trapped_valid"`
	Eligible             int     `json:"eligible"`
	Selected             int     `json:"selected"`
	TopN                 int     `json:"top_n"`
	MaxTrappedRatioPct   float64 `json:"max_trapped_ratio_pct"`
}

type trappedScanFunc func(context.Context, trappedscan.Options) (trappedscan.Result, error)

type lowValuationScanFunc func(context.Context) (lowvaluation.Recommendation, error)

func valueTrappedSummaryOf(rec valuetrapped.Recommendation) valueTrappedSummary {
	return valueTrappedSummary{
		ID: rec.ID, AsOf: rec.AsOf, Target: rec.Target, UniverseTotal: rec.UniverseTotal, Priced: rec.Priced,
		Valued: rec.Valued, LowValuationEligible: rec.LowValuationEligible, TrappedValid: rec.TrappedValid,
		Eligible: rec.Eligible, Selected: rec.Selected, TopN: rec.TopN, MaxTrappedRatioPct: rec.MaxTrappedRatioPct,
	}
}

func runScanValueTrapped(ctx context.Context, c config.Config, log *logging.Logger, asOf string, workers int, timeout time.Duration) error {
	if !c.ValueTrapped.Enabled {
		return errors.New("value_trapped is disabled")
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
	now := time.Now()
	metrics := market.NewSohuMetricsClient(20*time.Second, 350*time.Millisecond)
	defer metrics.Close()
	scanner := &trappedscan.Scanner{Store: db, Metrics: metrics, Log: log, Now: func() time.Time { return now }}
	_, recommendation, lowErr, valueErr := generateMarketScreens(ctx, c, log, db, workers, now,
		func(scanCtx context.Context) (lowvaluation.Recommendation, error) {
			return scanLowValuationCandidatesWithDB(scanCtx, c, log, db, asOf, workers, now)
		},
		func(scanCtx context.Context, options trappedscan.Options) (trappedscan.Result, error) {
			return scanner.Scan(scanCtx, options)
		})
	if lowErr != nil {
		return lowErr
	}
	if valueErr != nil {
		return valueErr
	}
	return json.NewEncoder(os.Stdout).Encode(valueTrappedSummaryOf(recommendation))
}

// generateMarketScreens persists the low-valuation snapshot first and only
// evaluates trapped supply after that upstream scan has passed all coverage
// gates. A value-trapped failure leaves the low-valuation snapshot intact.
func generateMarketScreens(ctx context.Context, c config.Config, log *logging.Logger, db *storage.AllMarketDB,
	workers int, now time.Time, scanLow lowValuationScanFunc, scanTrapped trappedScanFunc) (
	lowvaluation.Recommendation, valuetrapped.Recommendation, error, error) {
	lowRecommendation, err := scanLow(ctx)
	if err != nil {
		return lowRecommendation, valuetrapped.Recommendation{}, err, nil
	}
	lowRecommendation, err = persistLowValuationRecommendation(ctx, db, lowRecommendation)
	if err != nil {
		return lowRecommendation, valuetrapped.Recommendation{}, err, nil
	}
	if !c.ValueTrapped.Enabled {
		return lowRecommendation, valuetrapped.Recommendation{}, nil, nil
	}
	valueRecommendation, err := generateValueTrappedRecommendation(ctx, c, log, db, lowRecommendation, workers, now, scanTrapped)
	if err != nil {
		return lowRecommendation, valuetrapped.Recommendation{}, nil, err
	}
	return lowRecommendation, valueRecommendation, nil, nil
}

func generateValueTrappedRecommendation(ctx context.Context, c config.Config, log *logging.Logger, db *storage.AllMarketDB,
	lowRecommendation lowvaluation.Recommendation, workers int, now time.Time, scanTrapped trappedScanFunc) (valuetrapped.Recommendation, error) {
	if log == nil {
		log = logging.Nop()
	}
	if db == nil || scanTrapped == nil {
		return valuetrapped.Recommendation{}, errors.New("value-trapped generation dependencies are unavailable")
	}
	if !c.ValueTrapped.Enabled {
		return valuetrapped.Recommendation{}, errors.New("value_trapped is disabled")
	}
	codes := make([]string, 0, len(lowRecommendation.AllCandidates))
	seen := make(map[string]bool, len(lowRecommendation.AllCandidates))
	for _, candidate := range lowRecommendation.AllCandidates {
		if candidate.Code != "" && !seen[candidate.Code] {
			seen[candidate.Code] = true
			codes = append(codes, candidate.Code)
		}
	}
	if lowRecommendation.Eligible > 0 && len(codes) == 0 {
		return valuetrapped.Recommendation{}, errors.New("low valuation scan did not expose its complete candidate set")
	}
	trappedResult := trappedscan.Result{AsOf: lowRecommendation.AsOf}
	if len(codes) > 0 {
		result, err := scanTrapped(ctx, trappedscan.Options{
			AsOf: lowRecommendation.AsOf, TopN: len(codes), Codes: codes, Workers: workers,
			MinHistorySessions: valuetrapped.DefaultMinHistorySessions, MinAmountCNY: valuetrapped.DefaultMinAmountCNY,
		})
		if err != nil {
			return valuetrapped.Recommendation{}, err
		}
		trappedResult = result
	}
	recommendation, err := valuetrapped.Build(lowRecommendation, trappedResult, valuetrapped.Options{
		TopN: c.ValueTrapped.TopN, MaxTrappedRatio: c.ValueTrapped.MaxTrappedRatio, Now: func() time.Time { return now },
	})
	if err != nil {
		return recommendation, err
	}
	payload, err := json.Marshal(recommendation)
	if err != nil {
		return recommendation, errors.New("cannot encode value-trapped recommendation")
	}
	if err := db.SaveRecommendation(ctx, storage.RecommendationArtifact{
		Market: valuetrapped.Market, AsOf: recommendation.AsOf, ID: recommendation.ID, Target: recommendation.Target,
		BuiltAt: recommendation.GeneratedAt, Payload: payload,
	}); err != nil {
		return recommendation, err
	}
	log.Info("Value-trapped recommendation generated", "as_of", recommendation.AsOf, "target", recommendation.Target,
		"id", recommendation.ID, "low_valuation_eligible", recommendation.LowValuationEligible,
		"trapped_valid", recommendation.TrappedValid, "eligible", recommendation.Eligible, "selected", recommendation.Selected)
	return recommendation, nil
}
