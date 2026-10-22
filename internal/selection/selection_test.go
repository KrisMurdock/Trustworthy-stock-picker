package selection

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScoreMinimumAndFlatSeries(t *testing.T) {
	bars := make([]market.Bar, 35)
	for i := range bars {
		bars[i] = market.Bar{Close: 10, Volume: 100}
	}
	m, err := Score(bars)
	if err != nil {
		t.Fatal(err)
	}
	if m.Score != 17 || m.RSI14 != 50 || m.VolumeRatio != 1 || m.MACDScore != 0 {
		t.Fatalf("unexpected flat score %+v", m)
	}
	if _, err := Score(bars[:34]); err == nil {
		t.Fatal("accepted insufficient history")
	}
}

func TestGoldenMetricsAndCandidateFilters(t *testing.T) {
	bars := make([]market.Bar, 61)
	for i := range bars {
		bars[i] = market.Bar{Open: 50 + 0.1*float64(i) + 0.4*math.Sin(float64(i)*1.5), High: 100, Low: 1, Close: 50 + 0.1*float64(i) + 0.4*math.Sin(float64(i)*1.5), Volume: 100}
	}
	d, _ := time.Parse("2006-01-02", "2026-10-13")
	for i := len(bars) - 1; i >= 0; i-- {
		for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			d = d.AddDate(0, 0, -1)
		}
		bars[i].Date = d.Format("2006-01-02")
		d = d.AddDate(0, 0, -1)
	}
	bars[len(bars)-1].Volume = 150
	m, err := Score(bars)
	if err != nil {
		t.Fatal(err)
	}
	// Independent reference computation using SMA-seeded EMA and Wilder RSI.
	for _, pair := range [][2]float64{{m.MA5, 55.85512823671642}, {m.MA10, 55.60504368300839}, {m.MA20, 55.056574362233356}, {m.DIF, 0.7243723402201709}, {m.DEA, 0.7008433148807519}, {m.RSI14, 68.26690998527425}} {
		if math.Abs(pair[0]-pair[1]) > 1e-9 {
			t.Fatalf("metric %v != %v", pair[0], pair[1])
		}
	}
	c := config.Defaults()
	c.Selection.Enabled = true
	c.Stocks = []config.Stock{{Code: "sz000001"}}
	h := market.History{Version: 1, Code: "sz000001", Name: "测试股票", Source: "tencent", DataField: "qfqday", VolumeUnit: "provider_raw", FetchedAt: time.Now(), AsOf: "2026-10-13", Adjust: "qfq", Bars: bars}
	r, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now())
	if err != nil || len(r.Selected) != 1 || r.Selected[0].Metrics.Score < 70 {
		t.Fatalf("expected candidate %+v %v", r, err)
	}
	same, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now().Add(time.Hour))
	if err != nil || same.ID != r.ID {
		t.Fatal("unstable event id")
	}
	h.Name = "*ST测试"
	r, err = Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now())
	if err != nil || len(r.Selected) != 0 || r.Filtered[0].Reason != "special_treatment" {
		t.Fatal("ST stock recommended")
	}
	h.Name = "正常股票"
	h.Bars = h.Bars[:len(h.Bars)-1]
	if _, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now()); err == nil {
		t.Fatal("stale pool should fail")
	}
}

func TestFirstStageFailureDoesNotBlockTrendDecision(t *testing.T) {
	bars := make([]market.Bar, 61)
	for i := range bars {
		close := 50 + 0.1*float64(i) + 0.4*math.Sin(float64(i)*1.5)
		bars[i] = market.Bar{Open: close + 0.2, High: 100, Low: 1, Close: close, Volume: 100}
	}
	bars[len(bars)-1].Volume = 150
	d, _ := time.Parse("2006-01-02", "2026-10-13")
	for i := len(bars) - 1; i >= 0; i-- {
		for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			d = d.AddDate(0, 0, -1)
		}
		bars[i].Date = d.Format("2006-01-02")
		d = d.AddDate(0, 0, -1)
	}
	c := config.Defaults()
	c.Stocks = []config.Stock{{Code: "sz000001"}}
	c.Selection.Enabled = true
	c.FirstStage.Enabled = true
	c.FirstStage.Budget = 0
	h := market.History{Version: 1, Code: "sz000001", Name: "测试股票", Source: "tencent", DataField: "qfqday", VolumeUnit: "provider_raw", FetchedAt: time.Now(), AsOf: bars[len(bars)-1].Date, Adjust: "qfq", Bars: bars}
	report, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if report.FirstStage == nil || report.FirstStage.Status != "failed" || report.FirstStage.Reason == "" {
		t.Fatalf("first-stage failure was not recorded: %+v", report.FirstStage)
	}
	if len(report.Selected) != 1 {
		t.Fatalf("first-stage failure blocked the trend decision: %+v", report)
	}
	same, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now().Add(time.Hour))
	if err != nil || same.ID != report.ID {
		t.Fatal("first-stage generation time changed the report id")
	}
}

func secondStageDailyFixture(t *testing.T) (config.Config, market.History) {
	t.Helper()
	bars := make([]market.Bar, 70)
	for i := range bars {
		close := 50 + 0.1*float64(i) + 0.4*math.Sin(float64(i)*1.5)
		bars[i] = market.Bar{Open: close + 0.2, High: 100, Low: 1, Close: close, Volume: 100}
	}
	bars[len(bars)-1].Volume = 150
	d, _ := time.Parse("2006-01-02", "2026-10-13")
	for i := len(bars) - 1; i >= 0; i-- {
		for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			d = d.AddDate(0, 0, -1)
		}
		bars[i].Date = d.Format("2006-01-02")
		d = d.AddDate(0, 0, -1)
	}
	c := config.Defaults()
	c.DataDir = t.TempDir()
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试股票"}}
	c.Selection.Enabled = true
	c.FirstStage.Enabled = true
	c.FirstStage.Budget = 1
	c.SecondStage.Enabled = true
	c.SecondStage.PushEnabled = true
	c.SecondStage.TopN = 10
	h := market.History{Version: 1, Code: "sz000001", Name: "测试股票", Source: "tencent", DataField: "qfqday", VolumeUnit: "provider_raw", FetchedAt: time.Now(), AsOf: bars[len(bars)-1].Date, Adjust: "qfq", Bars: bars}
	return c, h
}

func TestBuildAttachesDegradedSecondStageWithoutJEV(t *testing.T) {
	c, h := secondStageDailyFixture(t)
	report, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if report.FirstStage == nil || report.FirstStage.Status != firststage.StatusOK || len(report.FirstStage.Candidates) != 1 {
		t.Fatalf("unexpected first-stage result: %+v", report.FirstStage)
	}
	if report.SecondStage == nil || report.SecondStage.Status != secondstage.StatusOK || !report.SecondStage.PushEnabled || !report.SecondStage.Degraded || report.SecondStage.JEVStatus != secondstage.JEVStatusNone {
		t.Fatalf("second-stage did not degrade cleanly: %+v", report.SecondStage)
	}
	if len(report.SecondStage.Candidates) != 1 || report.SecondStage.Candidates[0].Code != "sz000001" {
		t.Fatalf("unexpected second-stage candidates: %+v", report.SecondStage)
	}
	sameMissing, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now().Add(time.Hour))
	if err != nil || sameMissing.ID != report.ID {
		t.Fatal("second-stage generation time changed the report id without JEV")
	}
	jevDir := filepath.Join(c.DataDir, "state", "jev")
	if err := os.MkdirAll(jevDir, 0o700); err != nil {
		t.Fatal(err)
	}
	jev, err := json.Marshal(secondstage.JEVDocument{SchemaVersion: 1, AsOf: h.AsOf, Scores: map[string]secondstage.JEVScore{"sz000001": {Adjacent15: 0.7, Full15Top12: 0.5}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jevDir, h.AsOf+".json"), jev, 0o600); err != nil {
		t.Fatal(err)
	}
	withJEV, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if withJEV.SecondStage == nil || withJEV.SecondStage.JEVStatus != secondstage.JEVStatusOK || withJEV.SecondStage.Degraded {
		t.Fatalf("valid JEV artifact was not consumed: %+v", withJEV.SecondStage)
	}
	sameJEV, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now().Add(2*time.Hour))
	if err != nil || sameJEV.ID != withJEV.ID {
		t.Fatal("second-stage generation time changed the report id with JEV")
	}
}

func TestExecuteLoadsJEVBeforeStrippingDataDir(t *testing.T) {
	c, h := secondStageDailyFixture(t)
	jevDir := filepath.Join(c.DataDir, "state", "jev")
	if err := os.MkdirAll(jevDir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc, err := json.Marshal(secondstage.JEVDocument{SchemaVersion: 1, AsOf: h.AsOf, Scores: map[string]secondstage.JEVScore{"sz000001": {Adjacent15: 0.7, Full15Top12: 0.5}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jevDir, h.AsOf+".json"), doc, 0o600); err != nil {
		t.Fatal(err)
	}
	engine, err := DefaultEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Execute(context.Background(), engine, Input{Config: c, Histories: []market.History{h}, AsOf: h.AsOf, Target: "2026-10-14", Now: time.Now()}, "test-build")
	if err != nil {
		t.Fatal(err)
	}
	if report.SecondStage == nil || report.SecondStage.JEVStatus != secondstage.JEVStatusOK || report.SecondStage.Degraded {
		t.Fatalf("framework did not freeze JEV before stripping paths: %+v", report.SecondStage)
	}
}

func TestBuildLoadsMultipleSecondStageModels(t *testing.T) {
	c, h := secondStageDailyFixture(t)
	c.SecondStage.BlendID = "blend_jev_laya"
	c.SecondStage.FirstStageWeight = 0.4
	c.SecondStage.Models = []config.ModelWeight{{Name: "jev", Weight: 0.3}, {Name: "laya_official", Weight: 0.3}}
	for name, score := range map[string]float64{"jev": 0.7, "laya_official": 0.6} {
		dir := filepath.Join(c.DataDir, "state", "secondstage", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		doc, err := json.Marshal(secondstage.JEVDocument{SchemaVersion: 1, AsOf: h.AsOf, Scores: map[string]secondstage.JEVScore{"sz000001": {Adjacent15: score}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, h.AsOf+".json"), doc, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if report.SecondStage == nil || report.SecondStage.Blend.ID != "blend_jev_laya" || len(report.SecondStage.Models) != 2 || report.SecondStage.Degraded {
		t.Fatalf("multi-model second-stage was not loaded: %+v", report.SecondStage)
	}
	if len(report.SecondStage.Candidates) != 1 || len(report.SecondStage.Candidates[0].Models) != 2 {
		t.Fatalf("multi-model evidence missing: %+v", report.SecondStage)
	}
}

func TestRecomputeSecondStageConsumesArtifactsWithoutChangingFrozenInput(t *testing.T) {
	c, h := secondStageDailyFixture(t)
	report, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if report.SecondStage == nil || !report.SecondStage.Degraded {
		t.Fatalf("fixture should start degraded: %+v", report.SecondStage)
	}
	firstID := report.FirstStage.ModelID
	previousID := report.ID
	dir := filepath.Join(c.DataDir, "state", "secondstage", "jev")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc, err := json.Marshal(secondstage.JEVDocument{SchemaVersion: 1, AsOf: h.AsOf, Scores: map[string]secondstage.JEVScore{"sz000001": {Adjacent15: 0.7, Full15Top12: 0.5}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, h.AsOf+".json"), doc, 0o600); err != nil {
		t.Fatal(err)
	}
	refreshed, err := RecomputeSecondStage(report, c, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.SecondStage == nil || refreshed.SecondStage.Degraded || refreshed.SecondStage.Models[0].Status != secondstage.ModelStatusOK {
		t.Fatalf("artifact was not consumed: %+v", refreshed.SecondStage)
	}
	if refreshed.FirstStage == nil || refreshed.FirstStage.ModelID != firstID || refreshed.InputID != report.InputID {
		t.Fatal("reranking changed the frozen first-stage input")
	}
	if refreshed.ID == previousID {
		t.Fatal("reranked report kept the degraded report id")
	}
	if _, err := RecomputeSecondStage(refreshed, c, time.Now().Add(2*time.Minute)); err != nil {
		t.Fatalf("healthy rerank should be stable: %v", err)
	}
}

func TestRecomputeSecondStageRejectsDegradingHealthyReport(t *testing.T) {
	c, h := secondStageDailyFixture(t)
	dir := filepath.Join(c.DataDir, "state", "secondstage", "jev")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc, err := json.Marshal(secondstage.JEVDocument{SchemaVersion: 1, AsOf: h.AsOf, Scores: map[string]secondstage.JEVScore{"sz000001": {Adjacent15: 0.7}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, h.AsOf+".json"), doc, 0o600); err != nil {
		t.Fatal(err)
	}
	healthy, err := Build(c, []market.History{h}, h.AsOf, "2026-10-14", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if healthy.SecondStage == nil || healthy.SecondStage.Degraded {
		t.Fatal("fixture did not build a healthy rerank")
	}
	if err := os.Remove(filepath.Join(dir, h.AsOf+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := RecomputeSecondStage(healthy, c, time.Now()); err == nil {
		t.Fatal("healthy report was replaced by a degraded recomputation")
	}
}

func TestReportIdentityIgnoresNestedGenerationTimes(t *testing.T) {
	report := Report{
		Version:     1,
		AsOf:        "2026-10-20",
		Target:      "2026-10-21",
		GeneratedAt: time.Unix(1, 0),
		FirstStage: &firststage.Report{
			Version:     firststage.ReportSchemaVersion,
			Status:      firststage.StatusOK,
			AsOf:        "2026-10-20",
			Target:      "2026-10-21",
			GeneratedAt: time.Unix(2, 0),
			Candidates:  []firststage.Candidate{{Rank: 1, Code: "sh600001"}},
		},
		SecondStage: &secondstage.Report{
			Version:     secondstage.ReportSchemaVersion,
			Status:      secondstage.StatusOK,
			AsOf:        "2026-10-20",
			Target:      "2026-10-21",
			GeneratedAt: time.Unix(3, 0),
			Candidates:  []secondstage.Candidate{{Rank: 1, Code: "sh600001"}},
		},
	}
	first, err := json.Marshal(reportIdentity(report))
	if err != nil {
		t.Fatal(err)
	}
	report.GeneratedAt = time.Unix(10, 0)
	report.FirstStage.GeneratedAt = time.Unix(20, 0)
	report.SecondStage.GeneratedAt = time.Unix(30, 0)
	second, err := json.Marshal(reportIdentity(report))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("nested generation time changed the report identity")
	}
}
