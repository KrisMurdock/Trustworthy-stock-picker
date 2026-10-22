package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
)

func secondStageArticles(r selection.Report, expires time.Time) []notify.Message {
	result := r.SecondStage
	if result == nil || !result.PushEnabled || result.Status != secondstage.StatusOK || len(result.Candidates) == 0 {
		return nil
	}
	return []notify.Message{{
		Version:        2,
		EventID:        r.ID + ":second-stage:article",
		DisplayEventID: r.ID,
		PartLabel:      "精排文章",
		Title:          secondStageArticleTitle(*result),
		PlainText:      secondStageArticleText(r, *result),
		ExpiresAt:      expires,
	}}
}

func secondStageArticleTitle(result secondstage.Report) string {
	monthDay := result.Target
	if parsed, err := time.Parse("2006-01-02", result.Target); err == nil {
		monthDay = fmt.Sprintf("%d月%d日", parsed.Month(), parsed.Day())
	}
	title := fmt.Sprintf("A股龙头精排｜%s Top%d 观察名单", monthDay, len(result.Candidates))
	if result.Degraded {
		title += "（降级）"
	}
	return title
}

func secondStageArticleText(r selection.Report, result secondstage.Report) string {
	var out strings.Builder
	title := secondStageArticleTitle(result)
	out.WriteString("【公众号标题】\n")
	out.WriteString(title)
	out.WriteString("\n\n【公众号正文】\n")
	out.WriteString(fmt.Sprintf("基于 %s 收盘数据，本文整理面向 %s 的精排观察名单。模型从 %d 只初筛候选中输出 %d 只，排序标签是未来 3 个交易日最高价相对信号日收盘的最大涨幅。\n\n",
		result.AsOf, result.Target, result.InputCount, len(result.Candidates)))

	out.WriteString("先说结论\n")
	out.WriteString(secondStageConclusion(result))
	out.WriteString("\n\n")

	out.WriteString("一、精排名单\n")
	for index, candidate := range result.Candidates {
		rank := candidate.Rank
		if rank <= 0 {
			rank = index + 1
		}
		name := articleValue(candidate.Name, candidate.Code)
		sector := articleValue(candidate.Sector, "行业未标注")
		out.WriteString(fmt.Sprintf("%d. %s（%s）｜%s\n", rank, name, candidate.Code, sector))
		firstStageRank := fmt.Sprintf("初筛第 %d", candidate.FirstStageRank)
		if candidate.FirstStageRank <= 0 {
			firstStageRank = "初筛排名未记录"
		}
		out.WriteString(fmt.Sprintf("   精排第 %d，%s，模型预测未来 3 日最大高点涨幅为 %+.2f%%。\n", rank, firstStageRank, candidate.PredictedMaxHigh))
		out.WriteString(fmt.Sprintf("   动量：近 5 日 %+.2f%%，收盘价相对 20 日均线 %+.2f%%，趋势分 %d。\n", candidate.Features.Return5, candidate.Features.MA20Distance, candidate.Features.TrendScore))
		out.WriteString(fmt.Sprintf("   交易特征：量比 %.2f，RSI14 %.1f，距 20 日高点 %+.2f%%，收盘位置 %.0f%%。\n", candidate.Features.VolumeRatio, candidate.Features.RSI14, candidate.Features.High20Distance, candidate.Features.ClosePosition*100))
		if !candidate.JEVPresent {
			out.WriteString("   JEV 分数缺失，本条使用中性特征参与排序。\n")
		}
		if len(candidate.Reasons) > 0 && !(len(candidate.Reasons) == 1 && candidate.Reasons[0] == "jev_missing") {
			out.WriteString("   标记：" + articleValues(candidate.Reasons) + "。\n")
		}
		out.WriteString("\n")
	}

	out.WriteString("二、行业分布\n")
	out.WriteString(secondStageSectorSummary(result))
	out.WriteString("\n\n")

	out.WriteString("三、这份名单怎么用\n")
	out.WriteString("精排解决的是“从初筛候选里先看谁”，不是买卖信号。名单靠前不等于次日一定上涨，模型预测值也不是目标价或可实现收益。实际决策仍需结合正式推荐、结构门控、风险门槛、开盘偏离和自身仓位约束。\n\n")

	out.WriteString("四、数据与方法\n")
	out.WriteString(fmt.Sprintf("模型：%s；信号日：%s；目标交易日：%s。\n", articleValue(result.ModelID, "未记录"), result.AsOf, result.Target))
	if result.Degraded {
		out.WriteString("本次 JEV 状态为 " + articleValue(result.JEVStatus, "unknown") + "，精排使用中性 JEV 特征，结果标记为降级；降级结果不应与完整特征结果直接比较。\n")
	} else {
		out.WriteString("本次 JEV 特征完整。JEV 只作为离线教师特征使用，生产进程不启动 Python、子进程或外部推理服务。\n")
	}
	if r.FirstStage != nil {
		out.WriteString(fmt.Sprintf("初筛池：有效 %d/%d，初筛模型 %s。\n", r.FirstStage.Eligible, r.FirstStage.PoolSize, articleValue(r.FirstStage.ModelID, "未记录")))
	}

	out.WriteString("\n风险提示\n")
	out.WriteString("本文仅用于数据研究和信息整理，不构成任何证券买卖建议、收益承诺或成功概率判断。历史统计和模型排序不代表未来表现，市场有风险，决策需独立审慎。\n")
	return out.String()
}

func secondStageConclusion(result secondstage.Report) string {
	if len(result.Candidates) == 0 {
		return "本次没有可展示的精排候选。"
	}
	first := result.Candidates[0]
	name := articleValue(first.Name, first.Code)
	line := fmt.Sprintf("本次精排从 %d 只初筛候选中输出 %d 只，排在最前面的是%s（%s）。其初筛排名为第 %d，模型预测未来 3 日最大高点涨幅为 %+.2f%%。",
		result.InputCount, len(result.Candidates), name, first.Code, first.FirstStageRank, first.PredictedMaxHigh)
	if result.Degraded {
		line += " 本次 JEV 特征不完整，结果为降级版本，优先把它当作观察线索而不是完整信号。"
	}
	return line
}

func secondStageSectorSummary(result secondstage.Report) string {
	counts := map[string]int{}
	for _, candidate := range result.Candidates {
		sector := articleValue(candidate.Sector, "行业未标注")
		counts[sector]++
	}
	type sectorCount struct {
		name  string
		count int
	}
	sectors := make([]sectorCount, 0, len(counts))
	for name, count := range counts {
		sectors = append(sectors, sectorCount{name: name, count: count})
	}
	sort.Slice(sectors, func(i, j int) bool {
		if sectors[i].count != sectors[j].count {
			return sectors[i].count > sectors[j].count
		}
		return sectors[i].name < sectors[j].name
	})
	parts := make([]string, 0, len(sectors))
	for _, sector := range sectors {
		parts = append(parts, fmt.Sprintf("%s%d只", sector.name, sector.count))
	}
	return "精排名单覆盖 " + strings.Join(parts, "，") + "。行业分布只描述本次样本构成，不代表行业配置建议。"
}

func articleValue(value, fallback string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return fallback
	}
	return value
}

func articleValues(values []string) string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if cleaned := articleValue(value, ""); cleaned != "" {
			out = append(out, cleaned)
		}
	}
	return strings.Join(out, "、")
}
