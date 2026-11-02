package strategy

import (
	"math"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestAnalyzeTrappedSignalV3ClassifiesWithNearTermGate(t *testing.T) {
	tests := []struct {
		name         string
		trappedBars  int
		totalBars    int
		trappedPrice float64
		currentPrice float64
		wantStatus   string
		wantReason   string
	}{
		{name: "triggered", trappedBars: 80, totalBars: 120, trappedPrice: 10.3, currentPrice: 10, wantStatus: TrappedStatusTriggered, wantReason: "trapped_volume_ratio_ge_30pct"},
		{name: "watch", trappedBars: 30, totalBars: 120, trappedPrice: 10.3, currentPrice: 10, wantStatus: TrappedStatusWatch, wantReason: "trapped_volume_ratio_ge_20pct"},
		{name: "deep zone cannot trigger", trappedBars: 80, totalBars: 120, trappedPrice: 12, currentPrice: 10, wantStatus: TrappedStatusNone, wantReason: "near_term_share_below_threshold"},
		{name: "no overhead", trappedBars: 0, totalBars: 120, trappedPrice: 10, currentPrice: 10, wantStatus: TrappedStatusNone, wantReason: "no_overhead_volume"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			history := trappedHistory(test.totalBars, test.trappedBars, test.trappedPrice, test.currentPrice)
			signal := AnalyzeTrappedSignalV3(history, trappedMetrics(history, 0.001))
			if signal.Status != test.wantStatus || signal.Version != TrappedSignalVersion || signal.Method != TrappedSignalMethod {
				t.Fatalf("unexpected signal: %+v", signal)
			}
			if len(signal.Reasons) == 0 || signal.Reasons[0] != test.wantReason {
				t.Fatalf("reasons = %v, want first %q", signal.Reasons, test.wantReason)
			}
			if !signal.DecayApplied || signal.VolumeUnit != "shares" || signal.VolumeSource != history.Source {
				t.Fatalf("missing v3 provenance: %+v", signal)
			}
			if test.trappedBars > 0 && signal.TrappedVolumeRatio <= 0 {
				t.Fatalf("trapped ratio not computed: %+v", signal)
			}
			if test.trappedBars > 0 && signal.Zones == nil {
				t.Fatal("zones not built")
			}
		})
	}
}

func TestAnalyzeTrappedSignalV3DecaysOlderChips(t *testing.T) {
	history := trappedHistory(120, 60, 12, 10)
	signal := AnalyzeTrappedSignalV3(history, trappedMetrics(history, 5))
	if signal.BaselineTrappedVolumeRatio <= signal.TrappedVolumeRatio {
		t.Fatalf("decay did not reduce early chips: baseline=%.4f decayed=%.4f", signal.BaselineTrappedVolumeRatio, signal.TrappedVolumeRatio)
	}
	if signal.BaselineOverheadCost <= 0 || signal.OverheadCost <= 0 {
		t.Fatalf("missing overhead costs: %+v", signal)
	}
}

func TestAnalyzeTrappedSignalV3RetainsZeroVolumeSessions(t *testing.T) {
	history := trappedHistory(120, 60, 12, 10)
	for i := 60; i < 120; i++ {
		history.Bars[i].Volume = 0
	}
	metrics := trappedMetrics(history, 0.001)
	signal := AnalyzeTrappedSignalV3(history, metrics)
	if signal.Sessions != 120 || signal.VolumeSessions != 60 || signal.ZeroVolumeSessions != 60 {
		t.Fatalf("session accounting = %+v", signal)
	}
	if signal.Status == TrappedStatusUnknown {
		t.Fatalf("zero-volume sessions should remain usable: %+v", signal)
	}
	if got := signal.TurnoverCoveragePct; math.Abs(got-100) > 1e-9 {
		t.Fatalf("turnover coverage = %.4f, want 100", got)
	}
}

func TestAnalyzeTrappedSignalV3RequiresTurnoverForPositiveVolume(t *testing.T) {
	history := trappedHistory(120, 80, 10.3, 10)
	signal := AnalyzeTrappedSignalV3(history, nil)
	if signal.Status != TrappedStatusUnknown || len(signal.Reasons) == 0 || signal.Reasons[0] != "turnover_history_unavailable" {
		t.Fatalf("missing turnover should be unknown: %+v", signal)
	}
	if signal.BaselineTrappedVolumeRatio <= 0 {
		t.Fatalf("baseline should remain visible: %+v", signal)
	}
}

func TestAnalyzeTrappedSignalV3RejectsAdjustedHistoryAndBadUnits(t *testing.T) {
	history := trappedHistory(120, 80, 10.3, 10)
	history.Adjust = "qfq"
	history.DataField = "qfqday"
	if got := AnalyzeTrappedSignalV3(history, trappedMetrics(history, 0.001)); got.Reasons[0] != "unadjusted_history_required" {
		t.Fatalf("adjusted history reason = %v", got.Reasons)
	}
	history = trappedHistory(120, 80, 10.3, 10)
	history.VolumeUnit = "shares"
	if got := AnalyzeTrappedSignalV3(history, trappedMetrics(history, 0.001)); got.Reasons[0] != "unsupported_volume_source_or_unit" {
		t.Fatalf("bad unit reason = %v", got.Reasons)
	}
}

func TestTrappedVolumeSharesAreConvertedByProvider(t *testing.T) {
	base := trappedHistory(120, 80, 10.3, 10)
	base.Source = "sina"
	base.Code = "sz000001"
	for i := range base.Bars {
		base.Bars[i].Volume = 10000
	}
	sina := AnalyzeTrappedSignalV3(base, trappedMetrics(base, 0.001))

	tencent := base
	tencent.Source = "tencent"
	tencent.Bars = append([]market.Bar(nil), base.Bars...)
	for i := range tencent.Bars {
		tencent.Bars[i].Volume = 100
	}
	// Sina and Tencent must both normalize to the same share count for the
	// same stock before distribution or decay is applied.
	tencentSignal := AnalyzeTrappedSignalV3(tencent, trappedMetrics(tencent, 0.001))
	if math.Abs(sina.TotalVolumeShares-tencentSignal.TotalVolumeShares) > 1e-9 {
		t.Fatalf("share conversion mismatch: sina=%.0f tencent=%.0f", sina.TotalVolumeShares, tencentSignal.TotalVolumeShares)
	}
}

func TestTriangularDistributionPreservesVolumeAndSpansRange(t *testing.T) {
	bar := market.Bar{Open: 10, Low: 10, High: 12, Close: 11, Volume: 100}
	points := triangularDistribution(bar, 100)
	if len(points) != trappedIntradayBuckets {
		t.Fatalf("buckets = %d", len(points))
	}
	var total float64
	low, high := math.Inf(1), math.Inf(-1)
	for _, point := range points {
		total += point.volume
		low = math.Min(low, point.low)
		high = math.Max(high, point.high)
	}
	if math.Abs(total-100) > 1e-9 {
		t.Fatalf("triangular volume = %.9f, want 100", total)
	}
	if low != 10 || high != 12 {
		t.Fatalf("range = %.2f~%.2f, want 10~12", low, high)
	}
	if points[0].volume <= 0 || points[len(points)-1].volume <= 0 {
		t.Fatalf("triangular tails should be populated: %+v", points)
	}
}

func TestBuildTrappedZonesNormalizesSharesTo100(t *testing.T) {
	zones, nearTerm, dominant := buildTrappedZones([]trappedPricedVolume{
		{price: 10.2, volume: 1},
		{price: 11.0, volume: 1},
		{price: 14.0, volume: 1},
	}, 10)
	if len(zones) != 3 {
		t.Fatalf("zones = %+v", zones)
	}
	sum := 0
	for _, zone := range zones {
		sum += zone.SharePct
	}
	if sum != 100 {
		t.Fatalf("share percentages sum to %d: %+v", sum, zones)
	}
	if math.Abs(nearTerm-2.0/3.0) > 1e-9 {
		t.Fatalf("near-term share = %.9f", nearTerm)
	}
	if dominant == "" {
		t.Fatal("dominant zone missing")
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

func TestAnalyzeTrappedSignalV3RequiresMinimumSessions(t *testing.T) {
	history := trappedHistory(59, 0, 12, 10)
	short := AnalyzeTrappedSignalV3(history, trappedMetrics(history, 0.001))
	if short.Status != TrappedStatusUnknown || short.Sessions != 59 || short.Reasons[0] != "insufficient_daily_bars" {
		t.Fatalf("short history = %+v", short)
	}
}

func trappedHistory(total, trapped int, trappedPrice, currentPrice float64) market.History {
	bars := make([]market.Bar, total)
	for i := range bars {
		price := currentPrice
		if i < trapped {
			price = trappedPrice
		}
		bars[i] = market.Bar{
			Date:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02"),
			Open:   price,
			High:   price,
			Low:    price,
			Close:  price,
			Volume: 100,
		}
	}
	return market.History{
		Version: 1, Code: "sz000001", Name: "测试", Source: "sina", Adjust: "none",
		DataField: "day", VolumeUnit: "provider_raw", AsOf: bars[len(bars)-1].Date,
		FetchedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Bars: bars,
	}
}

func trappedMetrics(history market.History, rate float64) *market.DailyMetricHistory {
	metrics := &market.DailyMetricHistory{
		Version: 1, Code: history.Code, Source: "sohu", FetchedAt: history.FetchedAt,
	}
	for _, bar := range history.Bars {
		if bar.Volume <= 0 {
			continue
		}
		metrics.Metrics = append(metrics.Metrics, market.DailyMetric{Date: bar.Date, TurnoverRatePct: rate, AmountCNY: 1e8})
	}
	return metrics
}

func TestBuildTrappedZonesClipsRangesAtBandBoundaries(t *testing.T) {
	zones, _, _ := buildTrappedZones([]trappedPricedVolume{
		{price: 10.48, low: 10.46, high: 10.50, volume: 1},
		{price: 10.52, low: 10.50, high: 10.54, volume: 1},
		{price: 11.52, low: 11.50, high: 11.54, volume: 1},
	}, 10)
	if len(zones) != 3 {
		t.Fatalf("zones = %+v", zones)
	}
	if zones[0].High > zones[1].Low+1e-9 || zones[1].High > zones[2].Low+1e-9 {
		t.Fatalf("zone ranges overlap: %+v", zones)
	}
	if math.Abs(zones[0].High-10.5) > 1e-9 || math.Abs(zones[1].Low-10.5) > 1e-9 || math.Abs(zones[2].Low-11.5) > 1e-9 {
		t.Fatalf("zone boundaries were not clipped: %+v", zones)
	}
}
