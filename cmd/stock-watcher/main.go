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
	"stock-watcher/internal/calendar"
	"stock-watcher/internal/config"
	"stock-watcher/internal/dashboard"
	"stock-watcher/internal/feishu"
	"stock-watcher/internal/intraday"
	"stock-watcher/internal/logging"
	"stock-watcher/internal/market"
	"stock-watcher/internal/monitor"
	"stock-watcher/internal/notify"
	"stock-watcher/internal/storage"
	"stock-watcher/internal/strategy"
	"syscall"
	"time"
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
		return errors.New("usage: stock-watcher check-config|init|check|sync|sync-intraday|preview|research|experiment|plan|run|once|status|retry [channel] [-config config.yaml] [-as-of YYYY-MM-DD]")
	}
	command := args[0]
	if command == "version" {
		fmt.Println(version)
		return nil
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	path := f.String("config", "config.yaml", "configuration file")
	asof := f.String("as-of", "", "completed data date (sync/preview/research/experiment only)")
	code := f.String("code", "", "stock code (plan only)")
	parent := f.String("parent", "", "parent experiment ID (experiment only)")
	policy := f.String("policy", "baseline", "baseline, upper-wick or risk-qualified (experiment only)")
	entry := f.Float64("entry-price", 0, "actual buy fill price (plan only)")
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
	case "check-config", "init", "check", "sync", "sync-intraday", "preview", "research", "experiment", "plan", "run", "once", "status", "retry":
	default:
		return errors.New("unknown command")
	}
	if *asof != "" && command != "sync" && command != "preview" && command != "research" && command != "experiment" {
		return errors.New("as-of is supported by sync, preview, research and experiment only")
	}
	if command != "plan" && (*code != "" || *entry != 0) {
		return errors.New("trade price flags require the plan command")
	}
	if command != "experiment" && (*parent != "" || *policy != "baseline") {
		return errors.New("experiment flags require the experiment command")
	}
	c, err := config.Load(*path)
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
		store, err := storage.Open(c.DataDir, c.RequireCIFS)
		if err != nil {
			return err
		}
		defer store.Close()
		marketDB, err := storage.InitMarket(c.DataDir, c.RequireCIFS)
		if err != nil {
			return err
		}
		defer marketDB.Close()
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "initialized", "market_db": marketDB.Path()})
	}
	options, err := logging.EnvOptions()
	if err != nil {
		return err
	}
	if command == "check-config" {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "valid", "pool_size": len(c.Stocks)})
	}
	if !filepath.IsAbs(options.Dir) {
		options.Dir = filepath.Join(filepath.Dir(*path), options.Dir)
	}
	log, err := logging.New(options)
	if err != nil {
		return err
	}
	defer log.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if command == "experiment" {
		return runExperiment(ctx, c, *asof, *parent, *policy, log)
	}
	openStore := storage.Open
	readOnly := false
	if command == "status" || command == "preview" || command == "research" || command == "check" {
		openStore = storage.OpenView
		readOnly = true
	}
	store, err := openStore(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer store.Close()
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

	source, err := market.NewSources(c, log)
	if err != nil {
		return err
	}
	defer source.Close()
	channels, closeChannels, err := buildChannels(c)
	if err != nil {
		return err
	}
	defer closeChannels()
	for _, channel := range c.NotificationChannels() {
		log.RegisterSecret(channel.Webhook)
		log.RegisterSecret(channel.Secret)
	}
	m := monitor.NewWithMarketDB(c, store, marketDB, source, channels, log)
	cal := calendar.New(c)
	if _, err := cal.Trading(calendar.Date(time.Now())); err != nil {
		return err
	}
	log.Info("Stock watcher started", "command", command, "version", version, "pool_size", len(c.Stocks), "notification_channels", len(channels))
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
		return printJSON(map[string]any{"status": "ready", "pool_size": len(c.Stocks), "cifs_required": c.RequireCIFS, "notification_channels": len(channels), "selection_enabled": c.Selection.Enabled, "calendar_through": c.Calendar.ValidThrough, "market_sources": source.Names()})
	case "status":
		s, err := m.Status()
		if err != nil {
			return err
		}
		return printJSON(s)
	case "sync", "preview", "research":
		day := *asof
		if day == "" {
			day, err = cal.Completed(time.Now())
			if err != nil {
				return err
			}
		}
		if command == "sync" {
			r, err := m.Sync(ctx, day)
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
	case "run":
		monitorRun := m.Run
		if c.Intraday.Enabled {
			client := market.NewMinuteClient(c)
			defer client.Close()
			minuteMonitor := intraday.New(c, store, marketDB, client, log)
			monitorRun = func(ctx context.Context) error { return runMonitors(ctx, m.Run, minuteMonitor.Run) }
		}
		var err error
		if c.Web.Enabled {
			err = runServices(ctx, c.Web.Listen, dashboard.NewHandler(c, marketDB, log), monitorRun, log)
		} else {
			err = monitorRun(ctx)
		}
		if errors.Is(err, context.Canceled) {
			log.Info("Stock watcher stopped")
			return nil
		}
		return err
	}
	return nil
}

func buildChannels(c config.Config) ([]notify.Channel, func(), error) {
	specs := c.NotificationChannels()
	channels := make([]notify.Channel, 0, len(specs))
	closers := []func(){}
	for _, spec := range specs {
		switch spec.Type {
		case "feishu":
			if spec.Webhook == "" {
				return nil, func() {}, errors.New("enabled Feishu channel requires a webhook")
			}
			if err := feishu.ValidateWebhook(spec.Webhook); err != nil {
				return nil, func() {}, errors.New("enabled Feishu channel has an invalid webhook")
			}
			client := feishu.NewNamedClient(spec.Name, spec.Webhook, spec.Secret, config.Duration(c.Timeout))
			channels = append(channels, client)
			closers = append(closers, client.Close)
		default:
			return nil, func() {}, errors.New("unsupported notification channel type")
		}
	}
	return channels, func() {
		for _, close := range closers {
			close()
		}
	}, nil
}
