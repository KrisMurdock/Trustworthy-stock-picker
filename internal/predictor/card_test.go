package predictor

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestBuildCardAggregatesPredictions(t *testing.T) {
	date := "2026-10-20"
	target := time.Date(2026, 10, 20, 9, 40, 0, 0, market.Shanghai)
	info := ModelInfo{
		ModelID: "m1", DataTo: "2026-10-19",
		Metrics: ModelMetrics{
			Samples:         6000,
			MAE:             TargetMetrics{High: 0.02, Low: 0.02, Close: 0.02},
			BaselineLastMAE: TargetMetrics{High: 0.01, Low: 0.01, Close: 0.01},
		},
	}
	predictions := []Prediction{{
		Code: "sh600000", BasePrice: 10, HighPct: 0.01, LowPct: -0.01, ClosePct: 0,
		Trend: "偏强", Confidence: "中",
	}}
	message, err := BuildCard(date, target, info, predictions, CardData{Names: map[string]string{"sh600000": "浦发银行"}})
	if err != nil {
		t.Fatalf("build card: %v", err)
	}
	if message.EventID != "intraday:2026-10-20:"+strconv.FormatInt(target.Unix(), 10) {
		t.Fatalf("event id=%q", message.EventID)
	}
	if !message.ExpiresAt.Equal(target) {
		t.Fatalf("expiry=%s want %s", message.ExpiresAt, target)
	}
	if !strings.Contains(message.Title, "5分钟预测") {
		t.Fatalf("title=%q", message.Title)
	}
	if len(message.Sections) == 0 || message.Sections[0].Table == nil || len(message.Sections[0].Table.Rows) != 1 {
		t.Fatalf("missing prediction table row")
	}
	for _, column := range message.Sections[0].Table.Columns {
		if !strings.HasSuffix(column.Width, "%") {
			t.Fatalf("card table width must be a percentage: %s=%q", column.Key, column.Width)
		}
	}
	if column := message.Sections[0].Table.Columns[0]; column.Key != "name" || column.Label != "名称" {
		t.Fatalf("first column=%+v want stock name", column)
	}
	row := message.Sections[0].Table.Rows[0]
	if row[0].Text != "浦发银行" || !strings.HasPrefix(row[2].Text, "10.10") || !strings.HasPrefix(row[3].Text, "9.90") {
		t.Fatalf("unexpected row: %+v", row)
	}
	meta := strings.Join(message.Sections[0].Lines, " ")
	if !strings.Contains(meta, "m1") || !strings.Contains(meta, "未优于基线") {
		t.Fatalf("meta=%q", meta)
	}
}

func TestBuildCardRejectsEmptyPredictions(t *testing.T) {
	_, err := BuildCard("2026-10-20", time.Now(), ModelInfo{}, nil, CardData{})
	if err == nil {
		t.Fatalf("expected empty prediction error")
	}
}

func TestBuildCardReportsPreviousActualDeviation(t *testing.T) {
	target := time.Date(2026, 10, 20, 9, 45, 0, 0, market.Shanghai)
	previousTarget := target.Add(-5 * time.Minute)
	data := CardData{
		Names: map[string]string{"sh600000": "浦发银行"},
		Previous: []Prediction{{
			Code: "sh600000", ModelID: "m1", TargetEnd: previousTarget, BasePrice: 10,
			HighPct: 0.01, LowPct: -0.01, ClosePct: 0,
		}},
		Actuals: []Actual{{
			Code: "sh600000", ModelID: "m1", TargetEnd: previousTarget, Status: "resolved",
			HighPct: 0.03, LowPct: 0.01, ClosePct: 0.02,
		}},
	}
	message, err := BuildCard("2026-10-20", target, ModelInfo{}, []Prediction{{
		Code: "sh600000", BasePrice: 10.2, HighPct: 0.01, LowPct: -0.01, Trend: "震荡", Confidence: "中",
	}}, data)
	if err != nil {
		t.Fatalf("build card: %v", err)
	}
	if len(message.Sections) < 3 || message.Sections[1].Heading != "上一根实际对比" || message.Sections[1].Table == nil {
		t.Fatalf("missing previous actual section: %+v", message.Sections)
	}
	row := message.Sections[1].Table.Rows[0]
	want := []string{"浦发银行", "10.30 (+3.00%)", "10.10 (+1.00%)", "10.20 (+2.00%)", "+0.20 (+2.00pp)", "+0.20 (+2.00pp)", "+0.20 (+2.00pp)"}
	for index := range want {
		if row[index].Text != want[index] {
			t.Fatalf("previous actual column %d=%q want %q", index, row[index].Text, want[index])
		}
	}
}

func TestBuildCardMarksUnresolvedPreviousActual(t *testing.T) {
	target := time.Date(2026, 10, 20, 9, 45, 0, 0, market.Shanghai)
	previousTarget := target.Add(-5 * time.Minute)
	message, err := BuildCard("2026-10-20", target, ModelInfo{}, []Prediction{{
		Code: "sh600000", BasePrice: 10, HighPct: 0.01, LowPct: -0.01, Trend: "震荡", Confidence: "中",
	}}, CardData{
		Names:    map[string]string{"sh600000": "浦发银行"},
		Previous: []Prediction{{Code: "sh600000", ModelID: "m1", TargetEnd: previousTarget, BasePrice: 10}},
		Actuals:  []Actual{{Code: "sh600000", ModelID: "m1", TargetEnd: previousTarget, Status: "unresolved"}},
	})
	if err != nil {
		t.Fatalf("build card: %v", err)
	}
	row := message.Sections[1].Table.Rows[0]
	if row[1].Text != "待确认" || row[4].Text != "—" {
		t.Fatalf("unresolved previous actual row: %+v", row)
	}
}
