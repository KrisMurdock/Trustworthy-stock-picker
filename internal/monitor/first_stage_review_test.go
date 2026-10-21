package monitor

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func TestFirstStageReviewWindowUsesThreeTradingDays(t *testing.T) {
	c := config.Defaults()
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	signal, horizon, err := firstStageReviewWindow(calendar.New(c), "2026-10-21")
	if err != nil {
		t.Fatal(err)
	}
	if signal != "2026-10-16" || !reflect.DeepEqual(horizon, []string{"2026-10-19", "2026-10-20", "2026-10-21"}) {
		t.Fatalf("signal=%s horizon=%v", signal, horizon)
	}
}

func TestFirstStageReviewPoolUsesReportUniverse(t *testing.T) {
	report := &firststage.Report{
		PoolSize: 3,
		Candidates: []firststage.Candidate{
			{Rank: 1, Code: "sh600001", Name: "候选", Sector: "制造"},
		},
		Excluded: []firststage.Excluded{
			{Code: "sz000002", Reason: "outside_budget"},
			{Code: "sz000003", Reason: "outside_budget"},
		},
	}
	pool, err := firstStageReviewPool(report)
	if err != nil {
		t.Fatal(err)
	}
	want := []config.Stock{{Code: "sh600001", Name: "候选", Sector: "制造"}, {Code: "sz000002"}, {Code: "sz000003"}}
	if !reflect.DeepEqual(pool, want) {
		t.Fatalf("pool=%+v want=%+v", pool, want)
	}
}

func TestFirstStageReviewUsesPreviousReportAndEnqueuesOnce(t *testing.T) {
	c := config.Defaults()
	c.RequireCIFS = false
	c.Daily.Adjust = "qfq"
	c.Daily.HistoryDays = 90
	c.Stocks = []config.Stock{
		{Code: "sz000001", Name: "测试一", ListedFrom: "2026-01-01"},
		{Code: "sz000002", Name: "测试二", ListedFrom: "2026-01-01"},
	}
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.FirstStage.Enabled = true
	c.FirstStage.PushEnabled = true
	c.Notify.Channels = []config.NotifyChannel{
		{Name: "first-stage-bot", Type: "feishu", Enabled: true, Route: config.NotifyRouteFirstStage},
		{Name: "selection-bot", Type: "feishu", Enabled: true, Route: config.NotifyRouteSelection},
	}
	c.Strategy.Enabled = false
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db, err := storage.InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	firstStage := &fakeChannel{name: "first-stage-bot"}
	selection := &fakeChannel{name: "selection-bot"}
	m := NewWithMarketDB(c, store, db, &fakeSource{}, []notify.Channel{firstStage, selection}, nil)
	m.Now = func() time.Time {
		return time.Date(2026, 10, 13, 19, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	}
	previous, err := m.Sync(context.Background(), "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	if previous.FirstStage == nil || len(previous.FirstStage.Candidates) == 0 || !previous.FirstStage.PushEnabled {
		t.Fatalf("previous first-stage report is incomplete: %+v", previous.FirstStage)
	}
	m.Now = func() time.Time {
		return time.Date(2026, 10, 16, 19, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	}
	if _, err := m.Sync(context.Background(), "2026-10-16"); err != nil {
		t.Fatal(err)
	}

	if err := m.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	eventID := "first-stage-review:2026-10-13:2026-10-16"
	event, ok := state.Events[eventID]
	if !ok {
		t.Fatalf("review event missing: %+v", state.Events)
	}
	if event.Message.Title == "" || !strings.Contains(event.Message.Title, "3日触达召回率") {
		t.Fatalf("unexpected review title: %q", event.Message.Title)
	}
	if _, ok := event.Channels["first-stage-bot"]; !ok {
		t.Fatalf("review was not routed to first-stage bot: %+v", event.Channels)
	}
	if _, ok := event.Channels["selection-bot"]; ok {
		t.Fatalf("review leaked to selection bot: %+v", event.Channels)
	}
	if got := eventChannelState(t, state, eventID, "first-stage-bot").Status; got != notify.Delivered {
		t.Fatalf("review status = %s", got)
	}
	sent := firstStage.calls
	if err := m.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if firstStage.calls != sent {
		t.Fatalf("review was delivered twice: calls=%d want=%d", firstStage.calls, sent)
	}
}
