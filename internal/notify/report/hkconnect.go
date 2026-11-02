package report

import (
	"fmt"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/articlechart"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/hkrecommend"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
)

// HKConnectArticle renders the independent Stock Connect rule report. It uses
// the same article-only delivery path as the A-share draft but never claims to
// reuse A-share model or execution assumptions.
func HKConnectArticle(recommendation hkrecommend.Recommendation) (notify.Message, error) {
	expires, err := time.ParseInLocation("2006-01-02 15:04", recommendation.Target+" 09:30", market.Shanghai)
	if err != nil {
		return notify.Message{}, err
	}
	title := hkConnectArticleTitle(recommendation)
	body := hkConnectArticleBody(recommendation)
	return notify.Message{
		Version:        2,
		EventID:        recommendation.ID + ":hk-connect:article",
		DisplayEventID: recommendation.ID,
		PartLabel:      "港股通",
		Title:          title,
		PlainText:      "【公众号标题】\n" + title + "\n\n【公众号正文】\n" + stripArticleChartTokens(body),
		Article: &notify.Article{
			Title:  title,
			Digest: hkConnectArticleDigest(recommendation),
			Body:   body,
			Charts: hkConnectArticleCharts(recommendation),
		},
		ExpiresAt: expires,
	}, nil
}

func hkConnectArticleTitle(recommendation hkrecommend.Recommendation) string {
	return fmt.Sprintf("港股通精选｜%s 日线趋势观察", articleMonthDay(recommendation.Target))
}

func hkConnectArticleDigest(recommendation hkrecommend.Recommendation) string {
	if len(recommendation.Candidates) == 0 {
		return fmt.Sprintf("%s港股通规则筛选：本期没有同时满足趋势门槛的观察对象。", articleMonthDay(recommendation.Target))
	}
	return fmt.Sprintf("%s港股通规则筛选：%d 只观察对象，附趋势分、均线/MACD/RSI 与结构图。独立于 A 股模型。", articleMonthDay(recommendation.Target), len(recommendation.Candidates))
}

func hkConnectArticleBody(recommendation hkrecommend.Recommendation) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s 港股通日线趋势观察\n\n", articleMonthDay(recommendation.Target))
	fmt.Fprintf(&out, "信号日 %s，目标观察日 %s。当前港股通股票池 %d 只，其中 %d 只有完整当日日线，%d 只通过全部趋势门槛；本期展示前 %d 只。本文是按 A 股日线趋势规则独立复算的港股通观察稿，不是 A 股初筛或精排模型的迁移结果。\n\n",
		recommendation.AsOf, recommendation.Target, recommendation.UniverseTotal, recommendation.Eligible, recommendation.Passed, recommendation.Selected)
	out.WriteString("机会分来自均线、MACD、RSI、连续上涨与量比的规则评分，不是成功概率，也不是未来收益承诺。\n\n")

	if len(recommendation.Candidates) == 0 {
		out.WriteString("## 本期结论\n\n今天没有股票同时满足全部趋势门槛。该结果不是“看空港股通”，而是本期不生成观察名单；停牌、数据不完整或未达门槛的标的均不补位。\n\n")
	} else {
		out.WriteString("## 一、本期观察名单\n\n")
		for _, candidate := range recommendation.Candidates {
			writeHKConnectCandidate(&out, candidate)
		}
	}

	if len(recommendation.Filtered) > 0 {
		out.WriteString("## 二、筛选分布\n\n")
		for _, item := range recommendation.Filtered {
			fmt.Fprintf(&out, "- %s：%d 只\n", hkConnectReasonLabel(item.Reason), item.Count)
		}
		out.WriteString("\n")
	}

	out.WriteString("## 三、口径与边界\n\n")
	for _, rule := range recommendation.Rules {
		out.WriteString("- " + rule + "\n")
	}
	for _, limitation := range recommendation.Limitations {
		out.WriteString("- " + limitation + "\n")
	}
	out.WriteString("- 数据来自独立 `hk-market.db` 的腾讯港股日线；前复权用于趋势指标，文章不使用 A 股分钟模型、第二层精排或成交模拟。\n\n")
	out.WriteString("风险提示：本文仅用于数据研究和观察，不构成证券买卖建议，也不承诺收益。\n")
	return out.String()
}

func writeHKConnectCandidate(out *strings.Builder, candidate hkrecommend.RecommendationCandidate) {
	fmt.Fprintf(out, "### 第%d名｜%s %s\n\n", candidate.Rank, escape(candidate.Name), candidate.Code)
	fmt.Fprintf(out, "- **收盘**：%.3f；日涨跌 %+.2f%%\n", candidate.Close, candidate.ChangePct)
	fmt.Fprintf(out, "- **趋势分**：%d/100；MA5 %.3f / MA10 %.3f / MA20 %.3f\n", candidate.Metrics.Score, candidate.Metrics.MA5, candidate.Metrics.MA10, candidate.Metrics.MA20)
	fmt.Fprintf(out, "- **动能**：RSI14 %.1f；量比 %.2f；DIF %.4f / DEA %.4f；连续上涨 %d 日\n\n",
		candidate.Metrics.RSI14, candidate.Metrics.VolumeRatio, candidate.Metrics.DIF, candidate.Metrics.DEA, candidate.Metrics.UpDays)
	if candidate.Chart != nil {
		out.WriteString("#### 结构图谱\n\n")
		out.WriteString(articleChartTokenPrefix + candidate.Chart.ID + "]]\n\n")
	}
}

func hkConnectArticleCharts(recommendation hkrecommend.Recommendation) []articlechart.Spec {
	out := make([]articlechart.Spec, 0, len(recommendation.Candidates))
	seen := map[string]bool{}
	for _, candidate := range recommendation.Candidates {
		if candidate.Chart == nil || seen[candidate.Chart.ID] {
			continue
		}
		spec := *candidate.Chart
		spec.Caption = fmt.Sprintf("%s %s 港股日K（前复权）；橙笔与蓝色结构区用于观察，不构成交易建议。", candidate.Code, escape(candidate.Name))
		out = append(out, spec)
		seen[spec.ID] = true
	}
	return out
}

func hkConnectReasonLabel(reason string) string {
	labels := map[string]string{
		"missing_data":                    "缺少日线",
		"stale_or_suspended":              "停牌或非当日数据",
		"no_trading_volume":               "当日无成交",
		"insufficient_history":            "历史不足",
		"insufficient_or_invalid_history": "指标输入无效",
		"special_treatment":               "风险警示名称",
		"no_bullish_alignment":            "均线未形成多头排列",
		"insufficient_volume_or_macd":     "量比或 MACD 未达门槛",
		"overheat_or_weakness":            "RSI 或日涨跌幅不在区间",
		"score_below_threshold":           "趋势分不足",
	}
	if label := labels[reason]; label != "" {
		return label
	}
	return reason
}
