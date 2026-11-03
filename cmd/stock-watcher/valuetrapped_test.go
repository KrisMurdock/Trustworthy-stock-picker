package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/lowvaluation"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/trappedscan"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/valuetrapped"
)

func valueTrappedLowCandidate(code string, close, gap float64) lowvaluation.Candidate {
	return lowvaluation.Candidate{
		Code: code, Name: code, Sector: "制造业", PriceDate: "2026-10-31", Close: close,
		GapToLowerPct: gap, UpsideToCentralPct: 20,
		Estimate: fairvalue.Estimate{Code: code, AsOf: "2026-10-31", CurrentPrice: close, Lower: 20, Central: 24, Upper: 28},
	}
}

func valueTrappedLowRecommendation(candidates ...lowvaluation.Candidate) lowvaluation.Recommendation {
	top := candidates
	if len(top) > 2 {
		top = top[:2]
	}
	return lowvaluation.Recommendation{
		Version: lowvaluation.SchemaVersion, ID: "low_valuation-2026-10-31-20", Market: lowvaluation.Market,
		AsOf: "2026-10-31", Target: "2026-11-02", GeneratedAt: time.Date(2026, 10, 31, 19, 0, 0, 0, market.Shanghai),
		UniverseTotal: 5000, Priced: 4800, Valued: 4300, Eligible: len(candidates), Selected: len(top), TopN: 20,
		Candidates: top, AllCandidates: candidates, SectorCoveragePct: 99,
		Limitations: []string{"财务区间不是目标价。"},
	}
}

func valueTrappedSignal(ratio float64) *strategy.TrappedSignal {
	return &strategy.TrappedSignal{
		Version: strategy.TrappedSignalVersion, Method: strategy.TrappedSignalMethod, Status: strategy.TrappedStatusNone,
		CurrentPrice: 18, TrappedVolumeRatio: ratio, CurrentZone: strategy.TrappedZoneShallow, DominantZone: strategy.TrappedZoneShallow,
	}
}

func TestGenerateValueTrappedPersistsIntersectionAndLimitsTopN(t *testing.T) {
	root := t.TempDir()
	if err := storage.EnsureRoot(root, false); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenAllMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := config.Defaults()
	c.ValueTrapped.Enabled = true
	c.ValueTrapped.PushEnabled = true
	c.ValueTrapped.TopN = 2
	c.ValueTrapped.MaxTrappedRatio = 0.30
	low := valueTrappedLowRecommendation(
		valueTrappedLowCandidate("sh600001", 18, 1),
		valueTrappedLowCandidate("sh600002", 18, 2),
		valueTrappedLowCandidate("sh600003", 18, 4),
	)
	var captured trappedscan.Options
	now := time.Date(2026, 10, 31, 20, 0, 0, 0, market.Shanghai)
	rec, err := generateValueTrappedRecommendation(context.Background(), c, logging.Nop(), db, low, 2, now,
		func(_ context.Context, options trappedscan.Options) (trappedscan.Result, error) {
			captured = options
			return trappedscan.Result{AsOf: low.AsOf, ValidSignals: 3, Top: []trappedscan.Candidate{
				{Code: "sh600001", AvgAmount20CNY: 100_000_000, Trapped: valueTrappedSignal(0.10)},
				{Code: "sh600002", AvgAmount20CNY: 200_000_000, Trapped: valueTrappedSignal(0.05)},
				{Code: "sh600003", AvgAmount20CNY: 300_000_000, Trapped: valueTrappedSignal(0.20)},
			}}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.Codes) != 3 || captured.TopN != 3 || captured.MinHistorySessions != valuetrapped.DefaultMinHistorySessions || captured.MinAmountCNY != valuetrapped.DefaultMinAmountCNY {
		t.Fatalf("trapped scan options = %+v", captured)
	}
	if rec.Eligible != 3 || rec.Selected != 2 || len(rec.Candidates) != 2 || rec.Candidates[0].Code != "sh600001" || rec.Candidates[1].Code != "sh600002" {
		t.Fatalf("recommendation = %+v", rec)
	}
	artifact, err := db.LatestRecommendation(context.Background(), valuetrapped.Market)
	if err != nil {
		t.Fatal(err)
	}
	var stored valuetrapped.Recommendation
	if err := json.Unmarshal(artifact.Payload, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ID != rec.ID || stored.AsOf != rec.AsOf || stored.Target != rec.Target || stored.Selected != 2 || stored.Market != valuetrapped.Market {
		t.Fatalf("stored recommendation = %+v", stored)
	}
}

func TestGenerateValueTrappedRequiresCompleteLowValuationSet(t *testing.T) {
	root := t.TempDir()
	if err := storage.EnsureRoot(root, false); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenAllMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := config.Defaults()
	c.ValueTrapped.Enabled = true
	c.ValueTrapped.TopN = 20
	c.ValueTrapped.MaxTrappedRatio = 0.30
	low := valueTrappedLowRecommendation(valueTrappedLowCandidate("sh600001", 18, 1))
	low.AllCandidates = nil
	if _, err := generateValueTrappedRecommendation(context.Background(), c, logging.Nop(), db, low, 2, time.Now(),
		func(context.Context, trappedscan.Options) (trappedscan.Result, error) {
			return trappedscan.Result{AsOf: low.AsOf}, nil
		}); err == nil {
		t.Fatal("missing complete low-valuation set was accepted")
	}
	if _, err := db.LatestRecommendation(context.Background(), valuetrapped.Market); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("invalid generation saved an artifact: %v", err)
	}
}

func TestLowValuationCoverageFailureDoesNotGenerateOrSaveValueTrapped(t *testing.T) {
	root := t.TempDir()
	if err := storage.EnsureRoot(root, false); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenAllMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := config.Defaults()
	c.LowValuation.Enabled = true
	c.ValueTrapped.Enabled = true
	c.ValueTrapped.TopN = 20
	c.ValueTrapped.MaxTrappedRatio = 0.30
	calledTrapped := false
	_, _, lowErr, valueErr := generateMarketScreens(context.Background(), c, logging.Nop(), db, 2,
		time.Date(2026, 10, 31, 20, 0, 0, 0, market.Shanghai),
		func(context.Context) (lowvaluation.Recommendation, error) {
			return lowvaluation.Recommendation{}, errors.New("fundamentals coverage below minimum")
		},
		func(context.Context, trappedscan.Options) (trappedscan.Result, error) {
			calledTrapped = true
			return trappedscan.Result{}, nil
		})
	if lowErr == nil || valueErr != nil || calledTrapped {
		t.Fatalf("lowErr=%v valueErr=%v trappedCalled=%v", lowErr, valueErr, calledTrapped)
	}
	for _, marketName := range []string{lowvaluation.Market, valuetrapped.Market} {
		if _, err := db.LatestRecommendation(context.Background(), marketName); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s artifact exists after low-valuation failure: %v", marketName, err)
		}
	}
}

func TestTrappedFailureKeepsLowValuationSnapshot(t *testing.T) {
	root := t.TempDir()
	if err := storage.EnsureRoot(root, false); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenAllMarket(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := config.Defaults()
	c.LowValuation.Enabled = true
	c.ValueTrapped.Enabled = true
	c.ValueTrapped.TopN = 20
	c.ValueTrapped.MaxTrappedRatio = 0.30
	low := valueTrappedLowRecommendation(valueTrappedLowCandidate("sh600001", 18, 1))
	lowRecommendation, valueRecommendation, lowErr, valueErr := generateMarketScreens(context.Background(), c, logging.Nop(), db, 2,
		time.Date(2026, 10, 31, 20, 0, 0, 0, market.Shanghai),
		func(context.Context) (lowvaluation.Recommendation, error) { return low, nil },
		func(context.Context, trappedscan.Options) (trappedscan.Result, error) {
			return trappedscan.Result{}, errors.New("turnover source unavailable")
		})
	if lowErr != nil || valueErr == nil || lowRecommendation.ID != low.ID || valueRecommendation.ID != "" {
		t.Fatalf("lowErr=%v valueErr=%v low=%+v value=%+v", lowErr, valueErr, lowRecommendation, valueRecommendation)
	}
	artifact, err := db.LatestRecommendation(context.Background(), lowvaluation.Market)
	if err != nil || artifact.ID != low.ID {
		t.Fatalf("low valuation snapshot was not preserved: artifact=%+v err=%v", artifact, err)
	}
	if _, err := db.LatestRecommendation(context.Background(), valuetrapped.Market); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("value-trapped artifact exists after trapped failure: %v", err)
	}
}
