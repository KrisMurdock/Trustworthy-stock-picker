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
	"stock-watcher/internal/storage"
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
		return errors.New("usage: stock-watcher check|sync|preview|run|once|status|retry [-config config.yaml] [-as-of YYYY-MM-DD]")
	}
	command := args[0]
	if command == "version" {
		fmt.Println(version)
		return nil
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	path := f.String("config", "config.yaml", "configuration file")
	asof := f.String("as-of", "", "completed data date (sync/preview only)")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() > 0 {
		return errors.New("unexpected positional arguments")
	}
	switch command {
	case "check", "sync", "preview", "run", "once", "status", "retry":
	default:
		return errors.New("unknown command")
	}
	if *asof != "" && command != "sync" && command != "preview" {
		return errors.New("as-of is supported by sync and preview only")
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
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
	store, err := storage.Open(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer store.Close()
	source := market.NewClient(config.Duration(c.Timeout))
	defer source.Close()
	sender := feishu.NewClient(c.Feishu.Webhook, c.Feishu.Secret, config.Duration(c.Timeout))
	defer sender.Close()
	m := monitor.New(c, store, source, sender, log)
	cal := calendar.New(c)
	if _, err := cal.Trading(calendar.Date(time.Now())); err != nil {
		return err
	}
	log.Info("Stock watcher started", "command", command, "version", version, "pool_size", len(c.Stocks), "feishu_enabled", c.Feishu.Enabled)
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
		return printJSON(map[string]any{"status": "ready", "pool_size": len(c.Stocks), "cifs_required": c.RequireCIFS, "feishu_enabled": c.Feishu.Enabled, "selection_enabled": c.Selection.Enabled, "calendar_through": c.Calendar.ValidThrough})
	case "status":
		s, err := m.Status()
		if err != nil {
			return err
		}
		return printJSON(s)
	case "sync", "preview":
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
		r, err := m.Preview(day)
		if err != nil {
			return err
		}
		return printJSON(r)
	case "retry":
		return m.Retry()
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
