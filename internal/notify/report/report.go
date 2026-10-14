package report

import (
	"fmt"
	"stock-watcher/internal/notify"
	"stock-watcher/internal/selection"
	"strings"
	"time"
)

const title = "A股龙头池 · T+1 日线筛选"

func FromSelection(r selection.Report) (notify.Message, error) {
	expires, err := time.ParseInLocation("2006-01-02 15:04", r.Target+" 09:30", time.FixedZone("Asia/Shanghai", 8*60*60))
	if err != nil {
		return notify.Message{}, err
	}
	lines := []string{
		fmt.Sprintf("数据日期：%s · T+1：%s", r.AsOf, r.Target),
		fmt.Sprintf("龙头池 %d 只 · 有效日线 %d 只 · 排除 %d 只", r.PoolSize, r.Eligible, len(r.Excluded)),
		fmt.Sprintf("规则：%s · 分数 ≥ %d · 最多 %d 只", r.Strategy, r.MinScore, r.TopN),
		strings.Join(r.Rules, "\n"),
	}
	if r.Research != nil {
		s := r.Research.Parameters
		lines = []string{
			fmt.Sprintf("排名日期：%s · 计划买入日：%s", r.AsOf, r.Target),
			fmt.Sprintf("全池 %d 只 · 已排名 %d 只", r.PoolSize, len(r.Ranking)),
			strings.Join(r.Rules, "\n"),
			fmt.Sprintf("净收益口径 · 佣金 %.4f%% / 最低 %.2f 元 · 单股预算 %.0f 元 · 单边滑点 %.3f%%", s.CommissionRate*100, s.MinCommission, s.TradeCash, s.SlippageRate*100),
			"目标价和止损价以实际买入成交价重新计算；参考价不是买入报价。",
		}
		h := r.Research.Holdout
		lines = append(lines, fmt.Sprintf("独立验证 %s～%s · 规则通过 %d 个买入样本 · 净目标达标 %.1f%% · 已退出平均净收益 %.2f%%",
			h.From, h.Through, h.Recommended.Samples, h.Recommended.HitRate*100, h.Recommended.MeanNet))
		if !r.SelectionEnabled {
			lines = append(lines, "筛选尚未启用，以下仅为观察排名。")
		} else if len(r.Selected) == 0 {
			lines = append(lines, "本次无股票通过历史证据与风险门槛，以下仅为观察排名。")
		}
		displayed := map[string]bool{}
		for _, candidate := range r.Selected {
			displayed[candidate.Code] = true
		}
		order := append([]selection.Candidate(nil), r.Selected...)
		for i := range r.Ranking {
			candidate := &r.Ranking[i]
			if !displayed[candidate.Code] {
				order = append(order, selection.Candidate{Code: candidate.Code, Ranking: candidate})
			}
		}
		for _, candidate := range order[:min(len(order), 5)] {
			ranking := candidate.Ranking
			if ranking == nil {
				continue
			}
			label := "观察"
			if ranking.Recommended && r.SelectionEnabled {
				label = "规则通过"
			}
			line := fmt.Sprintf("全池第%d · **%s %s** · 机会分 %.1f · %s\n依据：%s · 风险：%s",
				ranking.Rank, ranking.Code, escape(ranking.Name), ranking.Score, label,
				tags(ranking.Reasons), tags(ranking.Risks))
			if ranking.Evidence != nil {
				line += fmt.Sprintf("\n历史模拟：%d 样本/%d 个信号日 · 净目标达标 %.1f%% · 已退出平均净收益 %.2f%%",
					ranking.Evidence.Samples, ranking.Evidence.Dates, ranking.Evidence.HitRate*100, ranking.Evidence.MeanNet)
			}
			if ranking.Plan != nil {
				line += fmt.Sprintf("\n参考买入 %.2f → 净+2.5%%价 %.2f / 净+3%%价 %.2f / 止损 %.2f",
					ranking.Plan.EntryPrice, ranking.Plan.Target25, ranking.Plan.Target30, ranking.Plan.StopPrice)
			}
			lines = append(lines, line)
		}
		lines = append(lines, "历史模拟与机会分不等于未来获利概率；买入当天不能卖出，跳空时亏损可能超过止损幅度。")
	}
	if !r.SelectionEnabled {
		lines = append(lines, "筛选尚未启用，不生成推荐股。")
	} else if len(r.Selected) == 0 && r.Research == nil {
		lines = append(lines, "本次没有满足筛选规则的股票。")
	}
	if r.Research == nil {
		for i, candidate := range r.Selected {
			lines = append(lines, fmt.Sprintf("%d. **%s %s** · %d 分 · 收盘 %.2f\nMA5/10/20 %.2f/%.2f/%.2f · RSI14 %.1f · 量比 %.2f · 连涨 %d 日",
				i+1, candidate.Code, escape(candidate.Name), candidate.Metrics.Score, candidate.Close,
				candidate.Metrics.MA5, candidate.Metrics.MA10, candidate.Metrics.MA20, candidate.Metrics.RSI14,
				candidate.Metrics.VolumeRatio, candidate.Metrics.UpDays))
		}
	}
	if len(r.Excluded) > 0 {
		codes := make([]string, 0, min(len(r.Excluded), 10))
		for _, candidate := range r.Excluded[:min(len(r.Excluded), 10)] {
			codes = append(codes, candidate.Code+" ("+candidate.Reason+")")
		}
		lines = append(lines, "排除："+strings.Join(codes, ", "))
	}
	lines = append(lines, "仅为规则筛选结果，不保证次日表现。", "事件 ID："+r.ID)
	return notify.Message{
		Version:   1,
		EventID:   r.ID,
		Title:     title,
		Sections:  []notify.Section{{Lines: lines}},
		ExpiresAt: expires,
	}, nil
}

func escape(s string) string {
	return strings.NewReplacer("<", "〈", ">", "〉", "*", "＊", "[", "［", "]", "］", "\n", " ", "\r", " ").Replace(s)
}

func tags(values []string) string {
	labels := map[string]string{
		"historical_rule_breakout":              "放量突破有历史证据",
		"historical_rule_pullback":              "趋势回踩有历史证据",
		"historical_rule_momentum":              "相对强势有历史证据",
		"historical_rule_volume_recovery":       "放量转强有历史证据",
		"rising_ma20":                           "20日均线上行",
		"stronger_than_pool":                    "近5日强于股池",
		"strong_close":                          "收盘接近日内高位",
		"insufficient_or_unprofitable_evidence": "历史样本或收益未达门槛",
		"extended_price":                        "短期涨幅或均线偏离过大",
		"large_daily_range":                     "日内波动过大",
		"upper_wick":                            "上影线较长",
		"cash_insufficient":                     "预算不足最小买入数量",
		"holdout_validation_not_passed":         "独立验证未通过",
	}
	if len(values) == 0 {
		return "无"
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = labels[value]
		if out[i] == "" {
			out[i] = value
		}
	}
	return escape(strings.Join(out, "、"))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
