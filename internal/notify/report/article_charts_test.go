package report

import (
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/articlechart"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/wechat"
)

func TestSecondStageArticleAttachesChartSpecsAndHidesTokensFromPlainText(t *testing.T) {
	r := articleTestReport(6)
	r.Ranking[0].Chart = reportChart(r.Ranking[0].Code)
	r.Ranking[1].Chart = reportChart(r.Ranking[1].Code)
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	article := cards.SecondStage[0]
	if len(article.Article.Charts) != 2 {
		t.Fatalf("charts = %d, want 2", len(article.Article.Charts))
	}
	for _, token := range []string{"[[chart:chart-sz300628]]", "[[chart:chart-sh688111]]"} {
		if !strings.Contains(article.Article.Body, token) {
			t.Fatalf("body missing %s:\n%s", token, article.Article.Body)
		}
		if strings.Contains(article.PlainText, token) || strings.Contains(article.PlainText, "结构图谱") {
			t.Fatalf("plain text leaked chart markup %s:\n%s", token, article.PlainText)
		}
	}
	if !strings.Contains(article.Article.Charts[0].Caption, "日K") || !strings.Contains(article.Article.Charts[0].Caption, "sz300628") {
		t.Fatalf("unexpected caption: %s", article.Article.Charts[0].Caption)
	}
	for _, want := range []string{"橙笔", "蓝点", "蓝区", "紫线位"} {
		if !strings.Contains(article.Article.Charts[0].Caption, want) {
			t.Fatalf("caption missing %q: %s", want, article.Article.Charts[0].Caption)
		}
	}
	for _, want := range []string{"缠论笔", "顶、底分型", "回撤刻度"} {
		if !strings.Contains(article.Article.Body, want) {
			t.Fatalf("legend missing %q from article body:\n%s", want, article.Article.Body)
		}
	}
	images := map[string]wechat.RenderedImage{}
	for _, chart := range article.Article.Charts {
		images[chart.ID] = wechat.RenderedImage{URL: "https://mmbiz.qpic.cn/" + chart.ID, Caption: chart.Caption}
	}
	rendered, err := wechat.RenderHTMLWithImages(article.Article.Body, images)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(rendered, "<img ") != 2 || strings.Count(rendered, "日K") < 2 {
		t.Fatalf("rendered charts are incomplete:\n%s", rendered)
	}
}

func TestWaveBreakdownTextDoesNotLookLikeOrdinaryPullback(t *testing.T) {
	lines := articleWaveLines(&strategy.Candidate{Wave: &strategy.WaveEvidence{
		Stage: "weak", Label: "结构偏弱（简化）", RetracementPct: 100,
		RetracementExceeded: true, Support: 12, Invalidation: 11.8,
		Limitation: "仅依据完成笔与回撤的启发式描述，不是严格艾略特浪级计数",
	}})
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"超过 100%", "跌破最近上行笔起点", "不是严格艾略特浪级计数"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("breakdown text missing %q:\n%s", want, joined)
		}
	}
}

func TestWaveTextCombinesEqualSupportAndInvalidation(t *testing.T) {
	lines := articleWaveLines(&strategy.Candidate{Wave: &strategy.WaveEvidence{
		Stage: "weak", Label: "结构偏弱（简化）", Support: 12, Invalidation: 12,
	}})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "支撑/失效位 12.00") || strings.Count(joined, "失效位") != 1 {
		t.Fatalf("shared support/invalidation was not combined:\n%s", joined)
	}
}

func TestWaveTextNormalizesLegacyDeepRetracement(t *testing.T) {
	lines := articleWaveLines(&strategy.Candidate{Wave: &strategy.WaveEvidence{
		Stage: "weak", Label: "结构偏弱（简化）", RetracementPct: 150,
	}})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "超过 100%") || strings.Contains(joined, "150.0%") {
		t.Fatalf("legacy deep retracement was not normalized:\n%s", joined)
	}
}

func TestPremarketArticleAttachesChartSpecs(t *testing.T) {
	r := articleTestReport(6)
	r.Ranking[0].Chart = reportChart(r.Ranking[0].Code)
	message, ok, err := BuildPremarketArticle(PremarketArticleInput{
		TargetDate: "2026-10-22", GeneratedAt: time.Date(2026, 10, 22, 8, 45, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)),
		InputThrough: "2026-10-21", PoolSize: 193, Report: &r,
		Predictions: r.DayRangeForecasts,
	})
	if err != nil || !ok {
		t.Fatalf("build article: ok=%v err=%v", ok, err)
	}
	if len(message.Article.Charts) != 1 || !strings.Contains(message.Article.Body, "[[chart:chart-sz300628]]") {
		t.Fatalf("premarket chart missing: charts=%d body=%s", len(message.Article.Charts), message.Article.Body)
	}
	if strings.Contains(message.PlainText, "[[chart:") || strings.Contains(message.PlainText, "结构图谱") {
		t.Fatalf("plain text leaked chart markup:\n%s", message.PlainText)
	}
}

func reportChart(code string) *articlechart.Spec {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]articlechart.Bar, 0, 40)
	for index := 0; index < 40; index++ {
		price := 15 + float64(index)*0.01
		bars = append(bars, articlechart.Bar{Date: start.AddDate(0, 0, index).Format("2006-01-02"), Open: price - 0.05, High: price + 0.15, Low: price - 0.15, Close: price})
	}
	return &articlechart.Spec{
		Version: articlechart.Version, ID: "chart-" + code, Code: code, AsOf: bars[len(bars)-1].Date, Adjust: "qfq",
		Bars:    bars,
		Strokes: []articlechart.Stroke{{FromDate: bars[3].Date, ToDate: bars[25].Date, From: 14.9, To: 15.4, Finished: true}},
		Zone:    &articlechart.Zone{FromDate: bars[8].Date, ToDate: bars[30].Date, Upper: 15.3, Lower: 15.0},
		Levels:  []articlechart.Level{{Kind: "support", Label: "SUP", Price: 15.0}, {Kind: "resistance", Label: "RES", Price: 15.4}},
		Stage:   "advance", RetracementPct: 38.2, CompletedStrokes: 3,
	}
}
