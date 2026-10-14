package notify

import (
	"testing"
	"time"
)

func TestLedgerEnqueueAndIndependentChannelStates(t *testing.T) {
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	message := Message{Version: 1, EventID: "event", Title: "title", Sections: []Section{{Lines: []string{"line"}}}, ExpiresAt: now.Add(14 * time.Hour)}
	ledger := NewLedger()
	if err := ledger.Enqueue("2026-10-13", message, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkDelivered(message.EventID, "a"); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkFailure(message.EventID, "b", &DeliveryError{Kind: Retryable, Reason: "temporary"}, now); err != nil {
		t.Fatal(err)
	}
	due := ledger.Due(now.Add(3 * time.Minute))
	if len(due) != 1 || due[0].Channel != "b" {
		t.Fatalf("unexpected due deliveries: %+v", due)
	}
}

func TestLedgerNormalizesInFlightAndExpires(t *testing.T) {
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	message := Message{Version: 1, EventID: "event", Sections: []Section{{Lines: []string{"line"}}}, ExpiresAt: now.Add(time.Hour)}
	ledger := NewLedger()
	if err := ledger.Enqueue("2026-10-13", message, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkInFlight(message.EventID, "a", now); err != nil {
		t.Fatal(err)
	}
	if !ledger.Normalize(now) {
		t.Fatal("in-flight state was not normalized")
	}
	state := ledger.Events[message.EventID].Channels["a"]
	if state.Status != Unknowned || !state.PossiblyDuplicate {
		t.Fatalf("unexpected recovered state: %+v", state)
	}
	if !ledger.Expire(now.Add(2 * time.Hour)) {
		t.Fatal("event was not expired")
	}
	if got := ledger.Events[message.EventID].Channels["a"].Status; got != Expired {
		t.Fatalf("unexpected expiry state %s", got)
	}
}

func TestLedgerPrunesOldTerminalEvents(t *testing.T) {
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	message := Message{Version: 1, EventID: "event", Sections: []Section{{Lines: []string{"line"}}}, ExpiresAt: now.Add(-31 * 24 * time.Hour)}
	ledger := NewLedger()
	if err := ledger.Enqueue("2026-09-12", message, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkDelivered(message.EventID, "a"); err != nil {
		t.Fatal(err)
	}
	if !ledger.Prune(now, 30*24*time.Hour) {
		t.Fatal("old terminal event was not pruned")
	}
	if len(ledger.Events) != 0 {
		t.Fatal("pruned event remains in ledger")
	}
}
