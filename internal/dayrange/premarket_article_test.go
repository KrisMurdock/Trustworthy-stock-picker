package dayrange

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
)

func TestPremarketArticlePublishesOnlyToWechatAndIsIdempotent(t *testing.T) {
	c := deliveryServiceConfig()
	c.Wechat.Enabled = true
	now := time.Date(2026, 10, 21, 8, 45, 0, 0, shanghai)
	service := NewService(c, newFakeDayRangeStore(), fakeExecutionMarket{report: executionTestReport()}, nil)
	service.Now = func() time.Time { return now }
	state := &fakeDeliveryStore{}
	feishuChannel := &fakeDayRangeChannel{name: "predictor-dayrange"}
	wechatChannel := &fakeDayRangeChannel{name: config.WechatDraftChannelName}
	if err := service.ConfigureDelivery(state, []notify.Channel{feishuChannel, wechatChannel}); err != nil {
		t.Fatal(err)
	}
	predictions := executionTestPredictions()
	if err := service.publishPremarketArticle(context.Background(), "2026-10-21", predictions, nil, now, now); err != nil {
		t.Fatal(err)
	}
	if len(wechatChannel.sent) != 1 || len(feishuChannel.sent) != 0 {
		t.Fatalf("route isolation failed: wechat=%d feishu=%d", len(wechatChannel.sent), len(feishuChannel.sent))
	}
	article := wechatChannel.sent[0]
	if article.Article == nil || article.EventID != "dayrange:premarket-article:2026-10-21" {
		t.Fatalf("unexpected article delivery: %+v", article)
	}
	expires := time.Date(2026, 10, 21, 9, 30, 0, 0, shanghai)
	if !article.ExpiresAt.Equal(expires) {
		t.Fatalf("expiry=%s want %s", article.ExpiresAt, expires)
	}
	if !strings.Contains(article.Article.Body, "可执行") || !strings.Contains(article.Article.Body, "CSYH（sz000001）") {
		t.Fatalf("article missing execution status or masked name:\n%s", article.Article.Body)
	}
	if strings.Contains(article.Article.Body, "测试银行") || strings.Contains(article.Article.Body, "测试制造") {
		t.Fatalf("article leaked a Chinese stock name:\n%s", article.Article.Body)
	}
	if len(service.ledger.Events) != 1 || service.ledger.Events[article.EventID].Channels[config.WechatDraftChannelName].Status != notify.Delivered {
		t.Fatalf("unexpected article ledger: %+v", service.ledger.Events)
	}
	if err := service.publishPremarketArticle(context.Background(), "2026-10-21", predictions, nil, now, now); err != nil {
		t.Fatal(err)
	}
	if len(wechatChannel.sent) != 1 || len(service.ledger.Events) != 1 {
		t.Fatalf("duplicate article: sent=%d events=%d", len(wechatChannel.sent), len(service.ledger.Events))
	}
}

func TestSavedPremarketPublishesFeishuCardsAndWechatDraft(t *testing.T) {
	c := deliveryServiceConfig()
	c.Wechat.Enabled = true
	now := time.Date(2026, 10, 21, 8, 45, 0, 0, shanghai)
	service := NewService(c, newFakeDayRangeStore(), fakeExecutionMarket{report: executionTestReport()}, nil)
	service.Now = func() time.Time { return now }
	state := &fakeDeliveryStore{}
	feishuChannel := &fakeDayRangeChannel{name: "predictor-dayrange"}
	wechatChannel := &fakeDayRangeChannel{name: config.WechatDraftChannelName}
	if err := service.ConfigureDelivery(state, []notify.Channel{feishuChannel, wechatChannel}); err != nil {
		t.Fatal(err)
	}
	records := make([]PredictionRecord, 0, 2)
	for _, prediction := range executionTestPredictions() {
		records = append(records, PredictionRecord{Prediction: prediction})
	}
	if err := service.publishSavedPremarket(context.Background(), "2026-10-21", records, nil, now); err != nil {
		t.Fatal(err)
	}
	if len(feishuChannel.sent) != 1 || len(wechatChannel.sent) != 1 {
		t.Fatalf("premarket delivery failed: feishu=%d wechat=%d", len(feishuChannel.sent), len(wechatChannel.sent))
	}
	if feishuChannel.sent[0].Article != nil || wechatChannel.sent[0].Article == nil {
		t.Fatalf("message types were mixed: feishu=%+v wechat=%+v", feishuChannel.sent[0], wechatChannel.sent[0])
	}
}

func TestPremarketArticleFallsBackWhenRankingIsMissing(t *testing.T) {
	c := deliveryServiceConfig()
	c.Wechat.Enabled = true
	now := time.Date(2026, 10, 21, 8, 45, 0, 0, shanghai)
	service := NewService(c, newFakeDayRangeStore(), fakeExecutionMarket{err: fs.ErrNotExist}, nil)
	service.Now = func() time.Time { return now }
	state := &fakeDeliveryStore{}
	wechatChannel := &fakeDayRangeChannel{name: config.WechatDraftChannelName}
	if err := service.ConfigureDelivery(state, []notify.Channel{wechatChannel}); err != nil {
		t.Fatal(err)
	}
	if err := service.publishPremarketArticle(context.Background(), "2026-10-21", executionTestPredictions(), nil, now, now); err != nil {
		t.Fatal(err)
	}
	if len(wechatChannel.sent) != 1 || wechatChannel.sent[0].Article == nil {
		t.Fatalf("fallback article not sent: %+v", wechatChannel.sent)
	}
	if !strings.Contains(wechatChannel.sent[0].Article.Body, "精排队列尚未就绪") {
		t.Fatalf("fallback caveat missing:\n%s", wechatChannel.sent[0].Article.Body)
	}
}

func TestPremarketArticleHonorsPushSwitch(t *testing.T) {
	c := deliveryServiceConfig()
	c.Wechat.Enabled = true
	c.DayRange.PushEnabled = false
	now := time.Date(2026, 10, 21, 8, 45, 0, 0, shanghai)
	service := NewService(c, newFakeDayRangeStore(), fakeExecutionMarket{report: executionTestReport()}, nil)
	service.Now = func() time.Time { return now }
	state := &fakeDeliveryStore{}
	wechatChannel := &fakeDayRangeChannel{name: config.WechatDraftChannelName}
	if err := service.ConfigureDelivery(state, []notify.Channel{wechatChannel}); err != nil {
		t.Fatal(err)
	}
	if err := service.publishPremarketArticle(context.Background(), "2026-10-21", executionTestPredictions(), nil, now, now); err != nil {
		t.Fatal(err)
	}
	if len(wechatChannel.sent) != 0 || len(service.ledger.Events) != 0 || state.writes != 0 {
		t.Fatalf("disabled push created draft: sent=%d events=%d writes=%d", len(wechatChannel.sent), len(service.ledger.Events), state.writes)
	}
}
