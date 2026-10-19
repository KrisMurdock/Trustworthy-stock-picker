package notify

import (
	"encoding/json"
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

func TestLedgerEnqueueAndValidateSupportsMessageVersions(t *testing.T) {
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	for _, test := range []struct {
		name    string
		version int
	}{
		{name: "v1", version: 1},
		{name: "v2", version: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := Message{Version: test.version, EventID: "event", Title: "title", ExpiresAt: now.Add(time.Hour)}
			ledger := NewLedger()
			if err := ledger.Enqueue("2026-10-13", message, []string{"a"}); err != nil {
				t.Fatalf("enqueue message version %d: %v", test.version, err)
			}
			if ledger.Version != 2 {
				t.Fatalf("ledger schema version = %d, want 2", ledger.Version)
			}
			if err := ledger.Validate(); err != nil {
				t.Fatalf("validate message version %d: %v", test.version, err)
			}
		})
	}
}

func TestLedgerRejectsUnsupportedMessageVersions(t *testing.T) {
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	for _, test := range []struct {
		name    string
		version int
	}{
		{name: "version_0", version: 0},
		{name: "version_3", version: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := Message{Version: test.version, EventID: "event", ExpiresAt: now.Add(time.Hour)}
			ledger := NewLedger()
			if err := ledger.Enqueue("2026-10-13", message, []string{"a"}); err == nil {
				t.Fatalf("enqueue accepted unsupported message version %d", test.version)
			}

			persisted := NewLedger()
			persisted.Events[message.EventID] = Event{
				AsOf:      "2026-10-13",
				ExpiresAt: message.ExpiresAt,
				Message:   message,
				Channels:  map[string]ChannelState{"a": {Status: Pending}},
			}
			if err := persisted.Validate(); err == nil {
				t.Fatalf("validate accepted unsupported message version %d", test.version)
			}
		})
	}
}

func TestSectionPreservesStructuredFieldsAndLegacyLines(t *testing.T) {
	input := []byte(`{"heading":"指标","lines":["旧格式文本"],"fields":[{"label":"收盘","value":"10.25"}],"optional":true}`)
	var section Section
	if err := json.Unmarshal(input, &section); err != nil {
		t.Fatalf("unmarshal section: %v", err)
	}
	encoded, err := json.Marshal(section)
	if err != nil {
		t.Fatalf("marshal section: %v", err)
	}
	var got struct {
		Heading string `json:"heading"`
		Lines   []string
		Fields  []struct {
			Label string `json:"label"`
			Value string `json:"value"`
		} `json:"fields"`
		Optional bool `json:"optional"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal encoded section: %v", err)
	}
	if got.Heading != "指标" || len(got.Lines) != 1 || got.Lines[0] != "旧格式文本" {
		t.Fatalf("legacy section content changed during round trip: %+v", got)
	}
	if len(got.Fields) != 1 || got.Fields[0].Label != "收盘" || got.Fields[0].Value != "10.25" || !got.Optional {
		t.Fatalf("structured section content was not preserved: %+v", got)
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

func TestLedgerOrdersDependentCardParts(t *testing.T) {
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	expires := now.Add(time.Hour)
	main := Message{Version: 2, EventID: "z:main", Title: "main", ExpiresAt: expires}
	detail1 := Message{Version: 2, EventID: "a:detail:01", Title: "detail 1", ExpiresAt: expires}
	detail2 := Message{Version: 2, EventID: "a:detail:02", Title: "detail 2", ExpiresAt: expires}
	ledger := NewLedger()
	if err := ledger.EnqueuePart("2026-10-13", main, []string{"a"}, "main", 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := ledger.EnqueuePart("2026-10-13", detail1, []string{"a"}, "detail", 1, main.EventID); err != nil {
		t.Fatal(err)
	}
	if err := ledger.EnqueuePart("2026-10-13", detail2, []string{"a"}, "detail", 2, detail1.EventID); err != nil {
		t.Fatal(err)
	}
	due := ledger.Due(now)
	if len(due) != 3 || due[0].EventID != main.EventID || due[1].EventID != detail1.EventID || due[2].EventID != detail2.EventID {
		t.Fatalf("unexpected delivery order: %+v", due)
	}
	if !ledger.CanDeliver(main.EventID, "a") || ledger.CanDeliver(detail1.EventID, "a") || ledger.CanDeliver(detail2.EventID, "a") {
		t.Fatal("dependencies were not enforced before main delivery")
	}
	if err := ledger.MarkDelivered(main.EventID, "a"); err != nil {
		t.Fatal(err)
	}
	if !ledger.CanDeliver(detail1.EventID, "a") || ledger.CanDeliver(detail2.EventID, "a") {
		t.Fatal("detail 1 should depend only on main")
	}
	if err := ledger.MarkDelivered(detail1.EventID, "a"); err != nil {
		t.Fatal(err)
	}
	if !ledger.CanDeliver(detail2.EventID, "a") {
		t.Fatal("detail 2 should be deliverable after detail 1")
	}
	if err := ledger.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerKeepsDependencyUntilDependentIsTerminal(t *testing.T) {
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	expires := now.Add(-31 * 24 * time.Hour)
	main := Message{Version: 2, EventID: "main", Title: "main", ExpiresAt: expires}
	detail := Message{Version: 2, EventID: "detail", Title: "detail", ExpiresAt: expires}
	ledger := NewLedger()
	if err := ledger.EnqueuePart("2026-09-12", main, []string{"a"}, "main", 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := ledger.EnqueuePart("2026-09-12", detail, []string{"a"}, "detail", 1, main.EventID); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkDelivered(main.EventID, "a"); err != nil {
		t.Fatal(err)
	}
	ledger.Prune(now, 30*24*time.Hour)
	if _, ok := ledger.Events[main.EventID]; !ok {
		t.Fatal("dependency was pruned while detail was still pending")
	}
	if err := ledger.MarkDelivered(detail.EventID, "a"); err != nil {
		t.Fatal(err)
	}
	if !ledger.Prune(now, 30*24*time.Hour) {
		t.Fatal("terminal card family was not pruned")
	}
	if len(ledger.Events) != 0 {
		t.Fatalf("terminal card family remains: %+v", ledger.Events)
	}
}

func TestLedgerOrdersCardFamiliesByDateBeforeSequence(t *testing.T) {
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	expires := now.Add(24 * time.Hour)
	ledger := NewLedger()
	for _, card := range []struct {
		asof     string
		eventID  string
		sequence int
		depends  string
	}{
		{"2026-10-13", "second:main", 0, ""},
		{"2026-10-13", "second:detail:01", 1, "second:main"},
		{"2026-10-12", "first:main", 0, ""},
		{"2026-10-12", "first:detail:01", 1, "first:main"},
	} {
		message := Message{Version: 2, EventID: card.eventID, Title: card.eventID, ExpiresAt: expires}
		if err := ledger.EnqueuePart(card.asof, message, []string{"a"}, "card", card.sequence, card.depends); err != nil {
			t.Fatal(err)
		}
	}
	due := ledger.Due(now)
	want := []string{"first:main", "first:detail:01", "second:main", "second:detail:01"}
	if len(due) != len(want) {
		t.Fatalf("due deliveries = %+v", due)
	}
	for index, eventID := range want {
		if due[index].EventID != eventID {
			t.Fatalf("delivery %d = %s, want %s", index, due[index].EventID, eventID)
		}
	}
}
