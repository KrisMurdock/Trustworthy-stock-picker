package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/marketcontext"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type koreaMinuteSyncItem struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Bars  int    `json:"bars"`
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
}

type koreaMinuteSyncResult struct {
	Command string                `json:"command"`
	AsOf    string                `json:"as_of"`
	From    string                `json:"from"`
	Through string                `json:"through"`
	Status  string                `json:"status"`
	Results []koreaMinuteSyncItem `json:"results"`
}

func runSyncKoreaMinutes(ctx context.Context, c config.Config, asOf string, log *logging.Logger) error {
	db, err := storage.InitContext(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := syncKoreaMinutes(ctx, c, db, asOf, log)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func syncKoreaMinutes(ctx context.Context, c config.Config, db *storage.ContextDB, asOf string, log *logging.Logger) (koreaMinuteSyncResult, error) {
	result := koreaMinuteSyncResult{Command: "sync-korea-minutes", Status: "failed", Results: []koreaMinuteSyncItem{}}
	today := time.Now().In(market.Korea).Format("2006-01-02")
	if asOf == "" {
		asOf = today
	}
	parsed, err := time.ParseInLocation("2006-01-02", asOf, market.Korea)
	if err != nil || parsed.Format("2006-01-02") != asOf || asOf > today {
		return result, errors.New("korea minute as-of must be a valid date not after today in KST")
	}
	synced, err := marketcontext.SyncKoreaMinutes(ctx, c, db, asOf, log)
	if err != nil {
		return result, err
	}
	result.AsOf = synced.AsOf
	result.From = synced.From
	result.Through = synced.Through
	for _, item := range synced.Results {
		result.Results = append(result.Results, koreaMinuteSyncItem{Code: item.Code, Name: item.Name, Bars: item.Bars, First: item.First, Last: item.Last})
	}
	result.Status = "ok"
	return result, nil
}
