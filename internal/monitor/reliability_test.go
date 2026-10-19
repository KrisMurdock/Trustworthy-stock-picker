package monitor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"stock-watcher/internal/market"
	"stock-watcher/internal/notify"
	"stock-watcher/internal/storage"
	"testing"
	"time"
)

func TestStatusNeverWritesOrRecoversInFlight(t *testing.T) {
	m, _, ch := fixture(t)
	root := t.TempDir()
	writer, err := storage.Open(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	m.Store = writer
	state := notify.NewLedger()
	msg := notify.Message{Version: 1, EventID: "read-only", Title: "test", Sections: []notify.Section{{Lines: []string{"test"}}}, ExpiresAt: m.Now().Add(time.Hour)}
	if err := state.Enqueue("2026-10-13", msg, []string{ch.name}); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkInFlight(msg.EventID, ch.name, m.Now()); err != nil {
		t.Fatal(err)
	}
	if err := m.writeState(state); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "state/delivery.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	view, err := storage.OpenView(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	reader := New(m.Config, view, nil, nil, nil)
	reader.Now = m.Now
	got, err := reader.Status()
	if err != nil {
		t.Fatal(err)
	}
	if channelState(t, got, ch.name).Status != notify.InFlight {
		t.Fatal("status query recovered a live sender")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("status changed ledger bytes")
	}
}

func TestFlushRechecksExpiryBetweenChannels(t *testing.T) {
	m, _, a := fixture(t)
	a.name = "a"
	now := time.Date(2026, 10, 14, 9, 29, 59, 0, market.Shanghai)
	m.Now = func() time.Time { return now }
	b := &fakeChannel{name: "b"}
	a.cancel = func() { now = now.Add(2 * time.Second) }
	m.Channels = []notify.Channel{a, b}
	state := notify.NewLedger()
	msg := notify.Message{Version: 1, EventID: "expiry", Title: "test", Sections: []notify.Section{{Lines: []string{"test"}}}, ExpiresAt: now.Add(time.Second)}
	if err := state.Enqueue("2026-10-13", msg, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := m.flushState(context.Background(), &state); err != nil {
		t.Fatal(err)
	}
	if a.calls != 1 || b.calls != 0 {
		t.Fatalf("sent after expiry: a=%d b=%d", a.calls, b.calls)
	}
	if channelState(t, state, "b").Status != notify.Expired {
		t.Fatal("unsent channel not expired")
	}
}

type deadlineChannel struct{ called bool }

func (c *deadlineChannel) Name() string { return "deadline" }
func (c *deadlineChannel) Send(ctx context.Context, _ notify.Message) error {
	c.called = true
	<-ctx.Done()
	return ctx.Err()
}
func TestFlushDeadlineRetainsUncertainDelivery(t *testing.T) {
	m, _, _ := fixture(t)
	now := time.Now()
	m.Now = func() time.Time { return now }
	ch := &deadlineChannel{}
	m.Channels = []notify.Channel{ch}
	state := notify.NewLedger()
	msg := notify.Message{Version: 1, EventID: "timeout", Title: "test", Sections: []notify.Section{{Lines: []string{"test"}}}, ExpiresAt: now.Add(10 * time.Millisecond)}
	if err := state.Enqueue("2026-10-13", msg, []string{ch.Name()}); err != nil {
		t.Fatal(err)
	}
	if err := m.flushState(context.Background(), &state); err != context.DeadlineExceeded {
		t.Fatalf("deadline not enforced: %v", err)
	}
	got := channelState(t, state, ch.Name())
	if !ch.called || got.Status != notify.Expired || !got.PossiblyDuplicate {
		t.Fatalf("uncertainty lost: %+v", got)
	}
}
