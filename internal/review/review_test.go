package review

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"io/fs"
	"testing"
	"time"
)

type fakeLoader struct {
	report         selection.Report
	revisions      []Revision
	inputs         map[string]market.History
	latest         map[string]market.History
	dataAsOf       string
	calendar       CalendarWindow
	loadErr        error
	calendarErr    error
	seenAsOf       string
	seenRevision   string
	inputReport    selection.Report
	latestCodeCall []string
}

func (f *fakeLoader) ListRevisions(_ context.Context, asof string) ([]Revision, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.revisions, nil
}
func (f *fakeLoader) LoadReport(_ context.Context, asof, revision string) (selection.Report, error) {
	f.seenAsOf, f.seenRevision = asof, revision
	return f.report, f.loadErr
}
func (f *fakeLoader) LoadInputs(_ context.Context, report selection.Report) (map[string]market.History, error) {
	f.inputReport = report
	return f.inputs, f.loadErr
}
func (f *fakeLoader) LoadLatestMarkets(_ context.Context, codes []string) (map[string]market.History, string, error) {
	f.latestCodeCall = append([]string(nil), codes...)
	return f.latest, f.dataAsOf, f.loadErr
}
func (f *fakeLoader) LoadCalendar(_ context.Context, _, _ string) (CalendarWindow, error) {
	return f.calendar, f.calendarErr
}

func reviewFixture(t *testing.T) (*fakeLoader, strategy.Candidate) {
	t.Helper()
	params := config.Defaults()
	params.Version = 1
	params.Strategy.Enabled = true
	digest, raw := selection.ConfigDigest(params)
	report := selection.Report{Version: 2, ID: "report-id", AsOf: "2026-10-12", Target: "2026-10-13", GeneratedAt: time.Date(2026, 10, 12, 18, 5, 0, 0, time.FixedZone("CST", 8*60*60)), Strategy: "short_horizon_v1", Revision: "revision-one", Provenance: &selection.Provenance{ConfigID: digest, Parameters: raw}, Ranking: []strategy.Candidate{{Code: "sz000001", Name: "测试银行", Rank: 1, Recommended: true, Risks: []string{"risk"}}}}
	plan, err := strategy.NewPlan(params.Strategy, "sz000001", 10)
	if err != nil {
		t.Fatal(err)
	}
	report.Ranking[0].Plan = &plan
	signal := market.Bar{Date: "2026-10-12", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000}
	latest := market.History{Version: 1, Code: "sz000001", Name: "测试银行", Adjust: "none", AsOf: "2026-10-20", Bars: []market.Bar{
		signal,
		{Date: "2026-10-13", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000},
		{Date: "2026-10-14", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000},
		{Date: "2026-10-15", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000},
		{Date: "2026-10-16", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000},
		{Date: "2026-10-19", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000},
		{Date: "2026-10-20", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000},
	}}
	window := CalendarWindow{From: "2026-10-12", Through: "2026-10-20", Days: []string{"2026-10-12", "2026-10-13", "2026-10-14", "2026-10-15", "2026-10-16", "2026-10-19", "2026-10-20"}, Source: "test"}
	return &fakeLoader{report: report, revisions: []Revision{{ID: "revision-one", AsOf: "2026-10-12", GeneratedAt: report.GeneratedAt, SchemaVersion: 2}}, inputs: map[string]market.History{"sz000001": {Bars: []market.Bar{signal}}}, latest: map[string]market.History{"sz000001": latest}, dataAsOf: "2026-10-20", calendar: window}, report.Ranking[0]
}

func TestDayProjectsMatureOutcomeAndCheckpoints(t *testing.T) {
	loader, candidate := reviewFixture(t)
	service := New(loader)
	day, err := service.Day(context.Background(), "2026-10-12", "revision-one")
	if err != nil {
		t.Fatal(err)
	}
	if day.Revision != "revision-one" || day.DataAsOf != "2026-10-20" || len(day.Revisions) != 1 || day.Events[0].State != StateMature {
		t.Fatalf("unexpected day: %+v", day)
	}
	if day.Events[0].ExitReason != "timeout" || day.Events[0].NetPct == nil || day.Events[0].EntryDate != "2026-10-13" || day.Events[0].ExitDate != "2026-10-20" {
		t.Fatalf("unexpected outcome summary: %+v", day.Events[0])
	}
	if len(day.Events[0].Checkpoints) != 3 || day.Events[0].Checkpoints[1].Day != 3 || day.Events[0].Checkpoints[2].Day != 5 {
		t.Fatalf("checkpoints missing: %+v", day.Events[0].Checkpoints)
	}
	if day.Aggregate.All.Mature != 1 || day.Aggregate.All.Timeout != 1 || day.Aggregate.Recommended.Mature != 1 || day.Aggregate.Observed.Total != 0 {
		t.Fatalf("aggregate mismatch: %+v", day.Aggregate)
	}
	event, err := service.Event(context.Background(), "2026-10-12", candidate.Code, "")
	if err != nil {
		t.Fatal(err)
	}
	if event.ReportRevision != "revision-one" || event.SignalBar == nil || event.Outcome == nil || event.Plan == nil || len(event.Evidence) < 5 {
		t.Fatalf("event evidence missing: %+v", event)
	}
	if _, err := service.Event(context.Background(), "2026-10-12", "sz000002", ""); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("unknown event accepted: %v", err)
	}
}

func TestDayKeepsUnavailableCategoriesOutOfReturns(t *testing.T) {
	loader, _ := reviewFixture(t)
	loader.latest["sz000001"] = market.History{Bars: []market.Bar{{Date: "2026-10-12", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000}}}
	loader.dataAsOf = "2026-10-12"
	loader.calendar = CalendarWindow{}
	day, err := New(loader).Day(context.Background(), "2026-10-12", "")
	if err != nil {
		t.Fatal(err)
	}
	if day.Events[0].State != StatePending || day.Events[0].StateReason != "waiting_for_next_session" || day.Aggregate.All.Pending != 1 || day.Aggregate.All.Mature != 0 || day.Aggregate.All.MeanNetPct != nil {
		t.Fatalf("unavailable event entered returns: %+v", day)
	}
}

func TestDayMarksMissingFrozenSignalAndLegacyReports(t *testing.T) {
	loader, _ := reviewFixture(t)
	loader.inputs = map[string]market.History{"sz000001": {Bars: []market.Bar{{Date: "2026-10-10", Open: 10, Close: 10}}}}
	day, err := New(loader).Day(context.Background(), "2026-10-12", "")
	if err != nil {
		t.Fatal(err)
	}
	if day.Events[0].State != StateMissingData || day.Events[0].StateReason != "signal_bar_missing" || day.Aggregate.All.Missing != 1 {
		t.Fatalf("missing signal not classified: %+v", day)
	}

	legacy := selection.Report{Version: 1, AsOf: "2026-10-12", Strategy: "leader_daily_v1", Selected: []selection.Candidate{{Code: "sz000001", Name: "测试银行"}}}
	loader.report = legacy
	day, err = New(loader).Day(context.Background(), "2026-10-12", "")
	if err != nil {
		t.Fatal(err)
	}
	if day.Events[0].State != StateUnsupported || day.Aggregate.All.Unsupported != 1 {
		t.Fatalf("legacy report not explicit: %+v", day)
	}
}

func TestDayCalculatesNotEnteredAndParameterFailure(t *testing.T) {
	loader, _ := reviewFixture(t)
	history := loader.latest["sz000001"]
	history.Bars[1].Open = 12
	history.Bars[1].High = 12
	loader.latest["sz000001"] = history
	day, err := New(loader).Day(context.Background(), "2026-10-12", "")
	if err != nil {
		t.Fatal(err)
	}
	if day.Events[0].State != StateNotEntered || day.Events[0].StateReason != "entry_skipped" || day.Aggregate.All.NotEntered != 1 || day.Aggregate.All.Mature != 0 {
		t.Fatalf("not-entered event counted in returns: %+v", day)
	}

	loader.report.Provenance = nil
	day, err = New(loader).Day(context.Background(), "2026-10-12", "")
	if err != nil {
		t.Fatal(err)
	}
	if day.Events[0].State != StateUnsupported || day.Warnings[0] != "frozen_report_parameters_missing" {
		t.Fatalf("parameter failure not explicit: %+v", day)
	}
}

func TestReportParametersMustMatchDigest(t *testing.T) {
	loader, _ := reviewFixture(t)
	loader.report.Provenance.ConfigID = "different"
	day, err := New(loader).Day(context.Background(), "2026-10-12", "")
	if err != nil {
		t.Fatal(err)
	}
	if day.Events[0].State != StateUnsupported || day.Warnings[0] != "frozen_report_parameter_digest_mismatch" {
		t.Fatalf("digest mismatch not rejected: %+v", day)
	}
}

func TestEventRevisionAndNetworkErrors(t *testing.T) {
	loader, _ := reviewFixture(t)
	day, err := New(loader).Day(context.Background(), "2026-10-12", "revision-one")
	if err != nil || loader.seenRevision != "revision-one" || day.Revision != "revision-one" {
		t.Fatalf("revision not passed: %+v %v", day, err)
	}
	loader.loadErr = errors.New("private database failure")
	if _, err := New(loader).Day(context.Background(), "2026-10-12", ""); err == nil || err.Error() != "private database failure" {
		t.Fatalf("loader error hidden: %v", err)
	}
}

func TestJSONOmitsUnknownNumericValues(t *testing.T) {
	loader, _ := reviewFixture(t)
	loader.latest["sz000001"] = market.History{Bars: []market.Bar{{Date: "2026-10-12", Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000}}}
	loader.dataAsOf = "2026-10-12"
	loader.calendar = CalendarWindow{}
	day, err := New(loader).Day(context.Background(), "2026-10-12", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(day)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || day.Aggregate.All.MeanNetPct != nil {
		t.Fatal("invalid JSON projection")
	}
}
