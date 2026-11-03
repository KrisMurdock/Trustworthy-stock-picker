package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/hkintraday"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
)

func runSyncHKMinutes(ctx context.Context, c config.Config, log *logging.Logger, from, through, codes string, timeout time.Duration) error {
	if !c.HKIntraday.Enabled {
		return errors.New("HK minute synchronization is disabled")
	}
	var selected []string
	if codes != "" {
		seen := map[string]bool{}
		for _, part := range strings.Split(codes, ",") {
			code := strings.TrimSpace(part)
			if !config.ValidHKStockCode(code) || seen[code] {
				return errors.New("invalid or duplicate HK minute code")
			}
			seen[code] = true
			selected = append(selected, code)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	monitor, closeSources := hkintraday.NewProduction(c, log)
	defer closeSources()
	result, err := monitor.Sync(ctx, hkintraday.Options{From: from, Through: through, Codes: selected})
	if result.Version != 0 {
		if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
			return encodeErr
		}
	}
	if err != nil {
		return err
	}
	if result.Status != "complete" {
		return errors.New("HK minute synchronization is incomplete; inspect the reported gaps and retry")
	}
	return nil
}
