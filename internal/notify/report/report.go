package report

import (
	"fmt"
	"stock-watcher/internal/config"
	"stock-watcher/internal/notify"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/strategy"
	"strings"
	"time"
)

const (
	trendTitle = "A股龙头池 · T+1 日线筛选"
	shortTitle = "A股龙头池 · 短线机会排名"
)

func FromSelection(r selection.Report) (notify.Message, error) {
	expires, err := time.ParseInLocation("2006-01-02 15:04", r.Target+" 09:30", time.FixedZone("Asia/Shanghai", 8*60*60))
	if err != nil {
		return notify.Message{}, err
	}

	title := trendTitle
	sections := []notify.Section{{
		Heading: "概览",
		Fields:  overviewFields(r),
	}}
	if r.Research == nil {
		sections = append(sections, notify.Section{
			Heading: "筛选口径",
			Fields:  trendCriteriaFields(r),
		})
		sections = append(sections, trendCandidateSections(r)...)
		if len(r.Excluded) > 0 {
			sections = append(sections, optionalExclusionSection("数据排除", r.Excluded))
		}
		if len(r.Filtered) > 0 {
			sections = append(sections, optionalExclusionSection("规则未通过", r.Filtered))
		}
		sections = append(sections, notify.Section{Heading: "说明", Lines: []string{"规则筛选结果；不保证次日表现。"}})
	} else {
		title = shortTitle
		sections = append(sections,
			researchSection(r.Research),
			strategyParametersSection(r.Research.Parameters),
		)
		sections = append(sections, shortCandidateSections(r)...)
		if len(r.Excluded) > 0 {
			sections = append(sections, optionalExclusionSection("数据排除", r.Excluded))
		}
		sections = append(sections, notify.Section{Heading: "说明", Lines: []string{
			"历史统计/机会分不代表未来获利概率。",
			"参考价仅供比较，非买入报价；实际成交后重算；买入当日不可卖，跳空亏损可超止损。",
		}})
	}

	return notify.Message{
		Version:   2,
		EventID:   r.ID,
		Title:     title,
		Sections:  sections,
		ExpiresAt: expires,
	}, nil
}

func overviewFields(r selection.Report) []notify.Field {
	return []notify.Field{
		{Label: "数据日", Value: r.AsOf},
		{Label: "目标日", Value: r.Target},
		{Label: "股池", Value: fmt.Sprint(r.PoolSize)},
		{Label: "有效行情", Value: fmt.Sprint(r.Eligible)},
		{Label: "规则通过", Value: fmt.Sprint(r.Passed)},
		{Label: "数据排除", Value: fmt.Sprint(len(r.Excluded))},
		{Label: "规则未过", Value: fmt.Sprint(len(r.Filtered))},
		{Label: "筛选状态", Value: selectionState(r)},
	}
}

func selectionState(r selection.Report) string {
	if !r.SelectionEnabled {
		return "未启用"
	}
	if len(r.Selected) > 0 {
		return "已启用·有入选"
	}
	return "已启用·无入选"
}

func trendCriteriaFields(r selection.Report) []notify.Field {
	fields := []notify.Field{
		{Label: "策略", Value: r.Strategy},
		{Label: "评分门槛", Value: fmt.Sprintf("≥%d", r.MinScore)},
		{Label: "候选上限", Value: fmt.Sprint(r.TopN)},
	}
	for i, rule := range r.Rules {
		fields = append(fields, notify.Field{Label: fmt.Sprintf("规则%d", i+1), Value: compactRule(rule)})
	}
	return fields
}

func compactRule(rule string) string {
	rule = strings.TrimSpace(rule)
	return strings.NewReplacer(
		"收盘价", "收盘",
		" > ", ">",
		" ≥ ", "≥",
		" < ", "<",
		" ≤ ", "≤",
		" 且 ", "；",
		"MACD DIF", "MACD-DIF",
		"日涨跌幅", "涨幅",
		"排除 ST / 停牌", "剔除ST/停牌",
		"排除ST/停牌", "剔除ST/停牌",
		"，", "；",
		"～", "–",
	).Replace(rule)
}

func trendCandidateSections(r selection.Report) []notify.Section {
	sections := make([]notify.Section, 0, min(len(r.Selected), 5))
	for i, candidate := range r.Selected[:min(len(r.Selected), 5)] {
		sections = append(sections, notify.Section{
			Heading:  fmt.Sprintf("入选 %d", i+1),
			Optional: true,
			Fields: []notify.Field{
				{Label: "股票", Value: candidate.Code + " " + escape(candidate.Name)},
				{Label: "涨幅", Value: fmt.Sprintf("%+.2f%%", candidate.ChangePct)},
				{Label: "收盘", Value: fmt.Sprintf("%.2f", candidate.Close)},
				{Label: "评分", Value: fmt.Sprint(candidate.Metrics.Score)},
				{Label: "MA5/10/20", Value: fmt.Sprintf("%.2f / %.2f / %.2f", candidate.Metrics.MA5, candidate.Metrics.MA10, candidate.Metrics.MA20)},
				{Label: "RSI14", Value: fmt.Sprintf("%.1f", candidate.Metrics.RSI14)},
				{Label: "量比", Value: fmt.Sprintf("%.2f", candidate.Metrics.VolumeRatio)},
				{Label: "连涨", Value: fmt.Sprintf("%d日", candidate.Metrics.UpDays)},
			},
		})
	}
	return sections
}

func researchSection(research *strategy.Research) notify.Section {
	return notify.Section{
		Heading: "研究与验证",
		Fields: []notify.Field{
			{Label: "训练截至", Value: research.TrainingThrough},
			{Label: "研究样本", Value: fmt.Sprint(research.SampleCount)},
			{Label: "开发区间", Value: evaluationRange(research.Development)},
			{Label: "开发表现", Value: conciseStats(research.Development.Recommended)},
			{Label: "独立验证区间", Value: evaluationRange(research.Holdout)},
			{Label: "独立验证表现", Value: conciseStats(research.Holdout.Recommended)},
			{Label: "独立验证结论", Value: validationResult(research.ValidationPassed)},
		},
	}
}

func evaluationRange(evaluation strategy.Evaluation) string {
	rangeText := "日期未记录"
	switch {
	case evaluation.From != "" && evaluation.Through != "":
		rangeText = evaluation.From + "～" + evaluation.Through
	case evaluation.From != "":
		rangeText = evaluation.From + "起"
	case evaluation.Through != "":
		rangeText = "截至" + evaluation.Through
	}
	return fmt.Sprintf("%s · %d信号日", rangeText, evaluation.SignalDates)
}

func conciseStats(stats strategy.Stats) string {
	return fmt.Sprintf("%d样本/%d日 · 净目标达标%.1f%% · 已退出均值%+.2f%%", stats.Samples, stats.Dates, stats.HitRate*100, stats.MeanNet)
}

func validationResult(passed bool) string {
	if passed {
		return "通过"
	}
	return "未通过"
}

func strategyParametersSection(parameters config.Strategy) notify.Section {
	return notify.Section{
		Heading: "参数口径",
		Fields: []notify.Field{
			{Label: "买入时点", Value: "次日开盘（模拟）"},
			{Label: "净目标/止损", Value: fmt.Sprintf("净+%.1f%% / %.1f%%", parameters.TargetNetPct, parameters.StopNetPct)},
			{Label: "持有期限", Value: fmt.Sprintf("最多%d交易日", parameters.MaxHoldingDays)},
			{Label: "单股预算", Value: formatMoney(parameters.TradeCash)},
			{Label: "佣金", Value: fmt.Sprintf("%.4f%% / 最低%.2f元", parameters.CommissionRate*100, parameters.MinCommission)},
			{Label: "过户/印花", Value: fmt.Sprintf("%.4f%% / %.4f%%", parameters.TransferRate*100, parameters.SellStampRate*100)},
			{Label: "单边滑点", Value: fmt.Sprintf("%.4f%%", parameters.SlippageRate*100)},
			{Label: "入场跳空上限", Value: fmt.Sprintf("%.1f%%", parameters.MaxEntryGapPct)},
			{Label: "证据门槛", Value: fmt.Sprintf("≥%d样本 / ≥%d交易日", parameters.MinEvidence, parameters.MinTrainingDays)},
			{Label: "独立验证期", Value: fmt.Sprintf("%d交易日", parameters.HoldoutDays)},
			{Label: "机会分门槛", Value: fmt.Sprintf("≥%.1f", parameters.MinRankScore)},
		},
	}
}

func shortCandidateSections(r selection.Report) []notify.Section {
	if r.Research == nil {
		return nil
	}
	displayed := make(map[string]bool, len(r.Selected))
	for _, candidate := range r.Selected {
		displayed[candidate.Code] = true
	}
	order := append([]selection.Candidate(nil), r.Selected...)
	for i := range r.Ranking {
		ranking := &r.Ranking[i]
		if !displayed[ranking.Code] {
			order = append(order, selection.Candidate{Code: ranking.Code, Name: ranking.Name, Ranking: ranking})
		}
	}

	sections := make([]notify.Section, 0, min(len(order), 5))
	for _, candidate := range order[:min(len(order), 5)] {
		ranking := candidate.Ranking
		if ranking == nil {
			continue
		}
		status := "观察"
		if ranking.Recommended && r.SelectionEnabled {
			status = "通过"
		}
		fields := []notify.Field{
			{Label: "股票", Value: ranking.Code + " " + escape(ranking.Name)},
			{Label: "排名", Value: fmt.Sprintf("第%d", ranking.Rank)},
			{Label: "状态", Value: status},
			{Label: "机会分", Value: fmt.Sprintf("%.1f", ranking.Score)},
			{Label: "依据", Value: tags(ranking.Reasons)},
			{Label: "风险", Value: tags(ranking.Risks)},
		}
		if ranking.Evidence != nil {
			fields = append(fields, notify.Field{Label: "历史证据", Value: detailedStats(*ranking.Evidence)})
		}
		if ranking.Plan != nil {
			fields = append(fields,
				notify.Field{Label: "参考买入", Value: fmt.Sprintf("%.2f", ranking.Plan.EntryPrice)},
				notify.Field{Label: "净+2.5%价", Value: fmt.Sprintf("%.2f", ranking.Plan.Target25)},
				notify.Field{Label: "净+3%价", Value: fmt.Sprintf("%.2f", ranking.Plan.Target30)},
				notify.Field{Label: "参考止损", Value: fmt.Sprintf("%.2f", ranking.Plan.StopPrice)},
			)
		}
		sections = append(sections, notify.Section{
			Heading:  fmt.Sprintf("候选 · 第%d名", ranking.Rank),
			Optional: true,
			Fields:   fields,
		})
	}
	return sections
}

func detailedStats(stats strategy.Stats) string {
	return fmt.Sprintf("%d样本/%d日 · 净目标达标%.1f%% · 已退出均值%+.2f%% · 下界%.1f%%",
		stats.Samples, stats.Dates, stats.HitRate*100, stats.MeanNet, stats.LowerBound*100)
}

func formatMoney(amount float64) string {
	digits := fmt.Sprintf("%.0f", amount)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", strings.TrimPrefix(digits, "-")
	}
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	return sign + digits + "元"
}

func optionalExclusionSection(heading string, excluded []selection.Excluded) notify.Section {
	section := notify.Section{Heading: heading, Optional: true}
	for _, item := range excluded[:min(len(excluded), 10)] {
		value := exclusionReason(item.Reason)
		if item.LastDate != "" {
			value += " · " + item.LastDate
		}
		section.Fields = append(section.Fields, notify.Field{Label: item.Code, Value: value})
	}
	return section
}

func exclusionReason(reason string) string {
	labels := map[string]string{
		"missing_data":                    "缺行情",
		"invalid_or_stale_data":           "行情过期/口径无效",
		"no_history":                      "无历史行情",
		"stale_or_suspended":              "非目标日行情/停牌",
		"no_trading_volume":               "无成交量",
		"insufficient_or_invalid_history": "历史不足/无效",
		"special_treatment":               "ST/退市",
		"no_bullish_alignment":            "均线未多头",
		"insufficient_volume_or_macd":     "量能/MACD不足",
		"overheat_or_weakness":            "RSI/涨幅不符",
		"score_below_threshold":           "评分不足",
		"strategy_evidence_or_risk_gate":  "证据/风险门槛未过",
	}
	if label := labels[reason]; label != "" {
		return label
	}
	return strings.ReplaceAll(reason, "_", " ")
}

func escape(s string) string {
	return strings.NewReplacer("<", "〈", ">", "〉", "*", "＊", "[", "［", "]", "］", "\n", " ", "\r", " ").Replace(s)
}

func tags(values []string) string {
	labels := map[string]string{
		"historical_rule_breakout":              "历史突破",
		"historical_rule_pullback":              "历史回踩",
		"historical_rule_momentum":              "历史动量",
		"historical_rule_volume_recovery":       "历史放量恢复",
		"rising_ma20":                           "MA20↑",
		"stronger_than_pool":                    "近5日强于股池",
		"strong_close":                          "收盘偏强",
		"insufficient_or_unprofitable_evidence": "历史证据不足",
		"extended_price":                        "涨幅/偏离过大",
		"large_daily_range":                     "日内波幅过大",
		"upper_wick":                            "上影线较长",
		"cash_insufficient":                     "预算不足最小买入量",
		"holdout_validation_not_passed":         "独立验证未通过",
	}
	if len(values) == 0 {
		return "无"
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = labels[value]
		if out[i] == "" {
			out[i] = strings.ReplaceAll(value, "_", " ")
		}
	}
	return escape(strings.Join(out, " / "))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
