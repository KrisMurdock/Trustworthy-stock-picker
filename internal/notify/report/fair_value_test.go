package report

import (
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
)

func testFairValue(code string) fairvalue.Estimate {
	return fairvalue.Estimate{
		Version: fairvalue.Version, Code: code, AsOf: "2026-10-21", Source: "sina",
		FetchedAt: time.Date(2026, 10, 21, 18, 5, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)),
		Method:    fairvalue.Method, StatementBasis: "trailing_four_quarters", Confidence: "medium",
		CurrentPrice: 15.15, EPS: 1.2, BookValuePerShare: 8.5, GrowthPct: 15.3, GrowthKnown: true,
		ROEPct: 14.1, PE: 12.6, PB: 1.78, FairPE: 18.2, FairPB: 2.1,
		Lower: 14.40, Central: 16.00, Upper: 17.60, UpsidePct: 5.6,
	}
}

func TestSecondStageArticleIncludesFairValueRange(t *testing.T) {
	r := articleTestReport(1)
	r.FairValues = map[string]fairvalue.Estimate{r.SecondStage.Candidates[0].Code: testFairValue(r.SecondStage.Candidates[0].Code)}
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.SecondStage) != 1 || cards.SecondStage[0].Article == nil {
		t.Fatalf("missing daily article: %+v", cards.SecondStage)
	}
	body := cards.SecondStage[0].Article.Body
	for _, want := range []string{"#### 合理价值区间", "14.40~17.60 元", "TTM EPS 1.20 元", "ROE 14.1%", "当前 PE 12.6 倍、PB 1.8 倍", "不是目标价"} {
		if !strings.Contains(body, want) {
			t.Fatalf("daily article missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(cards.SecondStage[0].Article.Digest, "合理价值区间") {
		t.Fatalf("digest does not mention valuation: %q", cards.SecondStage[0].Article.Digest)
	}
}

func TestPremarketArticleIncludesFairValueRange(t *testing.T) {
	r := articleTestReport(1)
	code := r.SecondStage.Candidates[0].Code
	r.FairValues = map[string]fairvalue.Estimate{code: testFairValue(code)}
	message, ok, err := BuildPremarketArticle(PremarketArticleInput{
		TargetDate: "2026-10-22", GeneratedAt: time.Date(2026, 10, 22, 8, 45, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)),
		InputThrough: "2026-10-21", PoolSize: 193, Report: &r, Predictions: r.DayRangeForecasts,
		Statuses: map[string]PremarketExecutionStatus{code: {Status: "可执行", Reason: "覆盖目标"}},
	})
	if err != nil || !ok || message.Article == nil {
		t.Fatalf("build premarket article: ok=%v err=%v", ok, err)
	}
	for _, want := range []string{"#### 合理价值区间", "14.40~17.60 元", "TTM EPS 1.20 元", "合理价值区间"} {
		if !strings.Contains(message.Article.Body, want) {
			t.Fatalf("premarket article missing %q:\n%s", want, message.Article.Body)
		}
	}
}

func TestFairValueUnavailableIsExplicit(t *testing.T) {
	r := articleTestReport(1)
	code := r.SecondStage.Candidates[0].Code
	r.FairValues = map[string]fairvalue.Estimate{code: {Version: fairvalue.Version, Code: code, AsOf: r.AsOf, Method: fairvalue.Method, Reason: "最近四季度每股收益为负"}}
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	body := cards.SecondStage[0].Article.Body
	if !strings.Contains(body, "暂缺（最近四季度每股收益为负）") || strings.Contains(body, "14.40~17.60") {
		t.Fatalf("unavailable estimate was not explicit:\n%s", body)
	}
}
