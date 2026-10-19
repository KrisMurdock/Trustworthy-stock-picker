package report

import (
	"fmt"
	"sort"
	"stock-watcher/internal/config"
	"stock-watcher/internal/diagnostics"
	"stock-watcher/internal/notify"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/strategy"
	"strings"
	"time"
)

const (
	detailStocksPerPage = 8
	mainTableRows       = 10
)

var riskPriority = []string{
	"结构影子期·仅观察",
	"历史单笔净亏损超限",
	"独立验证未通过",
	"匹配规则未通过历史检验",
	"缠论未通过",
	"缠论结构未知",
	"预算不足最小买入量",
	"涨幅/偏离过大",
	"日内波幅过大",
	"上影线较长",
}

type Cards struct {
	Main    notify.Message
	Details []notify.Message
}

func FromSelection(r selection.Report) (Cards, error) {
	expires, err := time.ParseInLocation("2006-01-02 15:04", r.Target+" 09:30", time.FixedZone("Asia/Shanghai", 8*60*60))
	if err != nil {
		return Cards{}, err
	}

	short := r.Research != nil
	baseTitle := decisionBaseTitle(r, short)
	mainTitle, template := mainCardTitle(r, short, baseTitle)
	diagnosis := diagnostics.Explain(r)
	main := notify.Message{
		Version:        2,
		EventID:        r.ID + ":main",
		DisplayEventID: r.ID,
		PartLabel:      "主卡",
		Title:          mainTitle,
		Sections: []notify.Section{{
			Lines: []string{mainConclusion(r, short), metadataLine(r)},
			Table: mainCardTable(r, short, diagnosis),
		}},
		Template:   template,
		TrimPolicy: notify.TrimNone,
		ExpiresAt:  expires,
	}

	pages := detailPages(r, short, diagnosis)
	details := make([]notify.Message, 0, len(pages))
	for index, sections := range pages {
		number := index + 1
		details = append(details, notify.Message{
			Version:        2,
			EventID:        fmt.Sprintf("%s:detail:%02d", r.ID, number),
			DisplayEventID: r.ID,
			PartLabel:      fmt.Sprintf("详情 %d/%d", number, len(pages)),
			Title:          fmt.Sprintf("%s · 详情 %d/%d", baseTitle, number, len(pages)),
			Sections:       sections,
			Template:       template,
			TrimPolicy:     notify.TrimNone,
			ExpiresAt:      expires,
		})
	}

	return Cards{Main: main, Details: details}, nil
}

func decisionBaseTitle(r selection.Report, short bool) string {
	monthDay := r.Target
	if len(monthDay) > 5 {
		monthDay = monthDay[len(monthDay)-5:]
	}
	if short {
		return fmt.Sprintf("A股龙头池 · %s 短线机会", monthDay)
	}
	return fmt.Sprintf("A股龙头池 · %s T+1 筛选", monthDay)
}

func mainCardTitle(r selection.Report, short bool, baseTitle string) (string, string) {
	if short {
		switch {
		case !r.SelectionEnabled:
			return baseTitle + " · 筛选未启用", "orange"
		case r.ExecutionMode == strategy.StructureGuardShadow && len(r.Ranking) > 0:
			return baseTitle + " · 仅观察（结构影子期）", "blue"
		case len(r.Selected) > 0:
			return fmt.Sprintf("%s · %d 只可执行", baseTitle, len(r.Selected)), "green"
		case len(r.Ranking) > 0:
			return baseTitle + " · 仅观察", "blue"
		default:
			return baseTitle + " · 无候选", "grey"
		}
	}
	switch {
	case !r.SelectionEnabled:
		return baseTitle + " · 筛选未启用", "orange"
	case len(r.Selected) > 0:
		return fmt.Sprintf("%s · 有入选 %d 只", baseTitle, len(r.Selected)), "green"
	default:
		return baseTitle + " · 无入选", "grey"
	}
}

func mainConclusion(r selection.Report, short bool) string {
	if short {
		switch {
		case !r.SelectionEnabled:
			if len(r.Ranking) > 0 {
				return "筛选未启用；Top10 仅供观察"
			}
			return "筛选未启用；本次不生成候选名单"
		case r.ExecutionMode == strategy.StructureGuardShadow && len(r.Ranking) > 0:
			first := r.Ranking[0]
			return fmt.Sprintf("结构影子期：不产生可执行推荐；观察排名 %d 只，最高机会分 %s %s %.1f",
				len(r.Ranking), first.Code, escape(first.Name), first.Score)
		case len(r.Selected) > 0:
			first := shortFirstSelected(r.Selected)
			entry, stop := "—", "—"
			if first.Ranking != nil && first.Ranking.Plan != nil {
				entry = fmt.Sprintf("%.2f", first.Ranking.Plan.EntryPrice)
				stop = fmt.Sprintf("%.2f", first.Ranking.Plan.StopPrice)
			}
			return fmt.Sprintf("可执行 %d 只；首选 %s %s · 参考买入 %s · 止损 %s",
				len(r.Selected), first.Code, escape(first.Name), entry, stop)
		case len(r.Ranking) > 0:
			first := r.Ranking[0]
			return fmt.Sprintf("今日无可执行推荐；最高机会分 %s %s %.1f", first.Code, escape(first.Name), first.Score)
		default:
			return fmt.Sprintf("今日无候选；有效行情 %d/%d", r.Eligible, r.PoolSize)
		}
	}

	if !r.SelectionEnabled {
		return "筛选未启用；本次不生成入选名单"
	}
	selected := sortedTrendSelected(r.Selected)
	if len(selected) > 0 {
		first := selected[0]
		return fmt.Sprintf("入选 %d 只；最高评分 %s %s %d", len(selected), first.Code, escape(first.Name), first.Metrics.Score)
	}
	return fmt.Sprintf("今日无入选；有效行情 %d/%d", r.Eligible, r.PoolSize)
}

func shortFirstSelected(selected []selection.Candidate) selection.Candidate {
	first := selected[0]
	for _, candidate := range selected[1:] {
		if candidate.Ranking != nil && (first.Ranking == nil || candidate.Ranking.Rank < first.Ranking.Rank) {
			first = candidate
		}
	}
	return first
}

func metadataLine(r selection.Report) string {
	line := fmt.Sprintf("数据日 %s · 目标日 %s · 有效行情 %d/%d · 数据排除 %d",
		r.AsOf, r.Target, r.Eligible, r.PoolSize, len(r.Excluded))
	if hasStructureGuard(r) {
		line += fmt.Sprintf(" · 结构通过 %d/%d", r.GuardPassed, r.Eligible)
	}
	if r.ExecutionMode == strategy.StructureGuardShadow {
		line += " · 执行模式 结构影子期（仅观察）"
	}
	return line
}

func mainCardTable(r selection.Report, short bool, diagnosis *diagnostics.Report) *notify.Table {
	table := &notify.Table{
		PageSize:          mainTableRows,
		RowHeight:         "middle",
		FreezeFirstColumn: true,
	}
	if short {
		table.Columns = []notify.TableColumn{
			{Key: "stock", Label: "标的", Kind: notify.TableText, Width: "26%"},
			{Key: "status", Label: "状态", Kind: notify.TableStatus, Width: "10%"},
			{Key: "score", Label: "机会分", Kind: notify.TableText, Width: "10%"},
			{Key: "entry", Label: "参考买入", Kind: notify.TableText, Width: "12%"},
			{Key: "target", Label: "目标价", Kind: notify.TableText, Width: "12%"},
			{Key: "stop", Label: "止损", Kind: notify.TableText, Width: "12%"},
			{Key: "risk", Label: "主要风险", Kind: notify.TableText, Width: "18%"},
		}
		for _, candidate := range shortMainCandidates(r) {
			entry, target, stop := "—", "—", "—"
			if candidate.Plan != nil && candidate.Recommended && r.ExecutionMode != strategy.StructureGuardShadow {
				entry = fmt.Sprintf("%.2f", candidate.Plan.EntryPrice)
				target = fmt.Sprintf("%.2f", candidate.Plan.TakeProfit)
				stop = fmt.Sprintf("%.2f", candidate.Plan.StopPrice)
			}
			detail := diagnostics.Candidate{}
			if diagnosis != nil {
				detail = diagnosis.Candidates[candidate.Code]
			}
			table.Rows = append(table.Rows, []notify.TableCell{
				{Text: fmt.Sprintf("%d · %s %s", candidate.Rank, candidate.Code, escape(candidate.Name))},
				{Text: shortCandidateStatus(r, candidate)},
				{Text: fmt.Sprintf("%.1f", candidate.Score)},
				{Text: entry},
				{Text: target},
				{Text: stop},
				{Text: riskSummary(detail.Risks)},
			})
		}
		return table
	}

	table.Columns = []notify.TableColumn{
		{Key: "stock", Label: "标的", Kind: notify.TableText, Width: "28%"},
		{Key: "change", Label: "涨幅", Kind: notify.TableChange, Width: "9%"},
		{Key: "close", Label: "收盘", Kind: notify.TableText, Width: "9%"},
		{Key: "score", Label: "评分", Kind: notify.TableText, Width: "9%"},
		{Key: "ma", Label: "MA5/10/20", Kind: notify.TableText, Width: "18%"},
		{Key: "rsi", Label: "RSI14", Kind: notify.TableText, Width: "9%"},
		{Key: "volume", Label: "量比", Kind: notify.TableText, Width: "9%"},
		{Key: "updays", Label: "连涨", Kind: notify.TableText, Width: "9%"},
	}
	selected := sortedTrendSelected(r.Selected)
	for index, candidate := range selected[:min(len(selected), mainTableRows)] {
		tone := notify.ToneDefault
		switch {
		case candidate.ChangePct > 0:
			tone = notify.TonePositive
		case candidate.ChangePct < 0:
			tone = notify.ToneNegative
		}
		table.Rows = append(table.Rows, []notify.TableCell{
			{Text: fmt.Sprintf("%d · %s %s", index+1, candidate.Code, escape(candidate.Name))},
			{Text: fmt.Sprintf("%+.2f%%", candidate.ChangePct), Tone: tone},
			{Text: fmt.Sprintf("%.2f", candidate.Close)},
			{Text: fmt.Sprintf("%d", candidate.Metrics.Score)},
			{Text: fmt.Sprintf("%.2f / %.2f / %.2f", candidate.Metrics.MA5, candidate.Metrics.MA10, candidate.Metrics.MA20)},
			{Text: fmt.Sprintf("%.1f", candidate.Metrics.RSI14)},
			{Text: fmt.Sprintf("%.2f", candidate.Metrics.VolumeRatio)},
			{Text: fmt.Sprintf("%d日", candidate.Metrics.UpDays)},
		})
	}
	return table
}

func shortMainCandidates(r selection.Report) []strategy.Candidate {
	candidates := make([]strategy.Candidate, 0, len(r.Ranking)+len(r.Selected))
	if len(r.Ranking) > 0 {
		candidates = append(candidates, r.Ranking...)
	} else {
		for _, candidate := range r.Selected {
			if candidate.Ranking != nil {
				candidates = append(candidates, *candidate.Ranking)
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Rank != candidates[j].Rank {
			return candidates[i].Rank < candidates[j].Rank
		}
		return candidates[i].Code < candidates[j].Code
	})
	return candidates[:min(len(candidates), mainTableRows)]
}

func shortCandidateStatus(r selection.Report, candidate strategy.Candidate) string {
	if !r.SelectionEnabled {
		return "未启用"
	}
	if r.ExecutionMode == strategy.StructureGuardShadow {
		if candidate.StructureGuard != nil {
			switch candidate.StructureGuard.GuardStatus {
			case strategy.StructureGuardPass:
				return "结构通过·仅观察"
			case strategy.StructureGuardFail:
				return "结构未过·仅观察"
			case strategy.StructureGuardUnknown:
				return "结构未知·仅观察"
			}
		}
		return "仅观察"
	}
	if candidate.StructureGuard != nil {
		switch candidate.StructureGuard.GuardStatus {
		case strategy.StructureGuardFail:
			return "结构未过"
		case strategy.StructureGuardUnknown:
			return "结构未知"
		case strategy.StructureGuardPass:
			if candidate.Recommended {
				return "可执行"
			}
			return "结构通过"
		}
	}
	if candidate.Recommended {
		return "可执行"
	}
	return "观察"
}

func riskSummary(risks []string) string {
	if len(risks) == 0 {
		return "无"
	}
	best := 0
	bestPriority := len(riskPriority) + 1
	for index, risk := range risks {
		priority := riskPriorityIndex(risk)
		if priority >= 0 && priority < bestPriority {
			best = index
			bestPriority = priority
		}
	}
	if len(risks) == 1 {
		return risks[best]
	}
	return fmt.Sprintf("%s +%d", risks[best], len(risks)-1)
}

func riskPriorityIndex(risk string) int {
	for index, label := range riskPriority {
		if risk == label || strings.HasPrefix(risk, label) {
			return index
		}
	}
	return -1
}

func detailPages(r selection.Report, short bool, diagnosis *diagnostics.Report) [][]notify.Section {
	if short {
		if !r.SelectionEnabled || (len(r.Ranking) == 0 && len(r.Selected) == 0) {
			return nil
		}
		validation := diagnostics.Validation{}
		if diagnosis != nil {
			validation = diagnosis.Validation
		}
		global := []notify.Section{
			researchSection(r.Research, validation),
			strategyParametersSection(r.Research.Parameters),
		}
		if section, ok := structureGuardSection(r); ok {
			global = append(global, section)
		}
		return paginateDetails(global, shortCandidateDetailSections(r, diagnosis), exclusionSections(r))
	}
	if !r.SelectionEnabled {
		return nil
	}
	return paginateDetails(
		[]notify.Section{{Heading: "筛选口径", Fields: trendCriteriaFields(r)}},
		trendCandidateDetailSections(r),
		exclusionSections(r),
	)
}

func paginateDetails(global, candidates, exclusions []notify.Section) [][]notify.Section {
	pages := 1
	if len(candidates) > 0 {
		pages = (len(candidates) + detailStocksPerPage - 1) / detailStocksPerPage
	}
	out := make([][]notify.Section, 0, pages)
	for page := 0; page < pages; page++ {
		sections := make([]notify.Section, 0, len(global)+detailStocksPerPage+len(exclusions))
		if page == 0 {
			sections = append(sections, global...)
		}
		start := page * detailStocksPerPage
		end := min(start+detailStocksPerPage, len(candidates))
		if start < end {
			sections = append(sections, candidates[start:end]...)
		}
		if page == pages-1 {
			sections = append(sections, exclusions...)
		}
		for index := range sections {
			sections[index].Optional = false
		}
		out = append(out, sections)
	}
	return out
}

func shortCandidateDetailSections(r selection.Report, diagnosis *diagnostics.Report) []notify.Section {
	candidates := shortMainCandidates(r)
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		seen[candidate.Code] = true
	}
	supplements := make([]strategy.Candidate, 0)
	for _, selected := range r.Selected {
		if selected.Ranking == nil || seen[selected.Code] {
			continue
		}
		seen[selected.Code] = true
		supplements = append(supplements, *selected.Ranking)
	}
	sort.SliceStable(supplements, func(i, j int) bool {
		if supplements[i].Rank != supplements[j].Rank {
			return supplements[i].Rank < supplements[j].Rank
		}
		return supplements[i].Code < supplements[j].Code
	})
	candidates = append(candidates, supplements...)

	sections := make([]notify.Section, 0, len(candidates))
	for _, candidate := range candidates {
		detail := diagnostics.Candidate{}
		if diagnosis != nil {
			detail = diagnosis.Candidates[candidate.Code]
		}
		evidence := "—"
		if candidate.Evidence != nil {
			evidence = detailedStats(*candidate.Evidence)
		}
		score, breakdown := candidate.Score, candidate.ScoreBreakdown
		if candidate.EligibleRank > 0 && candidate.EligibleScore != 0 {
			score, breakdown = candidate.EligibleScore, candidate.EligibleScoreBreakdown
		}
		lines := []string{}
		if candidate.StructureGuard != nil {
			lines = append(lines, "结构门控："+structureGuardSummary(*candidate.StructureGuard))
		}
		lines = append(lines,
			"旧 5/15/30 诊断："+candidateChanSummary(candidate.Chan),
			"评分推导："+candidateScoreFormula(breakdown),
		)
		for _, rule := range detail.Rules {
			lines = append(lines, diagnosticRuleLine(rule))
		}
		lines = append(lines, "风险："+fullRisks(detail.Risks))
		heading := fmt.Sprintf("第%d名 · %s %s · %s",
			candidate.Rank, candidate.Code, escape(candidate.Name), shortCandidateStatus(r, candidate))
		if candidate.EligibleRank > 0 {
			eligibleLabel := "可入选"
			if r.ExecutionMode == strategy.StructureGuardShadow {
				eligibleLabel = "结构通过"
			}
			heading = fmt.Sprintf("%s第%d / 观察第%d名 · %s %s · %s",
				eligibleLabel, candidate.EligibleRank, observationRank(&candidate), candidate.Code, escape(candidate.Name), shortCandidateStatus(r, candidate))
		}
		if candidate.Rank > mainTableRows {
			heading = "推荐补充 · " + heading
		}
		sections = append(sections, notify.Section{
			Heading: heading,
			Fields: []notify.Field{
				{Label: "机会分", Value: fmt.Sprintf("%.1f", score)},
				{Label: "历史证据", Value: evidence},
				{Label: "依据", Value: tags(candidate.Reasons)},
				{Label: "规则", Value: tags(candidate.Rules)},
			},
			Lines: lines,
		})
	}
	return sections
}

func candidateChanSummary(evidence *strategy.ChanEvidence) string {
	if evidence == nil {
		return "未知：未检查；仅诊断，不影响正式结论"
	}
	return chanSummary(*evidence)
}

func hasStructureGuard(r selection.Report) bool {
	for i := range r.Ranking {
		if r.Ranking[i].StructureGuard != nil {
			return true
		}
	}
	return false
}

func observationRank(candidate *strategy.Candidate) int {
	if candidate.ObservationRank > 0 {
		return candidate.ObservationRank
	}
	return candidate.Rank
}

func structureGuardSection(r selection.Report) (notify.Section, bool) {
	found := false
	mode, rule := "", strategy.StructureGuardRule
	for i := range r.Ranking {
		if r.Ranking[i].StructureGuard == nil {
			continue
		}
		found = true
		mode = r.Ranking[i].StructureGuard.Mode
		rule = r.Ranking[i].StructureGuard.Rule
		break
	}
	if !found {
		return notify.Section{}, false
	}
	return notify.Section{
		Heading: "结构门控",
		Fields: []notify.Field{
			{Label: "规则", Value: rule},
			{Label: "模式", Value: mode},
			{Label: "结构通过", Value: fmt.Sprintf("%d/%d", r.GuardPassed, r.Eligible)},
			{Label: "口径", Value: "日线定方向；30m/5m 仅能否决，不生成买点"},
		},
	}, true
}

func structureGuardSummary(evidence strategy.StructureGuardEvidence) string {
	switch evidence.GuardStatus {
	case strategy.StructureGuardPass:
		return "pass：日线结构未逆向，30m/5m 未触发失效；仅代表未被过滤，不是买点"
	case strategy.StructureGuardFail:
		return "fail：" + escape(evidence.Reason) + "；" + structureGuardConsequence(evidence)
	case strategy.StructureGuardUnknown:
		if evidence.Reason == "" {
			return "unknown：结构证据不足；" + structureGuardConsequence(evidence)
		}
		return "unknown：" + escape(evidence.Reason) + "；" + structureGuardConsequence(evidence)
	default:
		return "unknown：未评估；" + structureGuardConsequence(evidence)
	}
}

func structureGuardConsequence(evidence strategy.StructureGuardEvidence) string {
	if evidence.Mode == strategy.StructureGuardShadow {
		return "影子期仅记录，不产生可执行推荐"
	}
	return "已过滤"
}

func candidateScoreFormula(breakdown *strategy.ScoreBreakdown) string {
	if breakdown == nil || breakdown.Formula == "" {
		return "未记录"
	}
	return escape(breakdown.Formula)
}

func diagnosticRuleLine(rule diagnostics.Rule) string {
	parts := make([]string, 0, 1+len(rule.Failures))
	if rule.Summary != "" {
		parts = append(parts, escape(rule.Summary))
	}
	for _, failure := range rule.Failures {
		if failure.Message != "" {
			parts = append(parts, escape(failure.Message))
		}
	}
	value := "—"
	if len(parts) > 0 {
		value = strings.Join(parts, "；")
	}
	return fmt.Sprintf("%s（全股池历史）：%s", escape(rule.Label), value)
}

func fullRisks(risks []string) string {
	if len(risks) == 0 {
		return "无"
	}
	return escape(strings.Join(risks, " / "))
}

func sortedTrendSelected(selected []selection.Candidate) []selection.Candidate {
	out := append([]selection.Candidate(nil), selected...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Metrics.Score != out[j].Metrics.Score {
			return out[i].Metrics.Score > out[j].Metrics.Score
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func trendCandidateDetailSections(r selection.Report) []notify.Section {
	selected := sortedTrendSelected(r.Selected)
	sections := make([]notify.Section, 0, len(selected))
	for index, candidate := range selected {
		sections = append(sections, notify.Section{
			Heading: fmt.Sprintf("入选 %d · %s %s", index+1, candidate.Code, escape(candidate.Name)),
			Fields: []notify.Field{
				{Label: "涨幅", Value: fmt.Sprintf("%+.2f%%", candidate.ChangePct)},
				{Label: "收盘", Value: fmt.Sprintf("%.2f", candidate.Close)},
				{Label: "评分", Value: fmt.Sprintf("%d", candidate.Metrics.Score)},
				{Label: "MA5/10/20", Value: fmt.Sprintf("%.2f / %.2f / %.2f", candidate.Metrics.MA5, candidate.Metrics.MA10, candidate.Metrics.MA20)},
				{Label: "RSI14", Value: fmt.Sprintf("%.1f", candidate.Metrics.RSI14)},
				{Label: "量比", Value: fmt.Sprintf("%.2f", candidate.Metrics.VolumeRatio)},
				{Label: "连涨", Value: fmt.Sprintf("%d日", candidate.Metrics.UpDays)},
			},
		})
	}
	return sections
}

func exclusionSections(r selection.Report) []notify.Section {
	sections := make([]notify.Section, 0, 2)
	if len(r.Excluded) > 0 {
		sections = append(sections, optionalExclusionSection("数据排除", r.Excluded))
	}
	if len(r.Filtered) > 0 {
		sections = append(sections, optionalExclusionSection("规则未通过", r.Filtered))
	}
	return sections
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

func researchSection(research *strategy.Research, diagnosis diagnostics.Validation) notify.Section {
	conclusion := diagnosis.Summary
	if diagnosis.Status == "passed" {
		conclusion = "通过"
	}
	section := notify.Section{
		Heading: "研究与验证",
		Fields: []notify.Field{
			{Label: "训练截至", Value: research.TrainingThrough},
			{Label: "研究样本", Value: fmt.Sprint(research.SampleCount)},
			{Label: "可用/已评估样本", Value: fmt.Sprintf("%d / %d（过滤候选 %d）", research.AvailableSamples, research.EvaluatedSamples, research.AuditCandidates)},
			{Label: "开发区间", Value: evaluationRange(research.Development)},
			{Label: "开发表现", Value: conciseStats(research.Development.Recommended)},
			{Label: "开发 cohort", Value: conciseCohort(research.Development.Cohort)},
			{Label: "独立验证区间", Value: evaluationRange(research.Holdout)},
			{Label: "独立验证表现", Value: conciseStats(research.Holdout.Recommended)},
			{Label: "独立验证 cohort", Value: conciseCohort(research.Holdout.Cohort)},
			{Label: "独立验证结论", Value: conclusion},
			{Label: "验证状态", Value: flagLabel(research.Verdict)},
			{Label: "日历覆盖", Value: calendarLabel(research.Calendar)},
			{Label: "限制标记", Value: flagList(research.Flags)},
		},
	}
	for _, failure := range diagnosis.Failures {
		section.Lines = append(section.Lines, failure.Message)
	}
	return section
}

func conciseCohort(stats strategy.CohortStats) string {
	if stats.Version == 0 || stats.SignalDates == 0 {
		return "尚无完整信号日 cohort"
	}
	paired := ""
	if stats.PairedDates > 0 {
		paired = fmt.Sprintf("/配对%d日", stats.PairedDates)
	}
	return fmt.Sprintf("%d信号日/%d计划槽位/%d已成交%s · 日均净收益%+.2f%% · 2.5%%触达%.1f%%",
		stats.SignalDates, stats.PlannedSlots, stats.FilledSlots, paired, stats.MeanNet, stats.Rate25*100)
}

func calendarLabel(c strategy.CalendarCoverage) string {
	if c.Source == "" {
		return "来源未记录"
	}
	state := "完整"
	if !c.Complete {
		state = "不完整"
	}
	return fmt.Sprintf("%s · %s～%s · %s", state, c.From, c.Through, c.Source)
}

func flagList(flags []string) string {
	if len(flags) == 0 {
		return "无"
	}
	out := make([]string, len(flags))
	for i, flag := range flags {
		out[i] = flagLabel(flag)
	}
	return strings.Join(out, " / ")
}

func flagLabel(flag string) string {
	labels := map[string]string{
		"validated":                    "通过独立验证",
		"exploratory":                  "探索性结果",
		"exploratory_current_universe": "当前股池探索性验证",
		"validation_failed":            "独立验证失败",
		"insufficient_evidence":        "证据不足",
		"partial_calendar":             "交易日历不完整",
		"configured_calendar":          "使用配置日历",
		"current_universe_bias":        "当前股池幸存者偏差",
		"historical_st_unknown":        "历史 ST 状态未知",
		"incomplete_signal_date":       "存在不完整信号日",
		"contaminated":                 "保留区间已污染",
		"corporate_action":             "公司行为未决",
	}
	if text := labels[flag]; text != "" {
		return text
	}
	return strings.ReplaceAll(flag, "_", " ")
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
	if stats.Samples == 0 {
		return "0样本/0日 · 暂无可统计收益"
	}
	if stats.Resolved == 0 {
		return fmt.Sprintf("%d样本/%d日 · 尚无已退出样本，暂无可统计收益", stats.Samples, stats.Dates)
	}
	return fmt.Sprintf("%d样本/%d日 · 净目标达标%.1f%% · 已退出均值%+.2f%%", stats.Samples, stats.Dates, stats.HitRate*100, stats.MeanNet)
}

func strategyParametersSection(parameters config.Strategy) notify.Section {
	return notify.Section{
		Heading: "参数口径",
		Fields: []notify.Field{
			{Label: "买入时点", Value: "次日开盘（模拟）"},
			{Label: "净目标/止损", Value: fmt.Sprintf("净+%.1f%% / %.1f%%", parameters.TargetNetPct, parameters.StopNetPct)},
			{Label: "历史风险准入", Value: "规则历史每笔净收益 > -3%；止损触发目标与历史准入分别校验"},
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

func chanSummary(evidence strategy.ChanEvidence) string {
	switch evidence.Status {
	case strategy.ChanPass:
		details := []string{}
		for _, level := range evidence.Levels {
			switch level.Period {
			case 30:
				if level.Zone != nil {
					details = append(details, "30m未破中枢下沿")
				} else if level.LastStroke == "up" {
					details = append(details, "30m最近完成笔向上")
				}
			case 15, 5:
				if level.LastFractal == "bottom" {
					details = append(details, fmt.Sprintf("%dm底分型收复", level.Period))
				}
			}
		}
		if len(details) == 0 {
			return "通过：5m/15m/30m 旧路径条件满足；仅诊断，不构成买点"
		}
		return "通过：" + strings.Join(details, "、") + "；仅诊断，不构成买点"
	case strategy.ChanFail:
		if evidence.Reason == "" {
			return "未通过：旧路径结构不满足；仅诊断，不影响正式结论"
		}
		return "未通过：" + escape(evidence.Reason) + "；仅诊断，不影响正式结论"
	case strategy.ChanUnknown:
		if evidence.Reason == "" {
			return "未知：旧路径证据不足；仅诊断，不影响正式结论"
		}
		return "未知：" + escape(evidence.Reason) + "；仅诊断，不影响正式结论"
	default:
		return "未知：未检查；仅诊断，不影响正式结论"
	}
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
	section := notify.Section{Heading: heading}
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
		"chan_filtered":                   "缠论未通过",
		"chan_unknown":                    "缠论结构未知",
		"structure_guard_fail":            "结构门控不通过",
		"structure_guard_unknown":         "结构门控数据不足",
		"shadow_structure_not_validated":  "结构影子期·仅观察",
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
		"baseline":                              "股池基准",
		"breakout":                              "突破",
		"pullback":                              "回踩",
		"momentum":                              "动量",
		"volume_recovery":                       "放量恢复",
		"rising_ma20":                           "MA20↑",
		"stronger_than_pool":                    "近5日强于股池",
		"strong_close":                          "收盘偏强",
		"chan_filtered":                         "缠论未通过",
		"chan_unknown":                          "缠论结构未知",
		"structure_guard_fail":                  "结构门控不通过",
		"structure_guard_unknown":               "结构门控数据不足",
		"shadow_structure_not_validated":        "结构影子期·仅观察",
		"insufficient_or_unprofitable_evidence": "匹配规则未通过历史检验",
		"historical_net_loss_limit_breached":    "历史单笔净亏损超限",
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
