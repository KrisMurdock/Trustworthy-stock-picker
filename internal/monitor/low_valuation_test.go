package monitor

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/lowvaluation"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func lowValuationMonitorRecommendation(asOf, target string) lowvaluation.Recommendation {
	return lowvaluation.Recommendation{
		Version: lowvaluation.SchemaVersion, ID: "low_valuation-" + asOf + "-20", Market: lowvaluation.Market,
		AsOf: asOf, Target: target, GeneratedAt: time.Date(2026, 10, 13, 18, 50, 0, 0, market.Shanghai),
		UniverseTotal: 5000, Priced: 4800, Valued: 4300, Eligible: 1, Selected: 1, TopN: 20, SectorCoveragePct: 99,
		Candidates: []lowvaluation.Candidate{{
			Rank: 1, Code: "sh600001", Name: "接近区间", Sector: "制造业", PriceDate: asOf, Close: 13,
			GapToLowerPct: 5.3, UpsideToCentralPct: 23.1,
			Estimate: fairvalue.Estimate{
				Version: fairvalue.Version, Code: "sh600001", AsOf: asOf, Source: "sina", FetchedAt: time.Date(2026, 10, 13, 18, 0, 0, 0, market.Shanghai),
				Method: fairvalue.Method, StatementBasis: "trailing_four_quarters", Confidence: "medium",
				CurrentPrice: 13, EPS: 1.2, BookValuePerShare: 8.5, GrowthPct: 15.3, GrowthKnown: true,
				ROEPct: 14.1, PE: 10.8, PB: 1.5, FairPE: 18.2, FairPB: 2.1, BookValueAnchored: true,
				Lower: 14.4, Central: 16, Upper: 17.6, UpsidePct: 23.1,
			},
		}},
		Limitations: []string{"低估不等于短期上涨。"},
	}
}

func newLowValuationMonitor(t *testing.T, loader func(context.Context) (lowvaluation.Recommendation, error)) (*Monitor, *fakeChannel) {
	t.Helper()
	c := config.Defaults()
	c.RequireCIFS = false
	c.LowValuation.Enabled = true
	c.LowValuation.PushEnabled = true
	c.LowValuation.TopN = 20
	c.Notify.Channels = []config.NotifyChannel{
		{Name: "a-article", Type: "feishu", Enabled: true, Route: config.NotifyRouteSecondStage},
		{Name: "low-article", Type: "wechat", Enabled: true, Route: config.NotifyRouteLowValuation},
	}
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	aChannel := &fakeChannel{name: "a-article"}
	lowChannel := &fakeChannel{name: "low-article"}
	m := New(c, store, nil, []notify.Channel{aChannel, lowChannel}, nil)
	m.Now = func() time.Time { return time.Date(2026, 10, 13, 19, 0, 0, 0, market.Shanghai) }
	m.LowValuationRecommendations = loader
	return m, lowChannel
}

func TestLowValuationQueuesOnOwnRouteAndDoesNotDuplicate(t *testing.T) {
	recommendation := lowValuationMonitorRecommendation("2026-10-13", "2026-10-14")
	m, channel := newLowValuationMonitor(t, func(context.Context) (lowvaluation.Recommendation, error) {
		return recommendation, nil
	})
	state := notify.NewLedger()
	queued, err := m.enqueueLowValuation(context.Background(), &state)
	if err != nil || !queued {
		t.Fatalf("enqueue low valuation: queued=%v err=%v", queued, err)
	}
	eventID := recommendation.ID + ":low_valuation"
	event, ok := state.Events[eventID]
	if !ok || event.Part != "low_valuation" || event.Channels[channel.name].Status != notify.Pending {
		t.Fatalf("event = %+v", event)
	}
	if _, leaked := event.Channels["a-article"]; leaked {
		t.Fatalf("low valuation article leaked into A-share route: %+v", event.Channels)
	}
	queued, err = m.enqueueLowValuation(context.Background(), &state)
	if err != nil || queued {
		t.Fatalf("duplicate enqueue: queued=%v err=%v", queued, err)
	}
	if err := m.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].Title != "全市场低估值股｜10月14日 财务折价观察" {
		t.Fatalf("delivered = %+v", channel.sent)
	}
}

func TestLowValuationUnavailableDoesNotBlockCycle(t *testing.T) {
	for name, loader := range map[string]func(context.Context) (lowvaluation.Recommendation, error){
		"missing database": func(context.Context) (lowvaluation.Recommendation, error) {
			return lowvaluation.Recommendation{}, fs.ErrNotExist
		},
		"load error": func(context.Context) (lowvaluation.Recommendation, error) {
			return lowvaluation.Recommendation{}, errors.New("temporary failure")
		},
		"expired": func(context.Context) (lowvaluation.Recommendation, error) {
			return lowValuationMonitorRecommendation("2026-10-12", "2026-10-13"), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := newLowValuationMonitor(t, loader)
			state := notify.NewLedger()
			queued, err := m.enqueueLowValuation(context.Background(), &state)
			if err != nil || queued || len(state.Events) != 0 {
				t.Fatalf("queued=%v err=%v events=%+v", queued, err, state.Events)
			}
		})
	}
}
