package monitor

import (
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	reportmessage "github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify/report"
)

func TestEnqueueCardsOrdersFirstStageAfterReportDetails(t *testing.T) {
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
	if err := enqueueCards(&state, "2026-10-21", cards, []string{"feishu-main"}); err != nil {
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
	if state.CanDeliver("report:first-stage:01", "feishu-main") {
		t.Fatal("first-stage page delivered before report details")
	}
	for _, eventID := range want[:3] {
		if err := state.MarkDelivered(eventID, "feishu-main"); err != nil {
			t.Fatal(err)
		}
	}
	if !state.CanDeliver("report:first-stage:01", "feishu-main") {
		t.Fatal("first-stage page did not follow the detail chain")
	}
	if err := markCardsDelivered(&state, cards, "feishu-main"); err != nil {
		t.Fatal(err)
	}
	for _, eventID := range want {
		if got := eventChannelState(t, state, eventID, "feishu-main"); got.Status != notify.Delivered {
			t.Fatalf("event %s status = %s", eventID, got.Status)
		}
	}
}
