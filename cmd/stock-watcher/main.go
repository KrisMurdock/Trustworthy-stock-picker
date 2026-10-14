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
	"stock-watcher/internal/feishu"
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
		return errors.New("usage: stock-watcher init|check|sync|preview|research|plan|run|once|status|retry [channel] [-config config.yaml] [-as-of YYYY-MM-DD]")
	}
	command := args[0]
	if command == "version" {
		fmt.Println(version)
		return nil
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	path := f.String("config", "config.yaml", "configuration file")
	asof := f.String("as-of", "", "completed data date (sync/preview/research only)")
	code := f.String("code", "", "stock code (plan only)")
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
	case "init", "check", "sync", "preview", "research", "plan", "run", "once", "status", "retry":
	default:
		return errors.New("unknown command")
	}
	if *asof != "" && command != "sync" && command != "preview" && command != "research" {
		return errors.New("as-of is supported by sync, preview and research only")
	}
	if command != "plan" && (*code != "" || *entry != 0) {
		return errors.New("trade price flags require the plan command")
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
	source := market.NewClient(config.Duration(c.Timeout))
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
		return printJSON(map[string]any{"status": "ready", "pool_size": len(c.Stocks), "cifs_required": c.RequireCIFS, "notification_channels": len(channels), "selection_enabled": c.Selection.Enabled, "calendar_through": c.Calendar.ValidThrough})
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
		err := m.Run(ctx)
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
