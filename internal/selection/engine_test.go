package selection

import (
	"context"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"strings"
	"testing"
	"time"
)

type testEngine struct {
	mutate          bool
	invalid         bool
	enable          bool
	first           bool
	firstCode       string
	second          bool
	secondBad       bool
	secondDuplicate bool
}

func (testEngine) Descriptor() EngineDescriptor {
	return EngineDescriptor{ID: "test_v1", Version: 1, Periods: []string{"day"}, Adjust: "none", FinalOnly: true, MissingPolicy: "exclude"}
}
func (e testEngine) Analyze(ctx context.Context, in Input) (Report, error) {
	if in.Config.Feishu.Webhook != "" || in.Config.DataDir != "" {
		return Report{}, errors.New("credentials leaked")
	}
	if e.mutate {
		in.Histories[0].Bars[0].Close = 100
	}
	r := Report{Version: 1, ID: "test", AsOf: in.AsOf, Target: in.Target, Adjust: "none", Strategy: "test_v1"}
	r.SelectionEnabled = e.enable
	if e.invalid {
		r.Selected = []Candidate{{Code: "sh600519"}}
	}
	if e.first {
		code := "sh600519"
		if e.firstCode != "" {
			code = e.firstCode
		}
		r.FirstStage = &firststage.Report{Version: 1, Status: firststage.StatusOK, AsOf: in.AsOf, Target: in.Target, Budget: 1, Candidates: []firststage.Candidate{{Rank: 1, Code: code}}}
	}
	if e.second {
		code := "sz000001"
		if e.secondBad {
			code = "sh600519"
		}
		candidates := []secondstage.Candidate{{Rank: 1, Code: code}}
		if e.secondDuplicate {
			candidates = []secondstage.Candidate{{Rank: 1, Code: code}, {Rank: 2, Code: code}}
		}
		r.SecondStage = &secondstage.Report{Version: secondstage.ReportSchemaVersion, Status: secondstage.StatusOK, AsOf: in.AsOf, Target: in.Target, TopN: len(candidates), Candidates: candidates}
	}
	return r, nil
}
func TestEngineBoundaryCopiesInputsAndRejectsInvalidResults(t *testing.T) {
	c := config.Defaults()
	c.Daily.Adjust = "none"
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "test"}}
	c.Feishu.Webhook = "private-hook"
	c.DataDir = "private-path"
	h := market.History{Version: 1, Code: "sz000001", Name: "test", Source: "tencent", Adjust: "none", DataField: "day", VolumeUnit: "provider_raw", AsOf: "2026-10-16", FetchedAt: time.Now(), Bars: []market.Bar{{Date: "2026-10-16", Open: 10, High: 11, Low: 9, Close: 10, Volume: 1}}}
	in := Input{Config: c, Histories: []market.History{h}, AsOf: h.AsOf, Target: "2026-10-19", Now: time.Now()}
	result, err := Execute(context.Background(), testEngine{mutate: true}, in, "test-build")
	if err != nil {
		t.Fatal(err)
	}
	if h.Bars[0].Close != 10 {
		t.Fatal("algorithm mutated collection")
	}
	if result.Provenance == nil || result.Provenance.ConfigID == "" || result.Provenance.Build != "test-build" || strings.Contains(string(result.Provenance.Parameters), "private") {
		t.Fatal("invalid provenance")
	}
	if _, err := Execute(context.Background(), testEngine{invalid: true}, in, "test"); err == nil {
		t.Fatal("outside-pool result accepted")
	}
	if _, err := Execute(context.Background(), testEngine{enable: true}, in, "test"); err == nil {
		t.Fatal("algorithm enabled disabled publishing")
	}
	if _, err := Execute(context.Background(), testEngine{first: true}, in, "test"); err == nil {
		t.Fatal("outside-pool first-stage result accepted")
	}
	if _, err := Execute(context.Background(), testEngine{first: true, firstCode: "sz000001", second: true, secondBad: true}, in, "test"); err == nil {
		t.Fatal("outside-pool second-stage result accepted")
	}
	if _, err := Execute(context.Background(), testEngine{first: true, firstCode: "sz000001", second: true, secondDuplicate: true}, in, "test"); err == nil {
		t.Fatal("duplicate second-stage result accepted")
	}
	if _, err := Execute(context.Background(), testEngine{second: true}, in, "test"); err == nil {
		t.Fatal("second-stage result without first-stage pool accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Execute(ctx, testEngine{}, in, "test"); err != context.Canceled {
		t.Fatal("cancellation ignored", err)
	}
	c.Analysis.Engine = "short_horizon_v1"
	if _, err := DefaultEngine(c); err == nil {
		t.Fatal("misconfigured production algorithm accepted")
	}
}

func TestBuiltinEngineKeepsLegacyChanDiagnosticAndRejectsNonFinalMinutes(t *testing.T) {
	c, histories, raw := executionFixture()
	asof := histories[0].AsOf
	h := singleDayMinuteHistory(asof, raw[0].Bars[len(raw[0].Bars)-1])
	engine, err := DefaultEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Config: c, Histories: histories, Raw: raw, Minutes: []market.MinuteHistory{h}, AsOf: asof, Target: "2026-10-20", Now: time.Now()}
	report, err := Execute(context.Background(), engine, in, "merge-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Ranking) != 1 || report.Ranking[0].Chan == nil || report.Ranking[0].Chan.Status != "unknown" || report.Ranking[0].Recommended {
		t.Fatalf("engine produced an executable shadow recommendation: %+v", report.Ranking)
	}
	if report.ExecutionMode != "shadow" || len(report.Selected) != 0 {
		t.Fatalf("shadow report selected an executable candidate: %+v", report)
	}
	in.Minutes[0].Bars[0].Final = false
	if _, err := Execute(context.Background(), engine, in, "merge-test"); err == nil {
		t.Fatal("forming minute bar accepted")
	}
	in.Minutes[0].Bars[0].Final = true
	for i := range in.Minutes[0].Bars {
		in.Minutes[0].Bars[i].End = in.Minutes[0].Bars[i].End.AddDate(0, 0, 7)
	}
	in.Minutes[0].FetchedAt = in.Minutes[0].FetchedAt.AddDate(0, 0, 7)
	if _, err := Execute(context.Background(), engine, in, "merge-test"); err == nil || !strings.Contains(err.Error(), "not final as of report date") {
		t.Fatal("future minute input did not fail the as-of guard", err)
	}
}

func TestEngineRequiresAlignedThirtyAndFiveMinuteSnapshot(t *testing.T) {
	c, histories, raw := executionFixture()
	asof := histories[0].AsOf
	base := singleDayMinuteHistory(asof, raw[0].Bars[len(raw[0].Bars)-1])
	derived := base
	derived.Period = 30
	derived.Bars = market.AggregateMinutes(base.Bars, 30, base.FetchedAt)
	engine, err := DefaultEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Config: c, Histories: histories, Raw: raw, Minutes: []market.MinuteHistory{derived, base}, AsOf: asof, Target: "2026-10-20", Now: time.Now()}
	if _, err := Execute(context.Background(), engine, in, "pair-test"); err != nil {
		t.Fatal(err)
	}
	in.Minutes[0].FetchedAt = in.Minutes[0].FetchedAt.Add(time.Minute)
	if _, err := Execute(context.Background(), engine, in, "pair-test"); err == nil || !strings.Contains(err.Error(), "one snapshot") {
		t.Fatal("cross-snapshot periods accepted", err)
	}
	in.Minutes[0] = derived
	in.Minutes[1].Bars = append([]market.MinuteBar(nil), base.Bars...)
	in.Minutes[1].Bars[0].High += 0.1
	if _, err := Execute(context.Background(), engine, in, "pair-test"); err == nil || !strings.Contains(err.Error(), "one snapshot") {
		t.Fatal("relabelled aggregate accepted", err)
	}
}
