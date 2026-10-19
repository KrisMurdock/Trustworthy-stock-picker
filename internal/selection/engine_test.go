package selection

import (
	"context"
	"errors"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"strings"
	"testing"
	"time"
)

type testEngine struct {
	mutate  bool
	invalid bool
	enable  bool
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

func TestBuiltinEnginePreservesChanGateAndRejectsNonFinalMinutes(t *testing.T) {
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
		t.Fatalf("engine bypassed Chan gate: %+v", report.Ranking)
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
