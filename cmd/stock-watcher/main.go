package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/channels"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/health"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/intraday"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/monitor"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/predictor"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	stockruntime "github.com/KrisMurdock/Trustworthy-stock-picker/runtime"
)

var version = "development"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: stock-watcher check-config|init|check|health|replay|sync|sync-context|sync-intraday|backfill-minutes|status-backfill|status-refresh|status-audit|preview|research|experiment|plan|run|once|status|retry|train-predictor|predictor-status|predictor-eval|train-dayrange|predict-dayrange|reconcile-dayrange|status-dayrange|replay-dayrange [channel] [-as-of YYYY-MM-DD]")
	}
	command := args[0]
	if command == "version" {
		fmt.Println(version)
		return nil
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	revision := f.String("revision", "", "saved report revision (replay only)")
	asof := f.String("as-of", "", "data date (sync/context/status/preview/research/experiment/predictor-eval/dayrange commands only)")
	code := f.String("code", "", "stock code (plan, predict-dayrange or replay-dayrange only)")
	parent := f.String("parent", "", "parent experiment ID (experiment only)")
	holdout := f.Bool("holdout", false, "consume the parent experiment's reserved holdout interval once (experiment only)")
	policy := f.String("policy", "baseline", "conditional-{entry,rsi,macd,bollinger,confluence,all-three,guard,confluence-guard,chan-5-15-30}, baseline, upper-wick, risk-qualified or next-day-{baseline,upper-wick,low-volatility,pullback,breakout,momentum,volume-recovery,balanced,risk-qualified} (experiment only)")
	from := f.String("from", "", "inclusive start date (experiment; pair with -through, or backfill-minutes)")
	through := f.String("through", "", "inclusive backtest signal end date (experiment only; pair with -from)")
	backfillWorkers := f.Int("workers", 3, "concurrent BaoStock workers (backfill-minutes only, 1..8)")
	backfillProvider := f.String("provider", "auto", "minute history provider: auto, baostock or sina (backfill-minutes only)")
	backfillCodes := f.String("codes", "", "comma-separated pool codes (backfill-minutes only; default all)")
	importFrom := f.String("import-from", "", "staging data directory to merge without network access (backfill-minutes only)")
	referenceDir := f.String("reference-dir", "", "read-only production data directory used for resume coverage (backfill-minutes fetch only)")
	entry := f.Float64("entry-price", 0, "actual buy fill price (plan only)")
	windows := f.String("windows", "40,60,120", "comma-separated rolling training windows in trading days (predictor-eval only)")
	validationDays := f.Int("validation-days", 20, "number of common out-of-sample trading days (predictor-eval only)")
	halfLife := f.Int("half-life", 60, "E1 exponential window half-life in trading days (predictor-eval only)")
	allowLateReports := f.Bool("allow-late-reports", false, "allow archived reports generated after target open when as_of is before target (predictor-eval only)")
	lossFunction := f.String("loss", string(predictor.LossL2), "training loss: l2 or l1 (predictor-eval only)")
	closeLossFunction := f.String("close-loss", "", "optional close-target loss override: l1 or l2 (predictor-eval only)")
	dayRangeOffline := f.Bool("offline", false, "train and evaluate dayrange in a temporary store without writing production data (train-dayrange only)")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() > 0 && command != "retry" {
		return errors.New("unexpected positional arguments")
	}
	if command == "retry" && f.NArg() > 1 {
		return errors.New("retry accepts at most one channel")
	}
	switch command {
	case "check-config", "init", "check", "health", "replay", "sync", "sync-context", "sync-intraday", "backfill-minutes", "status-backfill", "status-refresh", "status-audit", "preview", "research", "experiment", "plan", "run", "once", "status", "retry", "train-predictor", "predictor-status", "predictor-eval", "train-dayrange", "predict-dayrange", "reconcile-dayrange", "status-dayrange", "replay-dayrange":
	default:
		return errors.New("unknown command")
	}
	if command == "replay" && len(*revision) != 64 {
		return errors.New("replay requires -revision from a saved report")
	}
	if command != "replay" && *revision != "" {
		return errors.New("revision requires replay")
	}
	if *asof != "" && command != "sync" && command != "sync-context" && command != "backfill-minutes" && command != "status-backfill" && command != "status-refresh" && command != "status-audit" && command != "preview" && command != "research" && command != "experiment" && command != "predictor-eval" && command != "train-dayrange" && command != "predict-dayrange" && command != "reconcile-dayrange" && command != "status-dayrange" && command != "replay-dayrange" {
		return errors.New("as-of is supported by sync, context sync, minute backfill, status commands, preview, research, experiment, predictor-eval and dayrange commands only")
	}
	if command != "predictor-eval" && (*windows != "40,60,120" || *validationDays != 20 || *halfLife != 60) {
		return errors.New("predictor-eval flags require the predictor-eval command")
	}
	if command != "predictor-eval" && *allowLateReports {
		return errors.New("predictor-eval flags require the predictor-eval command")
	}
	if command != "predictor-eval" && (*lossFunction != string(predictor.LossL2) || *closeLossFunction != "") {
		return errors.New("predictor-eval flags require the predictor-eval command")
	}
	if *lossFunction != string(predictor.LossL1) && *lossFunction != string(predictor.LossL2) {
		return errors.New("predictor-eval loss must be l1 or l2")
	}
	if *closeLossFunction != "" && *closeLossFunction != string(predictor.LossL1) && *closeLossFunction != string(predictor.LossL2) {
		return errors.New("predictor-eval close-loss must be l1 or l2")
	}
	if command != "train-dayrange" && *dayRangeOffline {
		return errors.New("offline requires the train-dayrange command")
	}
	if command != "plan" && *entry != 0 {
		return errors.New("entry-price requires the plan command")
	}
	if command != "plan" && command != "predict-dayrange" && command != "replay-dayrange" && *code != "" {
		return errors.New("code is supported by plan, predict-dayrange and replay-dayrange only")
	}
	if command != "experiment" && (*parent != "" || *holdout || *policy != "baseline" || *through != "") {
		return errors.New("experiment flags require the experiment command")
	}
	if command != "experiment" && command != "backfill-minutes" && *from != "" {
		return errors.New("from requires the experiment or backfill-minutes command")
	}
	if command != "backfill-minutes" && (*backfillWorkers != 3 || *backfillProvider != "auto" || *backfillCodes != "" || *importFrom != "" || *referenceDir != "") {
		return errors.New("minute backfill flags require the backfill-minutes command")
	}
	if command == "experiment" && (*from == "") != (*through == "") {
		return errors.New("experiment -from and -through must be supplied together")
	}
	if *holdout && (*parent == "" || *from != "" || *through != "") {
		return errors.New("experiment -holdout requires -parent and cannot be combined with -from/-through")
	}
	if *holdout {
		explicitPolicy := false
		f.Visit(func(flag *flag.Flag) {
			if flag.Name == "policy" {
				explicitPolicy = true
			}
		})
		if *asof != "" {
			return errors.New("experiment -holdout cannot be combined with -as-of")
		}
		if explicitPolicy {
			return errors.New("experiment -holdout reuses the parent policy and cannot be combined with -policy")
		}
	}
	c, err := stockruntime.LoadConfig()
	if err != nil {
		return err
	}
	if command == "plan" {
		p, err := strategy.NewPlan(c.Strategy, *code, *entry)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(p)
	}
	if command == "init" {
		path, err := stockruntime.Init(&c)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "initialized", "market_db": path})
	}
	if command == "predictor-eval" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runPredictorEval(ctx, c, *asof, *windows, *validationDays, *halfLife, *allowLateReports, predictor.LossFunction(*lossFunction), predictor.LossFunction(*closeLossFunction))
	}
	options, err := logging.EnvOptions()
	if err != nil {
		return err
	}
	if command == "check-config" {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "valid", "pool_size": len(c.Stocks)})
	}
	if !filepath.IsAbs(options.Dir) {
		options.Dir = filepath.Join(".", options.Dir)
	}
	log, err := logging.New(options)
	if err != nil {
		return err
	}
	defer log.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if command == "health" {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		db, err := storage.OpenMarketView(c.DataDir, c.RequireCIFS)
		if err != nil {
			return err
		}
		defer db.Close()
		status, err := health.Read(probeCtx, c, db, time.Now())
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	if command == "predictor-status" {
		return predictorStatus(c)
	}
	if command == "train-predictor" {
		return trainPredictor(ctx, c, log)
	}
	if command == "train-dayrange" || command == "predict-dayrange" || command == "reconcile-dayrange" || command == "status-dayrange" || command == "replay-dayrange" {
		return runDayRange(ctx, c, log, command, *asof, *code, *dayRangeOffline)
	}
	if command == "experiment" {
		if *holdout {
			return runHoldout(ctx, c, *parent, log)
		}
		var interval *strategy.BacktestRange
		if *from != "" {
			interval = &strategy.BacktestRange{From: *from, Through: *through}
		}
		return runExperiment(ctx, c, *asof, *parent, *policy, log, interval)
	}
	if command == "run" {
		rt, err := stockruntime.New(stockruntime.Options{Version: version, Config: &c, Logger: log})
		if err != nil {
			return err
		}
		defer rt.Close()
		return rt.Run(ctx)
	}
	openStore := storage.Open
	readOnly := false
	if command == "replay" || command == "status" || command == "status-audit" || command == "preview" || command == "research" || command == "check" {
		openStore = storage.OpenView
		readOnly = true
	}
	store, err := openStore(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer store.Close()
	if command == "sync-context" {
		result, err := runContextSync(ctx, c, *asof, log)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			return err
		}
		if result.Status != "complete" {
			return errors.New("market context synchronization is incomplete")
		}
		return nil
	}
	openMarket := storage.OpenMarket
	if readOnly {
		openMarket = storage.OpenMarketView
	}
	marketDB, err := openMarket(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer marketDB.Close()
	if command == "check" && !marketDB.HasDatabase() {
		return errors.New("market database is not initialized; run stock-watcher init")
	}
	if command == "sync-intraday" {
		if !c.Intraday.Enabled {
			return errors.New("intraday synchronization is disabled")
		}
		client := market.NewMinuteClient(c)
		defer client.Close()
		r, err := intraday.New(c, store, marketDB, client, log).Sync(ctx, time.Now())
		if err != nil {
			return err
		}
		if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
			return err
		}
		if r.Failed > 0 {
			return errors.New("intraday synchronization has failed or incomplete stocks")
		}
		return nil
	}
	if command == "backfill-minutes" {
		return runMinuteBackfill(ctx, c, marketDB, log, *asof, *from, *backfillWorkers, *backfillProvider, *backfillCodes, *importFrom, *referenceDir)
	}

	source, err := market.NewSources(c, log)
	if err != nil {
		return err
	}
	defer source.Close()
	noticeChannels, closeChannels, err := channels.Build(c)
	if err != nil {
		return err
	}
	defer closeChannels()
	for _, channel := range c.NotificationChannels() {
		log.RegisterSecret(channel.Webhook)
		log.RegisterSecret(channel.Secret)
	}
	log.RegisterSecret(c.Wechat.AppSecret)
	m := monitor.NewWithMarketDB(c, store, marketDB, source, noticeChannels, log)
	m.BuildVersion = version
	if (command == "sync" || command == "once") && c.MarketContext.Enabled {
		ctxRuntime, err := openMarketContextRuntime(c, log)
		if err != nil {
			return err
		}
		defer ctxRuntime.Close()
		m.MarketContextSync = ctxRuntime.syncer.ForDate
	}
	cal := calendar.New(c)
	if _, err := cal.Trading(calendar.Date(time.Now())); err != nil {
		return err
	}
	log.Info("Stock watcher started", "command", command, "version", version, "pool_size", len(c.Stocks), "notification_channels", len(noticeChannels))
	printJSON := func(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
	switch command {
	case "check":
		if err := store.Probe(); err != nil {
			return err
		}
		_, err := m.Status()
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"status": "ready", "pool_size": len(c.Stocks), "cifs_required": c.RequireCIFS, "notification_channels": len(noticeChannels), "selection_enabled": c.Selection.Enabled, "calendar_through": c.Calendar.ValidThrough, "market_sources": source.Names()})
	case "replay":
		r, err := m.Replay(ctx, *revision)
		if err != nil {
			return err
		}
		return printJSON(r)
	case "status":
		s, err := m.Status()
		if err != nil {
			return err
		}
		return printJSON(s)
	case "sync", "status-backfill", "status-refresh", "status-audit", "preview", "research":
		day := *asof
		if day == "" {
			day, err = cal.Completed(time.Now())
			if err != nil {
				return err
			}
		}
		switch command {
		case "sync":
			r, err := m.Sync(ctx, day)
			if err != nil {
				return err
			}
			return printJSON(r)
		case "status-backfill":
			r, err := m.StatusBackfill(ctx, day)
			if err != nil {
				return err
			}
			return printJSON(r)
		case "status-refresh":
			r, err := m.StatusRefresh(ctx, day)
			if err != nil {
				return err
			}
			return printJSON(r)
		case "status-audit":
			r, err := m.StatusAudit(ctx, day)
			if err != nil {
				return err
			}
			return printJSON(r)
		}
		r, err := m.PreviewContext(ctx, day)
		if err != nil {
			return err
		}
		if command == "research" {
			if r.Research == nil {
				return errors.New("research requires enabled short-horizon strategy")
			}
			return printJSON(r.Research)
		}
		return printJSON(r)
	case "retry":
		channel := ""
		if f.NArg() == 1 {
			channel = f.Arg(0)
		}
		return m.Retry(channel)
	case "once":
		return m.Cycle(ctx)
	}
	return nil
}
