package main

import (
	"context"
	"errors"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/marketcontext"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type marketContextRuntime struct {
	db     *storage.ContextDB
	client *marketcontext.Client
	syncer *marketcontext.Syncer
}

func openMarketContextRuntime(c config.Config, log *logging.Logger) (*marketContextRuntime, error) {
	if !c.MarketContext.Enabled {
		return nil, errors.New("market context synchronization is disabled")
	}
	db, err := storage.InitContext(c.DataDir, c.RequireCIFS)
	if err != nil {
		return nil, err
	}
	client := marketcontext.NewClient(c.MarketContext, config.Duration(c.Timeout))
	return &marketContextRuntime{db: db, client: client, syncer: marketcontext.NewSyncer(c, db, client, log)}, nil
}

func (r *marketContextRuntime) Close() {
	if r == nil {
		return
	}
	if r.client != nil {
		r.client.Close()
	}
	if r.db != nil {
		_ = r.db.Close()
	}
}

func runContextSync(ctx context.Context, c config.Config, asOf string, log *logging.Logger) (marketcontext.SyncResult, error) {
	if !c.MarketContext.Enabled {
		return marketcontext.SyncResult{}, errors.New("market context synchronization is disabled")
	}
	cal := calendar.New(c)
	if asOf == "" {
		day, err := cal.Completed(time.Now())
		if err != nil {
			return marketcontext.SyncResult{}, err
		}
		asOf = day
	} else {
		trading, err := cal.Trading(asOf)
		if err != nil || !trading {
			return marketcontext.SyncResult{}, errors.New("context as-of must be a configured trading day")
		}
		latest, err := cal.Completed(time.Now())
		if err != nil {
			return marketcontext.SyncResult{}, err
		}
		if asOf > latest {
			return marketcontext.SyncResult{}, errors.New("context as-of must be a completed trading day")
		}
	}
	ctxRuntime, err := openMarketContextRuntime(c, log)
	if err != nil {
		return marketcontext.SyncResult{}, err
	}
	defer ctxRuntime.Close()
	result, err := ctxRuntime.syncer.ForDate(ctx, asOf)
	if err != nil {
		return result, err
	}
	if _, err := syncHangSengContext(ctx, c, ctxRuntime.db, asOf, log); err != nil && ctx.Err() == nil {
		log.Warn("Hang Seng shadow synchronization failed", "as_of", asOf, "reason", err.Error())
	}
	if _, err := syncKoreaMinutes(ctx, c, ctxRuntime.db, asOf, log); err != nil && ctx.Err() == nil {
		log.Warn("Korean minute synchronization failed", "as_of", asOf, "reason", err.Error())
	}
	return result, nil
}
