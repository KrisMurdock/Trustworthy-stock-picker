package main

import (
	"context"
	"errors"
	"fmt"
	"stock-watcher/internal/bottomstudy"
	"stock-watcher/internal/config"
	"stock-watcher/internal/experiment"
	"stock-watcher/internal/storage"
	"stock-watcher/internal/strategy"
	"time"
)

func archiveSpeedResult(ctx context.Context, snapshot experiment.Snapshot, report bottomstudy.SpeedReport, stockSHA, contextSHA string) (string, error) {
	if report.Engine != bottomstudy.SpeedEngine || report.SnapshotID != snapshot.ID {
		return "", errors.New("speed report does not match the frozen stock snapshot")
	}
	if len(report.Winners) == 0 {
		return "", errors.New("no training-qualified candidate to archive; run without -archive to inspect the grid")
	}
	c, err := config.Load()
	if err != nil {
		return "", err
	}
	spec := experiment.Spec{Title: "无止损 · 盈利目标持仓时间优化", Direction: "缩短达到净盈利2.5%／3%的等待时间", Hypothesis: "底部信号、入场确认及分批数能否让盈利目标更早触达。", Change: fmt.Sprintf("%d 对相同参数的个股与个股＋板块＋大盘实验；保存训练选中参数的两组结果。", len(report.Pairs)), Criteria: "不设价格止损，最多20个可卖出日。训练至少30入场、10信号日、10只股票；优先最早累计达到80%，再比较含失败等待分。后段只复查、不参与选优。", Range: &strategy.BacktestRange{From: report.Options.From, Through: report.Options.Through}}
	r, err := experiment.NewRecord(snapshot, spec, nil, version)
	if err != nil {
		return "", err
	}
	r.Engine = bottomstudy.SpeedEngine
	finished := time.Now().UTC()
	r.FinishedAt = &finished
	r.ResultSnapshot = bottomstudy.SpeedResultSnapshot(report, r)
	r.ResultSnapshot.InputSHA = map[string]string{"stocks": stockSHA, "context": contextSHA}
	if err := r.ResultSnapshot.Seal(); err != nil {
		return "", err
	}
	if err := r.ResultSnapshot.Validate(r); err != nil {
		return "", err
	}
	db, err := storage.OpenExperiments(ctx, c.DataDir, c.RequireCIFS, true)
	if err != nil {
		return "", err
	}
	defer db.Close()
	start := r
	start.FinishedAt, start.ResultSnapshot = nil, nil
	if err = db.Start(ctx, snapshot, start); err != nil {
		return "", err
	}
	r.Status, r.Verdict = "completed", "research"
	if err = db.Finish(ctx, r); err != nil {
		return "", err
	}
	return r.ID, nil
}
