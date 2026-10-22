package monitor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func secondStageRefreshFixture(t *testing.T) (*Monitor, selection.Report) {
	t.Helper()
	c := config.Defaults()
	c.RequireCIFS = false
	c.DataDir = t.TempDir()
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试股票"}}
	c.FirstStage.Enabled = true
	c.FirstStage.PushEnabled = true
	c.SecondStage.Enabled = true
	c.SecondStage.PushEnabled = true
	c.SecondStage.BlendID = "test_blend"
	c.SecondStage.FirstStageWeight = 0.5
	c.SecondStage.Models = []config.ModelWeight{{Name: "jev", Weight: 0.5}}
	c.SecondStage.PublishBy = "15:28"
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	m := New(c, store, &fakeSource{}, nil, nil)
	now := time.Date(2026, 10, 13, 15, 5, 0, 0, market.Shanghai)
	m.Now = func() time.Time { return now }
	first := &firststage.Report{
		Version: firststage.ReportSchemaVersion, Status: firststage.StatusOK,
		AsOf: "2026-10-13", Target: "2026-10-14", ModelID: firststage.ModelID,
		PoolSize: 1, Eligible: 1, Budget: 1,
		Candidates: []firststage.Candidate{{
			Code: "sz000001", Name: "测试股票", Close: 10, BaseScore: 80, ModelScore: 0.8,
			BaseRank: 1, ModelRank: 1, BlendScore: 1, Rank: 1,
			Features: firststage.FeatureSnapshot{Return5: 2, ATR14Pct: 2, RSI14: 60},
		}},
	}
	report := selection.Report{
		Version: 1, ID: "degraded-report", AsOf: "2026-10-13", Target: "2026-10-14",
		GeneratedAt: now, Strategy: "leader_daily_v1", SelectionEnabled: true,
		Adjust: "qfq", TopN: 1, PoolSize: 1, Eligible: 1,
		FirstStage: first,
		SecondStage: &secondstage.Report{
			Version: secondstage.ReportSchemaVersion, Status: secondstage.StatusOK,
			AsOf: "2026-10-13", Target: "2026-10-14", TopN: 10, InputCount: 1,
			PushEnabled: true, Degraded: true, Candidates: []secondstage.Candidate{{Rank: 1, Code: "sz000001", Name: "测试股票"}},
		},
	}
	return m, report
}

func writeRefreshArtifact(t *testing.T, m *Monitor, asOf string) {
	t.Helper()
	dir := filepath.Join(m.Config.DataDir, "state", "secondstage", "jev")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc, err := json.Marshal(secondstage.JEVDocument{SchemaVersion: 1, AsOf: asOf, Scores: map[string]secondstage.JEVScore{
		"sz000001": {Adjacent15: 0.7, Full15Top12: 0.5},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, asOf+".json"), doc, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareSecondStageWaitsUntilArtifactsAreComplete(t *testing.T) {
	m, report := secondStageRefreshFixture(t)
	deferred, wait, err := m.prepareSecondStage(context.Background(), nil, report)
	if err != nil {
		t.Fatal(err)
	}
	if !wait || deferred.ID != report.ID {
		t.Fatalf("missing artifacts should defer delivery: wait=%v report=%s", wait, deferred.ID)
	}
	writeRefreshArtifact(t, m, report.AsOf)
	refreshed, wait, err := m.prepareSecondStage(context.Background(), nil, report)
	if err != nil {
		t.Fatal(err)
	}
	if wait || refreshed.ID == report.ID || refreshed.SecondStage == nil || refreshed.SecondStage.Degraded {
		t.Fatalf("complete artifacts were not consumed: wait=%v report=%+v", wait, refreshed.SecondStage)
	}
	stored, err := m.readReport(context.Background(), report.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != refreshed.ID || stored.SecondStage == nil || stored.SecondStage.Degraded {
		t.Fatal("refreshed report was not persisted")
	}
}

func TestPrepareSecondStageStopsWaitingAtDeadline(t *testing.T) {
	m, report := secondStageRefreshFixture(t)
	m.Now = func() time.Time { return time.Date(2026, 10, 13, 15, 28, 0, 0, market.Shanghai) }
	deferred, wait, err := m.prepareSecondStage(context.Background(), nil, report)
	if err != nil {
		t.Fatal(err)
	}
	if wait || deferred.ID != report.ID {
		t.Fatalf("deadline should release the degraded report: wait=%v report=%s", wait, deferred.ID)
	}
}

func TestPrepareSecondStageConsumesArtifactsAtDeadline(t *testing.T) {
	m, report := secondStageRefreshFixture(t)
	writeRefreshArtifact(t, m, report.AsOf)
	m.Now = func() time.Time { return time.Date(2026, 10, 13, 15, 28, 0, 0, market.Shanghai) }
	refreshed, wait, err := m.prepareSecondStage(context.Background(), nil, report)
	if err != nil {
		t.Fatal(err)
	}
	if wait || refreshed.ID == report.ID || refreshed.SecondStage == nil || refreshed.SecondStage.Degraded {
		t.Fatalf("deadline must consume complete artifacts: wait=%v report=%+v", wait, refreshed.SecondStage)
	}
	if m.secondStagePending {
		t.Fatal("completed second stage left the short-poll flag set")
	}
}

func TestPrepareSecondStageDoesNotUpgradeAfterDegradedDelivery(t *testing.T) {
	m, report := secondStageRefreshFixture(t)
	m.Channels = []notify.Channel{channelFunc{name: "fake", send: func(context.Context, notify.Message) error { return nil }}}
	writeRefreshArtifact(t, m, report.AsOf)
	state := notify.NewLedger()
	state.Events["degraded:second-stage:article"] = notify.Event{
		AsOf: report.AsOf,
		Channels: map[string]notify.ChannelState{
			"fake": {Status: notify.Delivered},
		},
	}
	refreshed, wait, err := m.prepareSecondStage(context.Background(), &state, report)
	if err != nil {
		t.Fatal(err)
	}
	if wait || refreshed.ID != report.ID {
		t.Fatalf("recorded degraded delivery must stay final: wait=%v report=%s", wait, refreshed.ID)
	}
}

func TestDeferredSecondStageDoesNotBlockFirstDelivery(t *testing.T) {
	c := config.Defaults()
	c.Notify.Channels = []config.NotifyChannel{
		{Name: "selection-bot", Type: "feishu", Enabled: true, Route: config.NotifyRouteSelection},
		{Name: "second-stage-bot", Type: "feishu", Enabled: true, Route: config.NotifyRouteSecondStage},
	}
	store, err := storage.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 13, 15, 5, 0, 0, market.Shanghai)
	m := New(c, store, &fakeSource{}, []notify.Channel{&fakeChannel{name: "selection-bot"}, &fakeChannel{name: "second-stage-bot"}}, nil)
	m.Now = func() time.Time { return now }
	report := selection.Report{
		Version: 3, ID: "report-id", AsOf: "2026-10-13", Target: "2026-10-14",
		SelectionEnabled: true, Adjust: "qfq", PoolSize: 1, Eligible: 1,
		FirstStage:  &firststage.Report{Status: firststage.StatusOK, AsOf: "2026-10-13", Target: "2026-10-14", ModelID: firststage.ModelID, PoolSize: 1, Eligible: 1, PushEnabled: false},
		SecondStage: &secondstage.Report{Version: secondstage.ReportSchemaVersion, Status: secondstage.StatusOK, AsOf: "2026-10-13", Target: "2026-10-14", PushEnabled: true, InputCount: 1, Candidates: []secondstage.Candidate{{Rank: 1, Code: "sz000001", Name: "测试股票"}}},
	}
	state := notify.NewLedger()
	if queued, err := m.enqueueReportDeferred(&state, report, true); err != nil || queued != 1 {
		t.Fatalf("deferred report queued=%d err=%v", queued, err)
	}
	if _, ok := state.Events["report-id:second-stage:article"]; ok {
		t.Fatal("second-stage article was queued before artifacts were ready")
	}
	if queued, err := m.enqueueReportDeferred(&state, report, false); err != nil || queued != 1 {
		t.Fatalf("ready second stage queued=%d err=%v", queued, err)
	}
	if _, ok := state.Events["report-id:second-stage:article"]; !ok {
		t.Fatal("second-stage article was not queued after artifacts were ready")
	}
}
