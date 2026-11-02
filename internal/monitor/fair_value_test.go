package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

type fakeFairValueSource struct {
	estimates map[string]fairvalue.Estimate
	errs      map[string]error
	calls     []string
	prices    []float64
}

func (f *fakeFairValueSource) Estimate(_ context.Context, stock config.Stock, price float64, asOf string) (fairvalue.Estimate, error) {
	f.calls = append(f.calls, stock.Code)
	f.prices = append(f.prices, price)
	if err := f.errs[stock.Code]; err != nil {
		return fairvalue.Estimate{}, err
	}
	if estimate, ok := f.estimates[stock.Code]; ok {
		return estimate, nil
	}
	return fairvalue.Estimate{Version: fairvalue.Version, Code: stock.Code, AsOf: asOf, Method: fairvalue.Method, Lower: price, Central: price, Upper: price}, nil
}

func (f *fakeFairValueSource) Assess(ctx context.Context, stock config.Stock, price float64, asOf string) (fairvalue.Assessment, error) {
	estimate, err := f.Estimate(ctx, stock, price, asOf)
	if err != nil {
		return fairvalue.Assessment{}, err
	}
	return fairvalue.Assessment{
		V1: estimate,
		V2: fairvalue.EstimateV2{Version: fairvalue.VersionV2, Code: stock.Code, AsOf: asOf,
			Source: "test", Method: fairvalue.MethodV2, DataQuality: fairvalue.DataQualityTTMOnly,
			Lower: price, Central: price, Upper: price},
	}, nil
}

type legacyFairValueSource struct {
	calls []string
}

func (f *legacyFairValueSource) Estimate(_ context.Context, stock config.Stock, price float64, asOf string) (fairvalue.Estimate, error) {
	f.calls = append(f.calls, stock.Code)
	return fairvalue.Estimate{Version: fairvalue.Version, Code: stock.Code, AsOf: asOf, Method: fairvalue.Method,
		Lower: price, Central: price, Upper: price}, nil
}

type timeoutFairValueSource struct {
	calls int
}

func (f *timeoutFairValueSource) Estimate(_ context.Context, stock config.Stock, price float64, asOf string) (fairvalue.Estimate, error) {
	return fairvalue.Estimate{Version: fairvalue.Version, Code: stock.Code, AsOf: asOf, Method: fairvalue.Method,
		Lower: price, Central: price, Upper: price}, nil
}

func (f *timeoutFairValueSource) Assess(ctx context.Context, _ config.Stock, _ float64, _ string) (fairvalue.Assessment, error) {
	f.calls++
	<-ctx.Done()
	return fairvalue.Assessment{}, ctx.Err()
}

func fairValueTestReport() selection.Report {
	return selection.Report{
		AsOf: "2026-10-21", Target: "2026-10-22",
		Ranking: []strategy.Candidate{{Code: "sz000001", Close: 10}, {Code: "sz000002", Close: 20}},
		SecondStage: &secondstage.Report{Status: secondstage.StatusOK, Candidates: []secondstage.Candidate{
			{Code: "sz000001"}, {Code: "sz000002"},
		}},
	}
}

func TestEnrichFairValuesAttachesOnlyTopCandidates(t *testing.T) {
	report := fairValueTestReport()
	source := &fakeFairValueSource{estimates: map[string]fairvalue.Estimate{
		"sz000001": {Version: fairvalue.Version, Code: "sz000001", Lower: 9, Central: 10, Upper: 11},
	}}
	m := &Monitor{Config: config.Config{Stocks: []config.Stock{{Code: "sz000001"}, {Code: "sz000002"}}}, FairValues: source, Log: logging.Nop()}
	m.enrichFairValues(context.Background(), &report)
	if len(report.FairValues) != 2 || len(report.FairValuesV2) != 2 || len(source.calls) != 2 {
		t.Fatalf("valuations=%d v2=%d calls=%d", len(report.FairValues), len(report.FairValuesV2), len(source.calls))
	}
	if report.FairValues["sz000001"].Central != 10 {
		t.Fatalf("unexpected estimate: %+v", report.FairValues["sz000001"])
	}
	if estimate := report.FairValuesV2["sz000001"]; estimate.Central != 10 || estimate.Method != fairvalue.MethodV2 {
		t.Fatalf("unexpected v2 estimate: %+v", estimate)
	}
}

type fairValueTestEngine struct{}

func (fairValueTestEngine) Descriptor() selection.EngineDescriptor {
	return selection.EngineDescriptor{ID: "leader_daily_v1", Version: 1, Periods: []string{"day"}, Adjust: "none", FinalOnly: true, MissingPolicy: "exclude"}
}

func (fairValueTestEngine) Analyze(_ context.Context, in selection.Input) (selection.Report, error) {
	return selection.Report{Version: 1, ID: "event-fair-value", AsOf: in.AsOf, Target: in.Target, GeneratedAt: in.Now,
		Strategy: "leader_daily_v1", SelectionEnabled: in.Config.Selection.Enabled, Adjust: "none",
		Ranking: []strategy.Candidate{{Code: "sz000001", Close: 10}}}, nil
}

func TestSyncEnrichesFairValuesAndReplayReusesSnapshot(t *testing.T) {
	m, _, _ := fixture(t)
	listingDatabase(t, m)
	m.BuildVersion = "fair-value-test"
	m.Engine = fairValueTestEngine{}
	source := &fakeFairValueSource{}
	m.FairValues = source
	first, err := m.Sync(context.Background(), "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	if first.FairValues["sz000001"].Central != 10 {
		t.Fatalf("sync report missing fair value: %+v", first.FairValues)
	}
	if first.FairValuesV2["sz000001"].Central != 10 {
		t.Fatalf("sync report missing v2 fair value: %+v", first.FairValuesV2)
	}
	calls := len(source.calls)
	replay, err := m.Replay(context.Background(), first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.calls) != calls {
		t.Fatalf("replay fetched valuation data: %d -> %d calls", calls, len(source.calls))
	}
	if replay.FairValues["sz000001"].Central != 10 {
		t.Fatalf("replay did not restore frozen fair value: %+v", replay.FairValues)
	}
	if replay.FairValuesV2["sz000001"].Central != 10 {
		t.Fatalf("replay did not restore frozen v2 fair value: %+v", replay.FairValuesV2)
	}
	preview, err := m.Preview("2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	if preview.FairValues["sz000001"].Central != 10 {
		t.Fatalf("preview did not restore saved fair value: %+v", preview.FairValues)
	}
	if preview.FairValuesV2["sz000001"].Central != 10 {
		t.Fatalf("preview did not restore saved v2 fair value: %+v", preview.FairValuesV2)
	}
	if len(source.calls) != calls {
		t.Fatalf("preview fetched valuation data: %d -> %d calls", calls, len(source.calls))
	}
}

func TestEnrichFairValuesPrefersRankingPriceForAdjustedReport(t *testing.T) {
	report := selection.Report{
		AsOf: "2026-10-21", Target: "2026-10-22", Adjust: "qfq",
		Ranking:  []strategy.Candidate{{Code: "sz000001", Close: 10}},
		Selected: []selection.Candidate{{Code: "sz000001", Close: 99}},
	}
	source := &fakeFairValueSource{}
	m := &Monitor{Config: config.Config{
		Stocks:   []config.Stock{{Code: "sz000001"}},
		Strategy: config.Strategy{Enabled: true},
	}, FairValues: source, Log: logging.Nop()}
	m.Config.Daily.Adjust = "none"
	m.enrichFairValues(context.Background(), &report)
	if len(source.calls) != 1 || len(source.prices) != 1 {
		t.Fatalf("calls=%v prices=%v", source.calls, source.prices)
	}
	if source.prices[0] != 10 {
		t.Fatalf("valuation received selected price instead of ranking price: %v", source.prices)
	}
	if report.FairValues["sz000001"].Central != 10 {
		t.Fatalf("unexpected estimate: %+v", report.FairValues["sz000001"])
	}
}

func TestFairValueCodesSkipsFailedSecondStage(t *testing.T) {
	report := selection.Report{
		Selected: []selection.Candidate{{Code: "sz000001"}},
		Ranking:  []strategy.Candidate{{Code: "sz000001"}, {Code: "sz000002"}},
		SecondStage: &secondstage.Report{Status: secondstage.StatusFailed, Candidates: []secondstage.Candidate{
			{Code: "sz000003"},
		}},
	}
	codes := fairValueCodes(report)
	if len(codes) != 2 || codes[0] != "sz000001" || codes[1] != "sz000002" {
		t.Fatalf("degraded second stage leaked into valuation codes: %v", codes)
	}
}

func TestEnrichFairValuesRecordsSourceFailure(t *testing.T) {
	report := fairValueTestReport()
	source := &fakeFairValueSource{errs: map[string]error{"sz000001": errors.New("down")}}
	m := &Monitor{Config: config.Config{Stocks: []config.Stock{{Code: "sz000001"}}}, FairValues: source, Log: logging.Nop()}
	m.enrichFairValues(context.Background(), &report)
	estimate := report.FairValues["sz000001"]
	if estimate.Available() || estimate.Reason == "" {
		t.Fatalf("source failure was not recorded: %+v", estimate)
	}
	v2 := report.FairValuesV2["sz000001"]
	if v2.Available() || v2.Reason == "" || v2.Method != fairvalue.MethodV2 {
		t.Fatalf("source failure was not recorded for v2: %+v", v2)
	}
}

func TestEnrichFairValuesRequiresUnadjustedPrice(t *testing.T) {
	report := fairValueTestReport()
	source := &fakeFairValueSource{}
	m := &Monitor{Config: config.Config{Stocks: []config.Stock{{Code: "sz000001"}}}, FairValues: source, Log: logging.Nop()}
	m.Config.Daily.Adjust = "qfq"
	m.enrichFairValues(context.Background(), &report)
	if len(source.calls) != 0 {
		t.Fatalf("adjusted prices were sent to valuation source: %v", source.calls)
	}
	for _, code := range []string{"sz000001", "sz000002"} {
		if estimate := report.FairValues[code]; estimate.Available() || estimate.Reason != "需要不复权收盘价" {
			t.Fatalf("missing price-basis guard for %s: %+v", code, estimate)
		}
		if estimate := report.FairValuesV2[code]; estimate.Available() || estimate.Reason != "需要不复权收盘价" || estimate.Method != fairvalue.MethodV2 {
			t.Fatalf("missing v2 price-basis guard for %s: %+v", code, estimate)
		}
	}
}

func TestEnrichFairValuesWithoutAssessKeepsV2Empty(t *testing.T) {
	report := fairValueTestReport()
	source := &legacyFairValueSource{}
	m := &Monitor{Config: config.Config{
		Stocks:   []config.Stock{{Code: "sz000001"}, {Code: "sz000002"}},
		Strategy: config.Strategy{Enabled: true},
	}, FairValues: source, Log: logging.Nop()}
	m.enrichFairValues(context.Background(), &report)
	if len(report.FairValues) != 2 || len(source.calls) != 2 {
		t.Fatalf("legacy valuations=%d calls=%d", len(report.FairValues), len(source.calls))
	}
	if report.FairValuesV2 != nil {
		t.Fatalf("legacy source produced v2 values: %+v", report.FairValuesV2)
	}
}

func TestEnrichFairValuesTimeoutMarksBothVersionsUnavailable(t *testing.T) {
	report := fairValueTestReport()
	source := &timeoutFairValueSource{}
	m := &Monitor{Config: config.Config{
		Stocks:   []config.Stock{{Code: "sz000001"}, {Code: "sz000002"}},
		Strategy: config.Strategy{Enabled: true},
	}, FairValues: source, Log: logging.Nop()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	m.enrichFairValues(ctx, &report)
	if source.calls != 1 {
		t.Fatalf("timeout should stop further assessments: calls=%d", source.calls)
	}
	for _, code := range []string{"sz000001", "sz000002"} {
		if estimate := report.FairValues[code]; estimate.Available() || estimate.Reason == "" {
			t.Fatalf("timeout v1 result unavailable state missing for %s: %+v", code, estimate)
		}
		if estimate := report.FairValuesV2[code]; estimate.Available() || estimate.Reason == "" || estimate.Method != fairvalue.MethodV2 {
			t.Fatalf("timeout v2 result unavailable state missing for %s: %+v", code, estimate)
		}
	}
}
