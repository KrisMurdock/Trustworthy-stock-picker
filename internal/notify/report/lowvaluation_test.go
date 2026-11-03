package report

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/lowvaluation"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/wechat"
)

func lowValuationCandidateFixture(rank int, code, name string, close, gap float64) lowvaluation.Candidate {
	return lowvaluation.Candidate{
		Rank: rank, Code: code, Name: name, Sector: "制造业", PriceDate: "2026-10-31", Close: close,
		GapToLowerPct: gap, UpsideToCentralPct: (16 - close) / close * 100,
		Estimate: fairvalue.Estimate{
			Version: fairvalue.Version, Code: code, AsOf: "2026-10-31", Source: "sina",
			FetchedAt: time.Date(2026, 10, 31, 18, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)),
			Method:    fairvalue.Method, StatementBasis: "trailing_four_quarters", Confidence: "medium",
			CurrentPrice: close, EPS: 1.2, BookValuePerShare: 8.5, GrowthPct: 15.3, GrowthKnown: true,
			ROEPct: 14.1, PE: close / 1.2, PB: close / 8.5, FairPE: 18.2, FairPB: 2.1, BookValueAnchored: true,
			Lower: 14.4, Central: 16, Upper: 17.6, UpsidePct: (16/close - 1) * 100,
		},
	}
}

func lowValuationRecommendationFixture(candidates ...lowvaluation.Candidate) lowvaluation.Recommendation {
	return lowvaluation.Recommendation{
		Version: lowvaluation.SchemaVersion, ID: "low_valuation-2026-10-31-20", Market: lowvaluation.Market,
		AsOf: "2026-10-31", Target: "2026-11-02",
		GeneratedAt:   time.Date(2026, 10, 31, 19, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)),
		UniverseTotal: 5000, Priced: 4800, Valued: 4300, Eligible: len(candidates), Selected: len(candidates), TopN: 20,
		SectorCoveragePct: 99.2, Candidates: candidates,
		Filtered:    []lowvaluation.Filtered{{Reason: "not_below_lower", Count: 4188}, {Reason: "stale_or_suspended", Count: 200}},
		Limitations: []string{"低估不等于短期上涨。", "财务口径可能受周期利润影响。"},
	}
}

func TestLowValuationArticleIncludesRangeAndRankingMethod(t *testing.T) {
	rec := lowValuationRecommendationFixture(
		lowValuationCandidateFixture(1, "sh600001", "接近区间", 13, 5.3),
		lowValuationCandidateFixture(2, "sz000002", "深度折价", 10, 27.4),
	)
	message, err := LowValuationArticle(rec)
	if err != nil {
		t.Fatal(err)
	}
	if message.EventID != rec.ID+":low_valuation" || message.Article == nil {
		t.Fatalf("message = %+v", message)
	}
	if !strings.Contains(message.Article.Title, "全市场低估值股") || !strings.Contains(message.Article.Title, "财务折价观察") {
		t.Fatalf("title = %q", message.Article.Title)
	}
	for _, want := range []string{"排名口径", "距下沿折价", "不复权日线收盘价", "接近区间", "深度折价", "14.40~17.60 元", "财务研究区间", "不构成证券买卖建议"} {
		if !strings.Contains(message.Article.Body, want) {
			t.Fatalf("article missing %q:\n%s", want, message.Article.Body)
		}
	}
	if strings.Index(message.Article.Body, "接近区间") > strings.Index(message.Article.Body, "深度折价") {
		t.Fatalf("article order does not follow the scan ranking")
	}
}

func TestLowValuationArticleExplainsEmptyResult(t *testing.T) {
	rec := lowValuationRecommendationFixture()
	message, err := LowValuationArticle(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message.Article.Body, "本期没有股票") || !strings.Contains(message.Article.Digest, "没有股票") {
		t.Fatalf("empty article is not explicit:\n%s", message.Article.Body)
	}
}

func TestLowValuationArticleRejectsIdentityMismatch(t *testing.T) {
	rec := lowValuationRecommendationFixture()
	rec.Market = "hk_connect"
	if _, err := LowValuationArticle(rec); err == nil {
		t.Fatal("mismatched market was accepted")
	}
	rec = lowValuationRecommendationFixture(lowValuationCandidateFixture(1, "sh600001", "测试", 13, 5.3))
	rec.Selected = 2
	if _, err := LowValuationArticle(rec); err == nil {
		t.Fatal("mismatched candidate count was accepted")
	}
}

func TestLowValuationArticleWithTwentyCandidatesFitsWechatLimit(t *testing.T) {
	candidates := make([]lowvaluation.Candidate, 0, 20)
	for index := 0; index < 20; index++ {
		candidates = append(candidates, lowValuationCandidateFixture(index+1,
			fmt.Sprintf("sh6%05d", index), fmt.Sprintf("测试股票%02d", index), 10+float64(index), 5+float64(index)))
	}
	rec := lowValuationRecommendationFixture(candidates...)
	message, err := LowValuationArticle(rec)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := wechat.RenderHTML(message.Article.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(rendered)); got >= 20000 {
		t.Fatalf("rendered low valuation article has %d runes, exceeds WeChat limit", got)
	}
}
