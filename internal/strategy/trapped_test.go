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

func TestWeightedQuantileSortsObservationsByPrice(t *testing.T) {
	ascending := []trappedPricedVolume{
		{price: 10, volume: 1},
		{price: 12, volume: 3},
		{price: 14, volume: 1},
		{price: 15, volume: 2},
		{price: 20, volume: 1},
	}
	descending := []trappedPricedVolume{
		{price: 20, volume: 1},
		{price: 15, volume: 2},
		{price: 14, volume: 1},
		{price: 12, volume: 3},
		{price: 10, volume: 1},
	}
	shuffled := []trappedPricedVolume{
		{price: 15, volume: 2},
		{price: 10, volume: 1},
		{price: 20, volume: 1},
		{price: 12, volume: 3},
		{price: 14, volume: 1},
	}
	wantLow := weightedQuantile(ascending, 0.20)
	wantHigh := weightedQuantile(ascending, 0.80)
	if wantLow != 12 || wantHigh != 15 {
		t.Fatalf("ascending quantiles = %.2f~%.2f, want 12~15", wantLow, wantHigh)
	}

	for _, test := range []struct {
		name         string
		observations []trappedPricedVolume
	}{
		{name: "price ascending", observations: ascending},
		{name: "price descending", observations: descending},
		{name: "time order shuffled", observations: shuffled},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := append([]trappedPricedVolume(nil), test.observations...)
			low := weightedQuantile(test.observations, 0.20)
			high := weightedQuantile(test.observations, 0.80)
			if low != wantLow || high != wantHigh || low > high {
				t.Fatalf("quantiles = %.2f~%.2f, want %.2f~%.2f", low, high, wantLow, wantHigh)
			}
			for i := range before {
				if test.observations[i] != before[i] {
					t.Fatalf("input observations were reordered: got %+v, want %+v", test.observations, before)
				}
			}
		})
	}

	if got := weightedQuantile(descending, 0); got != 10 {
		t.Fatalf("quantile 0 = %.2f, want lowest price 10", got)
	}
	if got := weightedQuantile(descending, 1); got != 20 {
		t.Fatalf("quantile 1 = %.2f, want highest price 20", got)
	}
}

func TestAnalyzeTrappedSignalV3OrdersOverheadQuantilesForDescendingHistory(t *testing.T) {
	const sessions = 120
	bars := make([]market.Bar, sessions)
	step := 9.5 / float64(sessions-1)
	for i := range bars {
		price := 20 - step*float64(i)
		bars[i] = market.Bar{
			Date:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02"),
			Open:   price,
			High:   price,
			Low:    price,
			Close:  price,
			Volume: 100,
		}
	}
	history := market.History{
		Version: 1, Code: "sz000001", Name: "测试", Source: "sina", Adjust: "none",
		DataField: "day", VolumeUnit: "provider_raw", AsOf: bars[len(bars)-1].Date,
		FetchedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Bars: bars,
	}
	signal := AnalyzeTrappedSignalV3(history, trappedMetrics(history, 0.001))
	if signal.Status == TrappedStatusUnknown {
		t.Fatalf("unexpected unknown signal: %+v", signal)
	}
	if signal.OverheadLow > signal.OverheadHigh {
		t.Fatalf("overhead quantiles reversed: low=%.4f high=%.4f", signal.OverheadLow, signal.OverheadHigh)
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

func TestTriangularDistributionPeaksNearCloseAndCollapsesZeroRange(t *testing.T) {
	bar := market.Bar{Open: 9, Low: 8, High: 12, Close: 9.3, Volume: 100}
	points := triangularDistribution(bar, 100)
	if len(points) != trappedIntradayBuckets {
		t.Fatalf("buckets = %d, want %d", len(points), trappedIntradayBuckets)
	}
	var total float64
	peak := 0
	for i, point := range points {
		total += point.volume
		if point.volume > points[peak].volume {
			peak = i
		}
	}
	if math.Abs(total-100) > 1e-9 {
		t.Fatalf("triangular volume = %.9f, want 100", total)
	}
	bucketWidth := (bar.High - bar.Low) / trappedIntradayBuckets
	if points[peak].high < bar.Close-bucketWidth-trappedEpsilon || points[peak].low > bar.Close+bucketWidth+trappedEpsilon {
		t.Fatalf("peak bucket %.6f~%.6f is not near close %.6f", points[peak].low, points[peak].high, bar.Close)
	}

	flat := triangularDistribution(market.Bar{Open: 10, Low: 10, High: 10, Close: 10, Volume: 100}, 100)
	if len(flat) != 1 || flat[0].price != 10 || flat[0].volume != 100 || flat[0].low != 10 || flat[0].high != 10 {
		t.Fatalf("zero-range distribution = %+v, want one point at 10", flat)
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

func TestTrappedObservationsDecaysExistingChipsBeforeAddingCurrentDay(t *testing.T) {
	bars := []market.Bar{
		{Date: "2026-01-01", Open: 12, High: 12, Low: 12, Close: 12, Volume: 100},
		{Date: "2026-01-02", Open: 10, High: 10, Low: 10, Close: 10, Volume: 100},
	}
	history := market.History{Code: "sz000001", Source: "sina"}
	observations, _ := trappedObservations(history, trappedWindow{bars: bars}, map[string]float64{
		"2026-01-01": 10,
		"2026-01-02": 25,
	})
	ratio, cost, _, _ := summarizeTrapped(observations, 11)
	if want := 75.0 / 175.0; math.Abs(ratio-want) > 1e-9 {
		t.Fatalf("trapped ratio = %.9f, want %.9f: current-day chips were decayed before insertion", ratio, want)
	}
	if math.Abs(cost-12) > 1e-9 {
		t.Fatalf("overhead cost = %.9f, want 12", cost)
	}
}

func TestTrappedStatusThresholdBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		volumeRatio float64
		distancePct float64
		nearTerm    float64
		wantStatus  string
		wantReason  string
	}{
		{name: "observe volume boundary", volumeRatio: 0.20, distancePct: 1, nearTerm: 0.40, wantStatus: TrappedStatusWatch, wantReason: "trapped_volume_ratio_ge_20pct"},
		{name: "observe volume below", volumeRatio: 0.20 - 1e-6, distancePct: 1, nearTerm: 0.40, wantStatus: TrappedStatusNone, wantReason: "below_observation_threshold"},
		{name: "trigger volume boundary", volumeRatio: 0.30, distancePct: 2, nearTerm: 0.50, wantStatus: TrappedStatusTriggered, wantReason: "trapped_volume_ratio_ge_30pct"},
		{name: "trigger volume below", volumeRatio: 0.30 - 1e-6, distancePct: 2, nearTerm: 0.50, wantStatus: TrappedStatusWatch, wantReason: "trapped_volume_ratio_ge_20pct"},
		{name: "observe distance boundary", volumeRatio: 0.25, distancePct: 1, nearTerm: 0.45, wantStatus: TrappedStatusWatch, wantReason: "trapped_volume_ratio_ge_20pct"},
		{name: "observe distance below", volumeRatio: 0.25, distancePct: 1 - 1e-6, nearTerm: 0.45, wantStatus: TrappedStatusNone, wantReason: "below_observation_threshold"},
		{name: "trigger distance boundary", volumeRatio: 0.35, distancePct: 2, nearTerm: 0.55, wantStatus: TrappedStatusTriggered, wantReason: "trapped_volume_ratio_ge_30pct"},
		{name: "trigger distance below", volumeRatio: 0.35, distancePct: 2 - 1e-6, nearTerm: 0.55, wantStatus: TrappedStatusWatch, wantReason: "trapped_volume_ratio_ge_20pct"},
		{name: "observe near-term boundary", volumeRatio: 0.25, distancePct: 1.5, nearTerm: 0.40, wantStatus: TrappedStatusWatch, wantReason: "trapped_volume_ratio_ge_20pct"},
		{name: "observe near-term below", volumeRatio: 0.25, distancePct: 1.5, nearTerm: 0.40 - 1e-6, wantStatus: TrappedStatusNone, wantReason: "near_term_share_below_threshold"},
		{name: "trigger near-term boundary", volumeRatio: 0.35, distancePct: 2.5, nearTerm: 0.50, wantStatus: TrappedStatusTriggered, wantReason: "trapped_volume_ratio_ge_30pct"},
		{name: "trigger near-term below", volumeRatio: 0.35, distancePct: 2.5, nearTerm: 0.50 - 1e-6, wantStatus: TrappedStatusWatch, wantReason: "trapped_volume_ratio_ge_20pct"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, reasons := trappedStatus(test.volumeRatio, test.distancePct, test.nearTerm)
			if status != test.wantStatus {
				t.Fatalf("status = %q, want %q (reasons %v)", status, test.wantStatus, reasons)
			}
			if len(reasons) == 0 || reasons[0] != test.wantReason {
				t.Fatalf("reasons = %v, want first %q", reasons, test.wantReason)
			}
		})
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
