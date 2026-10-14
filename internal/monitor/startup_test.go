package monitor

import (
	"context"
	"errors"
	"io/fs"
	"stock-watcher/internal/calendar"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"stock-watcher/internal/notify"
	"stock-watcher/internal/selection"
	"testing"
	"testing/synctest"
	"time"
)

type sourceFunc func(context.Context, config.Stock, string, string, string) (market.History, error)

func (f sourceFunc) Fetch(ctx context.Context, s config.Stock, start, end, adjust string) (market.History, error) {
	return f(ctx, s, start, end, adjust)
}

func TestStartupUsesCompletedSessionAndPublicationWindow(t *testing.T) {
	for _, tc := range []struct {
		name, now, asof, target string
		queued                  int
	}{
		{"before open", "2026-10-13T09:29:00+08:00", "2026-10-12", "2026-10-13", 1},
		{"at open", "2026-10-13T09:30:00+08:00", "2026-10-12", "2026-10-13", 0},
		{"intraday UTC clock", "2026-10-13T06:59:00Z", "2026-10-12", "2026-10-13", 0},
		{"at close", "2026-10-13T15:00:00+08:00", "2026-10-13", "2026-10-14", 1},
		{"before scheduled time", "2026-10-13T17:00:00+08:00", "2026-10-13", "2026-10-14", 1},
		{"weekend", "2026-10-17T19:00:00+08:00", "2026-10-16", "2026-10-19", 1},
		{"holiday", "2026-10-01T19:00:00+08:00", "2026-09-30", "2026-10-09", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, source, channel := fixture(t)
			now, err := time.Parse(time.RFC3339, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			m.Now = func() time.Time { return now }
			m.Config.Calendar.Holidays = []string{"2026-10-01", "2026-10-02", "2026-10-05", "2026-10-06", "2026-10-07", "2026-10-08"}
			m.Calendar = calendar.New(m.Config)
			source.holidays = map[string]bool{}
			for _, holiday := range m.Config.Calendar.Holidays {
				source.holidays[holiday] = true
			}
			if err := m.prepareStartup(context.Background()); err != nil {
				t.Fatal(err)
			}
			var report selection.Report
			if err := m.Store.Read("reports/"+tc.asof+".json", &report); err != nil {
				t.Fatal(err)
			}
			if report.AsOf != tc.asof || report.Target != tc.target || source.calls != 1 || channel.calls != 0 {
				t.Fatalf("unexpected startup report: %+v", report)
			}
			if err := m.Cycle(context.Background()); err != nil {
				t.Fatal(err)
			}
			if source.calls != 1 || channel.calls != tc.queued {
				t.Fatalf("downloads=%d sends=%d, want 1/%d", source.calls, channel.calls, tc.queued)
			}
		})
	}
}

type channelFunc struct {
	name string
	send func(context.Context, notify.Message) error
}

func (f channelFunc) Name() string { return f.name }
func (f channelFunc) Send(ctx context.Context, message notify.Message) error {
	return f.send(ctx, message)
}

func TestRunRefreshesWholePoolBeforeSendingAndDoesNotRedownloadOnDeliveryRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, source, _ := fixture(t)
		m.Config.Stocks = append(m.Config.Stocks, config.Stock{Code: "sh600000", Name: "第二只"})
		m.Config.Strategy.Enabled = true
		if _, err := m.Sync(context.Background(), "2026-10-13"); err != nil {
			t.Fatal(err)
		}
		source.calls = 0
		start := time.Now()
		base := time.Date(2026, 10, 13, 17, 0, 0, 0, market.Shanghai)
		m.Now = func() time.Time { return base.Add(time.Since(start)) }
		m.Config.PollInterval = "1m"
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		sends := 0
		var firstAttempt time.Time
		m.Channels = []notify.Channel{channelFunc{name: "fake-main", send: func(context.Context, notify.Message) error {
			sends++
			if source.calls != 4 {
				t.Errorf("expected whole-pool refresh before delivery, got %d downloads", source.calls)
			}
			if sends == 1 {
				firstAttempt = m.Now()
				return &notify.DeliveryError{Kind: notify.Retryable, Reason: "temporary"}
			}
			if m.Now().Sub(firstAttempt) < 2*time.Minute {
				t.Error("delivery retry ignored backoff")
			}
			cancel()
			return nil
		}}}
		if err := m.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown: %v", err)
		}
		if source.calls != 4 || sends != 2 {
			t.Fatalf("downloads=%d sends=%d", source.calls, sends)
		}
		state, err := m.Status()
		if err != nil {
			t.Fatal(err)
		}
		if channelState(t, state, "fake-main").Status != notify.Delivered {
			t.Fatal("success was not persisted")
		}
	})
}

func TestRunRetriesFailedStartupWithoutSendingCachedReport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, source, _ := fixture(t)
		if err := m.prepareStartup(context.Background()); err != nil {
			t.Fatal(err)
		}
		source.calls = 0
		start := time.Now()
		attempts := 0
		m.Source = sourceFunc(func(ctx context.Context, s config.Stock, a, b, adjust string) (market.History, error) {
			attempts++
			if attempts == 1 {
				return market.History{}, context.DeadlineExceeded
			}
			if time.Since(start) < time.Minute {
				t.Error("startup ignored minimum retry delay")
			}
			return source.Fetch(ctx, s, a, b, adjust)
		})
		m.Config.PollInterval = "1s"
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		sends := 0
		m.Channels = []notify.Channel{channelFunc{name: "fake-main", send: func(context.Context, notify.Message) error {
			sends++
			if attempts != 2 || source.calls != 1 {
				t.Error("sent cached recommendation before refresh succeeded")
			}
			cancel()
			return nil
		}}}
		if err := m.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if sends != 1 || attempts != 2 {
			t.Fatalf("sends=%d attempts=%d", sends, attempts)
		}
	})
}

func TestStartupRefreshPreservesDeliveryForChangedReport(t *testing.T) {
	for _, kind := range []notify.Kind{"", notify.Retryable, notify.Permanent} {
		t.Run(string(kind), func(t *testing.T) {
			m, source, channel := fixture(t)
			if kind != "" {
				channel.err = &notify.DeliveryError{Kind: kind, Reason: "test"}
			}
			_ = m.Cycle(context.Background())
			before, err := m.Status()
			if err != nil {
				t.Fatal(err)
			}
			oldState := channelState(t, before, channel.name)
			m.Config.Selection.TopN++ // Changes the content-derived report ID.
			if err := m.prepareStartup(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := m.Cycle(context.Background()); err != nil {
				t.Fatal(err)
			}
			after, err := m.Status()
			if err != nil {
				t.Fatal(err)
			}
			if len(after.Events) != 1 || channelState(t, after, channel.name) != oldState || channel.calls != 1 || source.calls != 2 {
				t.Fatalf("refresh reset delivery: before=%+v after=%+v", before, after)
			}
			var refreshed selection.Report
			if err := m.Store.Read("reports/2026-10-13.json", &refreshed); err != nil {
				t.Fatal(err)
			}
			if _, exists := before.Events[refreshed.ID]; exists {
				t.Fatal("test did not change report identity")
			}
			second := &fakeChannel{name: "new-channel"}
			m.Channels = append(m.Channels, second)
			if err := m.prepareStartup(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := m.Cycle(context.Background()); err != nil {
				t.Fatal(err)
			}
			if second.calls != 1 || channel.calls != 1 {
				t.Fatal("new channel not delivered independently")
			}
		})
	}
}

func TestStartupDoesNotPublishPartialRawPool(t *testing.T) {
	m, source, channel := fixture(t)
	m.Config.Strategy.Enabled = true
	m.Config.Stocks = append(m.Config.Stocks, config.Stock{Code: "sh600000", Name: "第二只"})
	m.Source = sourceFunc(func(ctx context.Context, s config.Stock, a, b, adjust string) (market.History, error) {
		if s.Code == "sh600000" && adjust == "none" {
			return market.History{}, errors.New("second stock unavailable")
		}
		return source.Fetch(ctx, s, a, b, adjust)
	})
	if err := m.prepareStartup(context.Background()); err == nil {
		t.Fatal("partial pool accepted")
	}
	var report selection.Report
	if err := m.Store.Read("reports/2026-10-13.json", &report); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("partial report persisted: %v", err)
	}
	state, err := m.Status()
	if err != nil || len(state.Events) != 0 || channel.calls != 0 {
		t.Fatal("partial pool queued or sent")
	}
}

func TestStartupRejectsInvalidStateCalendarAndCancellation(t *testing.T) {
	for _, mode := range []string{"state", "calendar", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			m, source, channel := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "state":
				if err := m.Store.Write("state/delivery.json", map[string]int{"schema_version": 99}); err != nil {
					t.Fatal(err)
				}
			case "calendar":
				m.Now = func() time.Time { return time.Date(2027, 1, 1, 19, 0, 0, 0, market.Shanghai) }
			case "cancel":
				cancel()
			}
			if err := m.prepareStartup(ctx); err == nil {
				t.Fatal("invalid startup accepted")
			}
			if source.calls != 0 || channel.calls != 0 {
				t.Fatal("invalid startup performed side effects")
			}
		})
	}
}

func TestStartupRechecksSessionAfterDownload(t *testing.T) {
	m, source, channel := fixture(t)
	now := time.Date(2026, 10, 13, 14, 59, 0, 0, market.Shanghai)
	m.Now = func() time.Time { return now }
	m.Source = sourceFunc(func(ctx context.Context, s config.Stock, a, b, adjust string) (market.History, error) {
		now = time.Date(2026, 10, 13, 15, 1, 0, 0, market.Shanghai)
		return source.Fetch(ctx, s, a, b, adjust)
	})
	if err := m.prepareStartup(context.Background()); err == nil {
		t.Fatal("changed session accepted")
	}
	if channel.calls != 0 {
		t.Fatal("stale startup sent")
	}
	if err := m.prepareStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source.calls != 2 || channel.calls != 1 {
		t.Fatal("new completed session not processed")
	}
}

func TestStartupPreservesLegacyDeliveredDate(t *testing.T) {
	m, source, channel := fixture(t)
	if _, err := m.Sync(context.Background(), "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	if err := m.Store.Write("state/delivery.json", persistedState{Version: 1, LastDelivered: "2026-10-13"}); err != nil {
		t.Fatal(err)
	}
	m.Config.Selection.TopN++
	if err := m.prepareStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source.calls != 2 || channel.calls != 0 {
		t.Fatal("legacy delivered report was resent")
	}
	state, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	if channelState(t, state, channel.name).Status != notify.Delivered {
		t.Fatal("legacy delivery lost")
	}
}
