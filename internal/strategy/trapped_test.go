package strategy

import (
	"math"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestAnalyzeTrappedSignalClassifiesOverheadSupply(t *testing.T) {
	tests := []struct {
		name         string
		trappedBars  int
		totalBars    int
		wantStatus   string
		wantRatio    float64
		wantReason   string
		wantCost     float64
		wantDistance float64
	}{
		{name: "triggered", trappedBars: 80, totalBars: 120, wantStatus: TrappedStatusTriggered, wantRatio: 2.0 / 3.0, wantReason: "trapped_volume_ratio_ge_30pct", wantCost: 12, wantDistance: 20},
		{name: "watch", trappedBars: 30, totalBars: 120, wantStatus: TrappedStatusWatch, wantRatio: 0.25, wantReason: "trapped_volume_ratio_ge_20pct", wantCost: 12, wantDistance: 20},
		{name: "none", trappedBars: 0, totalBars: 120, wantStatus: TrappedStatusNone, wantRatio: 0, wantReason: "no_overhead_volume"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bars := trappedSignalBars(test.totalBars, test.trappedBars, 12, 10)
			before := bars[0]
			signal := AnalyzeTrappedSignal(bars)
			if signal.Status != test.wantStatus || signal.Version != TrappedSignalVersion || signal.Method != TrappedSignalMethod {
				t.Fatalf("unexpected signal: %+v", signal)
			}
			if math.Abs(signal.TrappedVolumeRatio-test.wantRatio) > 1e-9 {
				t.Fatalf("trapped ratio = %.9f, want %.9f", signal.TrappedVolumeRatio, test.wantRatio)
			}
			if len(signal.Reasons) == 0 || signal.Reasons[0] != test.wantReason {
				t.Fatalf("reasons = %v, want first %q", signal.Reasons, test.wantReason)
			}
			if test.wantCost > 0 {
				if math.Abs(signal.OverheadCost-test.wantCost) > 1e-9 || math.Abs(signal.OverheadLow-test.wantCost) > 1e-9 || math.Abs(signal.OverheadHigh-test.wantCost) > 1e-9 {
					t.Fatalf("cost/band = %.4f %.4f~%.4f", signal.OverheadCost, signal.OverheadLow, signal.OverheadHigh)
				}
				if math.Abs(signal.OverheadDistancePct-test.wantDistance) > 1e-9 {
					t.Fatalf("overhead distance = %.4f, want %.4f", signal.OverheadDistancePct, test.wantDistance)
				}
				if signal.CurrentZone != TrappedZoneDeep {
					t.Fatalf("current zone = %q, want deep", signal.CurrentZone)
				}
				if len(signal.Zones) != 3 || signal.Zones[2].Low != test.wantCost || signal.Zones[2].High != test.wantCost || math.Abs(signal.Zones[2].VolumeShare-1) > 1e-9 {
					t.Fatalf("zones = %+v", signal.Zones)
				}
			}
			if bars[0] != before {
				t.Fatal("analyzer mutated input bars")
			}
		})
	}
}

func TestBuildTrappedZonesSplitsByDistance(t *testing.T) {
	observations := []trappedPricedVolume{
		{price: 10.2, volume: 1},
		{price: 11.0, volume: 2},
		{price: 14.0, volume: 3},
	}
	zones := buildTrappedZones(observations, 10, 6)
	if len(zones) != 3 {
		t.Fatalf("zones = %+v", zones)
	}
	for index, want := range []struct {
		level string
		low   float64
		high  float64
		share float64
	}{
		{level: TrappedZoneShallow, low: 10.2, high: 10.2, share: 1.0 / 6.0},
		{level: TrappedZoneModerate, low: 11, high: 11, share: 2.0 / 6.0},
		{level: TrappedZoneDeep, low: 14, high: 14, share: 3.0 / 6.0},
	} {
		got := zones[index]
		if got.Level != want.level || got.Low != want.low || got.High != want.high || math.Abs(got.VolumeShare-want.share) > 1e-9 {
			t.Fatalf("zone %d = %+v, want %+v", index, got, want)
		}
	}
}

func TestTrappedZoneForDistanceUsesDocumentedBoundaries(t *testing.T) {
	for _, test := range []struct {
		distance float64
		want     string
	}{
		{distance: 0, want: ""},
		{distance: 5, want: TrappedZoneShallow},
		{distance: 5.01, want: TrappedZoneModerate},
		{distance: 15, want: TrappedZoneModerate},
		{distance: 15.01, want: TrappedZoneDeep},
	} {
		if got := trappedZoneForDistance(test.distance); got != test.want {
			t.Fatalf("distance %.2f -> %q, want %q", test.distance, got, test.want)
		}
	}
}

func TestAnalyzeTrappedSignalRequiresMinimumSessionsAndVolume(t *testing.T) {
	short := AnalyzeTrappedSignal(trappedSignalBars(59, 0, 12, 10))
	if short.Status != TrappedStatusUnknown || short.Sessions != 59 || short.Reasons[0] != "insufficient_daily_bars" {
		t.Fatalf("short history = %+v", short)
	}

	bars := trappedSignalBars(120, 80, 12, 10)
	for index := range bars {
		bars[index].Volume = 0
	}
	noVolume := AnalyzeTrappedSignal(bars)
	if noVolume.Status != TrappedStatusUnknown || noVolume.Reasons[0] != "insufficient_daily_bars" {
		t.Fatalf("no-volume history = %+v", noVolume)
	}
}

func TestAnalyzeTrappedSignalUsesVolumeWeightedCostBand(t *testing.T) {
	bars := trappedSignalBars(120, 80, 12, 10)
	for index := 40; index < 80; index++ {
		bars[index] = market.Bar{Open: 13, High: 13, Low: 13, Close: 13, Volume: 100}
	}
	signal := AnalyzeTrappedSignal(bars)
	if signal.Status != TrappedStatusTriggered {
		t.Fatalf("status = %s", signal.Status)
	}
	if math.Abs(signal.OverheadCost-12.5) > 1e-9 || signal.OverheadLow != 12 || signal.OverheadHigh != 13 {
		t.Fatalf("cost band = %.4f %.4f~%.4f", signal.OverheadCost, signal.OverheadLow, signal.OverheadHigh)
	}
}

func trappedSignalBars(total, trapped int, trappedPrice, currentPrice float64) []market.Bar {
	bars := make([]market.Bar, total)
	for index := range bars {
		price := currentPrice
		if index < trapped {
			price = trappedPrice
		}
		bars[index] = market.Bar{Open: price, High: price, Low: price, Close: price, Volume: 100}
	}
	return bars
}
