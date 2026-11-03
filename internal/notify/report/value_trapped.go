package report

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/valuetrapped"
)

// ValueTrappedArticle renders the independent full-market intersection of the
// financial valuation lower bound and low trapped-supply observation.
func ValueTrappedArticle(rec valuetrapped.Recommendation) (notify.Message, error) {
	if rec.Version != valuetrapped.SchemaVersion || rec.Market != valuetrapped.Market || strings.TrimSpace(rec.ID) == "" {
		return notify.Message{}, errors.New("invalid value-trapped recommendation identity")
	}
	if _, err := time.Parse("2006-01-02", rec.AsOf); err != nil {
		return notify.Message{}, errors.New("invalid value-trapped as-of date")
	}
	target, err := time.Parse("2006-01-02", rec.Target)
	if err != nil || target.Format("2006-01-02") <= rec.AsOf {
		return notify.Message{}, errors.New("invalid value-trapped target date")
	}
	if rec.Selected != len(rec.Candidates) || rec.Selected > rec.TopN || rec.TopN < 1 {
		return notify.Message{}, errors.New("invalid value-trapped candidate count")
	}
	if rec.MaxTrappedRatioPct <= 0 || rec.MaxTrappedRatioPct > 100 {
		return notify.Message{}, errors.New("invalid value-trapped ratio limit")
	}
	for _, candidate := range rec.Candidates {
		if candidate.Trapped == nil || candidate.Trapped.Status == strategy.TrappedStatusUnknown {
			return notify.Message{}, errors.New("value-trapped candidate has no valid trapped signal")
		}
		if candidate.Trapped.TrappedVolumeRatio*100 > rec.MaxTrappedRatioPct+1e-9 {
			return notify.Message{}, errors.New("value-trapped candidate exceeds the trapped ratio limit")
		}
	}
	expires, err := time.ParseInLocation("2006-01-02 15:04", rec.Target+" 09:30", time.FixedZone("Asia/Shanghai", 8*60*60))
	if err != nil {
		return notify.Message{}, err
	}
	article := notify.Article{
		Title:  valueTrappedArticleTitle(rec),
		Digest: valueTrappedArticleDigest(rec),
		Body:   valueTrappedArticleBody(rec),
	}
	return notify.Message{
		Version: 2, EventID: rec.ID + ":value_trapped", DisplayEventID: rec.ID,
		PartLabel: "低估值低套牢", Title: article.Title, Article: &article, ExpiresAt: expires,
	}, nil
}

func valueTrappedArticleTitle(rec valuetrapped.Recommendation) string {
	return fmt.Sprintf("低估值且低套牢｜%s 双重观察", articleMonthDay(rec.Target))
}

func valueTrappedArticleDigest(rec valuetrapped.Recommendation) string {
	if len(rec.Candidates) == 0 {
		return fmt.Sprintf("信号日 %s：%d 只低于财务研究区间下沿，但没有股票同时满足有效套牢信号和上方套牢量不超过 %.0f%%。", rec.AsOf, rec.LowValuationEligible, rec.MaxTrappedRatioPct)
	}
	return fmt.Sprintf("信号日 %s：%d 只低于财务研究区间下沿，%d 只套牢信号有效，%d 只同时满足上方套牢量不超过 %.0f%%；展示折价最小的前 %d 只。",
		rec.AsOf, rec.LowValuationEligible, rec.TrappedValid, rec.Eligible, rec.MaxTrappedRatioPct, len(rec.Candidates))
}

func valueTrappedArticleBody(rec valuetrapped.Recommendation) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# 低估值且低套牢｜%s 双重观察\n\n", articleMonthDay(rec.Target))
	fmt.Fprintf(&out, "信号日 %s，目标观察日 %s。全市场股票池覆盖 %d 只，其中 %d 只有当日不复权收盘价，%d 只可生成财务研究区间；%d 只低于保守区间下沿，%d 只套牢信号有效，最终 %d 只同时满足套牢量不超过 %.0f%%。本期展示 %d 只。\n\n",
		rec.AsOf, rec.Target, rec.UniverseTotal, rec.Priced, rec.Valued, rec.LowValuationEligible, rec.TrappedValid, rec.Eligible, rec.MaxTrappedRatioPct, len(rec.Candidates))
	out.WriteString("排名口径：先按距财务研究区间下沿的折价升序，再按上方套牢量升序、20 日成交额降序和代码升序排列。不计算综合分，避免把百分比折价与套牢量混成一个不可解释的分数。\n\n")

	if len(rec.Candidates) == 0 {
		out.WriteString("## 本期结论\n\n本期没有股票同时满足“现价低于财务研究区间下沿、套牢信号有效、上方套牢量不超过上限”。这不是对市场的单向判断；停牌、零成交、历史不足、流动性不足、套牢 unknown 或只满足单边条件的股票都不递补。\n\n")
	} else {
		out.WriteString("## 一、低估值且低套牢观察池\n\n")
		for _, candidate := range rec.Candidates {
			writeValueTrappedCandidate(&out, candidate)
		}
	}

	if len(rec.Filtered) > 0 || len(rec.TrappedUnknownReasons) > 0 {
		out.WriteString("## 二、筛选分布\n\n")
		for _, item := range rec.Filtered {
			fmt.Fprintf(&out, "- %s：%d 只\n", valueTrappedReasonLabel(item.Reason), item.Count)
		}
		unknownReasons := make([]string, 0, len(rec.TrappedUnknownReasons))
		for reason := range rec.TrappedUnknownReasons {
			unknownReasons = append(unknownReasons, reason)
		}
		sort.Strings(unknownReasons)
		for _, reason := range unknownReasons {
			fmt.Fprintf(&out, "- 套牢 unknown（%s）：%d 只\n", valueTrappedUnknownLabel(reason), rec.TrappedUnknownReasons[reason])
		}
		out.WriteString("\n")
	}

	out.WriteString("## 三、方法与边界\n\n")
	out.WriteString("- 财务研究区间沿用全市场低估值链路：TTM EPS、前一年 EPS、每股净资产和四季度净利润共同生成保守下沿、中枢与上沿，不是目标价。\n")
	out.WriteString("- 两条链路使用同一个 `all-market.db` 信号日、同一不复权收盘价；前复权价格不参与财务估值或历史成本。\n")
	out.WriteString("- 套牢信号使用最近 120 个交易日的不复权成交量和换手率，按剩余筹码逐日衰减，并按低-收-高三角分布切分浅度、中度和深度套牢区。\n")
	fmt.Fprintf(&out, "- 套牢扫描只覆盖低估值候选，避免对全市场重复抓取换手率；历史少于 %d 个交易日、信号日无成交或 20 日成交额低于 %.0f 万元的股票剔除。\n", rec.MinHistorySessions, rec.MinAmountCNY/10_000)
	out.WriteString("- 三档区间是上方成交筹码的价格带，不是支撑位、止损位或目标价；套牢量与真实持仓成本并不等价。\n")
	out.WriteString("- 流通股本校验由成交股数和换手率反推，只能发现明显的口径变化，不能替代正式除权、送转和股本事件数据。\n")
	for _, limitation := range rec.Limitations {
		out.WriteString("- " + limitation + "\n")
	}
	out.WriteString("\n风险提示：本文仅用于数据研究，不构成证券买卖建议、目标价、收益承诺或成功概率判断；历史成交与财务数据不代表未来表现。\n")
	return out.String()
}

func writeValueTrappedCandidate(out *strings.Builder, candidate valuetrapped.Candidate) {
	estimate := candidate.Estimate
	signal := candidate.Trapped
	fmt.Fprintf(out, "### 第%d名｜%s %s\n\n", candidate.Rank, escape(candidate.Name), candidate.Code)
	sector := articleValue(candidate.Sector, "行业未记录")
	dominant := "主要筹码在" + valueTrappedZoneLabel(signal.DominantZone)
	if signal.DominantZone == "" {
		dominant = "当前没有上方筹码"
	}
	fmt.Fprintf(out, "- **行业 / 现价**：%s；%.2f 元（%s 收盘）；20 日平均成交额 %.0f 万元\n", sector, candidate.Close, candidate.PriceDate, candidate.AvgAmount20CNY/10_000)
	fmt.Fprintf(out, "- **财务研究区间**：%.2f~%.2f 元；中枢 %.2f 元；距下沿折价 %.2f%%\n", estimate.Lower, estimate.Upper, estimate.Central, candidate.GapToLowerPct)
	fmt.Fprintf(out, "- **套牢状态**：%s；上方套牢量 %s；当前%s；%s\n",
		valueTrappedStatusLabel(signal.Status), valueTrappedPercent(signal.TrappedVolumeRatio), valueTrappedCurrentZone(signal.CurrentZone), dominant)
	fmt.Fprintf(out, "- **三档价格区间**：浅度 %s；中度 %s；深度 %s\n\n",
		valueTrappedZoneRange(signal, strategy.TrappedZoneShallow), valueTrappedZoneRange(signal, strategy.TrappedZoneModerate), valueTrappedZoneRange(signal, strategy.TrappedZoneDeep))
}

func valueTrappedZoneRange(signal *strategy.TrappedSignal, level string) string {
	label := valueTrappedZoneLabel(level)
	for _, zone := range signal.Zones {
		if zone.Level != level {
			continue
		}
		if zone.Low <= 0 || zone.High <= 0 || zone.VolumeShare <= 0 {
			return label + "无上方筹码"
		}
		return fmt.Sprintf("%s %.2f~%.2f 元（%d%%）", label, zone.Low, zone.High, zone.SharePct)
	}
	return label + "无上方筹码"
}

func valueTrappedCurrentZone(level string) string {
	switch level {
	case strategy.TrappedZoneShallow, strategy.TrappedZoneModerate, strategy.TrappedZoneDeep:
		return valueTrappedZoneLabel(level) + "套牢区"
	default:
		return "无上方套牢区"
	}
}

func valueTrappedZoneLabel(level string) string {
	switch level {
	case strategy.TrappedZoneShallow:
		return "浅度"
	case strategy.TrappedZoneModerate:
		return "中度"
	case strategy.TrappedZoneDeep:
		return "深度"
	default:
		return "无"
	}
}

func valueTrappedStatusLabel(status string) string {
	switch status {
	case strategy.TrappedStatusNone:
		return "无上方套牢"
	case strategy.TrappedStatusWatch:
		return "套牢观察"
	case strategy.TrappedStatusTriggered:
		return "套牢触发"
	default:
		return status
	}
}

func valueTrappedPercent(value float64) string {
	pct := value * 100
	if pct > 0 && pct < 0.01 {
		return fmt.Sprintf("%.3f%%", pct)
	}
	return fmt.Sprintf("%.2f%%", pct)
}

func valueTrappedReasonLabel(reason string) string {
	labels := map[string]string{
		"stale_or_suspended":        "停牌或缺少当日收盘",
		"no_trading_volume":         "当日无成交",
		"fundamentals_error":        "财务快照获取失败",
		"valuation_unavailable":     "估值条件不足",
		"not_below_lower":           "未低于区间下沿",
		"st_stock":                  "ST 股票",
		"insufficient_history":      "套牢历史不足 250 日",
		"stale_last_bar":            "套牢末日行情不匹配",
		"low_liquidity":             "20 日成交额不足",
		"invalid_history":           "不复权历史无效",
		"trapped_ratio_above_limit": "上方套牢量超过上限",
	}
	if label := labels[reason]; label != "" {
		return label
	}
	return reason
}

func valueTrappedUnknownLabel(reason string) string {
	labels := map[string]string{
		"turnover_history_unavailable":      "换手率历史不可用",
		"turnover_share_base_changed":       "推算流通股本口径变化",
		"no_volume_data":                    "无有效成交量",
		"insufficient_daily_bars":           "套牢日线不足",
		"unadjusted_history_required":       "需要不复权历史",
		"unsupported_volume_source_or_unit": "成交量来源或单位不支持",
	}
	if label := labels[reason]; label != "" {
		return label
	}
	return reason
}
