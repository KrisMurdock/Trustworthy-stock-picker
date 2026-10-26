package strategy

import (
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestBuildArticleChartKeepsBoundedWindowAndEvidence(t *testing.T) {
	bars := waveSeries(260, .02, .8)
	history := market.History{Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Adjust: "qfq", DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: bars[len(bars)-1].Date, Bars: bars}
	wave := AnalyzeWave(bars)
	chart := BuildArticleChart(history, &wave, &ChanZoneEvidence{Upper: 15.2, Lower: 14.7})
	if chart == nil {
		t.Fatal("chart was not built")
	}
	if chart.ID != "chart-sz000001" || chart.Code != "sz000001" || chart.Adjust != "qfq" {
		t.Fatalf("unexpected chart identity: %+v", chart)
	}
	if len(chart.Bars) != articleChartDisplayBars {
		t.Fatalf("bars = %d", len(chart.Bars))
	}
	if chart.Bars[0].Date != bars[len(bars)-articleChartDisplayBars].Date || chart.AsOf != bars[len(bars)-1].Date {
		t.Fatalf("chart window is wrong: first=%s asof=%s", chart.Bars[0].Date, chart.AsOf)
	}
	if chart.CompletedStrokes == 0 || len(chart.Strokes) == 0 || len(chart.Levels) == 0 || chart.Zone == nil {
		t.Fatalf("chart lost structure evidence: %+v", chart)
	}
	if err := chart.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildArticleChartRejectsInvalidHistory(t *testing.T) {
	history := market.History{Code: "sz000001", Adjust: "qfq", Bars: []market.Bar{{Date: "bad", Open: 1, High: 1, Low: 1, Close: 1}, {Date: "2026-10-21", Open: 1, High: 1, Low: 1, Close: 1}}}
	if chart := BuildArticleChart(history, nil, nil); chart != nil {
		t.Fatalf("invalid history produced chart: %+v", chart)
	}
}
