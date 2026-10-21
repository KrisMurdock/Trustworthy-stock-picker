package secondstage

import (
	"math"
	"testing"
)

func TestEvaluateTopNAndNDCG(t *testing.T) {
	observations := []Observation{
		{TargetDate: "2026-10-20", Code: "a", PredictedScore: 3, ActualMaxHighPct: 9, ActualClose3Pct: 4, Touch1Pct: true},
		{TargetDate: "2026-10-20", Code: "b", PredictedScore: 2, ActualMaxHighPct: 4, ActualClose3Pct: 1, Touch1Pct: true},
		{TargetDate: "2026-10-20", Code: "c", PredictedScore: 1, ActualMaxHighPct: 1, ActualClose3Pct: -1, Touch1Pct: false},
		{TargetDate: "2026-10-21", Code: "d", PredictedScore: 3, ActualMaxHighPct: 6, ActualClose3Pct: 2, Touch1Pct: true},
		{TargetDate: "2026-10-21", Code: "e", PredictedScore: 2, ActualMaxHighPct: 0, ActualClose3Pct: -2, Touch1Pct: false},
	}
	metrics, err := Evaluate(observations, 2)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Days != 2 || math.Abs(metrics.TopNMeanMaxHighPct-4.75) > 1e-9 || math.Abs(metrics.TopNMeanClose3Pct-1.25) > 1e-9 || math.Abs(metrics.TopNHit1Pct-0.75) > 1e-9 {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
	if metrics.TopNNDCG <= 0 || metrics.TopNNDCG > 1 {
		t.Fatalf("invalid NDCG: %+v", metrics)
	}
}

func TestEvaluateRejectsInvalidInput(t *testing.T) {
	if _, err := Evaluate(nil, 10); err == nil {
		t.Fatal("empty observations accepted")
	}
	if _, err := Evaluate([]Observation{{TargetDate: "2026-10-20", PredictedScore: math.NaN()}}, 10); err == nil {
		t.Fatal("NaN score accepted")
	}
}
