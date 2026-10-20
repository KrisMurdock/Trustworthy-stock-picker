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
	message, err := BuildCard(date, target, info, predictions)
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
	row := message.Sections[0].Table.Rows[0]
	if row[0].Text != "sh600000" || !strings.HasPrefix(row[2].Text, "10.10") || !strings.HasPrefix(row[3].Text, "9.90") {
		t.Fatalf("unexpected row: %+v", row)
	}
	meta := strings.Join(message.Sections[0].Lines, " ")
	if !strings.Contains(meta, "m1") || !strings.Contains(meta, "未优于基线") {
		t.Fatalf("meta=%q", meta)
	}
}

func TestBuildCardRejectsEmptyPredictions(t *testing.T) {
	_, err := BuildCard("2026-10-20", time.Now(), ModelInfo{}, nil)
	if err == nil {
		t.Fatalf("expected empty prediction error")
	}
}
