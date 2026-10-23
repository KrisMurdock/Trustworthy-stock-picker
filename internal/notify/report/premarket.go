package report

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const premarketArticleMaxRows = 10

// PremarketExecutionStatus is the article-facing execution status calculated
// by the dayrange service. It is deliberately text-only so this package does
// not need to depend on dayrange storage types.
type PremarketExecutionStatus struct {
	Status string
	Reason string
}

// PremarketArticleInput freezes the saved forecasts and ranking used to build
// one premarket WeChat article. Predictions are keyed by stock code and must
// belong to TargetDate.
type PremarketArticleInput struct {
	TargetDate   string
	GeneratedAt  time.Time
	InputThrough string
	PoolSize     int
	Report       *selection.Report
	Predictions  map[string]selection.DayRangeForecast
	Names        map[string]string
	Statuses     map[string]PremarketExecutionStatus
	Skipped      []string
}

type premarketArticleRow struct {
	rank        int
	code        string
	name        string
	sector      string
	observation *strategy.Candidate
	forecast    selection.DayRangeForecast
	hasForecast bool
	status      string
	statusWhy   string
}

// BuildPremarketArticle creates the article draft delivered after the
// premarket forecast is saved. It never generates or changes a forecast.
func BuildPremarketArticle(input PremarketArticleInput) (notify.Message, bool, error) {
	target, err := time.ParseInLocation(time.DateOnly, strings.TrimSpace(input.TargetDate), time.FixedZone("Asia/Shanghai", 8*60*60))
	if err != nil || target.Format(time.DateOnly) != input.TargetDate {
		return notify.Message{}, false, errors.New("premarket article requires a valid target date")
	}
	if input.GeneratedAt.IsZero() {
		return notify.Message{}, false, errors.New("premarket article generation time is required")
	}
	if len(input.Predictions) == 0 && (input.Report == nil || input.Report.SecondStage == nil) {
		return notify.Message{}, false, nil
	}
	rows := premarketRows(input)
	if len(rows) == 0 {
		return notify.Message{}, false, nil
	}
	reportCopy := premarketReportCopy(input)
	title := fmt.Sprintf("盘前观察｜%s 全天高低与执行确认", articleMonthDay(input.TargetDate))
	digest := fmt.Sprintf("%s盘前：%d只候选附全天高低点、边际区间和执行状态。研究观察，不构成投资建议。", articleMonthDay(input.TargetDate), len(rows))
	message := notify.Message{
		Version:        2,
		EventID:        "dayrange:premarket-article:" + input.TargetDate,
		DisplayEventID: "dayrange:premarket-article:" + input.TargetDate,
		PartLabel:      "盘前公众号稿",
		Title:          title,
		PlainText:      premarketArticleText(input, rows),
		Article: &notify.Article{
			Title:  title,
			Digest: digest,
			Body:   premarketArticleBody(input, reportCopy, rows),
		},
		ExpiresAt: time.Date(target.Year(), target.Month(), target.Day(), 9, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)),
	}
	return message, true, nil
}

func premarketRows(input PremarketArticleInput) []premarketArticleRow {
	predictions := map[string]selection.DayRangeForecast{}
	for code, forecast := range input.Predictions {
		if forecast.TargetDate != "" && forecast.TargetDate != input.TargetDate {
			continue
		}
		if forecast.Code == "" {
			forecast.Code = code
		}
		predictions[code] = forecast
	}
	if input.Report != nil && input.Report.SecondStage != nil && input.Report.SecondStage.Status == secondstage.StatusOK && len(input.Report.SecondStage.Candidates) > 0 {
		observations := map[string]*strategy.Candidate{}
		for index := range input.Report.Ranking {
			candidate := &input.Report.Ranking[index]
			observations[candidate.Code] = candidate
		}
		rows := make([]premarketArticleRow, 0, min(premarketArticleMaxRows, len(input.Report.SecondStage.Candidates)))
		for index, candidate := range input.Report.SecondStage.Candidates {
			if len(rows) >= premarketArticleMaxRows {
				break
			}
			rank := candidate.Rank
			if rank <= 0 {
				rank = index + 1
			}
			forecast, ok := predictions[candidate.Code]
			status := input.Statuses[candidate.Code]
			if status.Status == "" {
				status = PremarketExecutionStatus{Status: "观察", Reason: "盘前执行状态未记录"}
			}
			rows = append(rows, premarketArticleRow{
				rank: rank, code: candidate.Code, name: candidate.Name, sector: candidate.Sector,
				observation: observations[candidate.Code], forecast: forecast, hasForecast: ok,
				status: status.Status, statusWhy: status.Reason,
			})
		}
		return rows
	}

	rows := make([]premarketArticleRow, 0, min(premarketArticleMaxRows, len(predictions)))
	for _, forecast := range predictionForecasts(predictions, input.TargetDate) {
		if len(rows) >= premarketArticleMaxRows {
			break
		}
		status := input.Statuses[forecast.Code]
		if status.Status == "" {
			status = PremarketExecutionStatus{Status: "观察", Reason: "精排队列尚未就绪"}
		}
		rows = append(rows, premarketArticleRow{
			rank: len(rows) + 1, code: forecast.Code, name: input.Names[forecast.Code],
			forecast: forecast, hasForecast: true, status: status.Status, statusWhy: status.Reason,
		})
	}
	return rows
}

func predictionForecasts(predictions map[string]selection.DayRangeForecast, target string) []selection.DayRangeForecast {
	out := make([]selection.DayRangeForecast, 0, len(predictions))
	for code, forecast := range predictions {
		if forecast.TargetDate != "" && forecast.TargetDate != target {
			continue
		}
		if forecast.Code == "" {
			forecast.Code = code
		}
		out = append(out, forecast)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := forecastUpside(out[i]), forecastUpside(out[j])
		if left != right {
			return left > right
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func forecastUpside(forecast selection.DayRangeForecast) float64 {
	if forecast.ReferencePrice <= 0 || forecast.PointHigh <= 0 {
		return -1
	}
	return forecast.PointHigh/forecast.ReferencePrice - 1
}

func premarketReportCopy(input PremarketArticleInput) selection.Report {
	var reportCopy selection.Report
	if input.Report != nil {
		reportCopy = *input.Report
	}
	reportCopy.Target = input.TargetDate
	reportCopy.DayRangeForecasts = input.Predictions
	return reportCopy
}

func premarketArticleText(input PremarketArticleInput, rows []premarketArticleRow) string {
	return "【公众号标题】\n" + fmt.Sprintf("盘前观察｜%s 全天高低与执行确认", articleMonthDay(input.TargetDate)) + "\n\n【公众号正文】\n" + premarketArticleBody(input, premarketReportCopy(input), rows)
}

func premarketArticleBody(input PremarketArticleInput, r selection.Report, rows []premarketArticleRow) string {
	var out strings.Builder
	out.WriteString(fmt.Sprintf("# %s 盘前观察：全天高低与执行确认\n\n", articleMonthDay(input.TargetDate)))
	out.WriteString(fmt.Sprintf("目标交易日 %s，盘前预测生成于 %s，数据截止 %s。本轮覆盖 %d/%d 只，以下展示当前可用的 %d 只。\n\n",
		input.TargetDate, input.GeneratedAt.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02 15:04"), articleValue(input.InputThrough, "未记录"), len(input.Predictions), max(input.PoolSize, len(input.Predictions)), len(rows)))
	out.WriteString("> 本稿是盘前研究输出，不是成功概率、收益承诺或自动交易指令。09:25 仍需用实际盘口复核。\n\n")

	maintain, observe, abandon := premarketStatusCounts(rows)
	out.WriteString("## 一、执行状态概览\n\n")
	out.WriteString(fmt.Sprintf("- **可执行**：%d 只；\n- **观察**：%d 只；\n- **放弃**：%d 只。\n\n", maintain, observe, abandon))
	if input.Report == nil || input.Report.SecondStage == nil || input.Report.SecondStage.Status != secondstage.StatusOK {
		out.WriteString("精排队列尚未就绪，以下按预测高点相对参考价的降序展示，序号不是推荐排名，也不代表候选优先级。\n\n")
	} else if input.Report.SecondStage.Degraded {
		out.WriteString("精排处于降级状态，排序和执行状态只作观察参考。\n\n")
	}
	if input.Report != nil && (input.Report.ExecutionMode == strategy.StructureGuardShadow || !input.Report.SelectionEnabled) {
		out.WriteString("结构影子期或筛选未启用，本轮不产生可执行指令；“放弃”只表示不按参考计划执行。\n\n")
	}

	out.WriteString("## 二、盘前观察卡片\n\n")
	detailCount := min(articleDetailCount, len(rows))
	for index := 0; index < detailCount; index++ {
		writePremarketDetail(&out, r, rows[index])
	}
	if len(rows) > detailCount {
		out.WriteString("## 三、其余候选\n\n")
		for index := detailCount; index < len(rows); index++ {
			writePremarketBrief(&out, r, rows[index])
		}
	}

	out.WriteString("## 方法与边界\n\n")
	out.WriteString("- **高低预测**：参考价取上一完成交易日不复权收盘；高点、低点分别为点估计，区间是各自的名义边际区间，不是高、低联合覆盖。\n")
	out.WriteString("- **执行状态**：只比较已保存的盘前预测与参考计划。可执行要求为正式推荐且预测高低覆盖净目标与止损；观察表示无正式推荐或可能先触止损；放弃表示预测高不足以覆盖净目标。\n")
	out.WriteString("- **技术指标**：MA、RSI、MACD、BOLL、ATR、量比只描述当前状态，不把单一指标交叉当作交易信号。\n")
	out.WriteString("- **缠论结构**：采用包含关系处理、严格分型、完成笔和笔重叠区；笔重叠区不是严格缠论中枢。\n")
	out.WriteString("- **波浪视角**：只依据完成笔方向、回撤比例、均线方向和区间位置作启发式描述，不是严格艾略特浪级计数。\n\n")
	if skipped := premarketSkippedSummary(input.Skipped); skipped != "" {
		out.WriteString("## 数据说明\n\n" + skipped + "\n\n")
	}

	out.WriteString("## 风险提示\n\n")
	out.WriteString("本文仅用于数据研究和信息整理，不构成任何证券买卖建议、收益承诺或成功概率判断。历史统计和模型结果不代表未来表现，市场有风险，决策需独立审慎。\n")
	return out.String()
}

func writePremarketDetail(out *strings.Builder, r selection.Report, row premarketArticleRow) {
	out.WriteString(":::card " + premarketCardTone(row.status) + "\n")
	writePremarketHeading(out, row)
	writePremarketStatus(out, row)
	out.WriteString("#### 全天高低预测\n")
	for _, line := range premarketForecastLines(row) {
		out.WriteString("- **" + line.label + "**：" + line.value + "\n")
	}
	writePremarketTechnical(out, row.observation)
	writePremarketStructure(out, row.observation)
	writePremarketRisk(out, r, row)
	out.WriteString(":::\n\n")
}

func writePremarketBrief(out *strings.Builder, r selection.Report, row premarketArticleRow) {
	out.WriteString(":::card " + premarketCardTone(row.status) + "\n")
	writePremarketHeading(out, row)
	writePremarketStatus(out, row)
	forecast := make([]string, 0, len(premarketForecastLines(row)))
	for _, line := range premarketForecastLines(row) {
		forecast = append(forecast, line.label+" "+line.value)
	}
	out.WriteString("- **高低预测**：" + strings.Join(forecast, "；") + "。\n")
	if row.observation != nil {
		out.WriteString("- **指标**：" + strings.Join(articleBriefTechnical(row.observation), "；") + "。\n")
		out.WriteString("- **缠论**：" + articleBriefChan(row.observation) + "。\n")
		out.WriteString("- **波浪**：" + articleBriefWave(row.observation) + "。\n")
	}
	out.WriteString("- **边界**：" + premarketRiskText(r, row.observation) + "\n")
	out.WriteString(":::\n\n")
}

func writePremarketHeading(out *strings.Builder, row premarketArticleRow) {
	name := articleStockLabel(row.code, row.name)
	if name == row.code {
		out.WriteString(fmt.Sprintf("### %d. %s\n\n", row.rank, name))
	} else {
		out.WriteString(fmt.Sprintf("### %d. %s（%s）\n\n", row.rank, name, row.code))
	}
	parts := []string{fmt.Sprintf("盘前候选第%d位", row.rank)}
	if row.sector != "" {
		parts = append(parts, row.sector)
	}
	out.WriteString("**" + strings.Join(parts, " · ") + "**\n\n")
}

func writePremarketStatus(out *strings.Builder, row premarketArticleRow) {
	out.WriteString("#### 执行参考\n")
	out.WriteString("- **状态**：" + articleValue(row.status, "观察") + "；" + articleValue(row.statusWhy, "等待盘口复核") + "。\n")
	if row.observation != nil && row.observation.Plan != nil {
		plan := row.observation.Plan
		buy := "暂缺"
		if low, high, ok := articleBuyRange(row.observation); ok {
			buy = fmt.Sprintf("%.2f~%.2f", low, high)
		}
		out.WriteString(fmt.Sprintf("- **参考计划**：买区 %s；净目标 %.2f；止损 %.2f。\n", buy, plan.TakeProfit, plan.StopPrice))
	} else {
		out.WriteString("- **参考计划**：暂无正式计划，只作盘前观察。\n")
	}
	out.WriteString("- **复核**：09:25 用实际开盘价确认；高开超过参考买入价 3% 或已到目标价时放弃。\n\n")
}

func writePremarketTechnical(out *strings.Builder, candidate *strategy.Candidate) {
	if candidate == nil {
		return
	}
	out.WriteString("#### 技术指标\n")
	for _, line := range articleTechnicalLines(candidate) {
		out.WriteString("- " + line + "\n")
	}
	out.WriteString("\n")
}

func writePremarketStructure(out *strings.Builder, candidate *strategy.Candidate) {
	if candidate == nil {
		return
	}
	out.WriteString("#### 缠论结构\n")
	for _, line := range articleChanLines(candidate) {
		out.WriteString("- " + line + "\n")
	}
	out.WriteString("\n" + articleStructureParagraph(candidate) + "\n\n")
	out.WriteString("#### 波浪视角（简化）\n")
	for _, line := range articleWaveLines(candidate) {
		out.WriteString("- " + line + "\n")
	}
	out.WriteString("\n")
}

func writePremarketRisk(out *strings.Builder, r selection.Report, row premarketArticleRow) {
	out.WriteString("#### 观察边界\n")
	out.WriteString(premarketRiskText(r, row.observation) + "\n\n")
}

func premarketRiskText(r selection.Report, candidate *strategy.Candidate) string {
	if candidate == nil {
		return "当前没有精排技术证据，仅保留盘前高低预测；预测区间不是交易信号。"
	}
	return articleRiskParagraph(r, candidate)
}

type premarketForecastLine struct {
	label string
	value string
}

func premarketForecastLines(row premarketArticleRow) []premarketForecastLine {
	if !row.hasForecast {
		return []premarketForecastLine{{label: "预测", value: "暂缺（目标日 dayrange 预测未生成）"}}
	}
	forecast := row.forecast
	lines := []premarketForecastLine{
		{label: "参考价", value: fmt.Sprintf("%.2f", forecast.ReferencePrice)},
		{label: "预测高点", value: articleForecastHighText(forecast)},
		{label: "预测低点", value: articleForecastLowText(forecast)},
	}
	if forecast.ReferencePrice > 0 && forecast.PointHigh > 0 && forecast.PointLow > 0 {
		lines = append(lines, premarketForecastLine{label: "预测振幅", value: fmt.Sprintf("%.2f%%", (forecast.PointHigh-forecast.PointLow)/forecast.ReferencePrice*100)})
	}
	if low, high, ok := articleBuyRange(row.observation); ok {
		lines = append(lines, premarketForecastLine{label: "推荐买区", value: fmt.Sprintf("%.2f~%.2f 购入", low, high)})
	}
	return lines
}

func premarketCardTone(status string) string {
	switch status {
	case "可执行":
		return "green"
	case "放弃":
		return "amber"
	case "观察":
		return "blue"
	default:
		return "grey"
	}
}

func premarketStatusCounts(rows []premarketArticleRow) (maintain, observe, abandon int) {
	for _, row := range rows {
		switch row.status {
		case "可执行":
			maintain++
		case "放弃":
			abandon++
		default:
			observe++
		}
	}
	return maintain, observe, abandon
}

func premarketSkippedSummary(skipped []string) string {
	counts := map[string]int{}
	for _, item := range skipped {
		reason := "unknown"
		if index := strings.LastIndexByte(item, ':'); index >= 0 && index+1 < len(item) {
			reason = item[index+1:]
		}
		if reason == "already_published" {
			continue
		}
		counts[reason]++
	}
	if len(counts) == 0 {
		return ""
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%s %d", reason, counts[reason]))
	}
	return "本次未进入预测列表：" + strings.Join(parts, " · ") + "。"
}
