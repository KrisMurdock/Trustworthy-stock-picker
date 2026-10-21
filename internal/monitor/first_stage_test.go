package monitor

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	reportmessage "github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify/report"
)

func TestMonitorChannelNamesUseStageRoutes(t *testing.T) {
	c := config.Defaults()
	c.Notify.Channels = []config.NotifyChannel{
		{Name: "first-stage-bot", Type: "feishu", Enabled: true, Route: config.NotifyRouteFirstStage},
		{Name: "selection-bot", Type: "feishu", Enabled: true, Route: config.NotifyRouteSelection},
		{Name: "predictor-bot", Type: "feishu", Enabled: true, Route: config.NotifyRoutePredictorRealtime},
	}
	m := &Monitor{Config: c, Channels: []notify.Channel{
		&fakeChannel{name: "first-stage-bot"},
		&fakeChannel{name: "selection-bot"},
		&fakeChannel{name: "predictor-bot"},
	}}
	if got := m.channelNamesForRoute(config.NotifyRouteFirstStage); len(got) != 1 || got[0] != "first-stage-bot" {
		t.Fatalf("first-stage route = %v", got)
	}
	if got := m.channelNamesForRoute(config.NotifyRouteSelection); len(got) != 1 || got[0] != "selection-bot" {
		t.Fatalf("selection route = %v", got)
	}
	if got := m.channelNamesForRoute(config.NotifyRoutePredictorDayrange); len(got) != 0 {
		t.Fatalf("unexpected dayrange route = %v", got)
	}
}

func TestEnqueueCardsRoutesFirstStageAndSelectionSeparately(t *testing.T) {
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
	}
	state := notify.NewLedger()
	if err := enqueueCards(&state, "2026-10-21", cards, []string{"selection-bot"}, []string{"first-stage-bot"}); err != nil {
		t.Fatal(err)
	}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	due := state.Due(now)
	want := []string{"report:main", "report:detail:01", "report:detail:02", "report:first-stage:01", "report:first-stage:02"}
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
	for _, eventID := range want[3:] {
		if got := eventChannelState(t, state, eventID, "first-stage-bot"); got.Status != notify.Delivered {
			t.Fatalf("event %s status = %s", eventID, got.Status)
		}
	}
}
