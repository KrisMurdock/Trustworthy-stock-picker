package report

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/hkrecommend"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/indicators"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/wechat"
)

func TestHKConnectArticleIsIndependentAndCarriesCharts(t *testing.T) {
	recommendation := hkrecommend.Recommendation{
		Version: hkrecommend.SchemaVersion, ID: "rec-1", Market: hkrecommend.Market,
		AsOf: "2026-10-30", Target: "2026-11-02", GeneratedAt: time.Date(2026, 10, 30, 18, 50, 0, 0, market.Shanghai),
		UniverseTotal: 667, Eligible: 660, Passed: 2, Selected: 2, TopN: 5, MinScore: 70,
		Rules:       []string{"收盘价 > MA20 且 MA5 > MA10 > MA20"},
		Limitations: []string{"本稿是港股通日线规则观察，独立于 A 股初筛、精排模型和成交模拟。", "港股实行 T+0 且没有 A 股式涨跌停。"},
		Candidates: []hkrecommend.RecommendationCandidate{
			{Code: "hk00700", Name: "腾讯控股", Close: 420.5, ChangePct: 1.2, Rank: 1,
				Metrics: indicators.Metrics{Score: 92, MA5: 418, MA10: 414, MA20: 408, RSI14: 68, VolumeRatio: 1.6, DIF: 3.2, DEA: 2.1, UpDays: 3},
				Chart:   reportChart("hk00700")},
			{Code: "hk00005", Name: "汇丰控股", Close: 78.2, ChangePct: 0.4, Rank: 2,
				Metrics: indicators.Metrics{Score: 85, MA5: 77.5, MA10: 76.8, MA20: 75.2, RSI14: 62, VolumeRatio: 1.3, DIF: 0.8, DEA: 0.4, UpDays: 2},
				Chart:   reportChart("hk00005")},
		},
	}
	message, err := HKConnectArticle(recommendation)
	if err != nil {
		t.Fatal(err)
	}
	if message.EventID != "rec-1:hk-connect:article" || message.PartLabel != "港股通" || message.Article == nil {
		t.Fatalf("message identity = %+v", message)
	}
	if message.Article.Title != "港股通精选｜11月2日 日线趋势观察" {
		t.Fatalf("title = %q", message.Article.Title)
	}
	expires := time.Date(2026, 11, 2, 9, 30, 0, 0, market.Shanghai)
	if !message.ExpiresAt.Equal(expires) {
		t.Fatalf("expiry = %s, want %s", message.ExpiresAt, expires)
	}
	body := message.Article.Body
	for _, want := range []string{
		"### 第1名｜腾讯控股 hk00700",
		"**趋势分**：92/100",
		"**动能**：RSI14 68.0；量比 1.60",
		"不是 A 股初筛或精排模型的迁移结果",
		"港股实行 T+0 且没有 A 股式涨跌停",
		"独立 `hk-market.db`",
		"不构成证券买卖建议",
		"[[chart:chart-hk00700]]",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("article missing %q:\n%s", want, body)
		}
	}
	if len(message.Article.Charts) != 2 || !strings.Contains(message.Article.Charts[0].Caption, "港股日K") {
		t.Fatalf("article charts = %+v", message.Article.Charts)
	}
	if strings.Contains(message.PlainText, "[[chart:") || strings.Contains(message.PlainText, "结构图谱") {
		t.Fatalf("plain text leaked chart markup:\n%s", message.PlainText)
	}
	if utf8.RuneCountInString(message.Article.Title) > 32 || utf8.RuneCountInString(message.Article.Digest) > 120 || utf8.RuneCountInString(body) >= 20000 {
		t.Fatalf("wechat limits exceeded: title=%d digest=%d body=%d", utf8.RuneCountInString(message.Article.Title), utf8.RuneCountInString(message.Article.Digest), utf8.RuneCountInString(body))
	}
	images := map[string]wechat.RenderedImage{}
	for _, chart := range message.Article.Charts {
		images[chart.ID] = wechat.RenderedImage{URL: "https://mmbiz.qpic.cn/" + chart.ID, Caption: chart.Caption}
	}
	rendered, err := wechat.RenderHTMLWithImages(body, images)
	if err != nil {
		t.Fatalf("article does not render: %v", err)
	}
	if strings.Count(rendered, "<img ") != 2 {
		t.Fatalf("rendered charts = %d:\n%s", strings.Count(rendered, "<img "), rendered)
	}
}

func TestHKConnectArticleRendersEmptyObservationWithoutPlaceholders(t *testing.T) {
	recommendation := hkrecommend.Recommendation{
		Version: hkrecommend.SchemaVersion, ID: "rec-empty", Market: hkrecommend.Market,
		AsOf: "2026-10-30", Target: "2026-11-02", GeneratedAt: time.Date(2026, 10, 30, 18, 50, 0, 0, market.Shanghai),
		UniverseTotal: 667, Eligible: 660, Passed: 0, Selected: 0, TopN: 5, MinScore: 70,
		Limitations: []string{"港股实行 T+0 且没有 A 股式涨跌停。"},
	}
	message, err := HKConnectArticle(recommendation)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message.Article.Body, "今天没有股票同时满足全部趋势门槛") || strings.Contains(message.Article.Body, "[[chart:") {
		t.Fatalf("empty article body:\n%s", message.Article.Body)
	}
	if len(message.Article.Charts) != 0 {
		t.Fatalf("empty article charts = %+v", message.Article.Charts)
	}
	if _, err := wechat.RenderHTML(message.Article.Body); err != nil {
		t.Fatalf("empty article does not render: %v", err)
	}
}
