package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/allmarket"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func runFourLightAll(ctx context.Context, c config.Config, log *logging.Logger, asOf string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	db, err := storage.OpenAllMarketExisting(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer db.Close()
	runner := allmarket.NewFourLight(c, db, log)
	result, err := runner.Run(ctx, allmarket.FourLightOptions{AsOf: asOf})
	if err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return err
	}
	if result.Status != "complete" {
		return errors.New("four-light calculation is incomplete")
	}
	return nil
}
