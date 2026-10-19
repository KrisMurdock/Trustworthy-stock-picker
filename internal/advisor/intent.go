package advisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/llm"
)

// Intent is a validated sub-intent with its deterministic query slots.
type Intent struct {
	Name         string `json:"intent"`
	Code         string `json:"code,omitempty"`
	AsOf         string `json:"as_of,omitempty"`
	Revision     string `json:"revision,omitempty"`
	ExperimentID string `json:"experiment_id,omitempty"`
}

type classification struct {
	Intents         []Intent
	Source          string
	ClarifyQuestion string
	Warnings        []string
}

type resolution struct {
	Intents         []Intent
	Classification  string
	ClarifyQuestion string
	Warnings        []string
}

var (
	codePattern         = regexp.MustCompile(`(?i)\b(sh(?:60|68)[0-9]{4}|sz(?:00|30)[0-9]{4})\b`)
	datePattern         = regexp.MustCompile(`\b20[0-9]{2}-[0-9]{2}-[0-9]{2}\b`)
	experimentIDPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{32}\b`)
	sqlPattern          = regexp.MustCompile(`(?i)\b(select\s+.+\s+from|insert\s+into|update\s+.+\s+set|delete\s+from|drop\s+table|alter\s+table|create\s+table)\b`)
	dependencyPattern   = regexp.MustCompile(`(先.*(再|然后)|首先.*(再|然后)|然后|接着|基于(上面|刚才|前一个)|根据(上面|刚才|前一个)|前一个结果|这个结果|该结果|它的结果)`)
)

var allowedIntents = map[string]bool{
	IntentReviewExplain:   true,
	IntentReportQuery:     true,
	IntentStockEvidence:   true,
	IntentExperimentQuery: true,
	IntentMethodology:     true,
	IntentClarify:         true,
	IntentUnsupported:     true,
}

func (s *Service) classify(ctx context.Context, req Request) (classification, error) {
	if result, ok := ruleClassification(req); ok {
		return result, nil
	}
	if !s.llmEnabled {
		return clarifyClassification("", "fallback"), nil
	}
	result, err := s.classifyLLM(ctx, req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return classification{}, ctxErr
		}
		return clarifyClassification("", "fallback"), nil
	}
	return result, nil
}

func ruleClassification(req Request) (classification, bool) {
	question := strings.TrimSpace(req.Question)
	if question == "" {
		return clarifyClassification("", "rule"), true
	}
	if unsupportedQuestion(question) {
		return classification{Intents: []Intent{{Name: IntentUnsupported}}, Source: "rule"}, true
	}

	clauses := splitClauses(question)
	clauseIntents := make([]Intent, 0, len(clauses))
	allHigh := true
	for _, clause := range clauses {
		intent, confidence := classifyClause(clause, req)
		if confidence < 0.72 {
			if len(clauses) > 1 && strings.TrimSpace(clause) != "" {
				allHigh = false
			}
			continue
		}
		clauseIntents = append(clauseIntents, intent)
	}

	if len(clauseIntents) >= 2 {
		if dependencyPattern.MatchString(question) {
			return clarifyClassification("请先确认要优先处理哪一个问题；当前问题包含依赖关系。", "rule"), true
		}
		return classification{Intents: dedupeIntents(clauseIntents), Source: "rule"}, true
	}
	if len(clauseIntents) == 1 && (len(clauses) == 1 || allHigh) {
		return classification{Intents: clauseIntents, Source: "rule"}, true
	}

	intent, confidence := classifyClause(question, req)
	if confidence >= 0.72 {
		return classification{Intents: []Intent{intent}, Source: "rule"}, true
	}
	return classification{}, false
}

func classifyClause(clause string, req Request) (Intent, float64) {
	lower := strings.ToLower(clause)
	if unsupportedQuestion(lower) {
		return Intent{Name: IntentUnsupported}, 1
	}

	codes := extractCodes(lower)
	dates := extractDates(lower)
	experimentIDs := experimentIDPattern.FindAllString(lower, -1)
	base := Intent{Revision: strings.TrimSpace(req.Revision)}
	if len(codes) == 1 {
		base.Code = codes[0]
	}
	if len(dates) == 1 {
		base.AsOf = dates[0]
	}
	if len(experimentIDs) == 1 {
		base.ExperimentID = strings.ToLower(experimentIDs[0])
	}

	type scored struct {
		name  string
		score float64
	}
	scores := make([]scored, 0, 5)
	add := func(name string, score float64) {
		if score > 0 {
			scores = append(scores, scored{name: name, score: score})
		}
	}

	if containsAny(lower, "实验", "experiment", "holdout", "保留区间", "父子实验", "策略对比", "对照实验", "策略研究") {
		add(IntentExperimentQuery, 4.8)
	}
	if len(codes) > 0 {
		add(IntentStockEvidence, 4.7)
	} else if containsAny(lower, "这只股票", "个股", "单股", "个股证据", "股票证据") {
		add(IntentStockEvidence, 4.2)
	}
	if containsAny(lower,
		"什么是", "是什么", "如何理解", "怎么理解", "如何计算", "怎么计算", "计算方式", "指标含义", "方法论", "原理", "区别", "怎么用", "如何用", "适合什么") {
		add(IntentMethodology, 4.3)
	}
	if containsAny(lower,
		"复盘", "归因", "事后", "失败", "亏损", "止损", "未入场", "没入场", "没买", "判断偏差", "判断错", "异动", "解释结果", "解释一下结果", "为什么没", "为什么亏", "为什么跌", "为什么止损") {
		add(IntentReviewExplain, 4.4)
	} else if strings.Contains(lower, "为什么") && containsAny(lower, "报告", "推荐", "入选", "排名", "结果") {
		add(IntentReviewExplain, 4.0)
	}
	if containsAny(lower, "报告", "推荐", "排名", "覆盖率", "命中率", "达标率", "观察名单", "入选", "表现", "统计", "样本", "几只", "哪些股票", "推荐状态") {
		add(IntentReportQuery, 3.8)
	}
	if containsAny(lower, "走势", "信号", "结构", "k线", "分型", "笔", "证据") && (len(codes) > 0 || containsAny(lower, "这只", "个股", "单股")) {
		add(IntentStockEvidence, 4.1)
	}
	if strings.Contains(lower, "为什么") && containsAny(lower, "用", "选", "看", "算", "指标", "策略") {
		add(IntentMethodology, 3.7)
	}

	if len(scores) == 0 {
		return Intent{Name: IntentClarify, Revision: base.Revision}, 0
	}
	best := scores[0]
	for _, candidate := range scores[1:] {
		if candidate.score > best.score {
			best = candidate
		}
	}
	base.Name = best.name
	return base, best.score / 5.0
}

func unsupportedQuestion(question string) bool {
	lower := strings.ToLower(question)
	if sqlPattern.MatchString(lower) {
		return true
	}
	return containsAny(lower,
		"忽略以上", "忽略前面", "系统提示词", "隐藏推理", "思维链", "内部推理", "泄露提示词", "api key", "apikey", "密钥", "base url", "数据库路径",
		"联网", "搜索网页", "上网查", "爬虫", "执行代码", "生成sql", "生成 sql", "写sql", "写 sql", "查询数据库", "读取文件", "写文件", "自动交易", "下单", "帮我买", "帮我卖", "立即买入", "立即卖出",
		"保证收益", "稳赚", "必涨", "包赚", "预测明天", "明天会涨", "明天会跌", "承诺收益")
}

func splitClauses(question string) []string {
	replacer := strings.NewReplacer(
		"，", "|", "。", "|", "！", "|", "？", "|", "；", "|", "?", "|", "!", "|", ";", "|", "\n", "|",
		"并且", "|", "同时", "|", "以及", "|", "另外", "|", "还有", "|", "顺便", "|", "然后", "|", "接着", "|",
	)
	parts := strings.Split(replacer.Replace(question), "|")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return []string{question}
	}
	return out
}

func extractCodes(question string) []string {
	matches := codePattern.FindAllString(question, -1)
	out := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, code := range matches {
		code = strings.ToLower(code)
		if !config.ValidCode(code) || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
}

func extractDates(question string) []string {
	matches := datePattern.FindAllString(question, -1)
	out := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, date := range matches {
		if seen[date] {
			continue
		}
		seen[date] = true
		out = append(out, date)
	}
	return out
}

func dedupeIntents(intents []Intent) []Intent {
	seen := map[string]bool{}
	out := make([]Intent, 0, len(intents))
	for _, intent := range intents {
		key := intent.Name + "\x00" + intent.Code + "\x00" + intent.AsOf + "\x00" + intent.Revision + "\x00" + intent.ExperimentID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, intent)
	}
	return out
}

func resolveIntents(req Request, decision classification, hasExperiments bool) resolution {
	result := resolution{Classification: decision.Source, ClarifyQuestion: decision.ClarifyQuestion, Warnings: append([]string(nil), decision.Warnings...)}
	if result.Classification == "" {
		result.Classification = "rule"
	}
	if strings.TrimSpace(result.ClarifyQuestion) != "" || containsIntent(decision.Intents, IntentClarify) {
		result.Intents = []Intent{{Name: IntentClarify}}
		return result
	}
	if containsIntent(decision.Intents, IntentUnsupported) {
		result.Intents = []Intent{{Name: IntentUnsupported}}
		return result
	}
	if len(decision.Intents) == 0 {
		result.Intents = []Intent{{Name: IntentClarify}}
		result.ClarifyQuestion = "请说明您想复盘报告、查询股票证据、查看实验，还是了解方法论？"
		return result
	}

	questionCodes := extractCodes(req.Question)
	questionDates := extractDates(req.Question)
	questionExperiments := experimentIDPattern.FindAllString(req.Question, -1)
	reqCode := strings.ToLower(strings.TrimSpace(req.Code))
	reqAsOf := strings.TrimSpace(req.AsOf)
	if reqCode != "" && !config.ValidCode(reqCode) {
		return clarificationResolution(result, "请提供有效的股票代码（例如 sz000001）。")
	}
	if reqAsOf != "" && !validDate(reqAsOf) {
		return clarificationResolution(result, "请提供有效日期（YYYY-MM-DD）。")
	}
	if len(questionCodes) > 1 {
		return clarificationResolution(result, "一次只查询一只股票，请指定一个股票代码。")
	}
	if len(questionDates) > 1 {
		return clarificationResolution(result, "一次只查询一个日期，请确认要使用哪一天。")
	}
	if len(questionExperiments) > 1 {
		return clarificationResolution(result, "一次只查询一个实验，请指定一个实验 ID。")
	}
	if len(questionCodes) == 1 && reqCode != "" && questionCodes[0] != reqCode {
		return clarificationResolution(result, "问题中的股票代码与请求参数不一致，请确认要查询哪一只。")
	}
	if len(questionDates) == 1 && reqAsOf != "" && questionDates[0] != reqAsOf {
		return clarificationResolution(result, "问题中的日期与请求参数不一致，请确认要查询哪一天。")
	}

	resolved := make([]Intent, 0, len(decision.Intents))
	seen := map[string]bool{}
	for _, intent := range decision.Intents {
		if !allowedIntents[intent.Name] || intent.Name == IntentClarify || intent.Name == IntentUnsupported {
			return clarificationResolution(result, "无法可靠判断问题类型，请换一种更具体的问法。")
		}
		if intent.Code == "" {
			if intent.Name == IntentStockEvidence && len(questionCodes) == 1 {
				intent.Code = questionCodes[0]
			} else if intent.Name != IntentStockEvidence || reqCode != "" {
				intent.Code = reqCode
			}
		}
		intent.Code = strings.ToLower(strings.TrimSpace(intent.Code))
		if intent.AsOf == "" {
			if len(questionDates) == 1 {
				intent.AsOf = questionDates[0]
			} else {
				intent.AsOf = reqAsOf
			}
		}
		intent.AsOf = strings.TrimSpace(intent.AsOf)
		if intent.Revision == "" {
			intent.Revision = strings.TrimSpace(req.Revision)
		}
		if intent.Name == IntentExperimentQuery && intent.ExperimentID == "" && len(questionExperiments) == 1 {
			intent.ExperimentID = strings.ToLower(questionExperiments[0])
		}

		switch intent.Name {
		case IntentReviewExplain, IntentReportQuery:
			if intent.AsOf == "" {
				return clarificationResolution(result, "请提供要查询的报告日期（YYYY-MM-DD）。")
			}
			if !validDate(intent.AsOf) {
				return clarificationResolution(result, "请提供有效日期（YYYY-MM-DD）。")
			}
		case IntentStockEvidence:
			if intent.Code == "" {
				return clarificationResolution(result, "请提供要查询的股票代码（例如 sz000001）。")
			}
			if !config.ValidCode(intent.Code) {
				return clarificationResolution(result, "请提供有效的股票代码（例如 sz000001）。")
			}
			if intent.AsOf == "" {
				return clarificationResolution(result, "请提供该股票复盘对应的报告日期（YYYY-MM-DD）。")
			}
			if !validDate(intent.AsOf) {
				return clarificationResolution(result, "请提供有效日期（YYYY-MM-DD）。")
			}
		case IntentExperimentQuery:
			if !hasExperiments {
				intent.Name = IntentUnsupported
				result.Warnings = append(result.Warnings, "experiment_source_unavailable")
			}
		case IntentMethodology:
			intent.Code, intent.AsOf, intent.Revision, intent.ExperimentID = "", "", "", ""
		}

		key := intent.Name + "\x00" + intent.Code + "\x00" + intent.AsOf + "\x00" + intent.Revision + "\x00" + intent.ExperimentID
		if seen[key] {
			continue
		}
		for _, existing := range resolved {
			if existing.Name == intent.Name && existing != intent {
				return clarificationResolution(result, "同一类问题只能有一个查询条件，请确认要优先查询哪一个日期或股票。")
			}
		}
		seen[key] = true
		resolved = append(resolved, intent)
	}
	if len(resolved) > MaxSubIntents {
		return clarificationResolution(result, "一次最多回答三个互不依赖的问题，请先合并或选择最重要的三个。")
	}
	if len(resolved) == 0 {
		return clarificationResolution(result, "请提出一个更具体的问题。")
	}
	result.Intents = resolved
	return result
}

func clarificationResolution(result resolution, question string) resolution {
	result.Intents = []Intent{{Name: IntentClarify}}
	result.ClarifyQuestion = question
	return result
}

func validDate(value string) bool {
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func containsIntent(intents []Intent, name string) bool {
	for _, intent := range intents {
		if intent.Name == name {
			return true
		}
	}
	return false
}

func clarifyClassification(question, source string) classification {
	if strings.TrimSpace(question) == "" {
		question = "请说明您想复盘报告、查询股票证据、查看实验，还是了解方法论？"
	}
	return classification{Intents: []Intent{{Name: IntentClarify}}, Source: source, ClarifyQuestion: question}
}

type classifyResponse struct {
	Intents []struct {
		Intent       string `json:"intent"`
		Code         string `json:"code,omitempty"`
		AsOf         string `json:"as_of,omitempty"`
		Revision     string `json:"revision,omitempty"`
		ExperimentID string `json:"experiment_id,omitempty"`
	} `json:"intents"`
	NeedsClarification bool   `json:"needs_clarification"`
	ClarifyQuestion    string `json:"clarify_question,omitempty"`
}

func (s *Service) classifyLLM(ctx context.Context, req Request) (classification, error) {
	payload, err := json.Marshal(struct {
		Question string `json:"question"`
		AsOf     string `json:"as_of,omitempty"`
		Code     string `json:"code,omitempty"`
		Revision string `json:"revision,omitempty"`
		History  []Turn `json:"recent_history,omitempty"`
	}{Question: req.Question, AsOf: req.AsOf, Code: req.Code, Revision: req.Revision, History: req.History})
	if err != nil {
		return classification{}, err
	}
	result, err := s.client.Chat(ctx, llm.ChatRequest{
		System:          readPrompt("classify.md"),
		Messages:        []llm.Message{{Role: "user", Content: string(payload)}},
		Temperature:     0,
		MaxOutputTokens: 512,
	}, nil)
	if err != nil {
		return classification{}, err
	}
	if len(result.Text) > 8192 {
		return classification{}, errors.New("classification output is too large")
	}

	var response classifyResponse
	decoder := json.NewDecoder(strings.NewReader(result.Text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return classification{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return classification{}, errors.New("classification must contain one JSON object")
	}
	if response.NeedsClarification {
		return clarifyClassification(sanitizeQuestion(response.ClarifyQuestion), "llm"), nil
	}
	if len(response.Intents) == 0 {
		return clarificationForInvalidClassification(), nil
	}
	if len(response.Intents) > MaxSubIntents {
		return clarificationForInvalidClassification(), nil
	}
	intents := make([]Intent, 0, len(response.Intents))
	for _, item := range response.Intents {
		if !allowedIntents[item.Intent] {
			return clarificationForInvalidClassification(), nil
		}
		if item.Code != "" && !config.ValidCode(strings.ToLower(item.Code)) {
			return clarificationForInvalidClassification(), nil
		}
		if item.AsOf != "" && !validDate(item.AsOf) {
			return clarificationForInvalidClassification(), nil
		}
		intents = append(intents, Intent{
			Name:         item.Intent,
			Code:         strings.ToLower(strings.TrimSpace(item.Code)),
			AsOf:         strings.TrimSpace(item.AsOf),
			Revision:     strings.TrimSpace(item.Revision),
			ExperimentID: strings.ToLower(strings.TrimSpace(item.ExperimentID)),
		})
	}
	return classification{Intents: intents, Source: "llm"}, nil
}

func clarificationForInvalidClassification() classification {
	return clarifyClassification("请换一种更具体的问法，说明要复盘报告、查询股票证据、查看实验，还是了解方法论。", "fallback")
}

func sanitizeQuestion(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if utf8.RuneCountInString(value) > 240 {
		value = string([]rune(value)[:240])
	}
	return value
}

func intentNames(intents []Intent) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(intents))
	for _, intent := range intents {
		if seen[intent.Name] {
			continue
		}
		seen[intent.Name] = true
		out = append(out, intent.Name)
	}
	return out
}

func deterministicOnly(intents []Intent) bool {
	if len(intents) == 0 {
		return true
	}
	for _, intent := range intents {
		if intent.Name != IntentClarify && intent.Name != IntentUnsupported {
			return false
		}
	}
	return true
}

func deterministicAnswer(intents []Intent, clarify string) string {
	if len(intents) == 1 && intents[0].Name == IntentClarify {
		if strings.TrimSpace(clarify) == "" {
			clarify = "请补充一个关键条件后我再查询。"
		}
		return "请补充一个关键信息：" + sanitizeQuestion(clarify)
	}
	return "当前版本只支持本地报告复盘、股票证据、实验档案和方法论说明；不会生成 SQL、联网检索、读取隐藏提示词或给出交易指令。请把问题限定在这些范围内。"
}

func plansFor(intents []Intent) []Plan {
	plans := make([]Plan, 0, len(intents))
	for _, intent := range intents {
		plan := Plan{Intent: intent.Name}
		switch intent.Name {
		case IntentReviewExplain:
			plan.Source = "review.Day/review.Event"
			plan.Steps = []string{"按 as_of 与 revision 读取复盘投影", "优先选择失败、止损、未入场、缺失和异动事件", "每个子意图最多保留 12 条最小事实"}
		case IntentReportQuery:
			plan.Source = "review.Day"
			plan.Steps = []string{"读取报告元数据与聚合", "选择少量代表事件", "不发送完整报告"}
		case IntentStockEvidence:
			plan.Source = "review.Event"
			plan.Steps = []string{"按代码、as_of 与 revision 读取单股复盘事件", "提取状态、结果、计划、风险与缺失字段", "不发送完整日线"}
		case IntentExperimentQuery:
			plan.Source = "experiments.List/experiments.Get"
			plan.Steps = []string{"按实验 ID 读取单条记录，否则读取最近实验列表", "只保留状态、结论、假设和核心统计", "不读取完整快照或交易明细"}
		case IntentMethodology:
			plan.Source = "none"
			plan.Steps = []string{"不查询数据库", "仅使用方法论提示词回答"}
		case IntentUnsupported:
			plan.Source = "none"
			plan.Steps = []string{"不查询数据库", "返回只读能力边界"}
		default:
			plan.Source = "none"
			plan.Steps = []string{"不查询数据库", "追问一个关键条件"}
		}
		plans = append(plans, plan)
	}
	return plans
}
