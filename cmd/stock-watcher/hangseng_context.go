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
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/marketcontext"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type hangSengSyncResult struct {
	Command string `json:"command"`
	AsOf    string `json:"as_of"`
	Bars    int    `json:"bars"`
	First   string `json:"first,omitempty"`
	Last    string `json:"last,omitempty"`
	Status  string `json:"status"`
}

func runSyncHangSengContext(ctx context.Context, c config.Config, asOf string, log *logging.Logger) error {
	db, err := storage.InitContext(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := syncHangSengContext(ctx, c, db, asOf, log)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func syncHangSengContext(ctx context.Context, c config.Config, db *storage.ContextDB, asOf string, log *logging.Logger) (hangSengSyncResult, error) {
	result := hangSengSyncResult{Command: "sync-hang-seng-context", Status: "failed"}
	cal := calendar.New(c)
	if asOf == "" {
		completed, err := cal.Completed(time.Now())
		if err != nil {
			return result, err
		}
		asOf = completed
	} else {
		trading, err := cal.Trading(asOf)
		if err != nil || !trading {
			return result, errors.New("hang seng as-of must be a configured trading day")
		}
		completed, err := cal.Completed(time.Now())
		if err != nil {
			return result, err
		}
		if asOf > completed {
			return result, errors.New("hang seng as-of must be a completed trading day")
		}
	}
	synced, err := marketcontext.SyncHangSeng(ctx, c, db, asOf, log)
	if err != nil {
		return result, err
	}
	result.AsOf = synced.AsOf
	result.Bars = synced.Bars
	result.First = synced.First
	result.Last = synced.Last
	result.Status = "ok"
	return result, nil
}
