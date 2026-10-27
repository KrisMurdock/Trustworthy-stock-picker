package report

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/wechat"
)

func TestPremarketArticleIncludesForecastExecutionAndMaskedLabels(t *testing.T) {
	r := articleTestReport(6)
	statuses := map[string]PremarketExecutionStatus{
		"sz300628": {Status: "可执行", Reason: "预测高低覆盖参考目标与止损"},
		"sh688111": {Status: "放弃", Reason: "预测高低于净目标价"},
	}
	message, ok, err := BuildPremarketArticle(PremarketArticleInput{
		TargetDate: "2026-10-22", GeneratedAt: time.Date(2026, 10, 22, 8, 45, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)),
		InputThrough: "2026-10-21", PoolSize: 193, Report: &r,
		Predictions: r.DayRangeForecasts, Statuses: statuses,
		Skipped: []string{"sz000099:insufficient_history"},
	})
	if err != nil || !ok {
		t.Fatalf("build premarket article: ok=%v err=%v", ok, err)
	}
	if message.EventID != "dayrange:premarket-article:2026-10-22" || message.Article == nil {
		t.Fatalf("unexpected message: %+v", message)
	}
	if message.Article.Title != "盘前观察｜10月22日 全天高低与执行确认" {
		t.Fatalf("title = %q", message.Article.Title)
	}
	if utf8.RuneCountInString(message.Article.Title) > 32 || utf8.RuneCountInString(message.Article.Digest) > 120 {
		t.Fatalf("article title/digest exceeds WeChat limits: title=%d digest=%d", utf8.RuneCountInString(message.Article.Title), utf8.RuneCountInString(message.Article.Digest))
	}
	body := message.Article.Body
	for _, want := range []string{
		"# 10月22日 盘前观察：全天高低与执行确认",
		"### 1. YLWL（sz300628）",
		"#### 全天高低预测",
		"**参考价**：15.15",
		"**预测高点**：16.20；区间 15.90~16.50（名义覆盖 80%）",
		"**预测低点**：14.70；区间 14.40~15.00（名义覆盖 80%）",
		"**推荐买区**：15.00~15.30 购入",
		"#### 执行参考",
		"**状态**：可执行；预测高低覆盖参考目标与止损",
		"#### 技术指标",
		"**RSI14**：61.4",
		"#### 缠论结构",
		"**日线**：通过",
		"#### 波浪视角（简化）",
		"**阶段**：上升趋势中的调整（简化）",
		"### 2. JSBG（sh688111）",
		"**状态**：放弃；预测高低于净目标价",
		"结构影子期",
		"本次未进入预测列表：insufficient_history 1",
		"不构成任何证券买卖建议",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("article missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "精排队列尚未就绪") {
		t.Fatalf("ranking was available but article used fallback order:\n%s", body)
	}
	for _, unwanted := range []string{"亿联网络", "金山办公", "海螺水泥", "长江电力", "陕西煤业", "候选六"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("article leaked stock name %q:\n%s", unwanted, body)
		}
	}
	expires := time.Date(2026, 10, 22, 9, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	if !message.ExpiresAt.Equal(expires) {
		t.Fatalf("expiry = %s, want %s", message.ExpiresAt, expires)
	}
	if _, err := wechat.RenderHTML(body); err != nil {
		t.Fatalf("premarket article does not render: %v", err)
	}
}

func TestPremarketArticleFallsBackToSavedForecastsAndFitsWeChatLimit(t *testing.T) {
	r := articleTestReport(10)
	predictions := make(map[string]selection.DayRangeForecast, len(r.Ranking))
	for _, candidate := range r.Ranking {
		predictions[candidate.Code] = selection.DayRangeForecast{
			Code: candidate.Code, TargetDate: "2026-10-22", ReferencePrice: 15.15,
			PointHigh: 16.20, HighLower: 15.90, HighUpper: 16.50, HighCoverage: 0.8,
			PointLow: 14.70, LowLower: 14.40, LowUpper: 15.00, LowCoverage: 0.8,
		}
	}
	message, ok, err := BuildPremarketArticle(PremarketArticleInput{
		TargetDate: "2026-10-22", GeneratedAt: time.Date(2026, 10, 22, 8, 45, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)),
		InputThrough: "2026-10-21", PoolSize: 193, Predictions: predictions,
		Names: map[string]string{r.Ranking[0].Code: "亿联网络"},
	})
	if err != nil || !ok {
		t.Fatalf("build fallback article: ok=%v err=%v", ok, err)
	}
	body := message.Article.Body
	if !strings.Contains(body, "精排队列尚未就绪") || !strings.Contains(body, "YLWL（sz300628）") {
		t.Fatalf("fallback article missing caveat or masked label:\n%s", body)
	}
	if strings.Contains(body, "亿联网络") {
		t.Fatalf("fallback article leaked stock name:\n%s", body)
	}
	rendered, err := wechat.RenderHTML(body)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(rendered)) >= 20000 || len(rendered) >= 1<<20 {
		t.Fatalf("rendered article exceeds WeChat limit: runes=%d bytes=%d", len([]rune(rendered)), len(rendered))
	}
	if got := strings.Count(rendered, "<section style=\"margin:20px 0;"); got != 10 {
		t.Fatalf("rendered cards = %d, want 10", got)
	}
}
