package dayrange

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

type fakeExecutionMarket struct {
	fakeDayRangeMarket
	report selection.Report
	err    error
}

func (f fakeExecutionMarket) LoadReport(context.Context, string) (selection.Report, error) {
	return f.report, f.err
}

func executionTestReport() selection.Report {
	firstPlan := &strategy.Plan{Version: 1, EntryPrice: 10, TakeProfit: 10.3, StopPrice: 9.7, Shares: 1900}
	secondPlan := &strategy.Plan{Version: 1, EntryPrice: 20, TakeProfit: 20.6, StopPrice: 19.4, Shares: 900}
	firstRanked := strategy.Candidate{Code: "sz000001", Name: "测试银行", Rank: 1, Plan: firstPlan, Recommended: true}
	secondRanked := strategy.Candidate{Code: "sz000002", Name: "测试制造", Rank: 2, Plan: secondPlan}
	return selection.Report{
		Version:          3,
		ID:               "report-1",
		AsOf:             "2026-10-20",
		Target:           "2026-10-21",
		Strategy:         "short_horizon_v1",
		ExecutionMode:    "legacy_daily",
		SelectionEnabled: true,
		PoolSize:         3,
		Eligible:         3,
		Selected: []selection.Candidate{
			{Code: "sz000001", Name: "测试银行", Ranking: &firstRanked},
		},
		Ranking: []strategy.Candidate{firstRanked, secondRanked},
		SecondStage: &secondstage.Report{
			Version: 1, Status: secondstage.StatusOK, AsOf: "2026-10-20", Target: "2026-10-21",
			TopN: 3, InputCount: 3, PushEnabled: true,
			Candidates: []secondstage.Candidate{
				{Rank: 1, Code: "sz000001", Name: "测试银行", FirstStageRank: 3, PredictedMaxHigh: 5.2},
				{Rank: 2, Code: "sz000002", Name: "测试制造", FirstStageRank: 8, PredictedMaxHigh: 4.1},
				{Rank: 3, Code: "sz000003", Name: "测试材料", FirstStageRank: 12, PredictedMaxHigh: 3.5},
			},
		},
	}
}

func executionTestPredictions() []Prediction {
	return []Prediction{
		{
			Code: "sz000001", TargetDate: "2026-10-21", Mode: ModePremarket, InputThrough: "2026-10-20",
			ReferencePrice: 10, PointHigh: 10.5, PointLow: 9.8, Source: "model", ModelID: "tree-1",
			IntervalHigh: &Interval{Lower: 10.2, Upper: 10.7}, IntervalLow: &Interval{Lower: 9.6, Upper: 9.9},
		},
		{
			Code: "sz000002", TargetDate: "2026-10-21", Mode: ModePremarket, InputThrough: "2026-10-20",
			ReferencePrice: 20, PointHigh: 20.2, PointLow: 19.0, Source: "baseline",
			IntervalHigh: &Interval{Lower: 20.0, Upper: 20.4}, IntervalLow: &Interval{Lower: 18.8, Upper: 19.2},
		},
	}
}

func TestBuildExecutionCardJoinsRankingPredictionsAndPlanStatus(t *testing.T) {
	message, ok, err := BuildExecutionCard(ExecutionCardInput{
		SignalDate: "2026-10-20", TargetDate: "2026-10-21",
		GeneratedAt: time.Date(2026, 10, 21, 8, 45, 0, 0, shanghai),
		Report:      executionTestReport(), Predictions: executionTestPredictions(),
	})
	if err != nil || !ok {
		t.Fatalf("build execution card: ok=%v err=%v", ok, err)
	}
	if message.EventID != "premarket-execution:2026-10-21" || message.Title == "" {
		t.Fatalf("unexpected identity: %+v", message)
	}
	if !message.ExpiresAt.Equal(time.Date(2026, 10, 21, 9, 30, 0, 0, shanghai)) {
		t.Fatalf("expiry=%s", message.ExpiresAt)
	}
	if len(message.Sections) != 2 || message.Sections[1].Table == nil {
		t.Fatalf("missing table: %+v", message.Sections)
	}
	table := message.Sections[1].Table
	if len(table.Columns) != 8 || len(table.Rows) != 3 {
		t.Fatalf("columns=%d rows=%d", len(table.Columns), len(table.Rows))
	}
	if table.Rows[0][7].Text != "可执行" || table.Rows[1][7].Text != "放弃" || table.Rows[2][7].Text != "观察" {
		t.Fatalf("unexpected statuses: %q %q %q", table.Rows[0][7].Text, table.Rows[1][7].Text, table.Rows[2][7].Text)
	}
	lines := strings.Join(message.Sections[1].Lines, " ")
	if !strings.Contains(lines, "可执行 1") || !strings.Contains(lines, "放弃 1") || !strings.Contains(lines, "缺少盘前预测") || !strings.Contains(lines, "不构成投资建议") {
		t.Fatalf("missing status or caveat lines: %q", lines)
	}
	fields := message.Sections[0].Fields
	if !strings.Contains(fmtFields(fields), "正式推荐 1 只") || !strings.Contains(fmtFields(fields), "预测覆盖 2/3") {
		t.Fatalf("unexpected fields: %+v", fields)
	}
}

func TestBuildExecutionCardRequiresPredictionAndHandlesShadow(t *testing.T) {
	if _, ok, err := BuildExecutionCard(ExecutionCardInput{
		TargetDate: "2026-10-21", Report: executionTestReport(),
	}); err != nil || ok {
		t.Fatalf("card without predictions accepted: ok=%v err=%v", ok, err)
	}

	report := executionTestReport()
	report.ExecutionMode = strategy.StructureGuardShadow
	message, ok, err := BuildExecutionCard(ExecutionCardInput{
		TargetDate: "2026-10-21", Report: report, Predictions: executionTestPredictions(),
		GeneratedAt: time.Date(2026, 10, 21, 8, 45, 0, 0, shanghai),
	})
	if err != nil || !ok {
		t.Fatalf("shadow card: ok=%v err=%v", ok, err)
	}
	if got := message.Sections[1].Table.Rows[0][7].Text; got != "观察" {
		t.Fatalf("shadow status=%q want 观察", got)
	}
	if !strings.Contains(strings.Join(message.Sections[1].Lines, " "), "结构影子期") {
		t.Fatalf("shadow caveat missing: %+v", message.Sections[1].Lines)
	}
}

func TestPremarketExecutionDeliveryRoutesIndependentlyAndIsIdempotent(t *testing.T) {
	c := deliveryServiceConfig()
	c.Notify.Channels = append(c.Notify.Channels, config.NotifyChannel{Name: "premarket-execution", Type: "feishu", Enabled: true, Route: config.NotifyRoutePremarketExecution})
	now := time.Date(2026, 10, 21, 8, 45, 0, 0, shanghai)
	service := NewService(c, newFakeDayRangeStore(), fakeExecutionMarket{report: executionTestReport()}, nil)
	service.Now = func() time.Time { return now }
	state := &fakeDeliveryStore{}
	selectionChannel := &fakeDayRangeChannel{name: "selection-bot"}
	dayrangeChannel := &fakeDayRangeChannel{name: "predictor-dayrange"}
	executionChannel := &fakeDayRangeChannel{name: "premarket-execution"}
	if err := service.ConfigureExecutionDelivery(state, []notify.Channel{selectionChannel, dayrangeChannel, executionChannel}); err != nil {
		t.Fatal(err)
	}
	if err := service.publishExecutionCard(context.Background(), "2026-10-21", executionTestPredictions(), now); err != nil {
		t.Fatal(err)
	}
	if len(executionChannel.sent) != 1 || len(selectionChannel.sent) != 0 || len(dayrangeChannel.sent) != 0 {
		t.Fatalf("route isolation failed: execution=%d selection=%d dayrange=%d", len(executionChannel.sent), len(selectionChannel.sent), len(dayrangeChannel.sent))
	}
	if len(service.executionLedger.Events) != 1 {
		t.Fatalf("execution ledger events=%d want 1", len(service.executionLedger.Events))
	}
	if err := service.publishExecutionCard(context.Background(), "2026-10-21", executionTestPredictions(), now); err != nil {
		t.Fatal(err)
	}
	if len(executionChannel.sent) != 1 || len(service.executionLedger.Events) != 1 {
		t.Fatalf("duplicate delivery: sent=%d events=%d", len(executionChannel.sent), len(service.executionLedger.Events))
	}
}

func TestPremarketExecutionSkipsMissingReportAndCorruptLedger(t *testing.T) {
	c := deliveryServiceConfig()
	c.Notify.Channels = append(c.Notify.Channels, config.NotifyChannel{Name: "premarket-execution", Type: "feishu", Enabled: true, Route: config.NotifyRoutePremarketExecution})
	now := time.Date(2026, 10, 21, 8, 45, 0, 0, shanghai)
	channel := &fakeDayRangeChannel{name: "premarket-execution"}
	service := NewService(c, newFakeDayRangeStore(), fakeExecutionMarket{err: fs.ErrNotExist}, nil)
	service.Now = func() time.Time { return now }
	state := &fakeDeliveryStore{}
	if err := service.ConfigureExecutionDelivery(state, []notify.Channel{channel}); err != nil {
		t.Fatal(err)
	}
	if err := service.publishExecutionCard(context.Background(), "2026-10-21", executionTestPredictions(), now); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 0 || len(service.executionLedger.Events) != 0 {
		t.Fatalf("missing report created delivery: sent=%d events=%d", len(channel.sent), len(service.executionLedger.Events))
	}

	corrupt := &fakeDeliveryStore{exists: true, raw: []byte(`{"schema_version":2,"events":{"bad":{}}}`)}
	err := service.ConfigureExecutionDelivery(corrupt, []notify.Channel{channel})
	if err == nil || !strings.Contains(err.Error(), "invalid persisted delivery ledger") {
		t.Fatalf("corrupt execution ledger accepted: %v", err)
	}
}

func fmtFields(fields []notify.Field) string {
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, field.Label+" "+field.Value)
	}
	return strings.Join(parts, " ")
}
