package report

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const (
	articleDetailCount = 5
	articleBuyBandATR  = 0.35
)

type articleCandidate struct {
	rank        int
	candidate   secondstage.Candidate
	observation *strategy.Candidate
}

func secondStageArticles(r selection.Report, expires time.Time) []notify.Message {
	result := r.SecondStage
	if result == nil || !result.PushEnabled || result.Status != secondstage.StatusOK || len(result.Candidates) == 0 {
		return nil
	}
	title := secondStageArticleTitle(*result)
	return []notify.Message{{
		Version:        2,
		EventID:        r.ID + ":second-stage:article",
		DisplayEventID: r.ID,
		PartLabel:      "每日精选",
		Title:          title,
		PlainText:      secondStageArticleText(r, *result),
		Article: &notify.Article{
			Title:  title,
			Digest: secondStageArticleDigest(*result),
			Body:   secondStageArticleBody(r, *result),
		},
		ExpiresAt: expires,
	}}
}

func secondStageArticleTitle(result secondstage.Report) string {
	title := fmt.Sprintf("每日精选｜%s 三日短线观察", articleMonthDay(result.Target))
	if result.Degraded {
		title += "（降级）"
	}
	return title
}

func secondStageArticleText(r selection.Report, result secondstage.Report) string {
	var out strings.Builder
	out.WriteString("【公众号标题】\n")
	out.WriteString(secondStageArticleTitle(result))
	out.WriteString("\n\n【公众号正文】\n")
	out.WriteString(secondStageArticleBody(r, result))
	return out.String()
}

func secondStageArticleDigest(result secondstage.Report) string {
	digest := fmt.Sprintf("%s三日短线观察：精排 %d 只，附参考买入区间与信号日价格区间。", articleMonthDay(result.Target), len(result.Candidates))
	if result.Degraded {
		digest += "本次模型特征降级。"
	}
	return digest
}

func secondStageArticleBody(r selection.Report, result secondstage.Report) string {
	rows := buildArticleCandidates(r, result)
	if len(rows) == 0 {
		return "## 今日没有可展示的观察对象\n\n风险提示：本文仅用于数据研究，不构成证券买卖建议。\n"
	}

	var out strings.Builder
	formal := articleFormalCount(rows)
	out.WriteString(fmt.Sprintf("# %s 三日短线观察：%s\n\n", articleMonthDay(result.Target), articleLead(formal)))

	poolCount := len(r.Ranking)
	if poolCount == 0 {
		poolCount = result.InputCount
	}
	out.WriteString(fmt.Sprintf("今天的筛选结果显示，策略观察池共 %d 只，精排从中输出 %d 只进入本期观察名单。%s。以下内容仅用于研究和观察，不构成交易建议。\n\n",
		poolCount, len(rows), articleFormalSentence(formal)))
	out.WriteString("机会分不代表成功概率，也不代表未来收益。\n\n")

	out.WriteString("## 一、排名靠前的观察对象\n\n")
	detailCount := minInt(articleDetailCount, len(rows))
	for i := 0; i < detailCount; i++ {
		writeArticleDetail(&out, r, rows[i])
	}

	if len(rows) > detailCount {
		out.WriteString("## 二、其他进入观察排名的对象\n\n")
		for i := detailCount; i < len(rows); i++ {
			writeArticleBrief(&out, r, rows[i])
		}
	}

	out.WriteString("## 三、今天的整体结论\n\n")
	writeArticleConclusion(&out, r, rows, formal)

	out.WriteString("## 价格区间说明\n\n")
	out.WriteString(fmt.Sprintf("推荐区间以参考买价为中枢、ATR14 的 %.2g 倍为半宽生成，保留两位小数；日价格区间取信号日 %s 的实际最高价至最低价。价格区间不是目标价，也不保证能够成交。\n\n", articleBuyBandATR, articleMonthDay(result.AsOf)))

	out.WriteString("## 数据与方法\n\n")
	out.WriteString(fmt.Sprintf("信号日 %s，目标交易日 %s；精排模型 %s，输入 %d 只。", result.AsOf, result.Target, articleValue(result.ModelID, "未记录"), result.InputCount))
	if result.Degraded {
		out.WriteString("本次离线模型特征不完整，结果标记为降级，不应与完整模型结果直接比较。")
	} else {
		out.WriteString("离线模型只提供分数，生产进程不启动外部推理服务。")
	}
	out.WriteString("\n\n")

	out.WriteString("## 风险提示\n\n")
	out.WriteString("本文仅用于数据研究和信息整理，不构成任何证券买卖建议、收益承诺或成功概率判断。历史统计和模型排序不代表未来表现，市场有风险，决策需独立审慎。\n")
	return out.String()
}

func buildArticleCandidates(r selection.Report, result secondstage.Report) []articleCandidate {
	byCode := make(map[string]*strategy.Candidate, len(r.Ranking))
	for i := range r.Ranking {
		byCode[r.Ranking[i].Code] = &r.Ranking[i]
	}
	rows := make([]articleCandidate, 0, len(result.Candidates))
	for index, candidate := range result.Candidates {
		rank := candidate.Rank
		if rank <= 0 {
			rank = index + 1
		}
		rows = append(rows, articleCandidate{rank: rank, candidate: candidate, observation: byCode[candidate.Code]})
	}
	return rows
}

func writeArticleDetail(out *strings.Builder, r selection.Report, row articleCandidate) {
	code := articleValue(row.candidate.Code, "代码未记录")
	name := articleValue(row.candidate.Name, code)
	out.WriteString(fmt.Sprintf("### %d. %s（%s）｜%s\n", row.rank, name, code, articleRankLabel(row)))
	out.WriteString("- 结构状态：" + articleStructureStatus(row.observation) + "\n")
	out.WriteString("- 机会分：" + articleScore(row.observation) + "\n")
	out.WriteString("- 上涨延续分：" + articleContinuation(row.observation) + "\n")
	out.WriteString("- 主要依据：" + articleReasons(row.observation) + "\n")
	out.WriteString("- 触发规则：" + articleRules(row.observation) + "\n")
	for _, line := range articlePriceLines(r, row.observation) {
		out.WriteString("- " + line + "\n")
	}
	out.WriteString("\n")
	out.WriteString(articleStructureParagraph(row.observation) + "\n\n")
	out.WriteString(articleRiskParagraph(r, row.observation) + "\n\n")
}

func writeArticleBrief(out *strings.Builder, r selection.Report, row articleCandidate) {
	code := articleValue(row.candidate.Code, "代码未记录")
	name := articleValue(row.candidate.Name, code)
	out.WriteString(fmt.Sprintf("### %d. %s（%s）｜%s\n\n", row.rank, name, code, articleRankLabel(row)))
	out.WriteString(fmt.Sprintf("机会分%s。%s\n\n", articleScore(row.observation), articleStructureParagraph(row.observation)))
	out.WriteString(strings.Join(articlePriceLines(r, row.observation), "；") + "。\n\n")
	out.WriteString(articleRiskParagraph(r, row.observation) + "\n\n")
}

func articleRankLabel(row articleCandidate) string {
	parts := []string{fmt.Sprintf("精排第%d名", row.rank)}
	if row.observation != nil && row.observation.ObservationRank > 0 {
		parts = append(parts, fmt.Sprintf("观察第%d名", row.observation.ObservationRank))
	}
	return strings.Join(parts, " · ")
}

func articleStructureStatus(candidate *strategy.Candidate) string {
	if candidate == nil || candidate.StructureGuard == nil {
		return "未评估"
	}
	switch candidate.StructureGuard.GuardStatus {
	case strategy.StructureGuardPass:
		return "通过"
	case strategy.StructureGuardFail:
		return "未通过"
	case strategy.StructureGuardUnknown:
		return "未知"
	default:
		return "未知"
	}
}

func articleStructureParagraph(candidate *strategy.Candidate) string {
	if candidate == nil || candidate.StructureGuard == nil {
		return "本次未记录结构门控结果，仍需结合正式推荐和风险门槛判断。"
	}
	guard := candidate.StructureGuard
	reason := articleValue(guard.Reason, "结构证据不足")
	switch guard.GuardStatus {
	case strategy.StructureGuardPass:
		return "日线结构暂未出现逆向信号，30分钟和5分钟也未触发失效条件，因此进入结构观察前列。"
	case strategy.StructureGuardFail:
		return fmt.Sprintf("结构门控未通过：%s。即使短期指标仍偏强，也不能视为有效向上结构。", reason)
	case strategy.StructureGuardUnknown:
		return fmt.Sprintf("结构证据不足：%s。“未知”不等于通过，也不等于失败，代表当前数据不足以支持结构判断。", reason)
	default:
		return "结构状态尚未完成评估，暂不把它视作可通过信号。"
	}
}

func articleRiskParagraph(r selection.Report, candidate *strategy.Candidate) string {
	if candidate == nil {
		return "当前没有可用的历史证据和风险明细，暂不具备正式推荐条件。"
	}
	risks := uniqueArticleLabels(candidate.Risks, articleRiskLabels)
	status := shortCandidateStatus(r, *candidate)
	if candidate.Recommended {
		if len(risks) == 0 {
			return "当前已进入正式推荐，但仍需结合开盘偏离、成交情况和自身仓位约束执行。"
		}
		return fmt.Sprintf("虽然已进入正式推荐，但仍存在%s。执行时仍需结合开盘偏离、成交情况和自身仓位约束。", strings.Join(risks, "、"))
	}
	if len(risks) == 0 {
		return fmt.Sprintf("当前没有记录额外的硬性风险标记，但结论仍为“%s”，不是交易信号。", status)
	}
	return fmt.Sprintf("但历史检验仍未达标：%s。当前结论为“%s”，不是交易信号。", strings.Join(risks, "、"), status)
}

func articleScore(candidate *strategy.Candidate) string {
	if candidate == nil {
		return "未记录"
	}
	score := candidate.Score
	if candidate.ObservationScore > 0 {
		score = candidate.ObservationScore
	}
	return fmt.Sprintf("%.1f", score)
}

func articleContinuation(candidate *strategy.Candidate) string {
	if candidate == nil || candidate.Continuation == nil {
		return "未计算"
	}
	switch candidate.Continuation.Status {
	case "ready":
		return fmt.Sprintf("%.1f", candidate.Continuation.Score)
	case "not_applicable":
		return "暂不适用（今日未上涨）"
	default:
		return "样本不足，暂不评分"
	}
}

func articleReasons(candidate *strategy.Candidate) string {
	if candidate == nil {
		return "未记录"
	}
	return articleLabelList(candidate.Reasons, articleReasonLabels, "暂无明确动量依据")
}

func articleRules(candidate *strategy.Candidate) string {
	if candidate == nil {
		return "未记录"
	}
	return articleLabelList(candidate.Rules, articleRuleLabels, "无明确规则触发")
}

func articleLabelList(values []string, labels map[string]string, fallback string) string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		label := labels[value]
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, label)
	}
	if len(out) == 0 {
		return fallback
	}
	return strings.Join(out, "、")
}

func uniqueArticleLabels(values []string, labels map[string]string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		label := labels[value]
		if label == "" {
			label = strings.ReplaceAll(value, "_", " ")
		}
		if seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, label)
	}
	return out
}

func articlePriceLines(r selection.Report, candidate *strategy.Candidate) []string {
	low, high, ok := articleBuyRange(candidate)
	buy := "推荐：暂缺（价格数据不足）"
	if ok {
		buy = fmt.Sprintf("推荐：%.2f~%.2f 购入", low, high)
	}
	dayRange := "日价格区间：暂缺（信号日高低价未记录）"
	if candidate != nil && candidate.DayLow > 0 && candidate.DayHigh >= candidate.DayLow {
		dayRange = fmt.Sprintf("日价格区间：%.2f~%.2f（%s实际）", candidate.DayLow, candidate.DayHigh, articleMonthDay(r.AsOf))
	}
	return []string{buy, dayRange}
}

func articleBuyRange(candidate *strategy.Candidate) (float64, float64, bool) {
	if candidate == nil || candidate.Features.ATR14Pct <= 0 {
		return 0, 0, false
	}
	base := candidate.Close
	if candidate.Plan != nil && candidate.Plan.EntryPrice > 0 {
		base = candidate.Plan.EntryPrice
	}
	if base <= 0 || math.IsNaN(base) || math.IsInf(base, 0) || math.IsNaN(candidate.Features.ATR14Pct) || math.IsInf(candidate.Features.ATR14Pct, 0) {
		return 0, 0, false
	}
	half := base * candidate.Features.ATR14Pct / 100 * articleBuyBandATR
	low := math.Floor((base-half)*100+1e-9) / 100
	high := math.Ceil((base+half)*100-1e-9) / 100
	if low <= 0 {
		low = 0.01
	}
	if high <= low {
		high = math.Round((low+0.01)*100) / 100
	}
	return low, high, true
}

func writeArticleConclusion(out *strings.Builder, r selection.Report, rows []articleCandidate, formal int) {
	out.WriteString("本轮排名靠前的对象，主要受以下因素推动：\n\n")
	drivers := articleCommonDrivers(rows)
	for _, driver := range drivers {
		out.WriteString("- " + driver + "；\n")
	}
	out.WriteString("\n但从历史证据看，多个规则仍存在共同问题：\n\n")
	risks := articleCommonRisks(rows)
	for _, risk := range risks {
		out.WriteString("- " + risk + "；\n")
	}
	out.WriteString("\n因此，今天的结果更适合定义为：\n\n")
	if formal == 0 {
		out.WriteString("> 强势观察池，而不是正式推荐池。\n\n")
	} else {
		out.WriteString("> 观察池为主，只有少数对象进入正式推荐。\n\n")
	}
	out.WriteString("后续需要继续观察：\n")
	out.WriteString("1. 日线结构能否保持；\n")
	out.WriteString("2. 30分钟和5分钟是否出现失效信号；\n")
	out.WriteString("3. 是否出现明确规则触发；\n")
	out.WriteString("4. 历史样本和未决交易问题能否改善；\n")
	out.WriteString("5. 次日是否出现有效上涨延续。\n\n")
	if r.SelectionEnabled {
		out.WriteString("所有结论均基于截至信号日已保存的数据，历史统计不代表未来收益。\n\n")
	} else {
		out.WriteString("本轮筛选未启用，排名只用于研究观察。\n\n")
	}
}

func articleCommonDrivers(rows []articleCandidate) []string {
	out := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, row := range rows {
		if row.observation == nil {
			continue
		}
		for _, reason := range row.observation.Reasons {
			label := articleReasonLabels[reason]
			if label == "" || seen[label] {
				continue
			}
			seen[label] = true
			out = append(out, label)
		}
	}
	if articleGuardCount(rows, strategy.StructureGuardPass) > 0 && !seen["部分对象通过了日线结构过滤"] {
		out = append(out, "部分对象通过了日线结构过滤")
	}
	if len(out) == 0 {
		out = append(out, "精排综合分相对靠前")
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func articleCommonRisks(rows []articleCandidate) []string {
	out := make([]string, 0, 6)
	seen := map[string]bool{}
	for _, row := range rows {
		if row.observation == nil {
			continue
		}
		for _, risk := range row.observation.Risks {
			label := articleRiskLabels[risk]
			if label == "" || seen[label] {
				continue
			}
			seen[label] = true
			out = append(out, label)
		}
	}
	if articleGuardCount(rows, strategy.StructureGuardFail) > 0 && !seen["部分对象结构未通过"] {
		out = append(out, "部分对象结构未通过")
	}
	if articleGuardCount(rows, strategy.StructureGuardUnknown) > 0 && !seen["部分对象结构未知"] {
		out = append(out, "部分对象结构未知")
	}
	if len(out) == 0 {
		out = append(out, "暂未记录集中的历史风险标记")
	}
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

func articleGuardCount(rows []articleCandidate, status string) int {
	count := 0
	for _, row := range rows {
		if row.observation != nil && row.observation.StructureGuard != nil && row.observation.StructureGuard.GuardStatus == status {
			count++
		}
	}
	return count
}

func articleFormalCount(rows []articleCandidate) int {
	count := 0
	for _, row := range rows {
		if row.observation != nil && row.observation.Recommended {
			count++
		}
	}
	return count
}

func articleFormalSentence(formal int) string {
	if formal == 0 {
		return "本轮没有对象通过完整的历史证据和风险门槛"
	}
	return fmt.Sprintf("本轮有 %d 只进入正式推荐，其余对象仍只作观察", formal)
}

func articleLead(formal int) string {
	if formal == 0 {
		return "排名靠前，但暂未形成正式推荐"
	}
	return fmt.Sprintf("排名靠前，其中 %d 只进入正式推荐", formal)
}

func articleMonthDay(date string) string {
	if parsed, err := time.Parse("2006-01-02", date); err == nil {
		return fmt.Sprintf("%d月%d日", parsed.Month(), parsed.Day())
	}
	return date
}

func articleValue(value, fallback string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return fallback
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var articleReasonLabels = map[string]string{
	"rising_ma20":        "MA20向上",
	"stronger_than_pool": "近5日强于样本池",
	"strong_close":       "收盘偏强",
}

var articleRuleLabels = map[string]string{
	"baseline":        "样本池基准",
	"breakout":        "突破",
	"pullback":        "回踩",
	"momentum":        "动量",
	"volume_recovery": "放量恢复",
}

var articleRiskLabels = map[string]string{
	"historical_net_loss_limit_breached":    "历史单笔净亏损超过3%的限制",
	"insufficient_or_unprofitable_evidence": "匹配规则未通过历史检验",
	"holdout_validation_not_passed":         "独立验证未通过",
	"cash_insufficient":                     "预算不足最小买入量",
	"extended_price":                        "涨幅或偏离过大",
	"large_daily_range":                     "日内波幅过大",
	"upper_wick":                            "上影线较长",
	"structure_guard_fail":                  "结构门控不通过",
	"structure_guard_unknown":               "结构门控数据不足",
	"chan_filtered":                         "缠论未通过",
	"chan_unknown":                          "缠论结构未知",
}
