package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"stock-watcher/internal/config"
	"stock-watcher/internal/experiment"
	"stock-watcher/internal/logging"
	"stock-watcher/internal/market"
	"stock-watcher/internal/storage"
	"stock-watcher/internal/strategy"
	"time"
)

// The experiment path never constructs market providers or notification clients.
func runExperiment(ctx context.Context, c config.Config, asof, parentID, policy string, log *logging.Logger, ranges ...*strategy.BacktestRange) error {
	spec, err := experiment.Specification(policy)
	if err != nil {
		return err
	}
	if len(ranges) > 0 {
		spec.Range = ranges[0]
	}
	if parentID != "" && !experiment.ValidID(parentID) {
		return errors.New("invalid parent experiment ID")
	}
	if policy != "baseline" && policy != "next-day-baseline" && policy != "conditional-entry" && parentID == "" {
		return errors.New("a contrast experiment requires -parent")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	db, err := storage.OpenExperiments(ctx, c.DataDir, c.RequireCIFS, true)
	if err != nil {
		return err
	}
	defer db.Close()
	var snapshot experiment.Snapshot
	var parent *experiment.Record
	if parentID != "" {
		p, err := db.Get(ctx, parentID)
		if err != nil {
			return err
		}
		parent = &p
		if spec.Range == nil {
			spec.Range = p.Spec.Range
		}
		if asof != "" && asof != p.Data.AsOf {
			return errors.New("as-of differs from the parent snapshot")
		}
		snapshot, err = db.Snapshot(ctx, p.Data.ID)
		if err != nil {
			return err
		}
	} else {
		marketDB, err := storage.OpenMarketView(c.DataDir, c.RequireCIFS)
		if err != nil {
			return err
		}
		defer marketDB.Close()
		if !marketDB.HasDatabase() {
			return errors.New("experiments require an initialized market database")
		}
		if asof == "" {
			data, err := marketDB.LoadDashboard(ctx, c.Stocks, "qfq")
			if err != nil {
				return err
			}
			asof = data.AsOf
		}
		if asof == "" {
			return errors.New("no saved market data for an experiment")
		}
		h, r, err := marketDB.LoadHistoriesPair(ctx, c.Stocks, asof, "qfq", true)
		if err != nil {
			return err
		}
		snapshot, err = experiment.NewSnapshot(c, asof, h, r)
		if err != nil {
			return err
		}
		if spec.EntryMode != "" {
			minutes := []market.MinuteHistory{}
			for _, stock := range snapshot.Stocks {
				h, readErr := marketDB.LoadMinutes(ctx, stock, 5)
				if errors.Is(readErr, fs.ErrNotExist) {
					continue
				}
				if readErr != nil {
					return readErr
				}
				minutes = append(minutes, h.MinuteHistory)
			}
			snapshot, err = experiment.WithMinutes(snapshot, minutes)
			if err != nil {
				return err
			}
		}
	}
	if spec.Range != nil {
		frozen := c
		frozen.Calendar.ValidFrom = snapshot.Calendar.From
		frozen.Calendar.ValidThrough = snapshot.Calendar.Through
		frozen.Calendar.Holidays = snapshot.Calendar.Holidays
		horizon := snapshot.Parameters.MaxHoldingDays
		if spec.NextDayRule != "" {
			horizon = 1
		}
		if err := spec.Range.Validate(frozen, snapshot.AsOf, horizon); err != nil {
			return err
		}
	}
	record, err := experiment.NewRecord(snapshot, spec, parent, version)
	if err != nil {
		return err
	}
	if err = db.Start(ctx, snapshot, record); err != nil {
		return err
	}
	log.Info("Development experiment started", "experiment_id", record.ID, "parent_id", record.ParentID, "snapshot_id", snapshot.ID, "stocks", snapshot.Included)
	evaluationErr := experiment.Evaluate(ctx, snapshot, &record, parent)
	if evaluationErr != nil {
		record.Status, record.Verdict, record.Error = "failed", "pending", "实验未完成，请检查数据覆盖、参数及运行日志。"
		if errors.Is(evaluationErr, context.Canceled) || errors.Is(evaluationErr, context.DeadlineExceeded) {
			record.Status = "canceled"
			record.Error = "实验已取消或超过 10 分钟时限。"
		}
		now := time.Now().UTC()
		record.FinishedAt = &now
		log.Warn("Development experiment failed", "experiment_id", record.ID, "error", evaluationErr)
	}
	// Persist cancellation with a fresh bounded context while releasing resources.
	saveCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err = db.Finish(saveCtx, record); err != nil {
		return err
	}
	log.Info("Development experiment recorded", "experiment_id", record.ID, "status", record.Status, "verdict", record.Verdict)
	if err = json.NewEncoder(os.Stdout).Encode(record); err != nil {
		return err
	}
	return evaluationErr
}
