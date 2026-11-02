package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/hkconnect"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func runSyncHKConnect(ctx context.Context, c config.Config, log *logging.Logger, asOf string, full bool, workers int, timeout time.Duration, codes string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := storage.EnsureRoot(c.DataDir, c.RequireCIFS); err != nil {
		return err
	}
	db, err := storage.OpenHKMarket(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer db.Close()
	sinaUniverseClient := market.NewSinaHKConnectUniverseClient(config.Duration(c.Timeout), config.Duration(c.Market.RequestInterval), c.Market.HKUniverseEndpoint, c.Market.HKQuoteEndpoint)
	defer sinaUniverseClient.Close()
	universeClient := market.NewHKConnectUniverseClient(config.Duration(c.Timeout), config.Duration(c.Market.RequestInterval), c.Market.HKUniverseFallback)
	defer universeClient.Close()
	dailyClient := market.NewHKDailyClient(config.Duration(c.Timeout), config.Duration(c.Market.RequestInterval), c.Market.HKEndpoint, c.Market.HKEndpointFallback)
	defer dailyClient.Close()
	universe := hkconnect.FailoverUniverse{Sources: []hkconnect.UniverseSource{sinaUniverseClient, universeClient}}
	syncer := hkconnect.New(c, db, dailyClient, universe, dailyClient, log)
	result, err := syncer.Run(ctx, hkconnect.Options{AsOf: asOf, Full: full, Workers: workers, Codes: parseAllMarketCodes(codes)})
	if err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return err
	}
	if result.Status != "complete" {
		return errors.New("HK Connect synchronization is incomplete; rerun sync-hk-connect to resume")
	}
	return nil
}
