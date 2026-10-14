package monitor

import (
	"context"
	"errors"
	"io/fs"
	"stock-watcher/internal/config"
	"stock-watcher/internal/feishu"
	"stock-watcher/internal/market"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/storage"
	"testing"
	"time"
)

type fakeSource struct {
	calls      int
	err        error
	failAdjust string
}

func (f *fakeSource) Fetch(ctx context.Context, s config.Stock, start, end, adjust string) (market.History, error) {
	f.calls++
	if ctx.Err() != nil {
		return market.History{}, ctx.Err()
	}
	if f.err != nil && (f.failAdjust == "" || f.failAdjust == adjust) {
		return market.History{}, f.err
	}
	bars := []market.Bar{}
	d, _ := time.Parse("2006-01-02", end)
	for len(bars) < 60 {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			bars = append([]market.Bar{{Date: d.Format("2006-01-02"), Open: 10, Close: 10, High: 11, Low: 9, Volume: 100}}, bars...)
		}
		d = d.AddDate(0, 0, -1)
	}
	field := "qfqday"
	if adjust == "none" {
		field = "day"
	}
	return market.History{Version: 1, Code: s.Code, Name: s.Name, Source: "tencent", DataField: field, VolumeUnit: "provider_raw", FetchedAt: time.Now(), Adjust: adjust, AsOf: end, Bars: bars}, nil
}

type fakeSender struct {
	calls  int
	err    error
	cancel func()
}

func (f *fakeSender) Send(ctx context.Context, r selection.Report) error {
	f.calls++
	if f.cancel != nil {
		f.cancel()
		return ctx.Err()
	}
	return f.err
}
func fixture(t *testing.T) (*Monitor, *fakeSource, *fakeSender) {
	t.Helper()
	c := config.Defaults()
	c.RequireCIFS = false
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试股票"}}
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Selection.Enabled = true
	c.Feishu.Enabled = true
	s, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	source := &fakeSource{}
	sender := &fakeSender{}
	m := New(c, s, source, sender, nil)
	now, _ := time.ParseInLocation("2006-01-02 15:04", "2026-10-13 19:00", market.Shanghai)
	m.Now = func() time.Time { return now }
	return m, source, sender
}
func TestDurableRetryAndRestartDedupe(t *testing.T) {
	m, source, sender := fixture(t)
	sender.err = &feishu.Failure{Status: 503, Kind: "status"}
	if err := m.Cycle(context.Background()); err == nil {
		t.Fatal("expected delivery failure")
	}
	s, err := m.Status()
	if err != nil || s.Pending == nil || s.Pending.Attempts != 1 {
		t.Fatalf("missing durable pending: %+v %v", s, err)
	}
	if err := m.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 || source.calls != 1 {
		t.Fatal("retry ignored delay or redownloaded")
	}
	restarted := New(m.Config, m.Store, source, sender, nil)
	now := m.Now().Add(3 * time.Minute)
	restarted.Now = func() time.Time { return now }
	sender.err = nil
	if err := restarted.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 2 || source.calls != 1 {
		t.Fatal("pending not reused after restart")
	}
	if err := restarted.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 2 {
		t.Fatal("duplicate recommendation")
	}
}
func TestUnauthorizedAndExpiredPending(t *testing.T) {
	m, _, sender := fixture(t)
	sender.err = &feishu.Failure{Status: 403, Kind: "status"}
	if err := m.Cycle(context.Background()); err == nil {
		t.Fatal("403 accepted")
	}
	s, _ := m.Status()
	if s.Pending == nil || !s.Pending.Blocked {
		t.Fatal("403 should block retries")
	}
	if err := m.Cycle(context.Background()); err == nil || sender.calls != 1 {
		t.Fatal("permanent error retried automatically")
	}
	if err := m.Retry(); err != nil {
		t.Fatal(err)
	}
	sender.err = nil
	if err := m.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, _, sender = fixture(t)
	sender.err = &feishu.Failure{Status: 503, Kind: "status"}
	_ = m.Cycle(context.Background())
	now := m.Now().Add(15 * time.Hour)
	m.Now = func() time.Time { return now }
	if err := m.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ = m.Status()
	if s.Pending != nil || s.LastExpired != "2026-10-13" || sender.calls != 1 {
		t.Fatal("expired recommendation sent")
	}
}
func TestNoPartialPoolAndCancellation(t *testing.T) {
	m, source, sender := fixture(t)
	source.err = errors.New("download failed")
	if err := m.Cycle(context.Background()); err == nil || sender.calls != 0 {
		t.Fatal("partial recommendation sent")
	}
	source.err = nil
	ctx, cancel := context.WithCancel(context.Background())
	sender.cancel = cancel
	if err := m.Cycle(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation %v", err)
	}
	s, _ := m.Status()
	if s.Pending == nil || s.Pending.Attempts != 0 {
		t.Fatal("cancelled delivery was acknowledged or charged as failure")
	}
}
func TestCorruptStateAndBeforeClose(t *testing.T) {
	m, _, sender := fixture(t)
	m.Store.Write("state/delivery.json", map[string]any{"schema_version": 9})
	if err := m.Cycle(context.Background()); err == nil || sender.calls != 0 {
		t.Fatal("unsupported state reset")
	}
	m, source, _ := fixture(t)
	now := m.Now().Add(-5 * time.Hour)
	m.Now = func() time.Time { return now }
	if err := m.Cycle(context.Background()); err != nil || source.calls != 0 {
		t.Fatal("ran before close")
	}
}

func TestStrategySyncRawResearchPersistenceAndRetry(t *testing.T) {
	m, source, sender := fixture(t)
	m.Config.Strategy.Enabled = true
	sender.err = &feishu.Failure{Status: 503, Kind: "status"}
	if err := m.Cycle(context.Background()); err == nil {
		t.Fatal("expected failed notification")
	}
	if source.calls != 2 {
		t.Fatal("raw execution history not fetched")
	}
	state, err := m.Status()
	if err != nil || state.Pending == nil || state.Pending.Report.Version != 2 || len(state.Pending.Report.Ranking) != 1 {
		t.Fatalf("new report not persisted %+v %v", state, err)
	}
	preview, err := m.Preview("2026-10-13")
	if err != nil || preview.Research == nil || preview.ID != state.Pending.Report.ID {
		t.Fatalf("new preview mismatch %+v %v", preview, err)
	}
	now := m.Now().Add(3 * time.Minute)
	m.Now = func() time.Time { return now }
	sender.err = nil
	if err := m.Cycle(context.Background()); err != nil || source.calls != 2 || sender.calls != 2 {
		t.Fatal("v2 pending was not reused")
	}
	// Unknown nested contracts must not be silently sent after a restart.
	state.Pending.Report.Ranking[0].Plan.Version = 2
	if err := m.Store.Write("state/delivery.json", state); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Status(); err == nil {
		t.Fatal("unknown nested trade plan version accepted")
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
	source.err, source.failAdjust = errors.New("raw source unavailable"), "none"
	if err := m.Cycle(context.Background()); err == nil || source.calls != 2 || sender.calls != 0 {
		t.Fatal("missing raw prices did not block the whole report")
	}
	var report selection.Report
	if err := m.Store.Read("reports/2026-10-13.json", &report); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("adjusted-only report persisted")
	}
	state, err := m.Status()
	if err != nil || state.Pending != nil {
		t.Fatal("partial data became a pending notification")
	}
}
