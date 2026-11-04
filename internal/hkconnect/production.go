package hkconnect

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type Production struct {
	config   config.Config
	history  HistorySource
	universe UniverseSource
	calendar TradingDaysSource
	log      *logging.Logger
	now      func() time.Time
	close    func()
}

func NewProduction(c config.Config, log *logging.Logger) *Production {
	if log == nil {
		log = logging.Nop()
	}
	timeout := config.Duration(c.Timeout)
	interval := config.Duration(c.Market.RequestInterval)
	sina := market.NewSinaHKConnectUniverseClient(timeout, interval, c.Market.HKUniverseEndpoint, c.Market.HKQuoteEndpoint)
	fallback := market.NewHKConnectUniverseClient(timeout, interval, c.Market.HKUniverseFallback)
	daily := market.NewHKDailyClient(timeout, interval, c.Market.HKEndpoint, c.Market.HKEndpointFallback)
	return &Production{
		config: c, history: daily, universe: FailoverUniverse{Sources: []UniverseSource{sina, fallback}},
		calendar: daily, log: log, now: time.Now,
		close: func() {
			daily.Close()
			fallback.Close()
			sina.Close()
		},
	}
}

func (p *Production) Sync(ctx context.Context, options Options) (Result, error) {
	return p.sync(ctx, options, "")
}

func (p *Production) SyncThrough(ctx context.Context, through string) (Result, error) {
	parsed, err := time.Parse("2006-01-02", through)
	if err != nil || parsed.Format("2006-01-02") != through || through > p.clock().In(market.Shanghai).Format("2006-01-02") {
		return Result{}, errors.New("invalid HK daily synchronization deadline")
	}
	return p.sync(ctx, Options{Workers: defaultWorkers}, through)
}

func (p *Production) sync(ctx context.Context, options Options, through string) (Result, error) {
	if p == nil || p.history == nil || p.universe == nil || p.calendar == nil {
		return Result{}, errors.New("HK Connect production synchronization is not configured")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := storage.EnsureRoot(p.config.DataDir, p.config.RequireCIFS); err != nil {
		return Result{}, err
	}
	db, err := storage.OpenHKMarket(p.config.DataDir, p.config.RequireCIFS)
	if err != nil {
		return Result{}, err
	}
	defer db.Close()
	syncer := New(p.config, db, p.history, p.universe, p.calendar, p.log)
	syncer.Now = p.clock
	if through != "" {
		options.AsOf, err = syncer.latestTradingDay(ctx, through)
		if err != nil {
			return Result{}, err
		}
		newer, err := db.HasDailyBarsAfter(ctx, options.AsOf)
		if err != nil {
			return Result{}, err
		}
		if newer {
			return Result{AsOf: options.AsOf}, errors.New("HK market contains newer daily bars than the scheduled session")
		}
	}
	result, err := syncer.Run(ctx, options)
	if err != nil || result.Status != "complete" || !p.config.HKConnect.Enabled {
		return result, err
	}
	recommendation, err := GenerateRecommendation(ctx, p.config, db, p.calendar, result.AsOf, p.clock())
	if err != nil {
		p.log.Warn("HK Connect recommendation generation failed", "as_of", result.AsOf, "reason", err.Error())
		if through != "" {
			return result, fmt.Errorf("HK Connect recommendation generation failed: %w", err)
		}
		return result, nil
	}
	result.RecommendationID = recommendation.ID
	result.RecommendationTarget = recommendation.Target
	result.RecommendationCandidates = recommendation.Selected
	p.log.Info("HK Connect recommendation generated", "as_of", recommendation.AsOf, "target", recommendation.Target, "id", recommendation.ID, "eligible", recommendation.Eligible, "passed", recommendation.Passed, "selected", recommendation.Selected)
	return result, nil
}

func (p *Production) clock() time.Time {
	if p != nil && p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *Production) Close() {
	if p != nil && p.close != nil {
		p.close()
		p.close = nil
	}
}
