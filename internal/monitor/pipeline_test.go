package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"testing"
)

type brokenEngine struct{}

func (brokenEngine) Descriptor() selection.EngineDescriptor {
	return selection.EngineDescriptor{ID: "leader_daily_v1", Version: 1, Periods: []string{"day"}, Adjust: "none", FinalOnly: true, MissingPolicy: "exclude"}
}
func (brokenEngine) Analyze(context.Context, selection.Input) (selection.Report, error) {
	return selection.Report{}, errors.New("calculation failed")
}
func TestAnalysisFailureRetainsCollectedDataAndResumesWithoutDownload(t *testing.T) {
	m, source, _ := fixture(t)
	listingDatabase(t, m)
	m.Engine = brokenEngine{}
	if _, err := m.Sync(context.Background(), "2026-10-13"); err == nil {
		t.Fatal("analysis should fail")
	}
	if _, err := m.Market.LoadHistories(context.Background(), m.Config.Stocks, "2026-10-13", "none"); err != nil {
		t.Fatal("analysis lost valid downloaded data", err)
	}
	calls := source.calls
	m.Engine = nil
	if _, err := m.Sync(context.Background(), "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	if source.calls != calls {
		t.Fatal("analysis retry downloaded inputs again")
	}
	if _, err := m.Sync(context.Background(), "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	if source.calls == calls {
		t.Fatal("completed startup did not refresh")
	}
}
func TestInterruptedCollectionResumesSuccessfulStocks(t *testing.T) {
	m, source, _ := fixture(t)
	listingDatabase(t, m)
	m.Config.Stocks = append(m.Config.Stocks, config.Stock{Code: "sz000002", Name: "two", ListedFrom: "2026-07-15"})
	source.err = errors.New("interrupted source")
	source.failCode = "sz000002"
	if _, err := m.Sync(context.Background(), "2026-10-13"); err == nil {
		t.Fatal("expected interrupted collection")
	}
	calls := source.calls
	source.err = nil
	if _, err := m.Sync(context.Background(), "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	if source.calls-calls != 1 {
		t.Fatalf("downloaded %d stocks after resume", source.calls-calls)
	}
}

func TestStrategySkipsStockWithoutAlignedRawHistory(t *testing.T) {
	m, source, _ := fixture(t)
	listingDatabase(t, m)
	m.Config.Stocks = append(m.Config.Stocks, config.Stock{Code: "sz000002", Name: "two", ListedFrom: "2026-07-15"})
	m.Config.Strategy.Enabled = true
	m.Config.Daily.Adjust = "qfq"
	m.Config.FirstStage.Enabled = true
	m.Config.Sync.MinCoveragePct = 50
	m.Config.Sync.MinAnalyzablePct = 50
	source.err = &market.Failure{Kind: "network"}
	source.failCode = "sz000002"
	source.failAdjust = "none"
	report, err := m.Sync(context.Background(), "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	if report.FirstStage == nil || report.FirstStage.Eligible == 0 {
		t.Fatalf("aligned stock was not analyzed: %+v", report.FirstStage)
	}
	for _, candidate := range report.FirstStage.Candidates {
		if candidate.Code == "sz000002" {
			t.Fatal("stock without raw execution history leaked into the algorithm")
		}
	}
}

func TestReportReplayRestoresSupersededInputAndRejectsWrongBuild(t *testing.T) {
	m, _, _ := fixture(t)
	listingDatabase(t, m)
	m.BuildVersion = "test-build"
	first, err := m.Sync(context.Background(), "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision == "" || first.InputID == "" {
		t.Fatal("missing immutable input reference")
	}
	if _, err := m.Sync(context.Background(), "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	replay, err := m.Replay(context.Background(), first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(first)
	after, _ := json.Marshal(replay)
	if string(before) != string(after) {
		t.Fatalf("replay changed report\n%s\n%s", before, after)
	}
	m.BuildVersion = "changed-build"
	if _, err := m.Replay(context.Background(), first.Revision); err == nil {
		t.Fatal("wrong build replay accepted")
	}
}
