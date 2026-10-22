package report

import (
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
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
		"### 1. 亿联网络（sz300628）｜精排第1名 · 观察第1名",
		"- 结构状态：通过",
		"- 机会分：49.4",
		"- 上涨延续分：39.7",
		"- 主要依据：MA20向上、近5日强于样本池、收盘偏强",
		"- 触发规则：样本池基准、突破、动量、放量恢复",
		"- 推荐：15.00~15.30 购入",
		"- 日价格区间：14.80~15.50（10月21日实际）",
		"## 二、其他进入观察排名的对象",
		"### 6. 候选六（sz000006）｜精排第6名 · 观察第6名",
		"## 三、今天的整体结论",
		"> 强势观察池，而不是正式推荐池。",
		"ATR14 的 0.35 倍",
		"不构成任何证券买卖建议",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("article missing %q:\n%s", want, body)
		}
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
	lines := articlePriceLines(selection.Report{AsOf: "2026-10-21"}, &strategy.Candidate{Close: 10})
	if !strings.Contains(strings.Join(lines, "\n"), "日价格区间：暂缺（信号日高低价未记录）") {
		t.Fatalf("missing daily range fallback: %v", lines)
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

func articleTestReport(count int) selection.Report {
	names := []string{"亿联网络", "金山办公", "海螺水泥", "长江电力", "陕西煤业", "候选六"}
	codes := []string{"sz300628", "sh688111", "sh600585", "sh600900", "sh601225", "sz000006"}
	ranking := make([]strategy.Candidate, 0, count)
	candidates := make([]secondstage.Candidate, 0, count)
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
			Features:         strategy.Features{ATR14Pct: 2.8283},
			Continuation:     &strategy.ContinuationStats{Status: "ready", Score: 39.7},
			StructureGuard:   &strategy.StructureGuardEvidence{GuardStatus: guardStatus, Reason: reason},
		})
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
		Version:          3,
		ID:               "report-id",
		AsOf:             "2026-10-21",
		Target:           "2026-10-22",
		Strategy:         "short_horizon_v1",
		SelectionEnabled: true,
		ExecutionMode:    strategy.StructureGuardShadow,
		Adjust:           "qfq",
		PoolSize:         193,
		Eligible:         191,
		Ranking:          ranking,
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
