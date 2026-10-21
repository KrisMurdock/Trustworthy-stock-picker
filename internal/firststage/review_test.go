package firststage

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestEvaluateNextDayRecallAndTouchMetrics(t *testing.T) {
	candidates := []Candidate{
		{Rank: 1, Code: "sz000001", Name: "命中"},
		{Rank: 2, Code: "sz000002", Name: "触达未收涨"},
		{Rank: 3, Code: "sz000003", Name: "缺少评估日"},
		{Rank: 4, Code: "sz000004", Name: "超出评估范围"},
	}
	previous := Report{AsOf: "2026-10-21", Target: "2026-10-22", Status: StatusOK, Candidates: candidates}
	pool := []config.Stock{
		{Code: "sz000001", Name: "命中"},
		{Code: "sz000002", Name: "触达未收涨"},
		{Code: "sz000003", Name: "缺少评估日"},
		{Code: "sz000099", Name: "未入选命中"},
	}
	histories := []market.History{
		reviewHistory("sz000001", 10, 10.2, 10.3),
		reviewHistory("sz000002", 10, 9.8, 10.2),
		{Code: "sz000003", Adjust: "qfq", Bars: []market.Bar{{Date: "2026-10-21", Close: 10, High: 10.1, Low: 9.8, Open: 10}}},
		reviewHistory("sz000099", 10, 10.5, 10.6),
	}
	report, err := EvaluateNextDay(previous, histories, pool, ReviewOptions{
		SignalDate: "2026-10-21", EvaluationDate: "2026-10-22", Adjust: "qfq", SelectedLimit: 3,
		Now: time.Date(2026, 10, 22, 18, 5, 0, 0, market.Shanghai),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != ReviewStatusOK || report.Selected != 3 || report.Evaluated != 2 || report.Unavailable != 1 {
		t.Fatalf("unexpected counts: %+v", report)
	}
	if report.Hits != 1 || report.Misses != 1 || report.TouchHits != 2 {
		t.Fatalf("unexpected selected metrics: %+v", report)
	}
	if report.PoolHits != 2 || report.PoolTouchHits != 3 || !report.RecallAvailable || !report.TouchRecallAvailable {
		t.Fatalf("unexpected pool metrics: %+v", report)
	}
	if !closeTo(report.SelectionHitRatePct, 50) || !closeTo(report.RecallPct, 50) || !closeTo(report.TouchRecallPct, 200.0/3) {
		t.Fatalf("unexpected rates: %+v", report)
	}
	if report.Candidates[2].Status != "missing_evaluation" {
		t.Fatalf("missing evaluation status = %q", report.Candidates[2].Status)
	}
}

func TestEvaluateNextDayDefaultsToPushedTop30(t *testing.T) {
	candidates := make([]Candidate, 31)
	pool := make([]config.Stock, 31)
	histories := make([]market.History, 31)
	for i := range candidates {
		code := fmt.Sprintf("sz%06d", i+1)
		candidates[i] = Candidate{Rank: i + 1, Code: code, Name: code}
		pool[i] = config.Stock{Code: code, Name: code}
		closePrice := 9.9
		if i >= 29 {
			closePrice = 10.2
		}
		histories[i] = reviewHistory(code, 10, closePrice, closePrice+0.1)
	}
	previous := Report{AsOf: "2026-10-21", Target: "2026-10-22", Status: StatusOK, Candidates: candidates}
	report, err := EvaluateNextDay(previous, histories, pool, ReviewOptions{
		SignalDate: "2026-10-21", EvaluationDate: "2026-10-22", Adjust: "qfq",
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Selected != PushLimit || report.Evaluated != PushLimit {
		t.Fatalf("selected/evaluated = %d/%d", report.Selected, report.Evaluated)
	}
	if report.Hits != 1 || report.PoolHits != 2 || !closeTo(report.RecallPct, 50) {
		t.Fatalf("rank 31 leaked into Top 30 metrics: %+v", report)
	}
}

func reviewHistory(code string, signalClose, evaluationClose, evaluationHigh float64) market.History {
	return market.History{
		Code:   code,
		Adjust: "qfq",
		Bars: []market.Bar{
			{Date: "2026-10-21", Open: signalClose, Close: signalClose, High: signalClose + 0.1, Low: signalClose - 0.1},
			{Date: "2026-10-22", Open: evaluationClose, Close: evaluationClose, High: evaluationHigh, Low: evaluationClose - 0.1},
		},
	}
}

func closeTo(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}
