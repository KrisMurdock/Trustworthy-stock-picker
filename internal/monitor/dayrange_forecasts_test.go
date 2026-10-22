package monitor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	reportmessage "github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify/report"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func TestWithDayRangeForecastsInjectsReportCopy(t *testing.T) {
	m, report := secondStageRefreshFixture(t)
	report.SecondStage.Candidates = append(report.SecondStage.Candidates, secondstage.Candidate{Code: "sz000002", Name: "第二只"})
	report.Ranking = []strategy.Candidate{{Code: "sz000001", Name: "测试股票", Close: 10, DayHigh: 10.6, DayLow: 9.3}}
	called := false
	m.DayRangeForecasts = func(_ context.Context, target string, codes []string) (map[string]selection.DayRangeForecast, error) {
		called = true
		if target != report.Target {
			t.Fatalf("target = %s, want %s", target, report.Target)
		}
		if len(codes) != 2 || codes[0] != "sz000001" || codes[1] != "sz000002" {
			t.Fatalf("codes = %v", codes)
		}
		return map[string]selection.DayRangeForecast{
			"sz000001": {
				Code: "sz000001", TargetDate: target, ModelID: "model-1", Source: "saved",
				ReferencePrice: 10, PointHigh: 10.8, PointLow: 9.4,
				HighLower: 10.5, HighUpper: 11, HighCoverage: 0.8,
				LowLower: 9.2, LowUpper: 9.7, LowCoverage: 0.8,
			},
		}, nil
	}

	enriched := m.withDayRangeForecasts(context.Background(), report)
	if !called {
		t.Fatal("day-range forecast source was not called")
	}
	forecast, ok := enriched.DayRangeForecasts["sz000001"]
	if !ok || forecast.PointHigh != 10.8 || forecast.HighLower != 10.5 || forecast.LowUpper != 9.7 {
		t.Fatalf("forecast was not injected: %+v", enriched.DayRangeForecasts)
	}
	if report.DayRangeForecasts != nil {
		t.Fatal("forecast injection mutated the caller's report")
	}
	cards, err := reportmessage.FromSelection(enriched)
	if err != nil {
		t.Fatalf("article conversion rejected injected report: %v", err)
	}
	if len(cards.SecondStage) != 1 || cards.SecondStage[0].Article == nil {
		t.Fatal("second-stage article was not generated")
	}
	body := cards.SecondStage[0].Article.Body
	if !strings.Contains(body, "明日高点") || !strings.Contains(body, "10.80") ||
		!strings.Contains(body, "明日低点") || !strings.Contains(body, "9.40") {
		t.Fatalf("article did not include the injected forecast: %s", body)
	}
}

func TestEnqueueReportContinuesWhenDayRangeForecastSourceFails(t *testing.T) {
	m, report := secondStageRefreshFixture(t)
	m.Channels = []notify.Channel{channelFunc{name: "fake-main", send: func(context.Context, notify.Message) error { return nil }}}
	m.DayRangeForecasts = func(context.Context, string, []string) (map[string]selection.DayRangeForecast, error) {
		return nil, errors.New("stored forecasts unavailable")
	}
	state := notify.NewLedger()
	queued, err := m.enqueueReportDeferred(context.Background(), &state, report, false)
	if err != nil {
		t.Fatalf("forecast failure blocked enqueue: %v", err)
	}
	if queued == 0 || len(state.Events) == 0 {
		t.Fatal("report was not queued after forecast failure")
	}
}
