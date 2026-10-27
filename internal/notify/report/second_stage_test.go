package report

import (
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/indicators"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/wechat"
)

func TestSecondStageArticleUsesReferenceStyleAndPriceAnnotations(t *testing.T) {
	r := articleTestReport(6)
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.SecondStage) != 1 {
		t.Fatalf("second-stage articles = %d, want 1", len(cards.SecondStage))
	}
	article := cards.SecondStage[0]
	if article.EventID != "report-id:second-stage:article" || article.Article == nil {
		t.Fatalf("invalid second-stage article: %+v", article)
	}
	if article.Title != "每日精选｜10月22日 三日短线观察" || article.Article.Title != article.Title {
		t.Fatalf("unexpected title: %q / %+v", article.Title, article.Article)
	}
	if article.Article.Digest == "" || article.Article.Body == "" || article.PlainText == "" {
		t.Fatalf("missing article content: %+v", article.Article)
	}
	body := article.Article.Body
	for _, want := range []string{
		"# 10月22日 三日短线观察：排名靠前，但暂未形成正式推荐",
		"## 一、排名靠前的观察对象",
		":::card blue",
		"### 1. YLWL（sz300628）",
		"**精排第1名 · 观察第1名 · 测试行业**",
		"#### 价格与明日区间",
		"- **推荐买区**：15.00~15.30 购入",
		"- **明日高点**：16.20；区间 15.90~16.50（名义覆盖 80%）",
		"- **明日低点**：14.70；区间 14.40~15.00（名义覆盖 80%）",
		"- **信号日区间**：14.80~15.50（10月21日实际）",
		"#### 技术指标",
		"**均线**：MA5 15.20 / MA10 15.05 / MA20 14.90；多头排列",
		"**RSI14**：61.4（前值 59.4，+2.0；偏强）",
		"**MACD**：DIF 0.120 / DEA 0.080 / 柱 0.080；红柱扩张",
		"**BOLL**：上 15.80 / 中 14.90 / 下 14.00；带宽 12.1%；%B 0.89（上轨附近）",
		"**波动与量能**：ATR14 2.83%；量比 1.32；MA20 斜率 +1.20%；距 MA20 +3.10%",
		"#### 阶段判断（日线）",
		"**当前阶段**：上涨（72/100，中；规则定向+模型微调）",
		"**推理链**：价格位置",
		"**模型证据**：树模型：预测未来3日最高涨幅 6.82%",
		"**失效条件**：收盘跌破 14.70 后，当前上涨判断失效",
		"**边界**：规则化日线阶段分类，不是趋势确认，也不构成买卖建议",
		"## 阶段判断链路",
		"- **初筛**：股池 193 只，日线可用 191 只，进入阶段分析 6 只。",
		"- **精排**：树模型覆盖 6/6，JEV 覆盖 0/6",
		"- **验证**：规则阶段 walk-forward，指标见下方。",
		"### 验证指标（规则阶段）",
		"- **状态**：本次报告未保存验证结果。",
		"- **置信度**：趋势基础分为 48+6×|方向票|+5×强度",
		"- **边界**：阶段由日线规则定向；全池 walk-forward 验证的是规则阶段，特征使用当前前复权快照，股票共享信号日且非独立留出集，不代表收益或成功率。",
		"#### 缠论结构",
		"**日线**：通过；48根K线/42根合并/5个分型/5笔",
		"**30分钟**：通过；96根K线/80根合并/4个分型/4笔",
		"**5分钟**：通过；240根K线/201根合并/8个分型/5笔",
		"笔重叠区 14.70~15.20（中轴 14.95）",
		"#### 波浪视角（简化）",
		"**阶段**：上升趋势中的调整（简化）",
		"**回撤观察**：最近上行段回撤 38.2%",
		"**关键价位**：支撑 15.00 / 失效位 14.70 / 上方参照 16.20",
		"不是严格艾略特浪级计数",
		"#### 观察结论",
		"- **机会分 / 延续分**：49.4 / 39.7",
		"- **主要依据**：MA20向上、近5日强于样本池、收盘偏强",
		"- **触发规则**：样本池基准、突破、动量、放量恢复",
		":::card amber",
		":::card grey",
		"## 二、其他进入观察排名的对象",
		"### 6. HXL（sz000006）",
		"明日高低预测 暂缺（dayrange 模型未生成目标日预测）",
		"**阶段**：上涨（72/100，中）；失效位 14.70",
		"## 三、今天的整体结论",
		"> 强势观察池，而不是正式推荐池。",
		"ATR14 的 0.35 倍",
		"不是严格艾略特浪级计数",
		"不构成任何证券买卖建议",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("article missing %q:\n%s", want, body)
		}
	}
	if got, want := strings.Count(body, ":::card "), 6; got != want {
		t.Fatalf("stock cards = %d, want %d:\n%s", got, want, body)
	}
	if got, want := strings.Count(body, "\n:::\n"), 6; got != want {
		t.Fatalf("closed stock cards = %d, want %d:\n%s", got, want, body)
	}
	for _, unwanted := range []string{"jev_missing", "historical_net_loss_limit_breached", "【公众号", "亿联网络", "金山办公", "海螺水泥", "长江电力", "陕西煤业", "候选六"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("article leaked %q:\n%s", unwanted, body)
		}
	}
	expires := time.Date(2026, 10, 22, 9, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	if !article.ExpiresAt.Equal(expires) {
		t.Fatalf("expiry = %s, want %s", article.ExpiresAt, expires)
	}
}

func TestSecondStageCompactArticleKeepsPhasePipelineAndValidationBoundaries(t *testing.T) {
	r := articleTestReport(10)
	r.PhaseValidation = &strategy.PhaseValidation{
		Status: "ok", HorizonDays: 5, Predicted: 9650, Dates: 50, Stocks: 191, Universe: 191, PoolSize: 193, Evaluated: 11200, Coverage: 0.86,
		Accuracy: 0.61, BalancedAccuracy: 0.55, BaselineAccuracy: 0.48, LabelSource: "unadjusted_close",
	}
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	body := cards.SecondStage[0].Article.Body
	for _, want := range []string{
		"阶段链路：初筛 10/193 → 树 10/10、JEV 0/10 → 规则阶段验证 9650样本/50日/191/193只，覆盖86.0%，准确61.0%，基线48.0%，平衡55.0%。阶段按日线规则定向，模型仅微调置信度。",
		"**阶段**：上涨 72/100",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("compact article missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "上涨 72/100（模型支持）") {
		t.Fatalf("partial model coverage was presented as complete agreement:\n%s", body)
	}
}

func TestArticlePhaseValidationNamesRulePhaseAndSupport(t *testing.T) {
	r := articleTestReport(6)
	r.PhaseValidation = &strategy.PhaseValidation{
		Status: "ok", HorizonDays: 5, Predicted: 9650, Dates: 50, Evaluated: 11200, Coverage: 0.86,
		Stocks: 191, Universe: 191, PoolSize: 193, MinSamples: 300, MinStocks: 30, MinDates: 30,
		Accuracy: 0.61, BalancedAccuracy: 0.55, BaselineAccuracy: 0.48, LabelSource: "unadjusted_close",
		Classes: []strategy.PhaseClassValidation{
			{Phase: strategy.PhaseAdvance, Label: "上涨", Predicted: 50, Support: 60, Precision: 0.7, Recall: 0.58},
		},
		Confusion: []strategy.PhaseConfusion{{Predicted: strategy.PhaseAdvance, Actual: strategy.PhaseAdvance, Count: 35}, {Predicted: strategy.PhaseAdvance, Actual: strategy.PhaseRange, Count: 15}},
	}
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	body := cards.SecondStage[0].Article.Body
	for _, want := range []string{
		"### 验证指标（规则阶段）", "标签 未复权收盘标签", "9650 个预测", "50 个唯一信号日", "191/193 只",
		"准确率 61.0%；多数类基线 48.0%；平衡准确率 55.0%", "上涨 预测50/实际60，精确率 70.0%、召回率 58.0%",
		"预测上涨→上涨35、震荡15",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("article validation missing %q:\n%s", want, body)
		}
	}
}

func TestArticlePhaseValidationShowsCurrentMetricsWhenSamplesAreInsufficient(t *testing.T) {
	validation := &strategy.PhaseValidation{
		Status: "insufficient", HorizonDays: 5, Predicted: 8, Dates: 2, Stocks: 2, Universe: 10, PoolSize: 10, Evaluated: 10, Coverage: 0.8,
		MinSamples: 300, MinStocks: 30, MinDates: 30,
		Accuracy: 0.5, BalancedAccuracy: 0.45, BaselineAccuracy: 0.5, LabelSource: "adjusted_close_fallback",
	}
	detailed := articleTestReport(6)
	detailed.PhaseValidation = validation
	cards, err := FromSelection(detailed)
	if err != nil {
		t.Fatal(err)
	}
	body := cards.SecondStage[0].Article.Body
	for _, want := range []string{
		"复权快照回退", "8 个预测", "2 个唯一信号日", "2/10 只",
		"样本不足（当前 8 样本/2 日/2 只；门槛 300 样本/30 日/30 只），以下指标仅作观察",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("insufficient validation article missing %q:\n%s", want, body)
		}
	}

	compact := articleTestReport(10)
	compact.PhaseValidation = validation
	cards, err = FromSelection(compact)
	if err != nil {
		t.Fatal(err)
	}
	compactBody := cards.SecondStage[0].Article.Body
	for _, want := range []string{
		"规则阶段验证样本不足（8样本/2日/2只；需300/30/30），当前准确50.0%",
	} {
		if !strings.Contains(compactBody, want) {
			t.Fatalf("compact insufficient validation article missing %q:\n%s", want, compactBody)
		}
	}
}

func TestArticleDeclinePhaseUsesRiseInvalidation(t *testing.T) {
	phase := articleTestPhase()
	phase.Phase = strategy.PhaseDecline
	phase.RulePhase = strategy.PhaseDecline
	phase.Label = "下跌"
	phase.Invalidation = 15.50
	lines := strings.Join(articlePhaseLines(&strategy.Candidate{Phase: phase}, false), "\n")
	if !strings.Contains(lines, "收盘升破 15.50") || strings.Contains(lines, "收盘跌破 15.50") {
		t.Fatalf("decline invalidation direction is wrong:\n%s", lines)
	}
}

func TestPremarketStructureOmitsEmptyPhaseHeading(t *testing.T) {
	var out strings.Builder
	writePremarketStructure(&out, &strategy.Candidate{})
	if strings.Contains(out.String(), "阶段判断（日线）") {
		t.Fatalf("empty phase left a heading:\n%s", out.String())
	}
}

func TestArticlePriceAnnotationsUseOutwardCentRoundingAndShowMissingFallback(t *testing.T) {
	candidate := &strategy.Candidate{
		Close:   15.15,
		DayHigh: 15.50,
		DayLow:  14.80,
		Features: strategy.Features{
			ATR14Pct: 2.8283,
		},
	}
	low, high, ok := articleBuyRange(candidate)
	if !ok || low != 15.00 || high != 15.30 {
		t.Fatalf("buy range = %.2f~%.2f, ok=%v; want 15.00~15.30", low, high, ok)
	}
	if _, _, ok := articleBuyRange(&strategy.Candidate{Close: 10}); ok {
		t.Fatal("accepted a candidate without ATR")
	}
	lines := articlePriceLineTexts(selection.Report{AsOf: "2026-10-21"}, &strategy.Candidate{Close: 10})
	if !strings.Contains(strings.Join(lines, "\n"), "信号日区间 暂缺（信号日高低价未记录）") {
		t.Fatalf("missing daily range fallback: %v", lines)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "明日高低预测 暂缺（dayrange 模型未生成目标日预测）") {
		t.Fatalf("missing forecast fallback: %v", lines)
	}
}

func TestArticleWaveDistinguishesMissingFieldFromInsufficientData(t *testing.T) {
	missing := strings.Join(articleWaveLines(nil), "\n")
	if !strings.Contains(missing, "本次报告未保存波浪字段") || strings.Contains(missing, "数据不足") {
		t.Fatalf("missing wave field was described as insufficient data: %s", missing)
	}
	unknown := strings.Join(articleWaveLines(&strategy.Candidate{Wave: &strategy.WaveEvidence{Stage: "unknown"}}), "\n")
	if !strings.Contains(unknown, "数据不足") {
		t.Fatalf("unknown wave stage was not described as insufficient data: %s", unknown)
	}
	if got := articleBriefWave(nil); !strings.Contains(got, "需重新生成") {
		t.Fatalf("brief missing wave = %q", got)
	}
}

func TestSecondStageArticleRequiresExplicitPush(t *testing.T) {
	r := selection.Report{
		Version: 1, ID: "report-id", AsOf: "2026-10-21", Target: "2026-10-22",
		SelectionEnabled: true, Adjust: "qfq", PoolSize: 1,
		SecondStage: &secondstage.Report{
			Version: secondstage.ReportSchemaVersion, Status: secondstage.StatusOK,
			AsOf: "2026-10-21", Target: "2026-10-22", PushEnabled: false,
			Candidates: []secondstage.Candidate{{Rank: 1, Code: "sz000001", Name: "测试"}},
		},
	}
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.SecondStage) != 0 {
		t.Fatalf("unexpected second-stage articles: %+v", cards.SecondStage)
	}
}

func TestSecondStageArticleCardsRenderWithinWechatLimit(t *testing.T) {
	r := articleTestReport(10)
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.SecondStage) != 1 || cards.SecondStage[0].Article == nil {
		t.Fatalf("missing second-stage article: %+v", cards.SecondStage)
	}
	rendered, err := wechat.RenderHTML(cards.SecondStage[0].Article.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(rendered)) >= 20000 {
		t.Fatalf("rendered article has %d runes, exceeds WeChat draft limit", len([]rune(rendered)))
	}
	if strings.Count(rendered, "<section style=\"margin:18px 0;") != 10 {
		t.Fatalf("rendered cards = %d, want 10", strings.Count(rendered, "<section style=\"margin:18px 0;"))
	}
}

func TestSixStockDetailedArticleFitsWechatLimit(t *testing.T) {
	r := articleTestReport(6)
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := wechat.RenderHTML(cards.SecondStage[0].Article.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(rendered)); got >= 20000 {
		t.Fatalf("detailed six-stock article has %d runes, exceeds WeChat draft limit", got)
	}
}

func TestSecondStageArticleWithTenChartsFitsWechatLimit(t *testing.T) {
	r := articleTestReport(10)
	r.PhaseValidation = &strategy.PhaseValidation{
		Status: "ok", HorizonDays: 5, Predicted: 9650, Dates: 50, Stocks: 191, Universe: 191, PoolSize: 193, Evaluated: 11200, Coverage: 0.86,
		Accuracy: 0.61, BalancedAccuracy: 0.55, BaselineAccuracy: 0.48, LabelSource: "unadjusted_close",
	}
	images := make(map[string]wechat.RenderedImage, 10)
	for index := range r.Ranking {
		chart := reportChart(r.Ranking[index].Code)
		r.Ranking[index].Chart = chart
		images[chart.ID] = wechat.RenderedImage{URL: "https://mmbiz.qpic.cn/mmbiz_png/" + strings.Repeat("a", 160) + "/0?wx_fmt=png", Caption: articleChartCaption(*chart)}
	}
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.SecondStage) != 1 || cards.SecondStage[0].Article == nil {
		t.Fatalf("missing second-stage article: %+v", cards.SecondStage)
	}
	rendered, err := wechat.RenderHTMLWithImages(cards.SecondStage[0].Article.Body, images)
	if err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(rendered)); got >= 20000 {
		t.Fatalf("rendered article has %d runes, exceeds WeChat draft limit", got)
	}
	if got := strings.Count(rendered, "<img "); got != 10 {
		t.Fatalf("rendered charts = %d, want 10", got)
	}
}

func articleTestReport(count int) selection.Report {
	names := []string{"亿联网络", "金山办公", "海螺水泥", "长江电力", "陕西煤业", "候选六", "候选七", "候选八", "候选九", "候选十"}
	codes := []string{"sz300628", "sh688111", "sh600585", "sh600900", "sh601225", "sz000006", "sz000007", "sz000008", "sz000009", "sz000010"}
	ranking := make([]strategy.Candidate, 0, count)
	candidates := make([]secondstage.Candidate, 0, count)
	forecasts := make(map[string]selection.DayRangeForecast, count)
	for i := 0; i < count; i++ {
		rank := i + 1
		guardStatus := strategy.StructureGuardPass
		reason := "日线结构未逆向，低级别未触发失效"
		if rank == 3 {
			guardStatus = strategy.StructureGuardFail
			reason = "日线收盘跌破最近笔重叠区下沿"
		}
		if rank == 4 {
			guardStatus = strategy.StructureGuardUnknown
			reason = "日线尚无有效笔重叠区，未形成可验证的向上结构"
		}
		levels := []strategy.StructureLevelEvidence{
			{Period: "day", Bars: 48, MergedBars: 42, Fractals: 5, FinishedStrokes: 5, LastFractal: "bottom", LastFractalAt: "2026-10-21T15:00:00+08:00", LastFractalHigh: 15.10, LastFractalLow: 14.60, LastStroke: "up", Zone: &strategy.ChanZoneEvidence{Upper: 15.20, Lower: 14.70, Mid: 14.95}, Status: guardStatus, Reason: reason},
			{Period: "30m", Bars: 96, MergedBars: 80, Fractals: 4, FinishedStrokes: 4, LastFractal: "bottom", LastFractalHigh: 15.08, LastFractalLow: 14.72, LastStroke: "up", Zone: &strategy.ChanZoneEvidence{Upper: 15.18, Lower: 14.78, Mid: 14.98}, Status: guardStatus, Reason: reason},
			{Period: "5m", Bars: 240, MergedBars: 201, Fractals: 8, FinishedStrokes: 5, LastFractal: "top", LastFractalHigh: 15.22, LastFractalLow: 14.95, LastStroke: "down", Status: guardStatus, Reason: reason},
		}
		ranking = append(ranking, strategy.Candidate{
			Code:             codes[i],
			Name:             names[i],
			Close:            15.15,
			DayHigh:          15.50,
			DayLow:           14.80,
			ObservationRank:  rank,
			ObservationScore: 49.4,
			Score:            49.4,
			Reasons:          []string{"rising_ma20", "stronger_than_pool", "strong_close"},
			Rules:            []string{"baseline", "breakout", "momentum", "volume_recovery"},
			Risks:            []string{"historical_net_loss_limit_breached", "insufficient_or_unprofitable_evidence", "holdout_validation_not_passed"},
			Features: strategy.Features{
				Return1: 1.2, Return5: 4.2, Return10: 6.3,
				MA20Distance: 3.1, MA20Slope: 1.2, VolumeRatio: 1.32,
				ATR14Pct: 2.8283, ClosePosition: 0.8, UpperWick: 0.12,
				High20Distance: -2.5, Relative5: 2.1,
				Metrics: indicators.Metrics{MA5: 15.20, MA10: 15.05, MA20: 14.90, DIF: 0.12, DEA: 0.08, RSI14: 61.4, VolumeRatio: 1.32},
				SignalIndicators: &strategy.SignalIndicators{
					Version: 1, PreviousRSI14: 59.4, MACDHistogram: 0.08, PreviousMACDHistogram: 0.05,
					Bollinger:        strategy.Bollinger{Middle: 14.90, Upper: 15.80, Lower: 14.00, WidthPct: 12.1, PercentB: floatPtr(0.89)},
					PreviousPercentB: floatPtr(0.72),
				},
			},
			Continuation:   &strategy.ContinuationStats{Status: "ready", Score: 39.7},
			StructureGuard: &strategy.StructureGuardEvidence{GuardStatus: guardStatus, Reason: reason, DailyZone: &strategy.ChanZoneEvidence{Upper: 15.20, Lower: 14.70, Mid: 14.95}, Levels: levels},
			Wave:           &strategy.WaveEvidence{Version: 1, Method: "simplified_swing_v1", Stage: "pullback", Label: "上升趋势中的调整（简化）", Status: "confirmed", CompletedStrokes: 5, LastStroke: "down", RetracementPct: 38.2, Support: 15.00, Resistance: 16.20, Invalidation: 14.70, Evidence: "日线完成笔重心仍向上，当前回撤未跌破笔重叠区下沿", Limitation: "仅依据完成笔与回撤的启发式描述，不是严格艾略特浪级计数"},
			Phase:          articleTestPhase(),
		})
		if rank < count {
			forecasts[codes[i]] = selection.DayRangeForecast{
				Code: codes[i], TargetDate: "2026-10-22", ModelID: "dayrange-model-v1", Source: "model", ReferencePrice: 15.15,
				PointHigh: 16.20, HighLower: 15.90, HighUpper: 16.50, HighCoverage: 0.8,
				PointLow: 14.70, LowLower: 14.40, LowUpper: 15.00, LowCoverage: 0.8,
			}
		}
		candidates = append(candidates, secondstage.Candidate{
			Rank:             rank,
			Code:             codes[i],
			Name:             names[i],
			Sector:           "测试行业",
			FirstStageRank:   rank,
			PredictedMaxHigh: 6.82,
			JEVPresent:       false,
			Features: firststage.FeatureSnapshot{
				Return5:        4.2,
				MA20Distance:   3.1,
				RSI14:          61.4,
				VolumeRatio:    1.32,
				High20Distance: -2.5,
				ClosePosition:  0.8,
				TrendScore:     82,
				ATR14Pct:       2.8283,
			},
			Reasons: []string{"jev_missing"},
		})
	}
	return selection.Report{
		Version:           3,
		ID:                "report-id",
		AsOf:              "2026-10-21",
		Target:            "2026-10-22",
		Strategy:          "short_horizon_v1",
		SelectionEnabled:  true,
		ExecutionMode:     strategy.StructureGuardShadow,
		Adjust:            "qfq",
		PoolSize:          193,
		Eligible:          191,
		DayRangeForecasts: forecasts,
		Ranking:           ranking,
		FirstStage: &firststage.Report{
			Status:      firststage.StatusOK,
			AsOf:        "2026-10-21",
			Target:      "2026-10-22",
			ModelID:     firststage.ModelID,
			PushEnabled: true,
			PoolSize:    193,
			Eligible:    191,
			Candidates:  []firststage.Candidate{{Rank: 4, Code: "sz300628", Name: "亿联网络"}},
		},
		SecondStage: &secondstage.Report{
			Version:     secondstage.ReportSchemaVersion,
			Status:      secondstage.StatusOK,
			AsOf:        "2026-10-21",
			Target:      "2026-10-22",
			ModelID:     "second_stage_gbdt_v1",
			TopN:        10,
			PushEnabled: true,
			InputCount:  145,
			JEVStatus:   secondstage.JEVStatusNone,
			Degraded:    false,
			Candidates:  candidates,
		},
	}
}

func articleTestPhase() *strategy.PhaseEvidence {
	return &strategy.PhaseEvidence{
		Version: 1, Method: strategy.PhaseMethod, AsOf: "2026-10-21",
		Phase: strategy.PhaseAdvance, Label: "上涨", Confidence: "medium",
		ConfidenceScore: 72, BaseConfidenceScore: 72, Score: 4, Strength: 1,
		RulePhase: strategy.PhaseAdvance, RuleScore: 4, DecisionStage: "rule+model-confidence", ModelAgreement: "supportive",
		Models:          []strategy.PhaseModelEvidence{{Name: "tree", Status: "ok", Score: 0.40, Weight: 0.6, Detail: "预测未来3日最高涨幅 6.82%"}},
		MA20DistancePct: 3.1, MA20SlopePct: 1.2,
		RegressionSlopePct: 0.08, RegressionR2: 0.62, ATRMultiple: 0.8,
		RangePosition: 0.8, RangeLow: 14.20, RangeHigh: 15.50, Invalidation: 14.70,
		Steps: []strategy.PhaseStep{
			{Key: "data", Label: "数据完整性", Value: "160 根日线，满足至少 60 根", Verdict: "充足"},
			{Key: "price_vs_ma20", Label: "价格位置", Value: "收盘 15.15，MA20 14.90（+1.68%）", Verdict: "偏多", Score: 1},
			{Key: "ma20_slope", Label: "中期方向", Value: "MA20 五日斜率 +1.20%", Verdict: "向上", Score: 1},
			{Key: "ma_alignment", Label: "均线排列", Value: "MA5 15.20 / MA10 15.05 / MA20 14.90", Verdict: "多头排列", Score: 1},
			{Key: "regression", Label: "趋势质量", Value: "20 日回归斜率 +0.08%/日，R² 0.62，约 0.8 ATR", Verdict: "向上且质量可用", Score: 1},
			{Key: "range_position", Label: "区间位置", Value: "收盘位于 20 日区间 80%（14.20~15.50）", Verdict: "靠上"},
			{Key: "structure", Label: "缠论/波浪结构", Value: "最后一笔向下；波浪阶段上升趋势中的调整（简化）", Verdict: "上升中的调整"},
		},
		Evidence:   "收盘 15.15，MA20 14.90（+1.68%）；MA20 五日斜率 +1.20%；综合得分 +4，判定上涨。",
		Limitation: "规则化日线阶段分类，不是趋势确认，也不构成买卖建议",
	}
}

func floatPtr(value float64) *float64 { return &value }
