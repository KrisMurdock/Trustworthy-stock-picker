package report

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/lowvaluation"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
)

// LowValuationArticle renders the independent full-market low-valuation
// screen. It is article-only and deliberately does not reuse the A-share
// second-stage or HK Connect message types.
func LowValuationArticle(rec lowvaluation.Recommendation) (notify.Message, error) {
	if rec.Version != lowvaluation.SchemaVersion || rec.Market != lowvaluation.Market || strings.TrimSpace(rec.ID) == "" {
		return notify.Message{}, errors.New("invalid low valuation recommendation identity")
	}
	if _, err := time.Parse("2006-01-02", rec.AsOf); err != nil {
		return notify.Message{}, errors.New("invalid low valuation as-of date")
	}
	target, err := time.Parse("2006-01-02", rec.Target)
	if err != nil || target.Format("2006-01-02") <= rec.AsOf {
		return notify.Message{}, errors.New("invalid low valuation target date")
	}
	if rec.Selected != len(rec.Candidates) || rec.Selected > rec.TopN {
		return notify.Message{}, errors.New("invalid low valuation candidate count")
	}
	expires, err := time.ParseInLocation("2006-01-02 15:04", rec.Target+" 09:30", time.FixedZone("Asia/Shanghai", 8*60*60))
	if err != nil {
		return notify.Message{}, err
	}
	article := notify.Article{
		Title:  lowValuationArticleTitle(rec),
		Digest: lowValuationArticleDigest(rec),
		Body:   lowValuationArticleBody(rec),
	}
	return notify.Message{
		Version: 2, EventID: rec.ID + ":low_valuation", DisplayEventID: rec.ID,
		PartLabel: "低估值股", Title: article.Title, Article: &article, ExpiresAt: expires,
	}, nil
}

func lowValuationArticleTitle(rec lowvaluation.Recommendation) string {
	return fmt.Sprintf("全市场低估值股｜%s 财务折价观察", articleMonthDay(rec.Target))
}

func lowValuationArticleDigest(rec lowvaluation.Recommendation) string {
	if len(rec.Candidates) == 0 {
		return fmt.Sprintf("信号日 %s：全市场股票池 %d 只有行情、%d 只可估值，本期没有股票低于财务研究区间下沿。", rec.AsOf, rec.Priced, rec.Valued)
	}
	return fmt.Sprintf("全市场股票池 %d 只有行情、%d 只可估值，%d 只低于财务研究区间下沿；按距下沿折价升序展示前 %d 只。", rec.Priced, rec.Valued, rec.Eligible, len(rec.Candidates))
}

func lowValuationArticleBody(rec lowvaluation.Recommendation) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# 全市场低估值股｜%s 财务折价观察\n\n", articleMonthDay(rec.Target))
	fmt.Fprintf(&out, "信号日 %s，目标观察日 %s。全市场股票池覆盖 %d 只，其中 %d 只具有当日不复权收盘价，%d 只可生成财务研究区间，%d 只低于保守区间下沿；本期展示前 %d 只。\n\n",
		rec.AsOf, rec.Target, rec.UniverseTotal, rec.Priced, rec.Valued, rec.Eligible, len(rec.Candidates))
	out.WriteString("排名口径：只保留现价低于财务研究区间下沿的股票，并按“距下沿折价 =（下沿－现价）÷下沿”升序排列；折价越小，价格越接近估值区间。该排序不是收益预测、成功概率或买卖信号。\n\n")

	if len(rec.Candidates) == 0 {
		out.WriteString("## 本期结论\n\n本期没有股票同时满足“有当日行情、估值可用、现价低于保守下沿”。这不是全市场看空，而是当前数据下不生成低估值名单；缺失、停牌、未达条件或财务数据不足的股票不补位。\n\n")
	} else {
		out.WriteString("## 一、本期低估值名单\n\n")
		for _, candidate := range rec.Candidates {
			writeLowValuationCandidate(&out, candidate)
		}
	}

	if len(rec.Filtered) > 0 {
		out.WriteString("## 二、筛选分布\n\n")
		for _, item := range rec.Filtered {
			fmt.Fprintf(&out, "- %s：%d 只\n", lowValuationReasonLabel(item.Reason), item.Count)
		}
		out.WriteString("\n")
	}

	out.WriteString("## 三、方法与边界\n\n")
	out.WriteString("- 财务研究区间使用新浪公开公司资料中的 TTM EPS、前一年 EPS、每股净资产、四季度净利润和总股本；区间公式与日报中的财务研究区间一致。\n")
	out.WriteString("- 排名使用全市场 `all-market.db` 中已完成当日同步的不复权日线收盘价，与财务估值价格锚保持同一口径、同一信号日。\n")
	if rec.SectorCoveragePct > 0 {
		fmt.Fprintf(&out, "- Tushare 行业快照覆盖 %.1f%% 的已定价股票，仅用于金融/非金融估值边界；行业缺失的股票按非金融边界计算并保留风险。\n", rec.SectorCoveragePct)
	} else {
		out.WriteString("- 本次没有可用的行业快照，金融与非金融边界无法区分；结果只能作为数据质量受限的研究观察。\n")
	}
	for _, limitation := range rec.Limitations {
		out.WriteString("- " + limitation + "\n")
	}
	out.WriteString("- 估值使用最近四季度财报快照，盈利为负、净资产非正、ROE/PB 异常或财务请求失败时标记暂缺，不补造区间。\n\n")
	out.WriteString("风险提示：本文仅用于数据研究，不构成证券买卖建议、目标价、收益承诺或成功概率判断；历史财务数据不代表未来表现。\n")
	return out.String()
}

func writeLowValuationCandidate(out *strings.Builder, candidate lowvaluation.Candidate) {
	estimate := candidate.Estimate
	fmt.Fprintf(out, "### 第%d名｜%s %s\n\n", candidate.Rank, escape(candidate.Name), candidate.Code)
	sector := articleValue(candidate.Sector, "行业未记录")
	fmt.Fprintf(out, "- **行业 / 现价**：%s；%.2f 元（%s 收盘）\n", sector, candidate.Close, candidate.PriceDate)
	fmt.Fprintf(out, "- **财务研究区间**：%.2f~%.2f 元；中枢 %.2f 元\n", estimate.Lower, estimate.Upper, estimate.Central)
	fmt.Fprintf(out, "- **距下沿折价**：%.2f%%；中枢空间 %.2f%%\n", candidate.GapToLowerPct, candidate.UpsideToCentralPct)
	growth := "暂缺"
	if estimate.GrowthKnown {
		growth = fmt.Sprintf("%+.1f%%", estimate.GrowthPct)
	}
	fmt.Fprintf(out, "- **财务锚**：TTM EPS %.2f 元；每股净资产 %.2f 元；ROE %.1f%%；盈利同比 %s；PE %.1f 倍、PB %.1f 倍\n",
		estimate.EPS, estimate.BookValuePerShare, estimate.ROEPct, growth, estimate.PE, estimate.PB)
	confidence := "中"
	if estimate.Confidence == "low" {
		confidence = "低"
	}
	fmt.Fprintf(out, "- **置信度**：%s；%s\n\n", confidence, lowValuationConfidenceNote(estimate))
}

func lowValuationConfidenceNote(estimate fairvalue.Estimate) string {
	labels := map[string]string{
		"prior_year_eps_missing":            "前一年 EPS 缺失，区间已加宽",
		"eps_reconciliation_near_tolerance": "EPS 交叉校验接近容差",
	}
	notes := make([]string, 0, len(estimate.Warnings))
	for _, warning := range estimate.Warnings {
		if label := labels[warning]; label != "" {
			notes = append(notes, label)
		}
	}
	if len(notes) == 0 {
		return "TTM 相对估值，未触发额外数据告警。"
	}
	return strings.Join(notes, "；") + "。"
}

func lowValuationReasonLabel(reason string) string {
	labels := map[string]string{
		"stale_or_suspended":    "停牌或缺少当日收盘",
		"no_trading_volume":     "当日无成交",
		"fundamentals_error":    "财务快照获取失败",
		"valuation_unavailable": "估值条件不足",
		"not_below_lower":       "未低于区间下沿",
	}
	if label := labels[reason]; label != "" {
		return label
	}
	return reason
}
