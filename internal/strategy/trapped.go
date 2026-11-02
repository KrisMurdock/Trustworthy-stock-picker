package strategy

import (
	"math"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	TrappedSignalVersion = 2
	TrappedSignalMethod  = "volume_weighted_overhead_v2"

	TrappedLookbackSessions = 120
	TrappedMinimumSessions  = 60

	TrappedObserveVolumeRatio         = 0.20
	TrappedTriggerVolumeRatio         = 0.30
	TrappedObserveOverheadDistancePct = 1.0
	TrappedTriggerOverheadDistancePct = 2.0
	TrappedShallowMaxDistancePct      = 5.0
	TrappedModerateMaxDistancePct     = 15.0
	trappedEpsilon                    = 1e-9
)

const (
	TrappedStatusUnknown   = "unknown"
	TrappedStatusNone      = "none"
	TrappedStatusWatch     = "watch"
	TrappedStatusTriggered = "triggered"
)

const (
	TrappedZoneShallow  = "shallow"
	TrappedZoneModerate = "moderate"
	TrappedZoneDeep     = "deep"
)

// TrappedZone describes one distance band above the signal-day close. Volume
// share is relative to all overhead volume in the lookback window.
type TrappedZone struct {
	Level       string  `json:"level"`
	Label       string  `json:"label"`
	Low         float64 `json:"low,omitempty"`
	High        float64 `json:"high,omitempty"`
	VolumeShare float64 `json:"volume_share"`
}

// TrappedSignal estimates overhead supply from the volume-weighted prices of
// recent daily bars above the latest close. It is a diagnostic, not a trading
// signal: volume is a proxy for turnover, not a record of actual holdings.
type TrappedSignal struct {
	Version                    int           `json:"schema_version"`
	Method                     string        `json:"method"`
	Status                     string        `json:"status"`
	LookbackSessions           int           `json:"lookback_sessions"`
	Sessions                   int           `json:"sessions"`
	CurrentPrice               float64       `json:"current_price,omitempty"`
	RecentHigh                 float64       `json:"recent_high,omitempty"`
	DrawdownFromHighPct        float64       `json:"drawdown_from_high_pct,omitempty"`
	TrappedVolumeRatio         float64       `json:"trapped_volume_ratio"`
	OverheadCost               float64       `json:"overhead_cost,omitempty"`
	OverheadLow                float64       `json:"overhead_low,omitempty"`
	OverheadHigh               float64       `json:"overhead_high,omitempty"`
	OverheadDistancePct        float64       `json:"overhead_distance_pct,omitempty"`
	CurrentZone                string        `json:"current_zone,omitempty"`
	Zones                      []TrappedZone `json:"zones,omitempty"`
	ObserveVolumeRatio         float64       `json:"observe_volume_ratio"`
	TriggerVolumeRatio         float64       `json:"trigger_volume_ratio"`
	ObserveOverheadDistancePct float64       `json:"observe_overhead_distance_pct"`
	TriggerOverheadDistancePct float64       `json:"trigger_overhead_distance_pct"`
	ShallowMaxDistancePct      float64       `json:"shallow_max_distance_pct"`
	ModerateMaxDistancePct     float64       `json:"moderate_max_distance_pct"`
	Reasons                    []string      `json:"reasons,omitempty"`
	Limitation                 string        `json:"limitation,omitempty"`
}

type trappedPricedVolume struct {
	price  float64
	volume float64
}

// AnalyzeTrappedSignal derives one overhead-supply observation from the same
// daily bars used by the strategy. It never mutates the input slice and only
// uses information available at the final bar.
func AnalyzeTrappedSignal(bars []market.Bar) *TrappedSignal {
	signal := &TrappedSignal{
		Version:                    TrappedSignalVersion,
		Method:                     TrappedSignalMethod,
		Status:                     TrappedStatusUnknown,
		LookbackSessions:           TrappedLookbackSessions,
		ObserveVolumeRatio:         TrappedObserveVolumeRatio,
		TriggerVolumeRatio:         TrappedTriggerVolumeRatio,
		ObserveOverheadDistancePct: TrappedObserveOverheadDistancePct,
		TriggerOverheadDistancePct: TrappedTriggerOverheadDistancePct,
		ShallowMaxDistancePct:      TrappedShallowMaxDistancePct,
		ModerateMaxDistancePct:     TrappedModerateMaxDistancePct,
		Limitation:                 "按近120个交易日成交量与典型价估算上方成交密集区；成交量不等同于真实持仓成本，未考虑换手和股东结构，不构成交易信号。",
	}
	if len(bars) == 0 {
		signal.Reasons = []string{"no_daily_bars"}
		return signal
	}

	start := max(0, len(bars)-TrappedLookbackSessions)
	window := bars[start:]
	priced := make([]trappedPricedVolume, 0, len(window))
	var currentPrice, recentHigh float64
	for _, bar := range window {
		if !validTrappedBar(bar) {
			continue
		}
		currentPrice = bar.Close
		recentHigh = math.Max(recentHigh, bar.High)
		if bar.Volume <= 0 {
			continue
		}
		priced = append(priced, trappedPricedVolume{
			price:  (bar.High + bar.Low + bar.Close) / 3,
			volume: bar.Volume,
		})
	}
	signal.Sessions = len(priced)
	signal.CurrentPrice = currentPrice
	signal.RecentHigh = recentHigh
	if recentHigh > 0 && currentPrice > 0 {
		signal.DrawdownFromHighPct = (currentPrice/recentHigh - 1) * 100
	}
	if currentPrice <= 0 || len(priced) < TrappedMinimumSessions {
		signal.Reasons = []string{"insufficient_daily_bars"}
		return signal
	}

	var totalVolume, trappedVolume float64
	trapped := make([]trappedPricedVolume, 0, len(priced))
	for _, observation := range priced {
		totalVolume += observation.volume
		if observation.price > currentPrice+trappedEpsilon {
			trapped = append(trapped, observation)
			trappedVolume += observation.volume
		}
	}
	if totalVolume <= trappedEpsilon {
		signal.Reasons = []string{"no_volume_data"}
		return signal
	}
	signal.TrappedVolumeRatio = trappedVolume / totalVolume
	if trappedVolume <= trappedEpsilon {
		signal.Status = TrappedStatusNone
		signal.Reasons = []string{"no_overhead_volume"}
		return signal
	}

	var weightedPrice float64
	for _, observation := range trapped {
		weightedPrice += observation.price * observation.volume
	}
	signal.OverheadCost = weightedPrice / trappedVolume
	signal.OverheadDistancePct = (signal.OverheadCost/currentPrice - 1) * 100
	sort.Slice(trapped, func(i, j int) bool {
		if trapped[i].price == trapped[j].price {
			return trapped[i].volume < trapped[j].volume
		}
		return trapped[i].price < trapped[j].price
	})
	signal.OverheadLow = weightedQuantile(trapped, trappedVolume, 0.20)
	signal.OverheadHigh = weightedQuantile(trapped, trappedVolume, 0.80)
	signal.Zones = buildTrappedZones(trapped, currentPrice, trappedVolume)
	signal.CurrentZone = trappedZoneForDistance(signal.OverheadDistancePct)

	switch {
	case signal.TrappedVolumeRatio >= TrappedTriggerVolumeRatio && signal.OverheadDistancePct >= TrappedTriggerOverheadDistancePct:
		signal.Status = TrappedStatusTriggered
		signal.Reasons = []string{"trapped_volume_ratio_ge_30pct", "overhead_distance_ge_2pct"}
	case signal.TrappedVolumeRatio >= TrappedObserveVolumeRatio && signal.OverheadDistancePct >= TrappedObserveOverheadDistancePct:
		signal.Status = TrappedStatusWatch
		signal.Reasons = []string{"trapped_volume_ratio_ge_20pct", "overhead_distance_ge_1pct"}
	default:
		signal.Status = TrappedStatusNone
		signal.Reasons = []string{"below_observation_threshold"}
	}
	return signal
}

func buildTrappedZones(observations []trappedPricedVolume, currentPrice, trappedVolume float64) []TrappedZone {
	zones := []TrappedZone{
		{Level: TrappedZoneShallow, Label: "浅度"},
		{Level: TrappedZoneModerate, Label: "中度"},
		{Level: TrappedZoneDeep, Label: "深度"},
	}
	if currentPrice <= 0 || trappedVolume <= 0 {
		return zones
	}
	for _, observation := range observations {
		distance := (observation.price/currentPrice - 1) * 100
		index := 0
		switch {
		case distance > TrappedModerateMaxDistancePct:
			index = 2
		case distance > TrappedShallowMaxDistancePct:
			index = 1
		}
		zone := &zones[index]
		if zone.Low <= 0 || observation.price < zone.Low {
			zone.Low = observation.price
		}
		if observation.price > zone.High {
			zone.High = observation.price
		}
		zone.VolumeShare += observation.volume / trappedVolume
	}
	return zones
}

func trappedZoneForDistance(distancePct float64) string {
	switch {
	case distancePct <= 0:
		return ""
	case distancePct <= TrappedShallowMaxDistancePct:
		return TrappedZoneShallow
	case distancePct <= TrappedModerateMaxDistancePct:
		return TrappedZoneModerate
	default:
		return TrappedZoneDeep
	}
}

func validTrappedBar(bar market.Bar) bool {
	if bar.Close <= 0 || bar.High < math.Max(bar.Open, bar.Close) || bar.Low > math.Min(bar.Open, bar.Close) {
		return false
	}
	for _, value := range []float64{bar.Open, bar.Close, bar.High, bar.Low, bar.Volume} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return false
		}
	}
	return true
}

func weightedQuantile(observations []trappedPricedVolume, totalVolume, quantile float64) float64 {
	if len(observations) == 0 || totalVolume <= 0 {
		return 0
	}
	if quantile <= 0 {
		return observations[0].price
	}
	if quantile >= 1 {
		return observations[len(observations)-1].price
	}
	target := totalVolume * quantile
	var cumulative float64
	for _, observation := range observations {
		cumulative += observation.volume
		if cumulative+trappedEpsilon >= target {
			return observation.price
		}
	}
	return observations[len(observations)-1].price
}
