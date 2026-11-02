package experiment

import (
	"fmt"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

// No strategy promotion gate has been calibrated for this new objective.
// A measured wave occurrence must not inherit the old take-profit verdict.
func applyUpwardWaveFindings(record *Record, parent *Record) {
	result := record.Result
	summary := result.Development.UpwardWave
	record.Verdict = "wave_unverified"
	result.Findings = []Finding{}
	result.Next = []string{
		"先比较五日波段率、覆盖率、样本数和完整窗口回吐；十日单列，不用延长等待掩盖五日失败。",
		"在开发区间按月及转红灯类型检查差异，冻结候选后再使用新的或未消费的保留区间；不根据保留结果修改波段定义。",
	}
	if summary != nil && summary.Model == strategy.UpwardWaveModel {
		for _, s := range summary.Windows {
			result.Findings = append(result.Findings, Finding{"measured", fmt.Sprintf("%d 日窗口：预选 %d 条、确认入场 %d 条、明确未入场 %d 条、入场未知 %d 条；有效路径 %d 条，上涨波段 %d 条，失败 %d 条，已入场但路径未知 %d 条。", s.Days, s.Selected, s.Entered, s.NotEntered, s.EntryUnknown, s.Known, s.Success, s.Failed, s.Unknown), []string{}})
			if s.Days == 5 && s.Known > 0 {
				record.Verdict = "wave_observed"
			}
		}
		if parent != nil && Comparable(*record, *parent) && parent.Result != nil {
			baseline := parent.Result.Development.UpwardWave
			if baseline != nil && baseline.Model == summary.Model && len(baseline.Windows) == 2 && len(summary.Windows) == 2 {
				a, b := summary.Windows[0], baseline.Windows[0]
				if a.SuccessRate != nil && b.SuccessRate != nil && a.Coverage != nil && b.Coverage != nil {
					result.Findings = append(result.Findings, Finding{"measured", fmt.Sprintf("相对父实验：五日有效样本波段率变化 %+.2f 个百分点，路径覆盖率变化 %+.2f 个百分点，有效样本变化 %+d。该差异尚不是独立验证结论。", (*a.SuccessRate-*b.SuccessRate)*100, (*a.Coverage-*b.Coverage)*100, a.Known-b.Known), []string{}})
				}
			}
		}
	}
	stats := result.Development.Research.Development.TopRanked
	if record.Stage == "holdout" {
		stats = result.Development.Research.Holdout.TopRanked
	}
	result.Findings = append(result.Findings,
		Finding{"measured", fmt.Sprintf("旧交易模型旁证：完成 %d 笔，未决 %d 笔，平均实现净收益 %.4f%%，净亏损达到或超过 3%% 共 %d 笔。旧止盈止损结果不决定波段标签。", stats.Resolved, stats.Unresolved, stats.MeanNet, stats.LossBreaches), []string{}},
		Finding{"limitation", "本轮评价现有排名和成交约束选出的 Top N 入场集合，未按波段目标重新训练排名，也不是全市场每次四灯信号的无条件胜率。快照来自当前股池，存在选择与幸存者偏差。", []string{}},
		Finding{"limitation", "四灯公式仍为 four_light_approx_v1、近似且未对齐目标软件。upward_wave_v0 仅定义事后路径；上涨段末端、收盘峰值、净标记不代表可执行卖点或实际收益。阈值尚未经独立验证。", []string{}},
	)
	if record.Spec.Range != nil {
		result.Findings = append(result.Findings, Finding{"limitation", "显式信号日期区间未声明独立保留集；完整路径可读取信号区间之后、快照截止之前的行情。", []string{}})
	} else if record.Stage != "holdout" {
		result.Findings = append(result.Findings, Finding{"limitation", "开发窗口严格截于预留区间之前；跨界的十日窗口记 unknown，预留区间未消费。", []string{}})
	}
}
