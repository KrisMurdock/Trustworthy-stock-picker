package monitor

import (
	"context"
	"errors"
	"fmt"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	reportmessage "github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify/report"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"io/fs"
	"sort"
	"strings"
	"testing"
	"time"
)

type fakeSource struct {
	calls      int
	err        error
	failAdjust string
	failCode   string
	holidays   map[string]bool
	fetchHook  func(config.Stock)
}

func (f *fakeSource) Fetch(ctx context.Context, s config.Stock, start, end, adjust string) (market.History, error) {
	f.calls++
	if f.fetchHook != nil {
		f.fetchHook(s)
	}
	if ctx.Err() != nil {
		return market.History{}, ctx.Err()
	}
	if f.err != nil && (f.failCode == "" || f.failCode == s.Code) && (f.failAdjust == "" || f.failAdjust == adjust) {
		return market.History{}, f.err
	}
	bars := []market.Bar{}
	d, _ := time.Parse("2006-01-02", end)
	first, _ := time.Parse("2006-01-02", start)
	for !d.Before(first) {
		date := d.Format("2006-01-02")
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday && !f.holidays[date] {
			bars = append([]market.Bar{{Date: date, Open: 10, Close: 10, High: 11, Low: 9, Volume: 100}}, bars...)
		}
		d = d.AddDate(0, 0, -1)
	}
	field := "qfqday"
	if adjust == "none" {
		field = "day"
	}
	return market.History{Version: 1, Code: s.Code, Name: s.Name, Source: "tencent", DataField: field, VolumeUnit: "provider_raw", FetchedAt: time.Now(), Adjust: adjust, AsOf: end, Bars: bars}, nil
}

type fakeChannel struct {
	name       string
	calls      int
	err        error
	failOnPart string
	cancel     func()
	sent       []notify.Message
}

func (f *fakeChannel) Name() string { return f.name }

func (f *fakeChannel) Send(ctx context.Context, message notify.Message) error {
	f.calls++
	f.sent = append(f.sent, message)
	if f.cancel != nil {
		f.cancel()
		return ctx.Err()
	}
	if f.failOnPart != "" && message.PartLabel != f.failOnPart {
		return nil
	}
	return f.err
}

func fixture(t *testing.T) (*Monitor, *fakeSource, *fakeChannel) {
	t.Helper()
	c := config.Defaults()
	c.RequireCIFS = false
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试股票", ListedFrom: "2026-07-15"}}
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Selection.Enabled = true
	c.Daily.HistoryDays = 90
	c.Daily.Adjust = "none"
	c.Notify.Channels = []config.NotifyChannel{{Name: "fake-main", Type: "feishu", Enabled: true}}
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	source := &fakeSource{}
	channel := &fakeChannel{name: "fake-main"}
	m := New(c, store, source, []notify.Channel{channel}, nil)
	now, _ := time.ParseInLocation("2006-01-02 15:04", "2026-10-13 19:00", market.Shanghai)
	m.Now = func() time.Time { return now }
	return m, source, channel
}

func channelState(t *testing.T, state notify.Ledger, channel string) notify.ChannelState {
	t.Helper()
	for _, event := range state.Events {
		if event.Part == "main" {
			return eventChannelState(t, state, event.Message.EventID, channel)
		}
	}
	if len(state.Events) == 1 {
		for eventID := range state.Events {
			return eventChannelState(t, state, eventID, channel)
		}
	}
	t.Fatal("channel state not found")
	return notify.ChannelState{}
}

func eventChannelState(t *testing.T, state notify.Ledger, eventID, channel string) notify.ChannelState {
	t.Helper()
	event, ok := state.Events[eventID]
	if !ok {
		t.Fatalf("event %s not found", eventID)
	}
	value, ok := event.Channels[channel]
	if !ok {
		t.Fatalf("channel %s not found for event %s", channel, eventID)
	}
	return value
}

func detailEventIDs(t *testing.T, state notify.Ledger) []string {
	t.Helper()
	events := make([]notify.Event, 0)
	for _, event := range state.Events {
		if event.Part == "detail" {
			events = append(events, event)
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Sequence < events[j].Sequence })
	ids := make([]string, len(events))
	for index, event := range events {
		ids[index] = event.Message.EventID
	}
	return ids
}

func TestDurableRetryAndRestartDedupe(t *testing.T) {
	m, source, channel := fixture(t)
	channel.err = &notify.DeliveryError{Kind: notify.Retryable, Reason: "temporary"}
	if err := m.Cycle(context.Background()); err == nil {
		t.Fatal("expected delivery failure")
	}
	state, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	pending := channelState(t, state, channel.name)
	if pending.Status != notify.Pending || pending.Attempts != 1 {
		t.Fatalf("missing durable pending: %+v", pending)
	}
	details := detailEventIDs(t, state)
	if len(details) != 1 || eventChannelState(t, state, details[0], channel.name).Status != notify.Pending {
		t.Fatalf("detail page should wait for the main card: %+v", state.Events)
	}
	if err := m.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if channel.calls != 1 || source.calls != 1 {
		t.Fatal("retry ignored delay or redownloaded")
	}
	restarted := New(m.Config, m.Store, source, []notify.Channel{channel}, nil)
	now := m.Now().Add(3 * time.Minute)
	restarted.Now = func() time.Time { return now }
	channel.err = nil
	if err := restarted.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if channel.calls != 3 || source.calls != 1 {
		t.Fatal("pending not reused after restart")
	}
	for _, eventID := range details {
		if got := eventChannelState(t, state, eventID, channel.name); got.Status != notify.Pending {
			t.Fatalf("detail sent before retry: %+v", got)
		}
	}
	if err := restarted.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if channel.calls != 3 {
		t.Fatal("duplicate recommendation")
	}
}

func TestDetailFailureStopsLaterPagesAndRetriesOnlyFailedPage(t *testing.T) {
	m, _, channel := fixture(t)
	expires := m.Now().Add(14 * time.Hour)
	cards := reportmessage.Cards{
		Main: notify.Message{Version: 2, EventID: "event:main", Title: "main", ExpiresAt: expires},
		Details: []notify.Message{
			{Version: 2, EventID: "event:detail:01", PartLabel: "详情 1/2", Title: "detail 1", ExpiresAt: expires},
			{Version: 2, EventID: "event:detail:02", PartLabel: "详情 2/2", Title: "detail 2", ExpiresAt: expires},
		},
	}
	state := notify.NewLedger()
	if err := enqueueCards(&state, "2026-10-13", cards, []string{channel.name}, []string{channel.name}); err != nil {
		t.Fatal(err)
	}
	channel.failOnPart = "详情 1/2"
	channel.err = &notify.DeliveryError{Kind: notify.Retryable, Reason: "temporary"}
	if err := m.flushState(context.Background(), &state); err == nil {
		t.Fatal("expected detail page failure")
	}
	if channel.calls != 2 {
		t.Fatalf("main and failed detail should be attempted once, calls=%d", channel.calls)
	}
	if got := eventChannelState(t, state, cards.Main.EventID, channel.name); got.Status != notify.Delivered {
		t.Fatalf("main card was not delivered: %+v", got)
	}
	if got := eventChannelState(t, state, cards.Details[0].EventID, channel.name); got.Status != notify.Pending {
		t.Fatalf("failed detail should be pending: %+v", got)
	}
	if got := eventChannelState(t, state, cards.Details[1].EventID, channel.name); got.Status != notify.Pending {
		t.Fatalf("later detail should stay pending: %+v", got)
	}

	now := m.Now().Add(3 * time.Minute)
	m.Now = func() time.Time { return now }
	channel.err = nil
	if err := m.flushState(context.Background(), &state); err != nil {
		t.Fatal(err)
	}
	if channel.calls != 4 {
		t.Fatalf("retry should send only detail 1 then detail 2, calls=%d", channel.calls)
	}
	if channel.sent[2].EventID != cards.Details[0].EventID || channel.sent[3].EventID != cards.Details[1].EventID {
		t.Fatalf("unexpected retry order: %+v", channel.sent)
	}
}

func TestPermanentRetryAndExpiredPending(t *testing.T) {
	m, _, channel := fixture(t)
	channel.err = &notify.DeliveryError{Kind: notify.Permanent, Reason: "permission"}
	if err := m.Cycle(context.Background()); err == nil {
		t.Fatal("permanent failure accepted")
	}
	state, _ := m.Status()
	if got := channelState(t, state, channel.name); got.Status != notify.Blocked {
		t.Fatalf("permanent failure was not blocked: %+v", got)
	}
	if err := m.Cycle(context.Background()); err != nil || channel.calls != 1 {
		t.Fatal("permanent error retried automatically")
	}
	if err := m.Retry(channel.name); err != nil {
		t.Fatal(err)
	}
	channel.err = nil
	if err := m.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _ = m.Status()
	if channel.calls != 3 || channelState(t, state, channel.name).Status != notify.Delivered {
		t.Fatalf("main and detail were not delivered after retry: calls=%d state=%+v", channel.calls, state.Events)
	}
	m, _, channel = fixture(t)
	channel.err = &notify.DeliveryError{Kind: notify.Retryable, Reason: "temporary"}
	_ = m.Cycle(context.Background())
	now := m.Now().Add(15 * time.Hour)
	m.Now = func() time.Time { return now }
	if err := m.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _ = m.Status()
	if got := channelState(t, state, channel.name); got.Status != notify.Expired || channel.calls != 1 {
		t.Fatalf("expired recommendation sent: %+v", got)
	}
	for _, eventID := range detailEventIDs(t, state) {
		if got := eventChannelState(t, state, eventID, channel.name); got.Status != notify.Expired {
			t.Fatalf("detail page did not expire with the main card: %+v", got)
		}
	}
}

func TestNoPartialPoolAndCancellation(t *testing.T) {
	m, source, channel := fixture(t)
	source.err = errors.New("download failed")
	if err := m.Cycle(context.Background()); err == nil || channel.calls != 0 {
		t.Fatal("partial recommendation sent")
	}
	source.err = nil
	ctx, cancel := context.WithCancel(context.Background())
	channel.cancel = cancel
	if err := m.Cycle(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation %v", err)
	}
	state, _ := m.Status()
	if got := channelState(t, state, channel.name); got.Status != notify.Unknowned || got.Attempts != 0 {
		t.Fatalf("cancelled delivery was acknowledged or charged as failure: %+v", got)
	}
}

func TestCorruptStateAndBeforeClose(t *testing.T) {
	m, _, channel := fixture(t)
	_ = m.Store.Write("state/delivery.json", map[string]any{"schema_version": 9})
	if err := m.Cycle(context.Background()); err == nil || channel.calls != 0 {
		t.Fatal("unsupported state reset")
	}
	m, source, _ := fixture(t)
	now := m.Now().Add(-5 * time.Hour)
	m.Now = func() time.Time { return now }
	if err := m.Cycle(context.Background()); err != nil || source.calls != 0 {
		t.Fatal("ran before close")
	}
}

func TestSQLiteSyncPublishesAtConfiguredCoverageThreshold(t *testing.T) {
	m, source, _ := fixture(t)
	m.Config.Stocks = make([]config.Stock, 20)
	for i := range m.Config.Stocks {
		m.Config.Stocks[i] = config.Stock{Code: fmt.Sprintf("sz%06d", i+1), Name: "测试", ListedFrom: "2026-07-15"}
	}
	if m.Config.Sync.MinCoveragePct != 95 {
		t.Fatal("default recommendation gate changed")
	}
	marketDB, err := storage.InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	m.Market = marketDB
	t.Cleanup(func() { m.Market.Close() })
	source.err = &market.Failure{Status: 503, Kind: "status"}
	source.failCode = m.Config.Stocks[0].Code
	report, err := m.Sync(context.Background(), "2026-10-13")
	if err != nil || report.Eligible != 19 {
		t.Fatalf("95%% boundary rejected: %+v %v", report, err)
	}
	stored, err := m.Market.LoadReport(context.Background(), report.AsOf)
	if err != nil || stored.ID != report.ID {
		t.Fatalf("report missing: %+v %v", stored, err)
	}
}

func TestSQLiteSyncDoesNotPublishBelowCoverageThreshold(t *testing.T) {
	m, source, _ := fixture(t)
	m.Config.Sync.MinCoveragePct = 100
	marketDB, err := storage.InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	m.Market = marketDB
	t.Cleanup(func() { m.Market.Close() })
	source.err = &market.Failure{Status: 403, Kind: "status"}
	_, err = m.Sync(context.Background(), "2026-10-13")
	if err == nil || !strings.Contains(err.Error(), "coverage") {
		t.Fatalf("missing coverage gate: %v", err)
	}
	if _, err := m.Market.LoadReport(context.Background(), "2026-10-13"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("below-threshold sync published a report")
	}
}

func TestValidateHistoryEnforcesPublishWindow(t *testing.T) {
	m, _, _ := fixture(t)
	history := market.History{
		Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Adjust: "qfq",
		DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: "2026-10-13", FetchedAt: time.Now(),
		Bars: []market.Bar{
			{Date: "2026-10-10", Open: 10, Close: 10, High: 11, Low: 9, Volume: 100},
			{Date: "2026-10-13", Open: 10, Close: 10, High: 11, Low: 9, Volume: 100},
		},
	}
	if err := validateHistory(m.Calendar, config.Stock{Code: history.Code, Name: history.Name}, history, "2026-10-10", "2026-10-13"); err == nil {
		t.Fatal("accepted internal trading-day gap")
	}
	history.Bars[1].Date = "2026-10-12"
	if err := validateHistory(m.Calendar, config.Stock{Code: history.Code, Name: history.Name}, history, "2026-10-10", "2026-10-13"); err == nil {
		t.Fatal("accepted stale final bar")
	}
	history.DataField = "day"
	history.Bars[1].Date = "2026-10-13"
	if err := validateHistory(m.Calendar, config.Stock{Code: history.Code, Name: history.Name}, history, "2026-10-10", "2026-10-13"); err == nil {
		t.Fatal("accepted qfq fallback field without guard")
	}
}

func TestValidateHistoryRejectsContiguousTruncation(t *testing.T) {
	m, source, _ := fixture(t)
	full, err := source.Fetch(context.Background(), m.Config.Stocks[0], "2026-01-01", "2026-10-13", "qfq")
	if err != nil {
		t.Fatal(err)
	}
	full.Bars = full.Bars[len(full.Bars)-60:]
	if err := validateHistory(m.Calendar, m.Config.Stocks[0], full, "2026-01-01", "2026-10-13"); err == nil {
		t.Fatal("accepted contiguous truncated history")
	}
}

func TestMergeHistoryRespectsRequestedAsOf(t *testing.T) {
	previous := market.History{
		Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Adjust: "none",
		DataField: "day", VolumeUnit: "provider_raw", AsOf: "2026-10-14", FetchedAt: time.Now(),
		Bars: []market.Bar{
			{Date: "2026-10-13", Open: 10, Close: 10, High: 11, Low: 9, Volume: 100},
			{Date: "2026-10-14", Open: 10, Close: 10, High: 11, Low: 9, Volume: 100},
		},
	}
	recent := previous
	recent.AsOf = "2026-10-13"
	recent.Bars = []market.Bar{{Date: "2026-10-13", Open: 11, Close: 11, High: 12, Low: 10, Volume: 120}}
	merged := mergeHistory(previous, recent, config.Stock{Code: "sz000001", Name: "测试"}, "2026-10-10", "2026-10-13")
	if len(merged.Bars) != 1 || merged.Bars[0].Date != "2026-10-13" {
		t.Fatalf("future bar leaked into historical merge: %+v", merged.Bars)
	}
}

func TestStrategySyncRawResearchPersistenceAndRetry(t *testing.T) {
	m, source, sender := fixture(t)
	m.Config.Strategy.Enabled = true
	m.Config.Daily.Adjust = "qfq"
	sender.err = &notify.DeliveryError{Status: 503, Kind: notify.Retryable, Reason: "status"}
	if err := m.Cycle(context.Background()); err == nil {
		t.Fatal("expected failed notification")
	}
	if source.calls != 2 {
		t.Fatal("raw execution history not fetched")
	}
	state, err := m.Status()
	if err != nil || len(state.Events) != 2 {
		t.Fatalf("new report not persisted %+v %v", state, err)
	}
	var event notify.Event
	for _, candidate := range state.Events {
		if candidate.Part == "main" {
			event = candidate
		}
	}
	if event.Message.Version != 2 {
		t.Fatalf("new report message was not persisted: %+v", event.Message)
	}
	preview, err := m.Preview("2026-10-13")
	if err != nil || preview.Research == nil {
		t.Fatalf("new preview mismatch %+v %v", preview, err)
	}
	now := m.Now().Add(3 * time.Minute)
	m.Now = func() time.Time { return now }
	sender.err = nil
	if err := m.Cycle(context.Background()); err != nil || source.calls != 2 || sender.calls != 3 {
		t.Fatal("v2 pending was not reused")
	}
	state, err = m.Status()
	if err != nil {
		t.Fatal(err)
	}
	for eventID := range state.Events {
		if got := eventChannelState(t, state, eventID, sender.name); got.Status != notify.Delivered {
			t.Fatalf("card %s was not delivered: %+v", eventID, got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.PreviewContext(ctx, "2026-10-13"); !errors.Is(err, context.Canceled) {
		t.Fatal("preview cancellation ignored")
	}
}

func TestRawFailureDoesNotPublishAnAdjustedOnlyReport(t *testing.T) {
	m, source, sender := fixture(t)
	m.Config.Strategy.Enabled = true
	m.Config.Daily.Adjust = "qfq"
	source.err, source.failAdjust = errors.New("raw source unavailable"), "none"
	if err := m.Cycle(context.Background()); err == nil || source.calls != 2 || sender.calls != 0 {
		t.Fatal("missing raw prices did not block the whole report")
	}
	var report selection.Report
	if err := m.Store.Read("reports/2026-10-13.json", &report); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("adjusted-only report persisted")
	}
	state, err := m.Status()
	if err != nil || len(state.Events) != 0 {
		t.Fatal("partial data became a pending notification")
	}
}

func TestChannelsDeliverIndependently(t *testing.T) {
	m, source, first := fixture(t)
	second := &fakeChannel{name: "fake-secondary", err: &notify.DeliveryError{Kind: notify.Retryable, Reason: "temporary"}}
	m.Channels = []notify.Channel{first, second}
	if err := m.Cycle(context.Background()); err == nil {
		t.Fatal("expected secondary delivery failure")
	}
	if first.calls != 2 || second.calls != 1 || source.calls != 1 {
		t.Fatalf("unexpected initial fanout: first=%d second=%d source=%d", first.calls, second.calls, source.calls)
	}
	first.calls = 0
	second.calls = 0
	second.err = nil
	m.Now = func() time.Time { return time.Date(2026, 10, 13, 19, 3, 0, 0, market.Shanghai) }
	if err := m.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if first.calls != 0 || second.calls != 2 || source.calls != 1 {
		t.Fatalf("successful channel was resent: first=%d second=%d source=%d", first.calls, second.calls, source.calls)
	}
}

func TestReportOnlyCanBeDeliveredAfterChannelEnablement(t *testing.T) {
	m, source, channel := fixture(t)
	m.Channels = nil
	if err := m.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || channel.calls != 0 {
		t.Fatalf("report-only cycle did not stop after generation: source=%d channel=%d", source.calls, channel.calls)
	}
	state, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Events) != 0 {
		t.Fatal("report-only mode created delivery state")
	}
	m.Channels = []notify.Channel{channel}
	if err := m.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || channel.calls != 2 {
		t.Fatalf("report was not delivered from disk: source=%d channel=%d", source.calls, channel.calls)
	}
}

func TestInFlightStateIsRecoveredAsPossiblyDuplicate(t *testing.T) {
	m, _, channel := fixture(t)
	message := notify.Message{Version: 1, EventID: "event-in-flight", Title: "title", Sections: []notify.Section{{Lines: []string{"line"}}}, ExpiresAt: time.Date(2026, 10, 14, 9, 30, 0, 0, market.Shanghai)}
	state := notify.NewLedger()
	if err := state.Enqueue("2026-10-13", message, []string{channel.name}); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkInFlight(message.EventID, channel.name, m.Now()); err != nil {
		t.Fatal(err)
	}
	if err := m.Store.Write("state/delivery.json", state); err != nil {
		t.Fatal(err)
	}
	restarted := New(m.Config, m.Store, nil, []notify.Channel{channel}, nil)
	restarted.Now = m.Now
	recovered, err := restarted.readState()
	if err != nil {
		t.Fatal(err)
	}
	got := channelState(t, recovered, channel.name)
	if got.Status != notify.Unknowned || !got.PossiblyDuplicate {
		t.Fatalf("in-flight state was not recovered: %+v", got)
	}
	if err := restarted.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if channel.calls != 1 {
		t.Fatal("unknown delivery was not retried")
	}
}

func TestMigratesV1PendingDelivery(t *testing.T) {
	m, _, channel := fixture(t)
	legacy := selection.Report{Version: 1, ID: "legacy-event", AsOf: "2026-10-13", Target: "2026-10-14", SelectionEnabled: true}
	state := map[string]any{
		"schema_version": 1,
		"pending": map[string]any{
			"report":       legacy,
			"attempts":     4,
			"next_attempt": "2026-10-13T20:00:00+08:00",
			"blocked":      true,
		},
	}
	if err := m.Store.Write("state/delivery.json", state); err != nil {
		t.Fatal(err)
	}
	migrated, err := m.readState()
	if err != nil {
		t.Fatal(err)
	}
	got := channelState(t, migrated, channel.name)
	if got.Status != notify.Blocked || got.Attempts != 4 || got.Kind != notify.Permanent {
		t.Fatalf("v1 state was not migrated: %+v", got)
	}
	var persisted notify.Ledger
	if err := m.Store.Read("state/delivery.json", &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Version != 2 {
		t.Fatal("migration did not persist schema v2")
	}
	msg, err := reportmessage.FromSelection(legacy)
	if err != nil || persisted.Events[msg.Main.EventID].AsOf != legacy.AsOf {
		t.Fatal("migrated event is incomplete")
	}
}

func TestCoverageFailureSavesValidMarketDataWithoutSending(t *testing.T) {
	m, source, channel := fixture(t)
	m.Config.Daily.Adjust = "qfq"
	m.Config.Stocks = append(m.Config.Stocks, config.Stock{Code: "sz000002", Name: "隔离股票"})
	m.Config.Sync.MinCoveragePct = 95
	db, err := storage.InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m.Market = db
	source.failCode = "sz000002"
	source.err = &market.Failure{Status: 403, Kind: "status"}
	if err := m.Cycle(context.Background()); err == nil || !strings.Contains(err.Error(), "coverage") {
		t.Fatalf("coverage gate: %v", err)
	}
	got, err := db.LoadDashboard(context.Background(), m.Config.Stocks, "qfq")
	if err != nil || got.Summary.Available != 1 || got.Summary.CoveragePct != 50 || got.Report != nil || got.LatestSync.ErrorKind != "coverage" {
		t.Fatalf("market data discarded: %+v %v", got, err)
	}
	if channel.calls != 0 {
		t.Fatal("coverage failure sent a message")
	}
	state, err := m.Status()
	if err != nil || len(state.Events) != 0 {
		t.Fatalf("coverage failure enqueued a message: %+v %v", state, err)
	}
}
