package predictor

import (
	"testing"
	"time"
)

func TestKalmanTrendFollowsRisingSeries(t *testing.T) {
	k := NewKalman(DefaultKalmanParams())
	start := time.Date(2026, 10, 20, 9, 35, 0, 0, time.FixedZone("CST", 8*3600))
	price := 10.0
	for i := 0; i < 12; i++ {
		k.Update("sz000001", "2026-10-20", start.Add(time.Duration(i*5)*time.Minute), price)
		price *= 1.002
	}
	state, confidence, score := k.Trend()
	if state != "偏强" || confidence == "低" || score <= 0 {
		t.Fatalf("unexpected rising trend: %s/%s/%f", state, confidence, score)
	}
}

func TestKalmanTrendFollowsFallingSeries(t *testing.T) {
	k := NewKalman(DefaultKalmanParams())
	start := time.Date(2026, 10, 20, 9, 35, 0, 0, time.FixedZone("CST", 8*3600))
	price := 10.0
	for i := 0; i < 12; i++ {
		k.Update("sz000001", "2026-10-20", start.Add(time.Duration(i*5)*time.Minute), price)
		price *= 0.998
	}
	state, confidence, score := k.Trend()
	if state != "偏弱" || confidence == "低" || score >= 0 {
		t.Fatalf("unexpected falling trend: %s/%s/%f", state, confidence, score)
	}
}

func TestKalmanRestoreContinuesState(t *testing.T) {
	start := time.Date(2026, 10, 20, 9, 35, 0, 0, time.FixedZone("CST", 8*3600))
	k := NewKalman(DefaultKalmanParams())
	state := k.Update("sz000001", "2026-10-20", start, 10)
	restored := KalmanFromState(state, DefaultKalmanParams())
	next := restored.Update("sz000001", "2026-10-20", start.Add(5*time.Minute), 10.1)
	if !next.BarEnd.Equal(start.Add(5*time.Minute)) || next.Level == 0 || next.P11 <= 0 {
		t.Fatalf("unexpected restored state: %+v", next)
	}
}
