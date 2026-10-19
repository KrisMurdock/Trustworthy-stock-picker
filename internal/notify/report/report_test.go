package report

import (
	"encoding/json"
	"fmt"
	"stock-watcher/internal/config"
	"stock-watcher/internal/notify"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/strategy"
	"strings"
	"testing"
)

func TestShortMainCardStates(t *testing.T) {
	observed := shortCandidate("sz000001", "观察一", 1, 24, false)
	executable := shortCandidate("sz000002", "可执行一", 1, 88, true)
	executableWrapper := selection.Candidate{
		Code: executable.Code, Name: executable.Name, Ranking: &executable,
	}

	for _, test := range []struct {
		name        string
		enabled     bool
		ranking     []strategy.Candidate
		selected    []selection.Candidate
		wantTitle   string
		wantTpl     string
		wantLine    string
		wantDetails bool
	}{
		{
			name:        "disabled",
			enabled:     false,
			ranking:     []strategy.Candidate{observed},
			wantTitle:   "A股龙头池 · 10-19 短线机会 · 筛选未启用",
			wantTpl:     "orange",
			wantLine:    "筛选未启用；Top10 仅供观察",
			wantDetails: false,
		},
		{
			name:        "executable",
			enabled:     true,
			ranking:     []strategy.Candidate{executable},
			selected:    []selection.Candidate{executableWrapper},
			wantTitle:   "A股龙头池 · 10-19 短线机会 · 1 只可执行",
			wantTpl:     "green",
			wantLine:    "可执行 1 只；首选 sz000002 可执行一 · 参考买入 10.00 · 止损 9.70",
			wantDetails: true,
		},
		{
			name:        "observe",
			enabled:     true,
			ranking:     []strategy.Candidate{observed},
			wantTitle:   "A股龙头池 · 10-19 短线机会 · 仅观察",
			wantTpl:     "blue",
			wantLine:    "今日无可执行推荐；最高机会分 sz000001 观察一 24.0",
			wantDetails: true,
		},
		{
			name:        "no candidates",
			enabled:     true,
			wantTitle:   "A股龙头池 · 10-19 短线机会 · 无候选",
			wantTpl:     "grey",
			wantLine:    "今日无候选；有效行情 18/20",
			wantDetails: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := shortReport("short-state")
			r.SelectionEnabled = test.enabled
			r.Ranking = test.ranking
			r.Selected = test.selected
			cards, err := FromSelection(r)
			if err != nil {
				t.Fatal(err)
			}
			if cards.Main.Title != test.wantTitle || cards.Main.Template != test.wantTpl {
				t.Fatalf("main title/template = %q/%q, want %q/%q", cards.Main.Title, cards.Main.Template, test.wantTitle, test.wantTpl)
			}
			if len(cards.Main.Sections) != 1 {
				t.Fatalf("main card has %d sections, want exactly 1", len(cards.Main.Sections))
			}
			lines := cards.Main.Sections[0].Lines
			if len(lines) != 2 || lines[0] != test.wantLine {
				t.Fatalf("main lines = %#v, want conclusion %q followed by metadata", lines, test.wantLine)
			}
			if got, want := lines[1], "数据日 2026-10-16 · 目标日 2026-10-19 · 有效行情 18/20 · 数据排除 1"; got != want {
				t.Fatalf("metadata = %q, want %q", got, want)
			}
			if got := len(cards.Details) > 0; got != test.wantDetails {
				t.Fatalf("details generated = %t, want %t", got, test.wantDetails)
			}
		})
	}
}

func TestTrendMainCardStates(t *testing.T) {
	selected := []selection.Candidate{
		trendCandidate("sz000003", "低分", 70, -0.5, 8),
		trendCandidate("sz000002", "高分乙", 90, 0, 9),
		trendCandidate("sz000001", "高分甲", 90, 1.25, 10),
	}
	for _, test := range []struct {
		name        string
		enabled     bool
		selected    []selection.Candidate
		wantTitle   string
		wantTpl     string
		wantLine    string
		wantDetails int
	}{
		{
			name:        "disabled",
			enabled:     false,
			selected:    selected[:1],
			wantTitle:   "A股龙头池 · 10-19 T+1 筛选 · 筛选未启用",
			wantTpl:     "orange",
			wantLine:    "筛选未启用；本次不生成入选名单",
			wantDetails: 0,
		},
		{
			name:        "selected",
			enabled:     true,
			selected:    selected,
			wantTitle:   "A股龙头池 · 10-19 T+1 筛选 · 有入选 3 只",
			wantTpl:     "green",
			wantLine:    "入选 3 只；最高评分 sz000001 高分甲 90",
			wantDetails: 1,
		},
		{
			name:        "none",
			enabled:     true,
			wantTitle:   "A股龙头池 · 10-19 T+1 筛选 · 无入选",
			wantTpl:     "grey",
			wantLine:    "今日无入选；有效行情 18/20",
			wantDetails: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := trendReport("trend-state")
			r.SelectionEnabled = test.enabled
			r.Selected = test.selected
			cards, err := FromSelection(r)
			if err != nil {
				t.Fatal(err)
			}
			if cards.Main.Title != test.wantTitle || cards.Main.Template != test.wantTpl {
				t.Fatalf("main title/template = %q/%q, want %q/%q", cards.Main.Title, cards.Main.Template, test.wantTitle, test.wantTpl)
			}
			if len(cards.Main.Sections) != 1 || len(cards.Main.Sections[0].Lines) != 2 {
				t.Fatalf("unexpected main sections: %+v", cards.Main.Sections)
			}
			if got := cards.Main.Sections[0].Lines[0]; got != test.wantLine {
				t.Fatalf("conclusion = %q, want %q", got, test.wantLine)
			}
			if len(cards.Details) != test.wantDetails {
				t.Fatalf("details = %d, want %d", len(cards.Details), test.wantDetails)
			}
		})
	}
}

func TestCardsMetadataPartIdentityAndPagination(t *testing.T) {
	r := shortReport("event")
	for index := 1; index <= 9; index++ {
		r.Ranking = append(r.Ranking, shortCandidate(fmt.Sprintf("sz%06d", index), fmt.Sprintf("候选%d", index), index, float64(30-index), false))
	}
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.Details) != 2 {
		t.Fatalf("details = %d, want 2 pages for 9 candidates", len(cards.Details))
	}
	messages := append([]notify.Message{cards.Main}, cards.Details...)
	for _, message := range messages {
		if message.Version != 2 {
			t.Errorf("message %s version = %d, want 2", message.EventID, message.Version)
		}
		if message.TrimPolicy != notify.TrimNone {
			t.Errorf("message %s trim policy = %q", message.EventID, message.TrimPolicy)
		}
		if message.DisplayEventID != "event" {
			t.Errorf("message %s display event ID = %q", message.EventID, message.DisplayEventID)
		}
		if got := message.ExpiresAt.Format("2006-01-02 15:04"); got != "2026-10-19 09:30" {
			t.Errorf("message expiry = %s", got)
		}
	}
	if cards.Main.EventID != "event:main" || cards.Main.PartLabel != "主卡" {
		t.Fatalf("main identity = %q/%q", cards.Main.EventID, cards.Main.PartLabel)
	}
	if cards.Main.Template != "blue" {
		t.Fatalf("main template = %q, want blue", cards.Main.Template)
	}
	if got, want := cards.Details[0].EventID, "event:detail:01"; got != want {
		t.Fatalf("detail 1 event ID = %q, want %q", got, want)
	}
	if got, want := cards.Details[1].EventID, "event:detail:02"; got != want {
		t.Fatalf("detail 2 event ID = %q, want %q", got, want)
	}
	if cards.Details[0].PartLabel != "详情 1/2" || cards.Details[1].PartLabel != "详情 2/2" {
		t.Fatalf("detail labels = %q/%q", cards.Details[0].PartLabel, cards.Details[1].PartLabel)
	}
	if got, want := cards.Details[0].Title, "A股龙头池 · 10-19 短线机会 · 详情 1/2"; got != want {
		t.Fatalf("detail title = %q, want %q", got, want)
	}
	for _, detail := range cards.Details {
		if detail.Template != cards.Main.Template {
			t.Errorf("detail %s template = %q, want %q", detail.EventID, detail.Template, cards.Main.Template)
		}
		for _, section := range detail.Sections {
			if section.Optional {
				t.Errorf("detail %s section %q is optional", detail.EventID, section.Heading)
			}
		}
	}
}

func TestShortMainTableUsesStrictTop10AndPriorityRisk(t *testing.T) {
	r := shortReport("short-table")
	r.SelectionEnabled = true
	first := shortCandidate("sz000001", "首选*股", 1, 88.5, true)
	first.Plan.TakeProfit = 10.44
	first.Risks = []string{"upper_wick", "extended_price", "historical_net_loss_limit_breached", "cash_insufficient"}
	r.Ranking = append(r.Ranking, first)
	noPlan := shortCandidate("sz000002", "无计划", 2, 77, false)
	noPlan.Plan = nil
	r.Ranking = append(r.Ranking, noPlan)
	for index := 3; index <= 10; index++ {
		r.Ranking = append(r.Ranking, shortCandidate(fmt.Sprintf("sz%06d", index), fmt.Sprintf("候选%d", index), index, float64(100-index), false))
	}
	rank11 := shortCandidate("sz000011", "表外入选", 11, 50, true)
	r.Ranking = append(r.Ranking, rank11)
	r.Selected = []selection.Candidate{{Code: rank11.Code, Name: rank11.Name, Ranking: &rank11}}

	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	table := cards.Main.Sections[0].Table
	if table == nil || table.PageSize != 10 || table.RowHeight != "middle" || !table.FreezeFirstColumn {
		t.Fatalf("unexpected table settings: %+v", table)
	}
	wantColumns := []struct {
		key   string
		label string
		width string
	}{
		{"stock", "标的", "26%"}, {"status", "状态", "10%"}, {"score", "机会分", "10%"},
		{"entry", "参考买入", "12%"}, {"target", "目标价", "12%"}, {"stop", "止损", "12%"}, {"risk", "主要风险", "18%"},
	}
	if len(table.Columns) != len(wantColumns) {
		t.Fatalf("columns = %+v", table.Columns)
	}
	for index, want := range wantColumns {
		got := table.Columns[index]
		if got.Key != want.key || got.Label != want.label || got.Width != want.width {
			t.Errorf("column %d = %+v, want %+v", index, got, want)
		}
	}
	if len(table.Rows) != 10 {
		t.Fatalf("rows = %d, want strict top 10", len(table.Rows))
	}
	if got := cellText(table.Rows[0], 0); got != "1 · sz000001 首选＊股" {
		t.Errorf("first stock = %q", got)
	}
	if got := cellText(table.Rows[0], 1); got != "可执行" {
		t.Errorf("first status = %q", got)
	}
	if got := cellText(table.Rows[0], 2); got != "88.5" {
		t.Errorf("first score = %q", got)
	}
	if got := cellText(table.Rows[0], 3); got != "10.00" {
		t.Errorf("entry = %q", got)
	}
	if got := cellText(table.Rows[0], 4); got != "10.44" {
		t.Errorf("target = %q, want TakeProfit rather than Target30", got)
	}
	if got := cellText(table.Rows[0], 5); got != "9.70" {
		t.Errorf("stop = %q", got)
	}
	if got := cellText(table.Rows[0], 6); got != "历史单笔净亏损超限 +3" {
		t.Errorf("risk = %q", got)
	}
	if got := cellText(table.Rows[1], 3); got != "—" || cellText(table.Rows[1], 4) != "—" || cellText(table.Rows[1], 5) != "—" {
		t.Errorf("nil plan should render dashes: %+v", table.Rows[1])
	}
	if got := cellText(table.Rows[1], 1); got != "观察" {
		t.Errorf("non-recommended status = %q", got)
	}
	for _, row := range table.Rows {
		if strings.Contains(row[0].Text, "sz000011") {
			t.Fatal("selected candidate outside top 10 leaked into the main table")
		}
	}
}

func TestShortMainTableSortsByRank(t *testing.T) {
	r := shortReport("short-rank-order")
	r.SelectionEnabled = true
	r.Ranking = []strategy.Candidate{
		shortCandidate("sz000002", "第二名", 2, 80, false),
		shortCandidate("sz000001", "第一名", 1, 90, true),
	}
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	rows := cards.Main.Sections[0].Table.Rows
	if len(rows) != 2 || rows[0][0].Text != "1 · sz000001 第一名" || rows[1][0].Text != "2 · sz000002 第二名" {
		t.Fatalf("rows were not sorted by rank: %+v", rows)
	}
}

func TestTrendTableAndDetailsSortByScoreThenCode(t *testing.T) {
	r := trendReport("trend-table")
	r.SelectionEnabled = true
	r.Selected = []selection.Candidate{
		trendCandidate("sz000003", "低分", 70, -0.5, 8),
		trendCandidate("sz000002", "高分乙", 90, 0, 9),
		trendCandidate("sz000001", "高分甲", 90, 1.25, 10.23),
	}
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	table := cards.Main.Sections[0].Table
	if len(table.Columns) != 8 || len(table.Rows) != 3 {
		t.Fatalf("unexpected trend table: %+v", table)
	}
	wantLabels := []string{"标的", "涨幅", "收盘", "评分", "MA5/10/20", "RSI14", "量比", "连涨"}
	for index, want := range wantLabels {
		if got := table.Columns[index].Label; got != want {
			t.Errorf("column %d label = %q, want %q", index, got, want)
		}
	}
	wantCodes := []string{"sz000001", "sz000002", "sz000003"}
	for index, code := range wantCodes {
		wantPrefix := fmt.Sprintf("%d · %s ", index+1, code)
		if got := cellText(table.Rows[index], 0); !strings.HasPrefix(got, wantPrefix) {
			t.Errorf("row %d stock = %q, want prefix %q", index, got, wantPrefix)
		}
	}
	if got := cellText(table.Rows[0], 1); got != "+1.25%" || table.Rows[0][1].Tone != notify.TonePositive {
		t.Errorf("positive change = %q/%q", got, table.Rows[0][1].Tone)
	}
	if got := cellText(table.Rows[2], 1); got != "-0.50%" || table.Rows[2][1].Tone != notify.ToneNegative {
		t.Errorf("negative change = %q/%q", got, table.Rows[2][1].Tone)
	}
	if got := cellText(table.Rows[1], 1); got != "+0.00%" || table.Rows[1][1].Tone != notify.ToneDefault {
		t.Errorf("zero change = %q/%q", got, table.Rows[1][1].Tone)
	}
	if got := cellText(table.Rows[0], 2); got != "10.23" {
		t.Errorf("close = %q", got)
	}
	if got := cellText(table.Rows[0], 3); got != "90" {
		t.Errorf("score = %q", got)
	}
	if got := cellText(table.Rows[0], 4); got != "10.00 / 9.00 / 8.00" {
		t.Errorf("MA = %q", got)
	}
	if got := cellText(table.Rows[0], 5); got != "60.0" {
		t.Errorf("RSI = %q", got)
	}
	if got := cellText(table.Rows[0], 6); got != "1.50" {
		t.Errorf("volume = %q", got)
	}
	if got := cellText(table.Rows[0], 7); got != "3日" {
		t.Errorf("updays = %q", got)
	}

	sections := cards.Details[0].Sections
	if len(sections) != 4 || sections[0].Heading != "筛选口径" {
		t.Fatalf("unexpected detail sections: %+v", sections)
	}
	if !strings.HasPrefix(sections[1].Heading, "入选 1 · sz000001 ") || !strings.HasPrefix(sections[2].Heading, "入选 2 · sz000002 ") {
		t.Fatalf("detail order = %q, %q", sections[1].Heading, sections[2].Heading)
	}
	if got := fieldValue(t, sections[1], "涨幅"); got != "+1.25%" {
		t.Errorf("detail change = %q", got)
	}
	if got := fieldValue(t, sections[1], "收盘"); got != "10.23" {
		t.Errorf("detail close = %q", got)
	}
}

func TestShortDetailsPaginateAndAppendSelectedOutsideTop10(t *testing.T) {
	r := shortReport("pages")
	r.SelectionEnabled = true
	for index := 1; index <= 12; index++ {
		r.Ranking = append(r.Ranking, shortCandidate(fmt.Sprintf("sz%06d", index), fmt.Sprintf("候选%d", index), index, float64(100-index), index == 1))
	}
	rank11 := shortCandidate("sz000011", "补充候选", 11, 50, true)
	rank11.Risks = []string{"upper_wick", "extended_price", "historical_net_loss_limit_breached", "cash_insufficient"}
	top1 := r.Ranking[0]
	r.Selected = []selection.Candidate{
		{Code: top1.Code, Name: top1.Name, Ranking: &top1},
		{Code: rank11.Code, Name: rank11.Name, Ranking: &rank11},
	}
	for index := 1; index <= 12; index++ {
		r.Excluded = append(r.Excluded, selection.Excluded{Code: fmt.Sprintf("ex%02d", index), Reason: "missing_data"})
	}
	r.Filtered = []selection.Excluded{{Code: "flt01", Reason: "score_below_threshold"}}

	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.Details) != 2 {
		t.Fatalf("details = %d, want 2 pages", len(cards.Details))
	}
	first, second := cards.Details[0].Sections, cards.Details[1].Sections
	if len(first) != 10 || first[0].Heading != "研究与验证" || first[1].Heading != "参数口径" {
		t.Fatalf("unexpected first page sections: %+v", headings(first))
	}
	if got := detailCandidateSections(first); len(got) != 8 {
		t.Fatalf("first page candidates = %d, want 8", len(got))
	}
	if got := detailCandidateSections(second); len(got) != 3 {
		t.Fatalf("second page candidates = %d, want 3", len(got))
	}
	if got := headings(second); strings.Join(got, "|") != "第9名 · sz000009 候选9 · 观察|第10名 · sz000010 候选10 · 观察|推荐补充 · 第11名 · sz000011 补充候选 · 可执行|数据排除|规则未通过" {
		t.Fatalf("second page headings = %#v", got)
	}
	if len(second[len(second)-2].Fields) != 10 {
		t.Fatalf("data exclusion fields = %d, want capped at 10", len(second[len(second)-2].Fields))
	}
	for _, detail := range cards.Details {
		for _, section := range detail.Sections {
			if section.Optional {
				t.Errorf("section %q should not be optional", section.Heading)
			}
		}
	}
	for _, section := range first {
		if strings.Contains(section.Heading, "sz000012") {
			t.Fatal("rank 12 should not be included in the strict top-10 details")
		}
	}

	supplement := detailSectionByCode(t, cards, "sz000011")
	wantFields := []string{"机会分", "历史证据", "依据", "规则"}
	if len(supplement.Fields) != len(wantFields) {
		t.Fatalf("supplement fields = %+v", supplement.Fields)
	}
	for index, label := range wantFields {
		if supplement.Fields[index].Label != label {
			t.Errorf("supplement field %d = %q, want %q", index, supplement.Fields[index].Label, label)
		}
	}
	for _, forbidden := range []string{"参考买入", "目标价", "参考止损", "净+2.5%价", "净+3%价"} {
		if sectionHasField(supplement, forbidden) {
			t.Errorf("candidate detail repeats price field %q", forbidden)
		}
	}
	if !lineHasPrefix(supplement.Lines, "缠论门控：") || !lineHasPrefix(supplement.Lines, "评分推导：") {
		t.Errorf("candidate detail is missing Chan/score lines: %#v", supplement.Lines)
	}
	riskLine := lineWithPrefix(supplement.Lines, "风险：")
	for _, risk := range []string{"上影线较长", "涨幅/偏离过大", "历史单笔净亏损超限", "预算不足最小买入量"} {
		if !strings.Contains(riskLine, risk) {
			t.Errorf("full risk line %q missing %q", riskLine, risk)
		}
	}
}

func TestNoCandidateOrDisabledShortDoesNotSendDetails(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
		ranking []strategy.Candidate
	}{
		{name: "enabled without candidates", enabled: true},
		{name: "disabled with ranking", enabled: false, ranking: []strategy.Candidate{shortCandidate("sz000001", "观察", 1, 20, false)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := shortReport("no-details")
			r.SelectionEnabled = test.enabled
			r.Ranking = test.ranking
			cards, err := FromSelection(r)
			if err != nil {
				t.Fatal(err)
			}
			if len(cards.Details) != 0 {
				t.Fatalf("details = %d, want none", len(cards.Details))
			}
		})
	}

	trend := trendReport("trend-disabled")
	trend.Selected = []selection.Candidate{trendCandidate("sz000001", "入选", 80, 1, 10)}
	trend.SelectionEnabled = false
	cards, err := FromSelection(trend)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.Details) != 0 {
		t.Fatalf("disabled trend details = %d, want none", len(cards.Details))
	}
}

func TestEnabledTrendWithoutSelectionStillSendsDetailPage(t *testing.T) {
	r := trendReport("trend-empty")
	r.SelectionEnabled = true
	r.Excluded = []selection.Excluded{{Code: "sh600000", Reason: "special_treatment"}}
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.Details) != 1 {
		t.Fatalf("details = %d, want one detail page", len(cards.Details))
	}
	if got := headings(cards.Details[0].Sections); strings.Join(got, "|") != "筛选口径|数据排除" {
		t.Fatalf("detail headings = %#v", got)
	}
	if got := fieldValue(t, cards.Details[0].Sections[1], "sh600000"); got != "ST/退市" {
		t.Errorf("exclusion = %q", got)
	}
}

func TestFailedRuleMessageShowsMeasuredReasons(t *testing.T) {
	p := config.DefaultStrategy()
	r := selection.Report{Version: 2, ID: "failed-rule", AsOf: "2026-10-16", Target: "2026-10-19", SelectionEnabled: true,
		Ranking: []strategy.Candidate{{Code: "sh600900", Name: "测试候选", Rank: 1, Rules: []string{"baseline", "momentum"},
			Risks: []string{"historical_net_loss_limit_breached", "insufficient_or_unprofitable_evidence", "holdout_validation_not_passed"}}},
		Research: &strategy.Research{Parameters: p, Rules: []strategy.Stats{
			{Rule: "baseline", Target: 3, Horizon: 5, HitRate: .458},
			{Rule: "momentum", Target: 3, Horizon: 5, Samples: 1095, Dates: 154, Resolved: 1041, Unresolved: 54, MeanNet: .36, HitRate: .474, WorstNet: -15.3766, LossBreaches: 209, LossLimitPct: 3},
		}, Holdout: strategy.Evaluation{Method: "frozen_holdout", SignalDates: 40}}}
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cards)
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
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	risk := lineWithPrefix(detailSectionByCode(t, cards, "sh600276").Lines, "风险：")
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
			cards, err := FromSelection(selection.Report{
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
			section := detailSectionByCode(t, cards, "sh600900")
			chanLine := lineWithPrefix(section.Lines, "缠论门控：")
			if !strings.Contains(chanLine, test.want) {
				t.Errorf("Chan gate = %q, want contains %q", chanLine, test.want)
			}
			scoreLine := lineWithPrefix(section.Lines, "评分推导：")
			if !strings.Contains(scoreLine, "48.0×0.5 = 24.0") {
				t.Errorf("score derivation = %q", scoreLine)
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

func TestSelectionMessageRejectsInvalidTarget(t *testing.T) {
	if _, err := FromSelection(selection.Report{ID: "event", Target: "not-a-date"}); err == nil {
		t.Fatal("invalid target date was accepted")
	}
}

func shortReport(id string) selection.Report {
	return selection.Report{
		Version: 2, ID: id, AsOf: "2026-10-16", Target: "2026-10-19",
		Strategy: "short_horizon_v1", SelectionEnabled: true,
		PoolSize: 20, Eligible: 18,
		Excluded: []selection.Excluded{{Code: "sz000099", Reason: "missing_data"}},
		Research: &strategy.Research{
			Parameters:      config.DefaultStrategy(),
			TrainingThrough: "2026-09-30",
			SampleCount:     500,
		},
	}
}

func trendReport(id string) selection.Report {
	return selection.Report{
		Version: 1, ID: id, AsOf: "2026-10-16", Target: "2026-10-19",
		Strategy: "leader_daily_v1", SelectionEnabled: true,
		MinScore: 70, TopN: 5, PoolSize: 20, Eligible: 18,
		Rules: []string{"收盘价 > MA20 且 MA5 > MA10 > MA20", "量比 ≥ 1.20 且 MACD DIF > DEA"},
	}
}

func shortCandidate(code, name string, rank int, score float64, recommended bool) strategy.Candidate {
	return strategy.Candidate{
		Code: code, Name: name, Rank: rank, Score: score, Recommended: recommended,
		Reasons: []string{"historical_rule_breakout", "rising_ma20"},
		Rules:   []string{"baseline", "momentum"},
		Risks:   []string{"extended_price"},
		Evidence: &strategy.Stats{
			Samples: 60, Dates: 20, HitRate: .5, LowerBound: .3, MeanNet: 1.25,
		},
		Plan: &strategy.Plan{EntryPrice: 10, Target25: 10.3, Target30: 10.35, TakeProfit: 10.44, StopPrice: 9.7, ReferenceOnly: true},
	}
}

func trendCandidate(code, name string, score int, change, close float64) selection.Candidate {
	return selection.Candidate{
		Code: code, Name: name, Close: close, ChangePct: change,
		Metrics: selection.Metrics{
			Score: score, MA5: 10, MA10: 9, MA20: 8, RSI14: 60, VolumeRatio: 1.5, UpDays: 3,
		},
	}
}

func cellText(row []notify.TableCell, column int) string {
	if column >= len(row) {
		return ""
	}
	return row[column].Text
}

func headings(sections []notify.Section) []string {
	out := make([]string, len(sections))
	for index, section := range sections {
		out[index] = section.Heading
	}
	return out
}

func detailCandidateSections(sections []notify.Section) []notify.Section {
	out := []notify.Section{}
	for _, section := range sections {
		if strings.HasPrefix(section.Heading, "第") || strings.HasPrefix(section.Heading, "推荐补充 · 第") || strings.HasPrefix(section.Heading, "入选 ") {
			out = append(out, section)
		}
	}
	return out
}

func detailSectionByCode(t *testing.T, cards Cards, code string) notify.Section {
	t.Helper()
	for _, detail := range cards.Details {
		for _, section := range detail.Sections {
			if strings.Contains(section.Heading, " · "+code+" ") {
				return section
			}
		}
	}
	t.Fatalf("no detail section for %s", code)
	return notify.Section{}
}

func sectionHasField(section notify.Section, label string) bool {
	for _, field := range section.Fields {
		if field.Label == label {
			return true
		}
	}
	return false
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

func lineWithPrefix(lines []string, prefix string) string {
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

func lineHasPrefix(lines []string, prefix string) bool {
	return lineWithPrefix(lines, prefix) != ""
}
