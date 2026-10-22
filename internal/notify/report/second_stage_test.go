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
		"### 1. 亿联网络（sz300628）",
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
		"### 6. 候选六（sz000006）",
		"明日高低预测 暂缺（dayrange 模型未生成目标日预测）",
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
	for _, unwanted := range []string{"jev_missing", "historical_net_loss_limit_breached", "【公众号"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("article leaked %q:\n%s", unwanted, body)
		}
	}
	expires := time.Date(2026, 10, 22, 9, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	if !article.ExpiresAt.Equal(expires) {
		t.Fatalf("expiry = %s, want %s", article.ExpiresAt, expires)
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
	if strings.Count(rendered, "<section style=\"margin:22px 0;") != 10 {
		t.Fatalf("rendered cards = %d, want 10", strings.Count(rendered, "<section style=\"margin:22px 0;"))
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

func floatPtr(value float64) *float64 { return &value }
