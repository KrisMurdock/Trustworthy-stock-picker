package bottomstudy

import (
	"fmt"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"math"
)

// SpeedResultSnapshot keeps every training-selected parameter pair in both
// arms, including unfavorable later results. It does not select on later data.
func SpeedResultSnapshot(report SpeedReport, record experiment.Record) *experiment.ResultSnapshot {
	s := &experiment.ResultSnapshot{Version: 1, Source: "no_stop_speed", ExperimentID: record.ID, DataID: record.Data.ID, Engine: record.Engine, CodeVersion: record.CodeVersion, FinishedAt: record.FinishedAt, Rows: []experiment.ResultRow{}, Notes: []string{
		"无止损；第一批买入后最多20个可卖出交易日。扣除手续费、税费、滑点并考虑T+1，达标日是可实现整笔净目标的首次交易日。",
		"只展示训练选出的参数及其相同参数的个股／个股＋板块＋大盘对照，包含后段变差的结果；没有按后段成绩挑选。完整网格仍保留在原始研究报告。",
		"达标平均持仓日仅统计成功交易；含失败等待分把未达标和未决记21日，不是实际平均持仓。最大已实现单笔亏损不是账户最大回撤，持仓期间最大浮亏本轮未记录。",
		"后段已被探索，不是独立盲测。当前行业归属回填历史存在回溯偏差；同日和同板块样本相关，少量样本不能保证未来胜率。",
	}}
	s.Notes = append(s.Notes, fmt.Sprintf("完整观察范围 %s 至 %s，训练完整路径结束边界 %s；当前快照显示训练入选参数的同参数两组对照。", report.Options.From, report.Options.Through, report.Options.TrainThrough))
	winners := map[string]bool{}
	for _, w := range report.Winners {
		winners[w.ID] = true
	}
	for _, pair := range report.Pairs {
		if !winners[pair.ID] {
			continue
		}
		for i, arm := range []SpeedArm{pair.Stock, pair.Combined} {
			label := []string{"个股", "个股＋板块＋大盘"}[i]
			for j, stats := range []SpeedStats{arm.Later, arm.Training} {
				period := []string{"后段复查（非盲测）", "训练选优"}[j]
				from, through := "", ""
				for _, t := range arm.Trades {
					if (j == 0 && t.Split != "later_period") || (j == 1 && t.Split != "training") {
						continue
					}
					if from == "" || t.SignalDate < from {
						from = t.SignalDate
					}
					if t.SignalDate > through {
						through = t.SignalDate
					}
				}
				row := experiment.ResultRow{ID: fmt.Sprintf("%s/%d/%d", pair.ID, i, j), Label: label, Period: period, From: from, Through: through, Rule: pair.Policy.Rule + " / " + pair.Confirmation, Target: pair.Policy.Target, Stop: experiment.Value(0.0), Horizon: 20, Batches: pair.Policy.Batches, Samples: stats.Samples, Resolved: stats.Resolved, Unresolved: stats.Unresolved + stats.UnknownEntries, Skipped: stats.Skipped}
				if stats.Samples > 0 {
					row.Targets, row.Rate, row.PenalizedDays = experiment.Value(stats.Targets), experiment.Value(stats.Rate), experiment.Value(stats.PenalizedDays)
				}
				if stats.Targets > 0 {
					row.TargetDays = experiment.Value(stats.SuccessDays)
				}
				if stats.Resolved > 0 {
					row.HoldingDays, row.MeanNet, row.MaxLoss = experiment.Value(stats.MeanDays), experiment.Value(stats.MeanNet), experiment.Value(math.Max(0, -stats.WorstNet))
				}
				s.Rows = append(s.Rows, row)
			}
		}
	}
	_ = s.Seal() // Values come from the validated finite simulation.
	return s
}
