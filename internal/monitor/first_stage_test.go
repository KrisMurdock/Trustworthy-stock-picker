package monitor

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	reportmessage "github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify/report"
)

func TestMonitorChannelNamesUseStageRoutes(t *testing.T) {
	c := config.Defaults()
	c.Notify.Channels = []config.NotifyChannel{
		{Name: "first-stage-bot", Type: "feishu", Enabled: true, Route: config.NotifyRouteFirstStage},
		{Name: "second-stage-bot", Type: "feishu", Enabled: true, Route: config.NotifyRouteSecondStage},
		{Name: "selection-bot", Type: "feishu", Enabled: true, Route: config.NotifyRouteSelection},
		{Name: "predictor-bot", Type: "feishu", Enabled: true, Route: config.NotifyRoutePredictorRealtime},
	}
	m := &Monitor{Config: c, Channels: []notify.Channel{
		&fakeChannel{name: "first-stage-bot"},
		&fakeChannel{name: "second-stage-bot"},
		&fakeChannel{name: "selection-bot"},
		&fakeChannel{name: "predictor-bot"},
	}}
	if got := m.channelNamesForRoute(config.NotifyRouteFirstStage); len(got) != 1 || got[0] != "first-stage-bot" {
		t.Fatalf("first-stage route = %v", got)
	}
	if got := m.channelNamesForRoute(config.NotifyRouteSelection); len(got) != 1 || got[0] != "selection-bot" {
		t.Fatalf("selection route = %v", got)
	}
	if got := m.channelNamesForRoute(config.NotifyRouteSecondStage); len(got) != 1 || got[0] != "second-stage-bot" {
		t.Fatalf("second-stage route = %v", got)
	}
	if got := m.channelNamesForRoute(config.NotifyRoutePredictorDayrange); len(got) != 0 {
		t.Fatalf("unexpected dayrange route = %v", got)
	}
}

func TestMonitorChannelNamesMatchMultipleStageRoutes(t *testing.T) {
	c := config.Defaults()
	c.Notify.Channels = []config.NotifyChannel{
		{Name: config.WechatDraftChannelName, Type: "wechat", Enabled: true, Route: config.NotifyRouteSecondStage + ", " + config.NotifyRouteWechatPremarket},
		{Name: "selection-bot", Type: "feishu", Enabled: true, Route: config.NotifyRouteSelection},
	}
	m := &Monitor{Config: c, Channels: []notify.Channel{
		&fakeChannel{name: config.WechatDraftChannelName},
		&fakeChannel{name: "selection-bot"},
	}}
	for _, route := range []string{config.NotifyRouteSecondStage, config.NotifyRouteWechatPremarket} {
		if got := m.channelNamesForRoute(route); len(got) != 1 || got[0] != config.WechatDraftChannelName {
			t.Fatalf("%s route = %v", route, got)
		}
	}
	if got := m.channelNamesForRoute(config.NotifyRouteSelection); len(got) != 1 || got[0] != "selection-bot" {
		t.Fatalf("selection route = %v", got)
	}
}

func TestEnqueueCardsRoutesEachStageSeparately(t *testing.T) {
	now := time.Date(2026, 10, 21, 19, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	expires := now.Add(14 * time.Hour)
	cards := reportmessage.Cards{
		Main: notify.Message{Version: 2, EventID: "report:main", Title: "main", ExpiresAt: expires},
		Details: []notify.Message{
			{Version: 2, EventID: "report:detail:01", Title: "detail 1", ExpiresAt: expires},
			{Version: 2, EventID: "report:detail:02", Title: "detail 2", ExpiresAt: expires},
		},
		FirstStage: []notify.Message{
			{Version: 2, EventID: "report:first-stage:01", Title: "first 1", ExpiresAt: expires},
			{Version: 2, EventID: "report:first-stage:02", Title: "first 2", ExpiresAt: expires},
		},
		SecondStage: []notify.Message{
			{Version: 2, EventID: "report:second-stage:article", Title: "article", ExpiresAt: expires},
		},
	}
	state := notify.NewLedger()
	if err := enqueueCards(&state, "2026-10-21", cards, []string{"selection-bot"}, []string{"first-stage-bot"}, []string{"second-stage-bot"}); err != nil {
		t.Fatal(err)
	}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	due := state.Due(now)
	want := []string{"report:main", "report:detail:01", "report:detail:02", "report:first-stage:01", "report:first-stage:02", "report:second-stage:article"}
	if len(due) != len(want) {
		t.Fatalf("due = %+v", due)
	}
	for i, eventID := range want {
		if due[i].EventID != eventID {
			t.Fatalf("delivery %d = %s, want %s", i, due[i].EventID, eventID)
		}
	}
	if !state.CanDeliver("report:first-stage:01", "first-stage-bot") {
		t.Fatal("first-stage page incorrectly depends on the selection channel")
	}
	if state.CanDeliver("report:first-stage:02", "first-stage-bot") {
		t.Fatal("second first-stage page delivered before the first page")
	}
	for _, eventID := range []string{"report:main", "report:detail:01", "report:detail:02"} {
		if _, ok := state.Events[eventID].Channels["selection-bot"]; !ok {
			t.Fatalf("selection event %s missing its channel", eventID)
		}
		if _, ok := state.Events[eventID].Channels["first-stage-bot"]; ok {
			t.Fatalf("selection event %s leaked to the first-stage channel", eventID)
		}
	}
	for _, eventID := range []string{"report:first-stage:01", "report:first-stage:02"} {
		if _, ok := state.Events[eventID].Channels["first-stage-bot"]; !ok {
			t.Fatalf("first-stage event %s missing its channel", eventID)
		}
		if _, ok := state.Events[eventID].Channels["selection-bot"]; ok {
			t.Fatalf("first-stage event %s leaked to the selection channel", eventID)
		}
	}
	if _, ok := state.Events["report:second-stage:article"].Channels["second-stage-bot"]; !ok {
		t.Fatal("second-stage article missing its channel")
	}
	if _, ok := state.Events["report:second-stage:article"].Channels["selection-bot"]; ok {
		t.Fatal("second-stage article leaked to the selection channel")
	}
	if err := markCardsDelivered(&state, cards, "selection-bot"); err != nil {
		t.Fatal(err)
	}
	for _, eventID := range want[:3] {
		if got := eventChannelState(t, state, eventID, "selection-bot"); got.Status != notify.Delivered {
			t.Fatalf("event %s status = %s", eventID, got.Status)
		}
	}
	if !state.CanDeliver("report:first-stage:01", "first-stage-bot") {
		t.Fatal("first-stage delivery was marked by the selection channel")
	}
	if err := markCardsDelivered(&state, cards, "first-stage-bot"); err != nil {
		t.Fatal(err)
	}
	for _, eventID := range want[3:5] {
		if got := eventChannelState(t, state, eventID, "first-stage-bot"); got.Status != notify.Delivered {
			t.Fatalf("event %s status = %s", eventID, got.Status)
		}
	}
	if err := markCardsDelivered(&state, cards, "second-stage-bot"); err != nil {
		t.Fatal(err)
	}
	if got := eventChannelState(t, state, "report:second-stage:article", "second-stage-bot"); got.Status != notify.Delivered {
		t.Fatalf("second-stage article status = %s", got.Status)
	}
}

func TestEnqueueReportQueuesSecondStageArticleOnItsOwnRoute(t *testing.T) {
	c := config.Defaults()
	c.Notify.Channels = []config.NotifyChannel{{Name: "second-stage-bot", Type: "feishu", Enabled: true, Route: config.NotifyRouteSecondStage}}
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	channel := &fakeChannel{name: "second-stage-bot"}
	m := New(c, store, &fakeSource{}, []notify.Channel{channel}, nil)
	now, _ := time.ParseInLocation("2006-01-02 15:04", "2026-10-21 19:00", time.FixedZone("Asia/Shanghai", 8*60*60))
	m.Now = func() time.Time { return now }
	report := selection.Report{
		Version: 3, ID: "report-id", AsOf: "2026-10-21", Target: "2026-10-22",
		SelectionEnabled: true, Adjust: "qfq", PoolSize: 1, Eligible: 1,
		FirstStage: &firststage.Report{
			Status: firststage.StatusOK, AsOf: "2026-10-21", Target: "2026-10-22",
			ModelID: firststage.ModelID, PoolSize: 1, Eligible: 1, PushEnabled: false,
		},
		SecondStage: &secondstage.Report{
			Version: secondstage.ReportSchemaVersion, Status: secondstage.StatusOK,
			AsOf: "2026-10-21", Target: "2026-10-22", ModelID: "second_stage_gbdt_v1",
			PushEnabled: true, InputCount: 1,
			Candidates: []secondstage.Candidate{{Rank: 1, Code: "sz000001", Name: "测试股票"}},
		},
	}
	state := notify.NewLedger()
	queued, err := m.enqueueReport(&state, report)
	if err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued = %d, want 1", queued)
	}
	article, ok := state.Events["report-id:second-stage:article"]
	if !ok {
		t.Fatalf("second-stage article not queued: %+v", state.Events)
	}
	if _, ok := article.Channels["second-stage-bot"]; !ok || len(article.Channels) != 1 {
		t.Fatalf("article routed to unexpected channels: %+v", article.Channels)
	}
	if article.Message.PlainText == "" {
		t.Fatal("queued article is not plain text")
	}
}
