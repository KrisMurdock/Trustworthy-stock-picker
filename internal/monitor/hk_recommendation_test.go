package monitor

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/hkrecommend"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func hkMonitorRecommendation(asOf, target string) hkrecommend.Recommendation {
	return hkrecommend.Recommendation{
		Version: hkrecommend.SchemaVersion, ID: "hk-rec-" + asOf, Market: hkrecommend.Market,
		AsOf: asOf, Target: target, GeneratedAt: time.Date(2026, 10, 13, 18, 50, 0, 0, market.Shanghai),
		UniverseTotal: 667, Eligible: 660, Passed: 1, Selected: 1, TopN: 5, MinScore: 70,
		Rules:       []string{"收盘价 > MA20"},
		Limitations: []string{"港股实行 T+0 且没有 A 股式涨跌停。"},
	}
}

func newHKMonitor(t *testing.T, loader func(context.Context) (hkrecommend.Recommendation, error)) (*Monitor, *fakeChannel, *fakeChannel) {
	t.Helper()
	c := config.Defaults()
	c.RequireCIFS = false
	c.HKConnect.Enabled = true
	c.HKConnect.PushEnabled = true
	c.HKConnect.TopN = 5
	c.Notify.Channels = []config.NotifyChannel{
		{Name: "a-article", Type: "feishu", Enabled: true, Route: config.NotifyRouteSecondStage},
		{Name: "hk-article", Type: "wechat", Enabled: true, Route: config.NotifyRouteHKConnect},
	}
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	aChannel := &fakeChannel{name: "a-article"}
	hkChannel := &fakeChannel{name: "hk-article"}
	m := New(c, store, nil, []notify.Channel{aChannel, hkChannel}, nil)
	m.Now = func() time.Time { return time.Date(2026, 10, 13, 19, 0, 0, 0, market.Shanghai) }
	m.HKRecommendations = loader
	return m, aChannel, hkChannel
}

func TestHKRecommendationQueuesBesideAShareAndDoesNotDuplicate(t *testing.T) {
	recommendation := hkMonitorRecommendation("2026-10-13", "2026-10-14")
	m, aChannel, hkChannel := newHKMonitor(t, func(context.Context) (hkrecommend.Recommendation, error) {
		return recommendation, nil
	})
	state := notify.NewLedger()
	aMessage := notify.Message{Version: 2, EventID: "a:main", Title: "A 股文章", PlainText: "A 股内容",
		ExpiresAt: time.Date(2026, 10, 14, 9, 30, 0, 0, market.Shanghai)}
	if err := state.EnqueuePart("2026-10-13", aMessage, []string{aChannel.name}, "main", 0, ""); err != nil {
		t.Fatal(err)
	}
	queued, err := m.enqueueHKConnect(context.Background(), &state)
	if err != nil || !queued {
		t.Fatalf("enqueue HK: queued=%v err=%v", queued, err)
	}
	if len(state.Events) != 2 {
		t.Fatalf("events = %+v", state.Events)
	}
	hkEvent, ok := state.Events["hk-rec-2026-10-13:hk-connect:article"]
	if !ok || hkEvent.Part != "hk_connect" || hkEvent.Channels[hkChannel.name].Status != notify.Pending {
		t.Fatalf("HK event = %+v", hkEvent)
	}
	if _, leaked := hkEvent.Channels[aChannel.name]; leaked {
		t.Fatalf("HK article leaked into A-share route: %+v", hkEvent.Channels)
	}
	if _, leaked := state.Events[aMessage.EventID].Channels[hkChannel.name]; leaked {
		t.Fatalf("A-share article leaked into HK route: %+v", state.Events[aMessage.EventID].Channels)
	}
	queued, err = m.enqueueHKConnect(context.Background(), &state)
	if err != nil || queued {
		t.Fatalf("duplicate enqueue: queued=%v err=%v", queued, err)
	}
	if err := m.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(aChannel.sent) != 1 || len(hkChannel.sent) != 1 {
		t.Fatalf("delivered A=%d HK=%d", len(aChannel.sent), len(hkChannel.sent))
	}
	if aChannel.sent[0].Title != "A 股文章" || hkChannel.sent[0].Title != "港股通精选｜10月14日 日线趋势观察" {
		t.Fatalf("delivered titles A=%q HK=%q", aChannel.sent[0].Title, hkChannel.sent[0].Title)
	}

	state, err = m.readState()
	if err != nil {
		t.Fatal(err)
	}
	recommendation.ID = "hk-rec-2026-10-13-v2"
	queued, err = m.enqueueHKConnect(context.Background(), &state)
	if err != nil || !queued {
		t.Fatalf("replacement enqueue: queued=%v err=%v", queued, err)
	}
	if _, ok := state.Events["hk-rec-2026-10-13-v2:hk-connect:article"]; !ok {
		t.Fatalf("replacement event missing: %+v", state.Events)
	}
	if err := m.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(hkChannel.sent) != 2 {
		t.Fatalf("HK deliveries = %d, want 2", len(hkChannel.sent))
	}
}

func TestHKRecommendationUnavailableDoesNotBlockCycle(t *testing.T) {
	for name, loader := range map[string]func(context.Context) (hkrecommend.Recommendation, error){
		"missing database": func(context.Context) (hkrecommend.Recommendation, error) {
			return hkrecommend.Recommendation{}, fs.ErrNotExist
		},
		"load error": func(context.Context) (hkrecommend.Recommendation, error) {
			return hkrecommend.Recommendation{}, errors.New("temporary failure")
		},
		"expired": func(context.Context) (hkrecommend.Recommendation, error) {
			return hkMonitorRecommendation("2026-10-12", "2026-10-13"), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, _, _ := newHKMonitor(t, loader)
			state := notify.NewLedger()
			queued, err := m.enqueueHKConnect(context.Background(), &state)
			if err != nil || queued || len(state.Events) != 0 {
				t.Fatalf("queued=%v err=%v events=%+v", queued, err, state.Events)
			}
		})
	}
}
