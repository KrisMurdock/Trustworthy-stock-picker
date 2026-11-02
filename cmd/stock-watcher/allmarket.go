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
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

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
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return err
	}
	if result.Status != "complete" {
		return errors.New("all-market synchronization is incomplete; rerun sync-all to resume")
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
