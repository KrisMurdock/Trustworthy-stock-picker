// Package runtime embeds the Stock Watcher synchronization, scheduling and
// dashboard services in another Go process.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/channels"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/dashboard"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/dayrange"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/intraday"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/marketcontext"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/monitor"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/predictor"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

// Config is the complete runtime configuration. Use LoadConfig or DefaultConfig
// to obtain a valid starting point, modify the exported fields, then pass it to
// New.
type Config = config.Config

// Stock identifies one A-share stock in the configured pool.
type Stock = config.Stock

// Logger receives runtime diagnostics. A nil logger in Options disables logging.
type Logger = logging.Logger

// LoggerOptions configures the built-in rotating file and stdout logger.
type LoggerOptions = logging.Options

// Options controls construction of a Runtime.
type Options struct {
	// Version is reported by status and dashboard responses. It defaults to
	// "embedded".
	Version string
	// Config is optional. Nil loads the built-in configuration and its
	// environment overrides. A supplied value is finalized before use.
	Config *Config
	// Logger is optional. Nil discards diagnostics and does not create log files.
	// The Runtime never closes a logger supplied by the host.
	Logger *Logger
}

var (
	// ErrClosed is returned when Run is called after Close.
	ErrClosed = errors.New("stock watcher runtime is closed")
	// ErrAlreadyRunning is returned when Run is called concurrently.
	ErrAlreadyRunning = errors.New("stock watcher runtime is already running")
)

// Runtime owns a writable stock watcher session. It should be constructed once
// per data directory, because the underlying store holds an exclusive writer
// lock. The host owns signal handling and should call Close after Run returns.
type Runtime struct {
	mu sync.Mutex

	config        config.Config
	version       string
	log           *Logger
	store         *storage.Store
	marketDB      *storage.MarketDB
	dayRangeDB    *storage.DayRangeDB
	contextDB     *storage.ContextDB
	contextClient *marketcontext.Client
	contextSyncer *marketcontext.Syncer
	sources       *market.Sources
	channels      []notify.Channel
	closeChannels func()
	monitor       *monitor.Monitor
	minuteClient  *market.MinuteClient
	minuteMonitor *intraday.Monitor
	predictor     *predictor.Monitor
	dayRange      *dayrange.Service
	dashboard     http.Handler
	runMonitor    func(context.Context) error

	running    bool
	cancel     context.CancelFunc
	runDone    chan struct{}
	closed     bool
	closedDone chan struct{}
	closeErr   error
}

// DefaultConfig returns the embedded production configuration before paths,
// stock pools and credentials are resolved. New applies those steps.
func DefaultConfig() Config { return config.Builtin() }

// LoadConfig resolves the built-in configuration, environment overrides, stock
// pool and credentials. Optional paths are intended for fixtures and offline
// research tools; production embedding normally calls it without arguments.
func LoadConfig(paths ...string) (Config, error) { return config.Load(paths...) }

// NewLogger creates the unified rotating logger used by the CLI.
func NewLogger(opts LoggerOptions) (*Logger, error) { return logging.New(opts) }

// NopLogger returns a logger that discards all diagnostics.
func NopLogger() *Logger { return logging.Nop() }

// Init creates the SQLite market database in the configured data directory and
// returns its path. Local mode creates a missing data directory; CIFS mode
// requires the share to be mounted and never creates a local fallback.
func Init(cfg *Config) (string, error) {
	resolved, err := resolveConfig(cfg)
	if err != nil {
		return "", err
	}
	if err := storage.EnsureRoot(resolved.DataDir, resolved.RequireCIFS); err != nil {
		return "", err
	}
	store, err := storage.Open(resolved.DataDir, resolved.RequireCIFS)
	if err != nil {
		return "", err
	}
	defer store.Close()
	marketDB, err := storage.InitMarket(resolved.DataDir, resolved.RequireCIFS)
	if err != nil {
		return "", err
	}
	defer marketDB.Close()
	if resolved.MarketContext.Enabled {
		contextDB, err := storage.InitContext(resolved.DataDir, resolved.RequireCIFS)
		if err != nil {
			return "", err
		}
		defer contextDB.Close()
	}
	if resolved.DayRange.Enabled {
		dayRangeDB, err := storage.InitDayRange(resolved.DataDir, resolved.RequireCIFS)
		if err != nil {
			return "", err
		}
		defer dayRangeDB.Close()
	}
	return marketDB.Path(), nil
}

// New validates configuration and opens the storage, market feeds, notification
// channels, monitors and dashboard handler. It does not start any loop and does
// not install signal handlers.
func New(opts Options) (*Runtime, error) {
	cfg, err := resolveConfig(opts.Config)
	if err != nil {
		return nil, err
	}
	version := opts.Version
	if version == "" {
		version = "embedded"
	}
	log := opts.Logger
	if log == nil {
		log = logging.Nop()
	}
	r := &Runtime{config: cfg, version: version, log: log, closedDone: make(chan struct{})}
	opened := false
	defer func() {
		if !opened {
			_ = r.release()
		}
	}()

	cal := calendar.New(cfg)
	if _, err := cal.Trading(calendar.Date(time.Now())); err != nil {
		return nil, fmt.Errorf("calendar validation failed: %w", err)
	}
	store, err := storage.Open(cfg.DataDir, cfg.RequireCIFS)
	if err != nil {
		return nil, err
	}
	r.store = store
	marketDB, err := storage.OpenMarket(cfg.DataDir, cfg.RequireCIFS)
	if err != nil {
		return nil, err
	}
	r.marketDB = marketDB
	dayRangeDB, err := storage.OpenDayRangeView(cfg.DataDir, cfg.RequireCIFS)
	if err != nil {
		return nil, err
	}
	if cfg.DayRange.Enabled {
		_ = dayRangeDB.Close()
		dayRangeDB, err = storage.InitDayRange(cfg.DataDir, cfg.RequireCIFS)
		if err != nil {
			return nil, err
		}
	}
	r.dayRangeDB = dayRangeDB
	if cfg.MarketContext.Enabled {
		contextDB, err := storage.InitContext(cfg.DataDir, cfg.RequireCIFS)
		if err != nil {
			return nil, err
		}
		r.contextDB = contextDB
		r.contextClient = marketcontext.NewClient(cfg.MarketContext, config.Duration(cfg.Timeout))
		r.contextSyncer = marketcontext.NewSyncer(cfg, contextDB, r.contextClient, log)
	}
	sources, err := market.NewSources(cfg, log)
	if err != nil {
		return nil, err
	}
	r.sources = sources
	noticeChannels, closeChannels, err := channels.Build(cfg)
	if err != nil {
		return nil, err
	}
	r.channels = noticeChannels
	r.closeChannels = closeChannels
	for _, channel := range cfg.NotificationChannels() {
		log.RegisterSecret(channel.Webhook)
		log.RegisterSecret(channel.Secret)
	}
	log.RegisterSecret(cfg.Wechat.AppSecret)
	r.monitor = monitor.NewWithMarketDB(cfg, store, marketDB, sources, noticeChannels, log)
	r.monitor.BuildVersion = version
	if r.contextSyncer != nil {
		r.monitor.MarketContextSync = r.contextSyncer.ForDate
	}
	if cfg.Intraday.Enabled {
		r.minuteClient = market.NewMinuteClient(cfg)
		r.minuteMonitor = intraday.New(cfg, store, marketDB, r.minuteClient, log)
	}
	if cfg.Predictor.Enabled {
		predictorMonitor, err := predictor.New(cfg, store, marketDB, noticeChannels, log)
		if err != nil {
			return nil, err
		}
		if predictorMonitor != nil {
			predictorMonitor.BuildVersion = version
			r.predictor = predictorMonitor
		}
	}
	if r.dayRangeDB != nil && r.dayRangeDB.Available() {
		r.dayRange = dayrange.NewService(cfg, r.dayRangeDB, marketDB, log)
		r.dayRange.BuildVersion = version
		if err := r.dayRange.ConfigureDelivery(store, noticeChannels); err != nil {
			return nil, err
		}
		if err := r.dayRange.ConfigureExecutionDelivery(store, noticeChannels); err != nil {
			return nil, err
		}
		r.monitor.DayRangeForecasts = r.loadDayRangeForecasts
	}
	var dayRangeReader dashboard.DayRangeReader
	if r.dayRange != nil {
		dayRangeReader = r.dayRange
	}
	var predictorController dashboard.PredictorController
	if r.predictor != nil {
		predictorController = r.predictor
	}
	r.dashboard = dashboard.NewHandlerWithDayRange(cfg, marketDB, log, predictorController, dayRangeReader)
	r.runMonitor = r.runMonitors
	opened = true
	return r, nil
}

func resolveConfig(cfg *Config) (config.Config, error) {
	if cfg == nil {
		return config.Load()
	}
	return config.Prepare(*cfg, ".")
}

// Config returns the finalized runtime configuration.
func (r *Runtime) Config() Config { return r.config }

// DashboardHandler returns the read-only dashboard handler. It remains mounted
// by the host when the built-in web listener is disabled.
func (r *Runtime) DashboardHandler() http.Handler { return r.dashboard }

// Run blocks until the context is canceled or a monitor fails. Cancellation is a
// normal shutdown and returns nil. The Runtime remains usable for inspection of
// its final state until Close is called.
func (r *Runtime) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	if r.running {
		r.mu.Unlock()
		return ErrAlreadyRunning
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.running = true
	r.cancel = cancel
	r.runDone = done
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		r.running = false
		r.cancel = nil
		r.runDone = nil
		r.mu.Unlock()
		close(done)
	}()
	r.log.Info("Stock watcher started", "command", "run", "version", r.version, "pool_size", len(r.config.Stocks), "notification_channels", len(r.channels))

	var err error
	if r.config.Web.Enabled {
		err = runServices(runCtx, r.config.Web.Listen, r.dashboard, r.runMonitor, r.log)
	} else {
		err = r.runMonitor(runCtx)
	}
	if errors.Is(err, context.Canceled) {
		r.log.Info("Stock watcher stopped")
		return nil
	}
	return err
}

func (r *Runtime) runMonitors(ctx context.Context) error {
	runners := []func(context.Context) error{r.monitor.Run}
	if r.minuteMonitor != nil {
		runners = append(runners, r.minuteMonitor.Run)
	}
	if r.predictor != nil {
		runners = append(runners, r.predictor.Run)
	}
	if r.dayRange != nil && r.config.DayRange.Enabled {
		runners = append(runners, r.dayRange.Run)
	}
	return runMonitors(ctx, runners...)
}

func (r *Runtime) loadDayRangeForecasts(ctx context.Context, target string, codes []string) (map[string]selection.DayRangeForecast, error) {
	if r.dayRange == nil {
		return nil, errors.New("dayrange service is unavailable")
	}
	records, err := r.dayRange.Predictions(ctx, target, "")
	if err != nil {
		return nil, err
	}
	forecasts := dayRangeForecasts(records, codes)
	if missingDayRangeForecasts(forecasts, codes) == 0 {
		return forecasts, nil
	}
	// The daily article is built immediately after the 18:05 sync, before the
	// dayrange loop's next tick. Generate the frozen forecast here, without
	// delivery, so the article has tomorrow's range and the scheduled 08:45
	// premarket push keeps its own timing.
	if _, err := r.dayRange.GeneratePredictions(ctx, target, ""); err != nil {
		if len(forecasts) == 0 {
			return nil, err
		}
		r.log.Warn("Dayrange forecast generation failed; using saved forecasts", "target", target, "error", err.Error())
		return forecasts, nil
	}
	records, err = r.dayRange.Predictions(ctx, target, "")
	if err != nil {
		return nil, err
	}
	forecasts = dayRangeForecasts(records, codes)
	if missing := missingDayRangeForecasts(forecasts, codes); missing > 0 {
		r.log.Warn("Dayrange forecast remained incomplete after generation", "target", target, "missing", missing)
	}
	return forecasts, nil
}

func missingDayRangeForecasts(forecasts map[string]selection.DayRangeForecast, codes []string) int {
	seen := make(map[string]struct{}, len(codes))
	missing := 0
	for _, code := range codes {
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		if _, ok := forecasts[code]; !ok {
			missing++
		}
	}
	return missing
}

func dayRangeForecasts(records []dayrange.PredictionRecord, codes []string) map[string]selection.DayRangeForecast {
	wanted := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		wanted[code] = struct{}{}
	}
	out := make(map[string]selection.DayRangeForecast, len(wanted))
	created := make(map[string]time.Time, len(wanted))
	for _, record := range records {
		if _, ok := wanted[record.Code]; !ok {
			continue
		}
		if previous, ok := created[record.Code]; ok && record.CreatedAt.Before(previous) {
			continue
		}
		forecast := selection.DayRangeForecast{
			Code: record.Code, TargetDate: record.TargetDate, ModelID: record.ModelID, Source: record.Source,
			ReferencePrice: record.ReferencePrice, PointHigh: record.PointHigh, PointLow: record.PointLow,
		}
		if record.IntervalHigh != nil {
			forecast.HighLower = record.IntervalHigh.Lower
			forecast.HighUpper = record.IntervalHigh.Upper
			forecast.HighCoverage = record.IntervalHigh.NominalCoverage
		}
		if record.IntervalLow != nil {
			forecast.LowLower = record.IntervalLow.Lower
			forecast.LowUpper = record.IntervalLow.Upper
			forecast.LowCoverage = record.IntervalLow.NominalCoverage
		}
		out[record.Code] = forecast
		created[record.Code] = record.CreatedAt
	}
	return out
}

// Close stops an active Run, waits for its loops to finish, and releases storage
// and network resources. It is idempotent and never closes a host-owned logger.
func (r *Runtime) Close() error {
	r.mu.Lock()
	if r.closed {
		done := r.closedDone
		r.mu.Unlock()
		<-done
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.closeErr
	}
	r.closed = true
	runDone, cancel := r.runDone, r.cancel
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if runDone != nil {
		<-runDone
	}
	err := r.release()
	r.mu.Lock()
	r.closeErr = err
	r.mu.Unlock()
	close(r.closedDone)
	return err
}

func (r *Runtime) release() error {
	var errs []error
	if r.closeChannels != nil {
		r.closeChannels()
		r.closeChannels = nil
	}
	if r.predictor != nil {
		errs = append(errs, r.predictor.Close())
		r.predictor = nil
	}
	if r.minuteClient != nil {
		r.minuteClient.Close()
		r.minuteClient = nil
	}
	if r.contextClient != nil {
		r.contextClient.Close()
		r.contextClient = nil
	}
	if r.sources != nil {
		r.sources.Close()
		r.sources = nil
	}
	if r.contextDB != nil {
		errs = append(errs, r.contextDB.Close())
		r.contextDB = nil
	}
	if r.dayRangeDB != nil {
		errs = append(errs, r.dayRangeDB.Close())
		r.dayRangeDB = nil
	}
	if r.marketDB != nil {
		errs = append(errs, r.marketDB.Close())
		r.marketDB = nil
	}
	if r.store != nil {
		errs = append(errs, r.store.Close())
		r.store = nil
	}
	return errors.Join(errs...)
}
