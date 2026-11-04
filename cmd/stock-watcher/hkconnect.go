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
)

func runSyncHKConnect(ctx context.Context, c config.Config, log *logging.Logger, asOf string, full bool, workers int, timeout time.Duration, codes string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	production := hkconnect.NewProduction(c, log)
	defer production.Close()
	result, err := production.Sync(ctx, hkconnect.Options{AsOf: asOf, Full: full, Workers: workers, Codes: parseAllMarketCodes(codes)})
	if err != nil {
		return err
	}
	if result.Status != "complete" {
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			return err
		}
		return errors.New("HK Connect synchronization is incomplete; rerun sync-hk-connect to resume")
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return err
	}
	return nil
}
