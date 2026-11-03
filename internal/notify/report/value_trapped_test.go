package report

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/valuetrapped"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/wechat"
)

func valueTrappedSignalFixture(ratio float64) *strategy.TrappedSignal {
	return &strategy.TrappedSignal{
		Version: strategy.TrappedSignalVersion, Method: strategy.TrappedSignalMethod, Status: strategy.TrappedStatusNone,
		CurrentPrice: 10, TrappedVolumeRatio: ratio, OverheadCost: 10.3, OverheadDistancePct: 3,
		CurrentZone: strategy.TrappedZoneShallow, DominantZone: strategy.TrappedZoneShallow,
		Zones: []strategy.TrappedZone{
			{Level: strategy.TrappedZoneShallow, Label: "浅度", Low: 10, High: 10.5, VolumeShare: 0.5, SharePct: 50},
			{Level: strategy.TrappedZoneModerate, Label: "中度", Low: 10.5, High: 11.5, VolumeShare: 0.3, SharePct: 30},
			{Level: strategy.TrappedZoneDeep, Label: "深度", Low: 11.5, High: 12, VolumeShare: 0.2, SharePct: 20},
		},
	}
}

func valueTrappedCandidateFixture(rank int, code, name string, close, gap, ratio float64) valuetrapped.Candidate {
	return valuetrapped.Candidate{
		Rank: rank, Code: code, Name: name, Sector: "制造业", PriceDate: "2026-10-31", Close: close,
		GapToLowerPct: gap, UpsideToCentralPct: (16 - close) / close * 100, AvgAmount20CNY: 120_000_000,
		Estimate: fairvalue.Estimate{
			Version: fairvalue.Version, Code: code, AsOf: "2026-10-31", Source: "sina",
			FetchedAt: time.Date(2026, 10, 31, 18, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)),
			Method:    fairvalue.Method, StatementBasis: "trailing_four_quarters", Confidence: "medium",
			CurrentPrice: close, Lower: 14.4, Central: 16, Upper: 17.6, UpsidePct: (16/close - 1) * 100,
		},
		Trapped: valueTrappedSignalFixture(ratio),
	}
}

func valueTrappedRecommendationFixture(candidates ...valuetrapped.Candidate) valuetrapped.Recommendation {
	return valuetrapped.Recommendation{
		Version: valuetrapped.SchemaVersion, ID: "value_trapped-2026-10-31-20", Market: valuetrapped.Market,
		AsOf: "2026-10-31", Target: "2026-11-02",
		GeneratedAt:   time.Date(2026, 10, 31, 19, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)),
		UniverseTotal: 5000, Priced: 4800, Valued: 4300, LowValuationEligible: 120, TrappedValid: 80,
		Eligible: len(candidates), Selected: len(candidates), TopN: 20, MaxTrappedRatioPct: 30,
		MinHistorySessions: 250, MinAmountCNY: 20_000_000, SectorCoveragePct: 99.2, Candidates: candidates,
		Filtered:              []valuetrapped.Filtered{{Reason: "trapped_ratio_above_limit", Count: 15}, {Reason: "low_liquidity", Count: 5}},
		TrappedUnknownReasons: map[string]int{"turnover_history_unavailable": 3},
		Limitations:           []string{"低估值与低套牢的交集不代表上涨。"},
	}
}

func TestValueTrappedArticleIncludesBothScreensAndZones(t *testing.T) {
	rec := valueTrappedRecommendationFixture(
		valueTrappedCandidateFixture(1, "sh600001", "接近区间", 13, 5.3, 0.08),
		valueTrappedCandidateFixture(2, "sz000002", "深度折价", 10, 27.4, 0.15),
	)
	message, err := ValueTrappedArticle(rec)
	if err != nil {
		t.Fatal(err)
	}
	if message.EventID != rec.ID+":value_trapped" || message.Article == nil {
		t.Fatalf("message = %+v", message)
	}
	if !strings.Contains(message.Article.Title, "低估值且低套牢") || !strings.Contains(message.Article.Title, "双重观察") {
		t.Fatalf("title = %q", message.Article.Title)
	}
	for _, want := range []string{"距下沿折价", "套牢状态", "当前浅度套牢区", "浅度 10.00~10.50 元（50%）", "中度 10.50~11.50 元（30%）", "深度 11.50~12.00 元（20%）", "不计算综合分", "不构成证券买卖建议", "接近区间", "深度折价"} {
		if !strings.Contains(message.Article.Body, want) {
			t.Fatalf("article missing %q:\n%s", want, message.Article.Body)
		}
	}
	if strings.Index(message.Article.Body, "接近区间") > strings.Index(message.Article.Body, "深度折价") {
		t.Fatalf("article order does not follow the scan ranking")
	}
}

func TestValueTrappedArticleExplainsEmptyIntersection(t *testing.T) {
	rec := valueTrappedRecommendationFixture()
	message, err := ValueTrappedArticle(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message.Article.Body, "本期没有股票同时满足") || !strings.Contains(message.Article.Digest, "没有股票同时满足") {
		t.Fatalf("empty article is not explicit:\n%s", message.Article.Body)
	}
}

func TestValueTrappedArticleRejectsInvalidCandidate(t *testing.T) {
	rec := valueTrappedRecommendationFixture(valueTrappedCandidateFixture(1, "sh600001", "测试", 13, 5.3, 0.08))
	rec.Candidates[0].Trapped.Status = strategy.TrappedStatusUnknown
	if _, err := ValueTrappedArticle(rec); err == nil {
		t.Fatal("unknown trapped signal was accepted")
	}
	rec = valueTrappedRecommendationFixture(valueTrappedCandidateFixture(1, "sh600001", "测试", 13, 5.3, 0.31))
	if _, err := ValueTrappedArticle(rec); err == nil {
		t.Fatal("candidate above the ratio limit was accepted")
	}
}

func TestValueTrappedArticleWithTwentyCandidatesFitsWechatLimit(t *testing.T) {
	candidates := make([]valuetrapped.Candidate, 0, 20)
	for index := 0; index < 20; index++ {
		candidates = append(candidates, valueTrappedCandidateFixture(index+1,
			fmt.Sprintf("sh6%05d", index), fmt.Sprintf("测试股票%02d", index), 10+float64(index), 5+float64(index), 0.05+float64(index)/100))
	}
	message, err := ValueTrappedArticle(valueTrappedRecommendationFixture(candidates...))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := wechat.RenderHTML(message.Article.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(rendered)); got >= 20000 {
		t.Fatalf("rendered value-trapped article has %d runes, exceeds WeChat limit", got)
	}
}
