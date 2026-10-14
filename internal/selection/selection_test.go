package selection

import (
	"stock-watcher/internal/market"
	"testing"
)

func TestScoreMinimumAndFlatSeries(t *testing.T) {
	bars := make([]market.Bar, 35)
	for i := range bars {
		bars[i] = market.Bar{Close: 10, VolumeLots: 100}
	}
	m, err := Score(bars)
	if err != nil {
		t.Fatal(err)
	}
	if m.Score != 17 || m.RSI14 != 50 || m.VolumeRatio != 1 || m.MACDScore != 0 {
		t.Fatalf("unexpected flat score %+v", m)
	}
	if _, err := Score(bars[:34]); err == nil {
		t.Fatal("accepted insufficient history")
	}
}
