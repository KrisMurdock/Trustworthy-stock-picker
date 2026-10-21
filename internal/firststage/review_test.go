package firststage

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestEvaluateHorizonUsesMaximumHighAcrossThreeDays(t *testing.T) {
	candidates := []Candidate{
		{Rank: 1, Code: "sz000001", Name: "窗口内触达"},
		{Rank: 2, Code: "sz000002", Name: "收盘上涨但未触达"},
		{Rank: 3, Code: "sz000003", Name: "缺少窗口行情"},
		{Rank: 4, Code: "sz000004", Name: "展示截断但候选命中"},
	}
	previous := Report{AsOf: "2026-10-21", Target: "2026-10-22", Status: StatusOK, Candidates: candidates}
	pool := []config.Stock{
		{Code: "sz000001", Name: "窗口内触达"},
		{Code: "sz000002", Name: "收盘上涨但未触达"},
		{Code: "sz000003", Name: "缺少窗口行情"},
		{Code: "sz000004", Name: "展示截断但候选命中"},
		{Code: "sz000099", Name: "未入选命中"},
	}
	histories := []market.History{
		reviewHistory("sz000001", 10, []float64{9.9, 9.7, 9.8}, []float64{10.1, 10.25, 10.05}),
		reviewHistory("sz000002", 10, []float64{10.1, 10.2, 10.2}, []float64{10.05, 10.08, 10.09}),
		{Code: "sz000003", Adjust: "qfq", Bars: []market.Bar{{Date: "2026-10-21", Close: 10, High: 10.1, Low: 9.8, Open: 10}}},
		reviewHistory("sz000004", 10, []float64{10.1, 10.2, 10.3}, []float64{10.4, 10.5, 10.6}),
		reviewHistory("sz000099", 10, []float64{10.1, 10.2, 10.3}, []float64{10.5, 10.6, 10.7}),
	}
	report, err := EvaluateHorizon(previous, histories, pool, ReviewOptions{
		SignalDate:     "2026-10-21",
		EvaluationDate: "2026-10-24",
		HorizonDates:   []string{"2026-10-22", "2026-10-23", "2026-10-24"},
		Adjust:         "qfq",
		SelectedLimit:  3,
		Now:            time.Date(2026, 10, 24, 18, 5, 0, 0, market.Shanghai),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != ReviewStatusOK || report.Selected != 3 || report.Evaluated != 2 || report.Unavailable != 1 {
		t.Fatalf("unexpected counts: %+v", report)
	}
	if report.Hits != 1 || report.Misses != 1 || report.PoolHits != 3 || !report.RecallAvailable {
		t.Fatalf("unexpected recall metrics: %+v", report)
	}
	if report.CandidateCount != 4 || report.CandidateEvaluated != 3 || report.CandidateUnavailable != 1 || report.CandidateHits != 2 {
		t.Fatalf("unexpected candidate metrics: %+v", report)
	}
	if !closeTo(report.TopHitRatePct, 50) || !closeTo(report.CandidateRecallPct, 200.0/3) || !closeTo(report.TopCoveragePct, 100.0/3) {
		t.Fatalf("unexpected rates: %+v", report)
	}
	if report.WindowStart != "2026-10-22" || report.EvaluationDate != "2026-10-24" || report.HorizonDays != 3 {
		t.Fatalf("unexpected window: %+v", report)
	}
	if report.Candidates[0].WindowHighDate != "2026-10-23" || report.Candidates[0].HorizonCloseReturn >= 0 {
		t.Fatalf("window high was not used independently of horizon close: %+v", report.Candidates[0])
	}
	if report.Candidates[2].Status != "missing_horizon" {
		t.Fatalf("missing horizon status = %q", report.Candidates[2].Status)
	}
}

func TestEvaluateHorizonDefaultsToPushedTop30(t *testing.T) {
	candidates := make([]Candidate, 31)
	pool := make([]config.Stock, 31)
	histories := make([]market.History, 31)
	for i := range candidates {
		code := fmt.Sprintf("sz%06d", i+1)
		candidates[i] = Candidate{Rank: i + 1, Code: code, Name: code}
		pool[i] = config.Stock{Code: code, Name: code}
		high := 10.05
		if i >= 29 {
			high = 10.2
		}
		histories[i] = reviewHistory(code, 10, []float64{10, 10, 10}, []float64{high, high, high})
	}
	previous := Report{AsOf: "2026-10-21", Target: "2026-10-22", Status: StatusOK, Candidates: candidates}
	report, err := EvaluateHorizon(previous, histories, pool, ReviewOptions{
		SignalDate:     "2026-10-21",
		EvaluationDate: "2026-10-24",
		HorizonDates:   []string{"2026-10-22", "2026-10-23", "2026-10-24"},
		Adjust:         "qfq",
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Selected != PushLimit || report.Evaluated != PushLimit {
		t.Fatalf("selected/evaluated = %d/%d", report.Selected, report.Evaluated)
	}
	if report.Hits != 1 || report.CandidateHits != 2 || report.PoolHits != 2 || !closeTo(report.CandidateRecallPct, 100) || !closeTo(report.TopCoveragePct, 50) {
		t.Fatalf("rank 31 leaked into Top 30 metrics: %+v", report)
	}
}

func reviewHistory(code string, signalClose float64, horizonCloses, horizonHighs []float64) market.History {
	bars := []market.Bar{{Date: "2026-10-21", Open: signalClose, Close: signalClose, High: signalClose + 0.1, Low: signalClose - 0.1}}
	dates := []string{"2026-10-22", "2026-10-23", "2026-10-24"}
	for i, day := range dates {
		bars = append(bars, market.Bar{Date: day, Open: horizonCloses[i], Close: horizonCloses[i], High: horizonHighs[i], Low: horizonCloses[i] - 0.1})
	}
	return market.History{Code: code, Adjust: "qfq", Bars: bars}
}

func closeTo(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}
