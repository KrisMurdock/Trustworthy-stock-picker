package report

import (
	"encoding/json"
	"stock-watcher/internal/config"
	"stock-watcher/internal/notify"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/strategy"
	"strings"
	"testing"
)

func TestFailedRuleMessageShowsMeasuredReasons(t *testing.T) {
	p := config.DefaultStrategy()
	r := selection.Report{Version: 2, ID: "failed-rule", AsOf: "2026-10-16", Target: "2026-10-19", SelectionEnabled: true,
		Ranking: []strategy.Candidate{{Code: "sh600900", Name: "测试候选", Rank: 1, Rules: []string{"baseline", "momentum"},
			Risks: []string{"historical_net_loss_limit_breached", "insufficient_or_unprofitable_evidence", "holdout_validation_not_passed"}}},
		Research: &strategy.Research{Parameters: p, Rules: []strategy.Stats{
			{Rule: "baseline", Target: 3, Horizon: 5, HitRate: .458},
			{Rule: "momentum", Target: 3, Horizon: 5, Samples: 1095, Dates: 154, Resolved: 1041, Unresolved: 54, MeanNet: .36, HitRate: .474, WorstNet: -15.3766, LossBreaches: 209, LossLimitPct: 3},
		}, Holdout: strategy.Evaluation{Method: "frozen_holdout", SignalDates: 40}}}
	m, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"历史单笔净亏损超限", "趋势动量", "209", "-15.38%", "无合格推荐样本", "暂无可统计收益"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing diagnostic %q", want)
		}
	}
	for _, bad := range []string{"historical net loss limit breached", "历史证据不足", "净目标达标0.0%"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("misleading diagnostic %q", bad)
		}
	}
}

func TestNoSignalIsNotPresentedAsInsufficientHistory(t *testing.T) {
	r := selection.Report{Version: 2, ID: "no-signal", AsOf: "2026-10-16", Target: "2026-10-19", SelectionEnabled: true,
		Ranking:  []strategy.Candidate{{Code: "sh600276", Name: "测试候选", Rank: 1, Rules: []string{"baseline"}, Risks: []string{"insufficient_or_unprofitable_evidence"}}},
		Research: &strategy.Research{Parameters: config.DefaultStrategy()}}
	m, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	risk := fieldValue(t, candidateSection(t, m, "sh600276"), "风险")
	if !strings.Contains(risk, "未触发买入规则") || strings.Contains(risk, "历史证据不足") {
		t.Fatalf("no signal mislabeled: %s", risk)
	}
}

func TestShortCandidateExplainsChanGateAndScoreDerivation(t *testing.T) {
	for _, test := range []struct {
		name     string
		evidence *strategy.ChanEvidence
		want     string
	}{
		{name: "passed", evidence: &strategy.ChanEvidence{Status: strategy.ChanPass}, want: "未被过滤"},
		{name: "unknown", evidence: &strategy.ChanEvidence{Status: strategy.ChanUnknown, Reason: "30m 尚无完成笔"}, want: "仅观察"},
		{name: "failed", evidence: &strategy.ChanEvidence{Status: strategy.ChanFail, Reason: "15m 最近分型不是底分型"}, want: "15m 最近分型不是底分型"},
	} {
		t.Run(test.name, func(t *testing.T) {
			message, err := FromSelection(selection.Report{
				Version: 2, ID: "rationale", AsOf: "2026-10-16", Target: "2026-10-19", SelectionEnabled: true,
				Ranking: []strategy.Candidate{{
					Code: "sh600900", Name: "测试候选", Rank: 1, Score: 24,
					ScoreBreakdown: &strategy.ScoreBreakdown{Formula: "25 + 13(日线52×0.25) = 48.0；无合格历史证据：48.0×0.5 = 24.0"},
					Chan:           test.evidence,
					Rules:          []string{"baseline", "momentum"},
					Reasons:        []string{"rising_ma20"},
					Risks:          []string{"chan_unknown"},
				}},
				Research: &strategy.Research{Parameters: config.DefaultStrategy()},
			})
			if err != nil {
				t.Fatal(err)
			}
			section := candidateSection(t, message, "sh600900")
			if got := fieldValue(t, section, "缠论门控"); !strings.Contains(got, test.want) {
				t.Errorf("Chan gate = %q, want contains %q", got, test.want)
			}
			if got := fieldValue(t, section, "评分推导"); !strings.Contains(got, "48.0×0.5 = 24.0") {
				t.Errorf("score derivation = %q", got)
			}
			if got := fieldValue(t, section, "规则"); !strings.Contains(got, "股池基准") || !strings.Contains(got, "动量") {
				t.Errorf("matched rules = %q", got)
			}
		})
	}
}

func TestChanSummaryNamesPassingLevelEvidence(t *testing.T) {
	summary := chanSummary(strategy.ChanEvidence{Status: strategy.ChanPass, Levels: []strategy.ChanLevelEvidence{
		{Period: 30, Status: strategy.ChanPass, LastStroke: "up"},
		{Period: 15, Status: strategy.ChanPass, LastFractal: "bottom"},
		{Period: 5, Status: strategy.ChanPass, LastFractal: "bottom"},
	}})
	for _, want := range []string{"30m最近完成笔向上", "15m底分型收复", "5m底分型收复", "未被过滤"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary %q missing %q", summary, want)
		}
	}
}

func TestTrendMessageUsesStructuredOverviewAndCandidateFields(t *testing.T) {
	message, err := FromSelection(selection.Report{
		Version:          1,
		ID:               "event-1",
		AsOf:             "2026-10-13",
		Target:           "2026-10-14",
		Strategy:         "leader_daily_v1",
		SelectionEnabled: true,
		MinScore:         70,
		TopN:             5,
		PoolSize:         3,
		Eligible:         2,
		Passed:           1,
		Rules: []string{
			"收盘价 > MA20 且 MA5 > MA10 > MA20",
			"量比 ≥ 1.20 且 MACD DIF > DEA",
			"RSI14 50～80，日涨跌幅 -3.0%～7.0%，排除 ST / 停牌",
		},
		Selected: []selection.Candidate{{
			Code:      "sz000001",
			Name:      "测试*股票",
			Close:     10.23,
			ChangePct: 1.25,
			Metrics: selection.Metrics{
				Score: 80, MA5: 10, MA10: 9, MA20: 8, RSI14: 60, VolumeRatio: 1.5, UpDays: 3,
			},
		}},
		Excluded: []selection.Excluded{{Code: "sh600000", Reason: "special_treatment"}},
		Filtered: []selection.Excluded{{Code: "sz000002", Reason: "score_below_threshold"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if message.Version != 2 || message.EventID != "event-1" || message.Title != "A股龙头池 · T+1 日线筛选" {
		t.Fatalf("unexpected message metadata: %+v", message)
	}

	overview := sectionByHeading(t, message, "概览")
	if overview.Optional {
		t.Fatal("overview must remain when optional details are trimmed")
	}
	for label, want := range map[string]string{
		"数据日":  "2026-10-13",
		"目标日":  "2026-10-14",
		"股池":   "3",
		"有效行情": "2",
		"规则通过": "1",
		"数据排除": "1",
		"筛选状态": "已启用·有入选",
	} {
		if got := fieldValue(t, overview, label); got != want {
			t.Errorf("overview %q = %q, want %q", label, got, want)
		}
	}

	criteria := sectionByHeading(t, message, "筛选口径")
	if criteria.Optional {
		t.Fatal("screening rules are part of the core message")
	}
	if got := fieldValue(t, criteria, "评分门槛"); got != "≥70" {
		t.Errorf("score threshold = %q, want ≥70", got)
	}
	if got := fieldValue(t, criteria, "候选上限"); got != "5" {
		t.Errorf("candidate limit = %q, want 5", got)
	}
	if got := fieldValue(t, criteria, "规则1"); !strings.Contains(got, "收盘>MA20") || strings.Contains(got, "且") {
		t.Errorf("trend rule was not compacted: %q", got)
	}

	candidate := candidateSection(t, message, "sz000001")
	if !candidate.Optional {
		t.Fatal("individual stock details should be trimmable")
	}
	for label, want := range map[string]string{
		"股票":        "sz000001 测试＊股票",
		"涨幅":        "+1.25%",
		"收盘":        "10.23",
		"评分":        "80",
		"MA5/10/20": "10.00 / 9.00 / 8.00",
		"RSI14":     "60.0",
		"量比":        "1.50",
		"连涨":        "3日",
	} {
		if got := fieldValue(t, candidate, label); got != want {
			t.Errorf("candidate %q = %q, want %q", label, got, want)
		}
	}
	if got := fieldValue(t, candidate, "涨幅"); !strings.Contains(got, "+") {
		t.Errorf("positive change should show its sign, got %q", got)
	}

	dataExclusions := sectionByHeading(t, message, "数据排除")
	if !dataExclusions.Optional || fieldValue(t, dataExclusions, "sh600000") != "ST/退市" {
		t.Errorf("data exclusion detail should be optional and compact: %+v", dataExclusions)
	}
	ruleExclusions := sectionByHeading(t, message, "规则未通过")
	if !ruleExclusions.Optional || fieldValue(t, ruleExclusions, "sz000002") != "评分不足" {
		t.Errorf("rule exclusion detail should be optional and compact: %+v", ruleExclusions)
	}
	if hasField(message, "事件 ID") {
		t.Fatal("event ID belongs to the card renderer, not report fields")
	}
}

func TestShortHorizonMessageUsesStructuredResearchAndRanksSelectedFirst(t *testing.T) {
	parameters := config.DefaultStrategy()
	selected := shortCandidate("sz000005", "优先入选", 5, 92, true)
	ranking := []strategy.Candidate{
		shortCandidate("sz000001", "观察一", 1, 98, false),
		shortCandidate("sz000002", "观察二", 2, 97, false),
		shortCandidate("sz000003", "观察三", 3, 96, false),
		shortCandidate("sz000004", "观察四", 4, 95, false),
		selected,
		shortCandidate("sz000006", "观察六", 6, 90, false),
	}
	message, err := FromSelection(selection.Report{
		Version:          2,
		ID:               "short-event",
		AsOf:             "2026-10-13",
		Target:           "2026-10-14",
		Strategy:         "short_horizon_v1",
		SelectionEnabled: true,
		PoolSize:         20,
		Eligible:         18,
		Passed:           1,
		Excluded:         []selection.Excluded{{Code: "sz000020", Reason: "missing_data"}},
		Selected: []selection.Candidate{{
			Code: "sz000005", Name: "优先入选", Ranking: &selected,
		}},
		Ranking: ranking,
		Research: &strategy.Research{
			Version:         1,
			Strategy:        "short_horizon_v1",
			AsOf:            "2026-10-13",
			TrainingThrough: "2026-09-30",
			Parameters:      parameters,
			SampleCount:     500,
			Development: strategy.Evaluation{
				Method: "rolling_validation", From: "2026-01-01", Through: "2026-06-30", SignalDates: 35,
				Recommended: strategy.Stats{Samples: 100, Resolved: 100, Dates: 25, HitRate: .42, LowerBound: .31, MeanNet: 1.2},
			},
			Holdout: strategy.Evaluation{
				Method: "frozen_holdout", From: "2026-08-01", Through: "2026-09-30", SignalDates: 20,
				Recommended: strategy.Stats{Samples: 60, Resolved: 60, Dates: 15, HitRate: .4, LowerBound: .28, MeanNet: 1.5},
			},
			ValidationPassed: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if message.Version != 2 || message.EventID != "short-event" {
		t.Fatalf("unexpected short message metadata: %+v", message)
	}

	overview := sectionByHeading(t, message, "概览")
	for label, want := range map[string]string{
		"数据日":  "2026-10-13",
		"目标日":  "2026-10-14",
		"股池":   "20",
		"有效行情": "18",
		"规则通过": "1",
		"数据排除": "1",
		"筛选状态": "已启用·有入选",
	} {
		if got := fieldValue(t, overview, label); got != want {
			t.Errorf("short overview %q = %q, want %q", label, got, want)
		}
	}

	research := sectionByHeading(t, message, "研究与验证")
	for label, want := range map[string]string{
		"训练截至":   "2026-09-30",
		"研究样本":   "500",
		"开发区间":   "2026-01-01～2026-06-30 · 35信号日",
		"开发表现":   "100样本/25日 · 净目标达标42.0% · 已退出均值+1.20%",
		"独立验证区间": "2026-08-01～2026-09-30 · 20信号日",
		"独立验证表现": "60样本/15日 · 净目标达标40.0% · 已退出均值+1.50%",
		"独立验证结论": "通过",
	} {
		if got := fieldValue(t, research, label); got != want {
			t.Errorf("research %q = %q, want %q", label, got, want)
		}
	}

	parametersSection := sectionByHeading(t, message, "参数口径")
	for label, want := range map[string]string{
		"净目标/止损": "净+3.0% / -3.0%",
		"持有期限":   "最多5交易日",
		"单股预算":   "20,000元",
		"佣金":     "0.0100% / 最低5.00元",
		"单边滑点":   "0.0500%",
	} {
		if got := fieldValue(t, parametersSection, label); got != want {
			t.Errorf("parameter %q = %q, want %q", label, got, want)
		}
	}

	candidates := candidateSections(message)
	if len(candidates) != 5 {
		t.Fatalf("got %d displayed candidates, want the 5-item cap", len(candidates))
	}
	if got := fieldValue(t, candidates[0], "股票"); got != "sz000005 优先入选" {
		t.Fatalf("selected candidate was not prioritized: first stock = %q", got)
	}
	if got := fieldValue(t, candidates[1], "股票"); got != "sz000001 观察一" {
		t.Fatalf("rank order after selected candidate changed: second stock = %q", got)
	}
	if got := fieldValue(t, candidates[0], "状态"); got != "通过" {
		t.Errorf("selected candidate state = %q, want 通过", got)
	}
	if got := fieldValue(t, candidates[1], "状态"); got != "观察" {
		t.Errorf("unselected candidate state = %q, want 观察", got)
	}
	for _, candidate := range candidates {
		if !candidate.Optional {
			t.Errorf("candidate detail should be optional: %+v", candidate)
		}
	}
	first := candidates[0]
	for label, want := range map[string]string{
		"机会分":     "92.0",
		"依据":      "历史突破 / MA20↑",
		"风险":      "涨幅/偏离过大",
		"历史证据":    "60样本/20日 · 净目标达标50.0% · 已退出均值+1.25% · 下界30.0%",
		"参考买入":    "10.00",
		"净+2.5%价": "10.30",
		"净+3%价":   "10.35",
		"参考止损":    "9.70",
	} {
		if got := fieldValue(t, first, label); got != want {
			t.Errorf("short candidate %q = %q, want %q", label, got, want)
		}
	}
	if hasField(message, "事件 ID") {
		t.Fatal("event ID belongs to the card renderer, not report fields")
	}
	if !containsLine(message, "历史统计/机会分不代表未来获利概率") || !containsLine(message, "买入当日不可卖") {
		t.Fatal("short message must preserve compact research and execution limitations")
	}
}

func TestSelectionStateAndOptionalExclusionDetails(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
		want    string
	}{
		{name: "disabled", enabled: false, want: "未启用"},
		{name: "enabled with no passing stocks", enabled: true, want: "已启用·无入选"},
	} {
		t.Run(test.name, func(t *testing.T) {
			message, err := FromSelection(selection.Report{
				ID: "event", AsOf: "2026-10-13", Target: "2026-10-14", SelectionEnabled: test.enabled,
				PoolSize: 1, Eligible: 0, Excluded: []selection.Excluded{{Code: "sz000001", Reason: "missing_data"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := fieldValue(t, sectionByHeading(t, message, "概览"), "筛选状态"); got != test.want {
				t.Errorf("selection state = %q, want %q", got, test.want)
			}
			exclusions := sectionByHeading(t, message, "数据排除")
			if !exclusions.Optional {
				t.Error("exclusion details should be optional")
			}
		})
	}
}

func TestSelectionMessageHasExpiry(t *testing.T) {
	message, err := FromSelection(selection.Report{ID: "event", AsOf: "2026-10-13", Target: "2026-10-14"})
	if err != nil {
		t.Fatal(err)
	}
	if message.ExpiresAt.IsZero() || message.ExpiresAt.Location() == nil {
		t.Fatal("message expiry is missing")
	}
	if message.ExpiresAt.Format("2006-01-02 15:04") != "2026-10-14 09:30" {
		t.Fatalf("unexpected expiry %s", message.ExpiresAt)
	}
}

func shortCandidate(code, name string, rank int, score float64, recommended bool) strategy.Candidate {
	return strategy.Candidate{
		Code: code, Name: name, Rank: rank, Score: score, Recommended: recommended,
		Reasons: []string{"historical_rule_breakout", "rising_ma20"},
		Risks:   []string{"extended_price"},
		Evidence: &strategy.Stats{
			Samples: 60, Dates: 20, HitRate: .5, LowerBound: .3, MeanNet: 1.25,
		},
		Plan: &strategy.Plan{EntryPrice: 10, Target25: 10.3, Target30: 10.35, StopPrice: 9.7, ReferenceOnly: true},
	}
}

func sectionByHeading(t *testing.T, message notify.Message, heading string) notify.Section {
	t.Helper()
	for _, section := range message.Sections {
		if section.Heading == heading {
			return section
		}
	}
	t.Fatalf("message has no section %q", heading)
	return notify.Section{}
}

func candidateSection(t *testing.T, message notify.Message, code string) notify.Section {
	t.Helper()
	for _, section := range message.Sections {
		for _, field := range section.Fields {
			if field.Label == "股票" && strings.HasPrefix(field.Value, code+" ") {
				return section
			}
		}
	}
	t.Fatalf("message has no candidate section for %s", code)
	return notify.Section{}
}

func candidateSections(message notify.Message) []notify.Section {
	out := []notify.Section{}
	for _, section := range message.Sections {
		for _, field := range section.Fields {
			if field.Label == "股票" {
				out = append(out, section)
				break
			}
		}
	}
	return out
}

func fieldValue(t *testing.T, section notify.Section, label string) string {
	t.Helper()
	for _, field := range section.Fields {
		if field.Label == label {
			return field.Value
		}
	}
	t.Fatalf("section %q has no field %q: %+v", section.Heading, label, section.Fields)
	return ""
}

func hasField(message notify.Message, label string) bool {
	for _, section := range message.Sections {
		for _, field := range section.Fields {
			if field.Label == label {
				return true
			}
		}
	}
	return false
}

func containsLine(message notify.Message, value string) bool {
	for _, section := range message.Sections {
		for _, line := range section.Lines {
			if strings.Contains(line, value) {
				return true
			}
		}
	}
	return false
}
