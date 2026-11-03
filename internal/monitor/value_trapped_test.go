package monitor

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/valuetrapped"
)

func valueTrappedMonitorRecommendation(asOf, target string) valuetrapped.Recommendation {
	return valuetrapped.Recommendation{
		Version: valuetrapped.SchemaVersion, ID: "value_trapped-" + asOf + "-20", Market: valuetrapped.Market,
		AsOf: asOf, Target: target, GeneratedAt: time.Date(2026, 10, 13, 18, 50, 0, 0, market.Shanghai),
		UniverseTotal: 5000, Priced: 4800, Valued: 4300, LowValuationEligible: 120, TrappedValid: 80,
		Eligible: 1, Selected: 1, TopN: 20, MaxTrappedRatioPct: 30, MinHistorySessions: 250, MinAmountCNY: 20_000_000,
		Candidates: []valuetrapped.Candidate{{
			Rank: 1, Code: "sh600001", Name: "接近区间", Sector: "制造业", PriceDate: asOf, Close: 13,
			GapToLowerPct: 5.3, AvgAmount20CNY: 100_000_000,
			Trapped: &strategy.TrappedSignal{
				Version: strategy.TrappedSignalVersion, Method: strategy.TrappedSignalMethod, Status: strategy.TrappedStatusWatch,
				CurrentPrice: 13, TrappedVolumeRatio: 0.12, CurrentZone: strategy.TrappedZoneShallow, DominantZone: strategy.TrappedZoneShallow,
				Zones: []strategy.TrappedZone{
					{Level: strategy.TrappedZoneShallow, Label: "浅度", Low: 13, High: 13.65, VolumeShare: 0.6, SharePct: 60},
					{Level: strategy.TrappedZoneModerate, Label: "中度", Low: 13.65, High: 14.95, VolumeShare: 0.4, SharePct: 40},
					{Level: strategy.TrappedZoneDeep, Label: "深度"},
				},
			},
		}},
		Limitations: []string{"低估值与低套牢的交集不代表上涨。"},
	}
}

func newValueTrappedMonitor(t *testing.T, loader func(context.Context) (valuetrapped.Recommendation, error)) (*Monitor, *fakeChannel) {
	t.Helper()
	c := config.Defaults()
	c.RequireCIFS = false
	c.LowValuation.Enabled = true
	c.ValueTrapped.Enabled = true
	c.ValueTrapped.PushEnabled = true
	c.ValueTrapped.TopN = 20
	c.Notify.Channels = []config.NotifyChannel{
		{Name: "a-article", Type: "feishu", Enabled: true, Route: config.NotifyRouteSecondStage},
		{Name: "value-article", Type: "wechat", Enabled: true, Route: config.NotifyRouteValueTrapped},
	}
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	aChannel := &fakeChannel{name: "a-article"}
	valueChannel := &fakeChannel{name: "value-article"}
	m := New(c, store, nil, []notify.Channel{aChannel, valueChannel}, nil)
	m.Now = func() time.Time { return time.Date(2026, 10, 13, 19, 0, 0, 0, market.Shanghai) }
	m.ValueTrappedRecommendations = loader
	return m, valueChannel
}

func TestValueTrappedQueuesOnOwnRouteAndDoesNotDuplicate(t *testing.T) {
	recommendation := valueTrappedMonitorRecommendation("2026-10-13", "2026-10-14")
	m, channel := newValueTrappedMonitor(t, func(context.Context) (valuetrapped.Recommendation, error) {
		return recommendation, nil
	})
	state := notify.NewLedger()
	queued, err := m.enqueueValueTrapped(context.Background(), &state)
	if err != nil || !queued {
		t.Fatalf("enqueue value trapped: queued=%v err=%v", queued, err)
	}
	eventID := recommendation.ID + ":value_trapped"
	event, ok := state.Events[eventID]
	if !ok || event.Part != "value_trapped" || event.Channels[channel.name].Status != notify.Pending {
		t.Fatalf("event = %+v", event)
	}
	if _, leaked := event.Channels["a-article"]; leaked {
		t.Fatalf("value-trapped article leaked into A-share route: %+v", event.Channels)
	}
	queued, err = m.enqueueValueTrapped(context.Background(), &state)
	if err != nil || queued {
		t.Fatalf("duplicate enqueue: queued=%v err=%v", queued, err)
	}
	if err := m.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].Title != "低估值且低套牢｜10月14日 双重观察" {
		t.Fatalf("delivered = %+v", channel.sent)
	}
}

func TestValueTrappedDoesNotDuplicateAfterRestart(t *testing.T) {
	recommendation := valueTrappedMonitorRecommendation("2026-10-13", "2026-10-14")
	loader := func(context.Context) (valuetrapped.Recommendation, error) {
		return recommendation, nil
	}
	m, channel := newValueTrappedMonitor(t, loader)
	state := notify.NewLedger()
	if queued, err := m.enqueueValueTrapped(context.Background(), &state); err != nil || !queued {
		t.Fatalf("initial enqueue: queued=%v err=%v", queued, err)
	}
	if err := m.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("initial delivery = %+v", channel.sent)
	}

	restarted := New(m.Config, m.Store, nil, []notify.Channel{channel}, nil)
	restarted.Now = m.Now
	restarted.ValueTrappedRecommendations = loader
	persisted, err := restarted.readState()
	if err != nil {
		t.Fatal(err)
	}
	queued, err := restarted.enqueueValueTrapped(context.Background(), &persisted)
	if err != nil || queued {
		t.Fatalf("restart enqueue: queued=%v err=%v", queued, err)
	}
	if err := restarted.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("restart duplicated delivery: %+v", channel.sent)
	}
}

func TestValueTrappedUnavailableDoesNotBlockCycle(t *testing.T) {
	for name, loader := range map[string]func(context.Context) (valuetrapped.Recommendation, error){
		"missing database": func(context.Context) (valuetrapped.Recommendation, error) {
			return valuetrapped.Recommendation{}, fs.ErrNotExist
		},
		"load error": func(context.Context) (valuetrapped.Recommendation, error) {
			return valuetrapped.Recommendation{}, errors.New("temporary failure")
		},
		"invalid article": func(context.Context) (valuetrapped.Recommendation, error) {
			recommendation := valueTrappedMonitorRecommendation("2026-10-13", "2026-10-14")
			recommendation.Selected = 2
			return recommendation, nil
		},
		"expired": func(context.Context) (valuetrapped.Recommendation, error) {
			return valueTrappedMonitorRecommendation("2026-10-12", "2026-10-13"), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := newValueTrappedMonitor(t, loader)
			state := notify.NewLedger()
			queued, err := m.enqueueValueTrapped(context.Background(), &state)
			if err != nil || queued || len(state.Events) != 0 {
				t.Fatalf("queued=%v err=%v events=%+v", queued, err, state.Events)
			}
		})
	}
}
