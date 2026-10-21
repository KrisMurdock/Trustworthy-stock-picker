package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/channels"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/dayrange"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func runDayRange(ctx context.Context, c config.Config, log *logging.Logger, command, asof, code string, offline bool) error {
	if offline {
		temporary, err := os.MkdirTemp("", "stock-watcher-dayrange-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temporary)
		marketDB, err := storage.OpenMarketView(c.DataDir, c.RequireCIFS)
		if err != nil {
			return err
		}
		defer marketDB.Close()
		dayRangeDB, err := storage.InitDayRange(temporary, false)
		if err != nil {
			return err
		}
		defer dayRangeDB.Close()
		return runDayRangeResult(ctx, c, log, command, asof, code, dayRangeDB, marketDB, nil)
	}
	writable := command == "train-dayrange" || command == "predict-dayrange" || command == "reconcile-dayrange"
	if command == "status-dayrange" || command == "replay-dayrange" {
		writable = false
	}
	openStore := storage.OpenView
	if writable {
		openStore = storage.Open
	}
	store, err := openStore(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer store.Close()
	marketDB, err := storage.OpenMarketView(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer marketDB.Close()
	openDayRange := storage.OpenDayRangeView
	if writable {
		openDayRange = storage.OpenDayRange
	}
	dayRangeDB, err := openDayRange(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer dayRangeDB.Close()
	if !dayRangeDB.Available() {
		if command != "status-dayrange" {
			return errors.New("dayrange database is not initialized; run stock-watcher init")
		}
	}

	var persistence dayrange.Persistence
	if dayRangeDB.Available() {
		persistence = dayRangeDB
	}
	var deliveryStore dayrange.DeliveryStore
	if command == "predict-dayrange" {
		deliveryStore = store
	}
	return runDayRangeResult(ctx, c, log, command, asof, code, persistence, marketDB, deliveryStore)
}

func runDayRangeResult(ctx context.Context, c config.Config, log *logging.Logger, command, asof, code string, persistence dayrange.Persistence, marketSource dayrange.MarketSource, deliveryStore dayrange.DeliveryStore) error {
	service := dayrange.NewService(c, persistence, marketSource, log)
	service.BuildVersion = version
	service.Now = func() time.Time { return time.Now().In(time.FixedZone("CST", 8*3600)) }
	if deliveryStore != nil && c.DayRange.PushEnabled {
		noticeChannels, closeChannels, err := channels.Build(c)
		if err != nil {
			return err
		}
		defer closeChannels()
		if err := service.ConfigureDelivery(deliveryStore, noticeChannels); err != nil {
			return err
		}
	}
	var result any
	var err error
	switch command {
	case "train-dayrange":
		result, err = service.Train(ctx, asof, false)
	case "predict-dayrange", "replay-dayrange":
		if code != "" && !poolCode(c, code) {
			return errors.New("dayrange code must be a stock in the configured pool")
		}
		if command == "predict-dayrange" {
			result, err = service.Predict(ctx, asof, code)
		} else {
			result, err = service.Replay(ctx, asof, code)
		}
	case "reconcile-dayrange":
		result, err = service.Reconcile(ctx, asof)
	case "status-dayrange":
		result, err = service.Status(ctx, asof)
	default:
		return errors.New("unknown dayrange command")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func poolCode(c config.Config, code string) bool {
	for _, stock := range c.Stocks {
		if stock.Code == code {
			return true
		}
	}
	return false
}
