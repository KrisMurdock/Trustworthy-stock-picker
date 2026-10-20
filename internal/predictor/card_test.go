package predictor

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
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
		Code: "sh600000", InputEnd: target.Add(-5 * time.Minute), BasePrice: 10,
		HighPct: 0.01, LowPct: -0.01, ClosePct: 0,
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
	if message.Title != "5 分钟价格预测" || !strings.Contains(message.Subtitle, "09:35 → 09:40") ||
		message.HeaderIcon != "insert-chart_outlined" || message.WidthMode != "fill" {
		t.Fatalf("title=%q", message.Title)
	}
	if len(message.Sections) != 3 || len(message.Sections[0].Fields) != 2 || message.Sections[1].Table == nil || len(message.Sections[1].Table.Rows) != 1 {
		t.Fatalf("missing prediction table row")
	}
	if got := fieldValue(message.Sections[0].Fields, "预测窗口"); got != "09:35 → 09:40" {
		t.Fatalf("prediction window=%q", got)
	}
	for _, section := range message.Sections {
		if section.Heading == "✅ 上一根实际对比" {
			t.Fatal("prediction card must not embed the deviation section")
		}
	}
	for _, column := range message.Sections[1].Table.Columns {
		if !strings.HasSuffix(column.Width, "%") {
			t.Fatalf("card table width must be a percentage: %s=%q", column.Key, column.Width)
		}
	}
	if column := message.Sections[1].Table.Columns[0]; column.Key != "name" || column.Label != "名称" {
		t.Fatalf("first column=%+v want stock name", column)
	}
	row := message.Sections[1].Table.Rows[0]
	if row[0].Text != "浦发银行" || !strings.HasPrefix(row[2].Text, "10.10") || !strings.HasPrefix(row[3].Text, "9.90") || row[5].Text != "偏强 · 中" {
		t.Fatalf("unexpected row: %+v", row)
	}
	model := message.Sections[2]
	if got := fieldValue(model.Fields, "模型"); got != "m1" {
		t.Fatalf("model=%q", got)
	}
	if status := strings.Join(model.Lines, " "); !strings.Contains(status, "未优于基线") {
		t.Fatalf("model status=%q", status)
	}
}

func TestBuildCardRejectsEmptyPredictions(t *testing.T) {
	_, err := BuildCard("2026-10-20", time.Now(), ModelInfo{}, nil, CardData{})
	if err == nil {
		t.Fatalf("expected empty prediction error")
	}
}

func TestBuildDeviationCardReportsPreviousActualDeviation(t *testing.T) {
	target := time.Date(2026, 10, 20, 9, 45, 0, 0, market.Shanghai)
	expiry := target.Add(5 * time.Minute)
	message, err := BuildDeviationCard("2026-10-20", target, expiry, ModelInfo{ModelID: "m1", DataTo: "2026-10-19"},
		[]Prediction{{
			Code: "sh600000", ModelID: "m1", InputEnd: target.Add(-5 * time.Minute), TargetEnd: target, BasePrice: 10,
			HighPct: 0.01, LowPct: -0.01, ClosePct: 0,
		}}, []Actual{{
			Code: "sh600000", ModelID: "m1", TargetEnd: target, Status: "resolved",
			HighPct: 0.03, LowPct: 0.01, ClosePct: 0.02,
		}}, map[string]string{"sh600000": "浦发银行"})
	if err != nil {
		t.Fatalf("build deviation card: %v", err)
	}
	if message.EventID != "intraday:2026-10-20:"+strconv.FormatInt(target.Unix(), 10)+":actual" || message.PartLabel != "上轮偏差" {
		t.Fatalf("unexpected deviation identity: %+v", message)
	}
	if message.Title != "上轮预测偏差" || !strings.Contains(message.Subtitle, "09:40 → 09:45") ||
		message.HeaderIcon != "insert-chart_outlined" || message.WidthMode != "fill" {
		t.Fatalf("unexpected deviation header: %+v", message)
	}
	if !message.ExpiresAt.Equal(expiry) {
		t.Fatalf("expiry=%s want %s", message.ExpiresAt, expiry)
	}
	if len(message.Sections) != 3 || message.Sections[1].Heading != "✅ 上一根实际对比" || message.Sections[1].Table == nil {
		t.Fatalf("missing previous actual section: %+v", message.Sections)
	}
	if got := fieldValue(message.Sections[0].Fields, "对比窗口"); got != "09:40 → 09:45" {
		t.Fatalf("comparison window=%q", got)
	}
	if got := fieldValue(message.Sections[0].Fields, "已确认"); got != "1 / 1" {
		t.Fatalf("confirmed=%q", got)
	}
	if got := fieldValue(message.Sections[0].Fields, "平均收盘误差"); got != "2.00pp" {
		t.Fatalf("close error=%q", got)
	}
	row := message.Sections[1].Table.Rows[0]
	want := []string{"浦发银行", "10.30 (+3.00%)", "10.10 (+1.00%)", "10.20 (+2.00%)", "+0.20 (+2.00pp)", "+0.20 (+2.00pp)", "+0.20 (+2.00pp)"}
	for index := range want {
		if row[index].Text != want[index] {
			t.Fatalf("previous actual column %d=%q want %q", index, row[index].Text, want[index])
		}
	}
}

func TestBuildDeviationCardMarksUnresolvedPreviousActual(t *testing.T) {
	target := time.Date(2026, 10, 20, 9, 45, 0, 0, market.Shanghai)
	message, err := BuildDeviationCard("2026-10-20", target, target.Add(5*time.Minute), ModelInfo{},
		[]Prediction{{Code: "sh600000", ModelID: "m1", InputEnd: target.Add(-5 * time.Minute), TargetEnd: target, BasePrice: 10}},
		[]Actual{{Code: "sh600000", ModelID: "m1", TargetEnd: target, Status: "unresolved"}},
		map[string]string{"sh600000": "浦发银行"})
	if err != nil {
		t.Fatalf("build deviation card: %v", err)
	}
	if got := fieldValue(message.Sections[0].Fields, "已确认"); got != "0 / 1" {
		t.Fatalf("confirmed=%q", got)
	}
	if got := fieldValue(message.Sections[0].Fields, "平均收盘误差"); got != "待确认" {
		t.Fatalf("close error=%q", got)
	}
	row := message.Sections[1].Table.Rows[0]
	if row[1].Text != "待确认" || row[4].Text != "—" {
		t.Fatalf("unresolved previous actual row: %+v", row)
	}
}

func TestBuildDeviationCardRejectsEmptyPrevious(t *testing.T) {
	if _, err := BuildDeviationCard("2026-10-20", time.Now(), time.Now().Add(time.Minute), ModelInfo{}, nil, nil, nil); err == nil {
		t.Fatal("expected empty previous prediction error")
	}
}

func fieldValue(fields []notify.Field, label string) string {
	for _, field := range fields {
		if field.Label == label {
			return field.Value
		}
	}
	return ""
}
