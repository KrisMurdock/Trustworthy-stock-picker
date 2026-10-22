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
	digest := fmt.Sprintf("%s三日短线观察：精排 %d 只，附参考买区、明日高低预测、技术指标与结构视角。", articleMonthDay(result.Target), len(result.Candidates))
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
	out.WriteString(fmt.Sprintf("推荐买区以参考买价为中枢、ATR14 的 %.2g 倍为半宽生成，保留两位小数；信号日实际区间取 %s 的最高价至最低价。明日高点和低点来自已保存的 dayrange 预测，点估计之外分别给出各自的名义边际区间；高、低区间不是联合覆盖，也不是收益承诺。预测缺失时文章会明确标注“暂缺”，不临时编造。\n\n", articleBuyBandATR, articleMonthDay(result.AsOf)))

	out.WriteString("## 理论与方法边界\n\n")
	out.WriteString("缠论部分采用包含关系处理、严格分型、完成笔和笔重叠区；笔重叠区只是连续完成笔的公共交集，不冒充严格缠论中枢。波浪部分采用“简化摆动视角”，依据完成笔方向、回撤比例、均线方向和区间位置作启发式描述，不是严格艾略特浪级计数，也不据此单独给出推荐。技术指标只解释当前状态，不把超买、超卖或单个指标交叉当作交易信号。\n\n")

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
	sector := articleValue(row.candidate.Sector, "行业未记录")
	out.WriteString(":::card " + articleCardTone(row) + "\n")
	out.WriteString(fmt.Sprintf("### %d. %s（%s）\n\n", row.rank, name, code))
	out.WriteString("**" + articleRankLabel(row) + " · " + sector + "**\n\n")

	out.WriteString("#### 价格与明日区间\n")
	for _, line := range articlePriceLines(r, row.observation) {
		out.WriteString("- **" + line.label + "**：" + line.value + "\n")
	}
	out.WriteString("\n#### 技术指标\n")
	for _, line := range articleTechnicalLines(row.observation) {
		out.WriteString("- " + line + "\n")
	}

	out.WriteString("\n#### 缠论结构\n")
	for _, line := range articleChanLines(row.observation) {
		out.WriteString("- " + line + "\n")
	}
	out.WriteString("\n" + articleStructureParagraph(row.observation) + "\n")

	out.WriteString("\n#### 波浪视角（简化）\n")
	for _, line := range articleWaveLines(row.observation) {
		out.WriteString("- " + line + "\n")
	}

	out.WriteString("\n#### 观察结论\n")
	out.WriteString(fmt.Sprintf("- **机会分 / 延续分**：%s / %s\n", articleScore(row.observation), articleContinuation(row.observation)))
	out.WriteString("- **主要依据**：" + articleReasons(row.observation) + "\n")
	out.WriteString("- **触发规则**：" + articleRules(row.observation) + "\n")
	out.WriteString("\n")
	out.WriteString(articleRiskParagraph(r, row.observation) + "\n\n")
	out.WriteString(":::\n\n")
}

func writeArticleBrief(out *strings.Builder, r selection.Report, row articleCandidate) {
	code := articleValue(row.candidate.Code, "代码未记录")
	name := articleValue(row.candidate.Name, code)
	sector := articleValue(row.candidate.Sector, "行业未记录")
	out.WriteString(":::card " + articleCardTone(row) + "\n")
	out.WriteString(fmt.Sprintf("### %d. %s（%s）\n\n", row.rank, name, code))
	out.WriteString("**" + articleRankLabel(row) + " · " + sector + "**\n\n")
	out.WriteString("- **价格**：" + strings.Join(articlePriceLineTexts(r, row.observation), "；") + "。\n")
	out.WriteString("- **指标**：" + strings.Join(articleBriefTechnical(row.observation), "；") + "。\n")
	out.WriteString("- **缠论**：" + articleBriefChan(row.observation) + "。\n")
	out.WriteString("- **波浪**：" + articleBriefWave(row.observation) + "。\n")
	out.WriteString(fmt.Sprintf("- **结论**：机会分 %s，延续分 %s；%s\n", articleScore(row.observation), articleContinuation(row.observation), articleRiskParagraph(r, row.observation)))
	out.WriteString("\n:::\n\n")
}

func articleCardTone(row articleCandidate) string {
	if row.observation != nil && row.observation.Recommended {
		return "green"
	}
	if row.observation == nil || row.observation.StructureGuard == nil {
		return "grey"
	}
	switch row.observation.StructureGuard.GuardStatus {
	case strategy.StructureGuardPass:
		return "blue"
	case strategy.StructureGuardFail:
		return "amber"
	default:
		return "grey"
	}
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

func articleChanLines(candidate *strategy.Candidate) []string {
	if candidate == nil || candidate.StructureGuard == nil {
		return []string{"**结构门控**：未评估；没有可展示的日线、30分钟或5分钟结构证据"}
	}
	guard := candidate.StructureGuard
	lines := []string{fmt.Sprintf("**结构门控**：%s；%s", articleStructureStatus(candidate), articleValue(guard.Reason, "结构证据不足"))}
	seen := map[string]bool{}
	for _, level := range guard.Levels {
		if level.Period == "" || seen[level.Period] {
			continue
		}
		seen[level.Period] = true
		lines = append(lines, articleChanLevelLine(level))
	}
	if !seen["day"] && guard.DailyZone != nil {
		lines = append(lines, "**日线**：最新笔重叠区 "+articleZoneText(guard.DailyZone))
	}
	if len(lines) == 1 {
		lines = append(lines, "**分级结构**：本次报告未保存日线/30分钟/5分钟明细")
	}
	return lines
}

func articleChanLevelLine(level strategy.StructureLevelEvidence) string {
	label := map[string]string{"day": "日线", "30m": "30分钟", "5m": "5分钟"}[level.Period]
	if label == "" {
		label = level.Period
	}
	status := map[string]string{
		strategy.StructureGuardPass:    "通过",
		strategy.StructureGuardFail:    "未通过",
		strategy.StructureGuardUnknown: "未知",
	}[level.Status]
	if status == "" {
		status = level.Status
	}
	parts := []string{fmt.Sprintf("**%s**：%s", label, status)}
	if level.Reason != "" && status != "通过" {
		parts = append(parts, level.Reason)
	}
	if level.Bars > 0 {
		parts = append(parts, fmt.Sprintf("%d根K线/%d根合并/%d个分型/%d笔", level.Bars, level.MergedBars, level.Fractals, level.FinishedStrokes))
	}
	if level.LastFractal != "" {
		fractal := "顶分型"
		if level.LastFractal == "bottom" {
			fractal = "底分型"
		}
		text := "最近" + fractal
		if level.LastFractalAt != "" {
			text += " " + articleShortTime(level.LastFractalAt)
		}
		if level.LastFractalHigh > 0 && level.LastFractalLow > 0 {
			text += fmt.Sprintf("（%.2f~%.2f）", level.LastFractalLow, level.LastFractalHigh)
		}
		parts = append(parts, text)
	}
	if level.LastStroke != "" {
		stroke := "向上笔"
		if level.LastStroke == "down" {
			stroke = "向下笔"
		}
		parts = append(parts, "最近完成"+stroke)
	}
	if level.Zone != nil {
		parts = append(parts, "笔重叠区 "+articleZoneText(level.Zone))
	}
	return strings.Join(parts, "；")
}

func articleBriefChan(candidate *strategy.Candidate) string {
	if candidate == nil || candidate.StructureGuard == nil {
		return "结构未评估"
	}
	guard := candidate.StructureGuard
	parts := []string{"门控" + articleStructureStatus(candidate)}
	for _, level := range guard.Levels {
		label := map[string]string{"day": "日线", "30m": "30m", "5m": "5m"}[level.Period]
		if label == "" {
			continue
		}
		status := map[string]string{
			strategy.StructureGuardPass:    "通过",
			strategy.StructureGuardFail:    "未通过",
			strategy.StructureGuardUnknown: "未知",
		}[level.Status]
		if status == "" {
			status = level.Status
		}
		parts = append(parts, label+" "+status)
	}
	return strings.Join(parts, "；")
}

func articleZoneText(zone *strategy.ChanZoneEvidence) string {
	if zone == nil || zone.Upper <= 0 || zone.Lower <= 0 {
		return "暂缺"
	}
	return fmt.Sprintf("%.2f~%.2f（中轴 %.2f）", zone.Lower, zone.Upper, zone.Mid)
}

func articleWaveLines(candidate *strategy.Candidate) []string {
	if candidate == nil || candidate.Wave == nil || candidate.Wave.Stage == "unknown" {
		return []string{"**阶段**：数据不足，暂不做波浪式确认", "**边界**：只有完成笔和有效区间足够时才描述摆动结构，不用未知证据补造浪级"}
	}
	wave := candidate.Wave
	lines := []string{fmt.Sprintf("**阶段**：%s", articleValue(wave.Label, wave.Stage))}
	if wave.CompletedStrokes > 0 {
		line := fmt.Sprintf("**完成笔**：%d 笔", wave.CompletedStrokes)
		if wave.LastStroke == "up" {
			line += "；最近完成笔向上"
		} else if wave.LastStroke == "down" {
			line += "；最近完成笔向下"
		}
		lines = append(lines, line)
	}
	if wave.RetracementPct > 0 {
		lines = append(lines, fmt.Sprintf("**回撤观察**：最近上行段回撤 %.1f%%", wave.RetracementPct))
	}
	levels := make([]string, 0, 3)
	if wave.Support > 0 {
		levels = append(levels, fmt.Sprintf("支撑 %.2f", wave.Support))
	}
	if wave.Invalidation > 0 {
		levels = append(levels, fmt.Sprintf("失效位 %.2f", wave.Invalidation))
	}
	if wave.Resistance > 0 {
		levels = append(levels, fmt.Sprintf("上方参照 %.2f", wave.Resistance))
	}
	if len(levels) > 0 {
		lines = append(lines, "**关键价位**："+strings.Join(levels, " / "))
	}
	if wave.Evidence != "" {
		lines = append(lines, "**依据**："+wave.Evidence)
	}
	limitation := articleValue(wave.Limitation, "仅依据完成笔与回撤的启发式描述，不是严格艾略特浪级计数")
	lines = append(lines, "**边界**："+limitation)
	return lines
}

func articleBriefWave(candidate *strategy.Candidate) string {
	if candidate == nil || candidate.Wave == nil || candidate.Wave.Stage == "unknown" {
		return "数据不足，暂不确认"
	}
	wave := candidate.Wave
	parts := []string{articleValue(wave.Label, wave.Stage)}
	if wave.RetracementPct > 0 {
		parts = append(parts, fmt.Sprintf("回撤 %.1f%%", wave.RetracementPct))
	}
	if wave.Invalidation > 0 {
		parts = append(parts, fmt.Sprintf("失效位 %.2f", wave.Invalidation))
	}
	parts = append(parts, "非严格浪级计数")
	return strings.Join(parts, "；")
}

func articleShortTime(value string) string {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z07:00"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.Format("01-02 15:04")
		}
	}
	return value
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

type articlePriceLine struct {
	label string
	value string
}

func articlePriceLines(r selection.Report, candidate *strategy.Candidate) []articlePriceLine {
	lines := make([]articlePriceLine, 0, 5)
	low, high, ok := articleBuyRange(candidate)
	buy := "暂缺（价格数据不足）"
	if ok {
		buy = fmt.Sprintf("%.2f~%.2f 购入", low, high)
	}
	lines = append(lines, articlePriceLine{label: "推荐买区", value: buy})

	forecast, hasForecast := articleForecast(r, candidate)
	if hasForecast && forecast.PointHigh > 0 && forecast.PointLow > 0 {
		lines = append(lines,
			articlePriceLine{label: "明日高点", value: articleForecastHighText(forecast)},
			articlePriceLine{label: "明日低点", value: articleForecastLowText(forecast)},
		)
	} else {
		lines = append(lines, articlePriceLine{label: "明日高低预测", value: "暂缺（dayrange 模型未生成目标日预测）"})
	}

	dayRange := "暂缺（信号日高低价未记录）"
	if candidate != nil && candidate.DayLow > 0 && candidate.DayHigh >= candidate.DayLow {
		dayRange = fmt.Sprintf("%.2f~%.2f（%s实际）", candidate.DayLow, candidate.DayHigh, articleMonthDay(r.AsOf))
	}
	lines = append(lines, articlePriceLine{label: "信号日区间", value: dayRange})
	if candidate != nil && candidate.Close > 0 {
		lines = append(lines, articlePriceLine{label: "信号日收盘", value: fmt.Sprintf("%.2f", candidate.Close)})
	}
	return lines
}

func articlePriceLineTexts(r selection.Report, candidate *strategy.Candidate) []string {
	lines := articlePriceLines(r, candidate)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, line.label+" "+line.value)
	}
	return out
}

func articleForecast(r selection.Report, candidate *strategy.Candidate) (selection.DayRangeForecast, bool) {
	if candidate == nil || r.DayRangeForecasts == nil {
		return selection.DayRangeForecast{}, false
	}
	forecast, ok := r.DayRangeForecasts[candidate.Code]
	if !ok || forecast.Code != "" && forecast.Code != candidate.Code {
		return selection.DayRangeForecast{}, false
	}
	if forecast.TargetDate != "" && r.Target != "" && forecast.TargetDate != r.Target {
		return selection.DayRangeForecast{}, false
	}
	return forecast, true
}

func articleForecastHighText(forecast selection.DayRangeForecast) string {
	text := fmt.Sprintf("%.2f", forecast.PointHigh)
	if forecast.HighLower > 0 && forecast.HighUpper >= forecast.HighLower {
		text += fmt.Sprintf("；区间 %.2f~%.2f", forecast.HighLower, forecast.HighUpper)
		if forecast.HighCoverage > 0 {
			text += fmt.Sprintf("（名义覆盖 %.0f%%）", forecast.HighCoverage*100)
		}
	}
	return text
}

func articleForecastLowText(forecast selection.DayRangeForecast) string {
	text := fmt.Sprintf("%.2f", forecast.PointLow)
	if forecast.LowLower > 0 && forecast.LowUpper >= forecast.LowLower {
		text += fmt.Sprintf("；区间 %.2f~%.2f", forecast.LowLower, forecast.LowUpper)
		if forecast.LowCoverage > 0 {
			text += fmt.Sprintf("（名义覆盖 %.0f%%）", forecast.LowCoverage*100)
		}
	}
	return text
}

func articleTechnicalLines(candidate *strategy.Candidate) []string {
	if candidate == nil {
		return []string{"技术指标未记录"}
	}
	f := candidate.Features
	m := f.Metrics
	lines := make([]string, 0, 7)
	if m.MA5 > 0 && m.MA10 > 0 && m.MA20 > 0 {
		lines = append(lines, fmt.Sprintf("**均线**：MA5 %.2f / MA10 %.2f / MA20 %.2f；%s", m.MA5, m.MA10, m.MA20, articleMAState(m.MA5, m.MA10, m.MA20)))
	} else {
		lines = append(lines, "**均线**：暂缺")
	}

	rsi := fmt.Sprintf("**RSI14**：%.1f（%s）", m.RSI14, articleRSIState(m.RSI14))
	if f.SignalIndicators != nil {
		delta := m.RSI14 - f.SignalIndicators.PreviousRSI14
		rsi = fmt.Sprintf("**RSI14**：%.1f（前值 %.1f，%+.1f；%s）", m.RSI14, f.SignalIndicators.PreviousRSI14, delta, articleRSIState(m.RSI14))
	}
	lines = append(lines, rsi)

	hist := 2 * (m.DIF - m.DEA)
	macd := fmt.Sprintf("**MACD**：DIF %.3f / DEA %.3f / 柱 %.3f；%s", m.DIF, m.DEA, hist, articleMACDState(m.DIF, m.DEA, hist, hist))
	if f.SignalIndicators != nil {
		macd = fmt.Sprintf("**MACD**：DIF %.3f / DEA %.3f / 柱 %.3f；%s", m.DIF, m.DEA, f.SignalIndicators.MACDHistogram, articleMACDState(m.DIF, m.DEA, f.SignalIndicators.MACDHistogram, f.SignalIndicators.PreviousMACDHistogram))
	}
	lines = append(lines, macd)

	if f.SignalIndicators != nil && f.SignalIndicators.Bollinger.Middle > 0 {
		b := f.SignalIndicators.Bollinger
		position := "位置不可计算"
		if b.PercentB != nil {
			position = fmt.Sprintf("%%B %.2f（%s）", *b.PercentB, articleBollingerState(*b.PercentB))
		}
		lines = append(lines, fmt.Sprintf("**BOLL**：上 %.2f / 中 %.2f / 下 %.2f；带宽 %.1f%%；%s", b.Upper, b.Middle, b.Lower, b.WidthPct, position))
	} else {
		lines = append(lines, "**BOLL**：本次报告未保存布林带明细")
	}

	lines = append(lines, fmt.Sprintf("**波动与量能**：ATR14 %.2f%%；量比 %.2f；MA20 斜率 %+.2f%%；距 MA20 %+.2f%%",
		f.ATR14Pct, f.VolumeRatio, f.MA20Slope, f.MA20Distance))
	lines = append(lines, fmt.Sprintf("**涨跌动量**：1日 %+.2f%% / 5日 %+.2f%% / 10日 %+.2f%%；相对样本池5日 %+.2f%%",
		f.Return1, f.Return5, f.Return10, f.Relative5))
	lines = append(lines, fmt.Sprintf("**区间位置**：收盘位于当日 %.0f%% 分位；距20日高点 %.2f%%；上影线占比 %.0f%%",
		f.ClosePosition*100, f.High20Distance, f.UpperWick*100))
	return lines
}

func articleBriefTechnical(candidate *strategy.Candidate) []string {
	if candidate == nil {
		return []string{"指标未记录"}
	}
	f := candidate.Features
	m := f.Metrics
	out := []string{
		fmt.Sprintf("MA5/10/20 %.2f/%.2f/%.2f", m.MA5, m.MA10, m.MA20),
		fmt.Sprintf("RSI14 %.1f（%s）", m.RSI14, articleRSIState(m.RSI14)),
	}
	if f.SignalIndicators != nil {
		out = append(out, fmt.Sprintf("MACD柱 %.3f，%s", f.SignalIndicators.MACDHistogram, articleMACDState(m.DIF, m.DEA, f.SignalIndicators.MACDHistogram, f.SignalIndicators.PreviousMACDHistogram)))
		if f.SignalIndicators.Bollinger.PercentB != nil {
			out = append(out, fmt.Sprintf("BOLL %%B %.2f", *f.SignalIndicators.Bollinger.PercentB))
		}
	}
	out = append(out, fmt.Sprintf("ATR %.2f%%", f.ATR14Pct), fmt.Sprintf("量比 %.2f", f.VolumeRatio))
	return out
}

func articleMAState(ma5, ma10, ma20 float64) string {
	switch {
	case ma5 > ma10 && ma10 > ma20:
		return "多头排列"
	case ma5 < ma10 && ma10 < ma20:
		return "空头排列"
	case ma5 > ma10:
		return "短期均线转强、尚未形成完整多头排列"
	default:
		return "均线交织"
	}
}

func articleRSIState(value float64) string {
	switch {
	case value >= 80:
		return "明显超买区"
	case value >= 70:
		return "偏热"
	case value >= 50:
		return "偏强"
	case value >= 30:
		return "偏弱"
	default:
		return "超卖区"
	}
}

func articleMACDState(dif, dea, hist, previous float64) string {
	switch {
	case dif > dea && hist > previous:
		return "红柱扩张"
	case dif > dea && hist <= previous:
		return "多头区间、动能收敛"
	case dif <= dea && hist > previous:
		return "空头区间、跌势收敛"
	default:
		return "绿柱扩张"
	}
}

func articleBollingerState(percentB float64) string {
	switch {
	case percentB > 1:
		return "上轨外"
	case percentB >= 0.8:
		return "上轨附近"
	case percentB >= 0.5:
		return "中轨上方"
	case percentB >= 0.2:
		return "中轨下方"
	case percentB >= 0:
		return "下轨附近"
	default:
		return "下轨外"
	}
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
