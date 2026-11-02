package strategy

import (
	"math"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	TrappedSignalVersion = 3
	TrappedSignalMethod  = "turnover_adjusted_v3"

	TrappedLookbackSessions = 120
	TrappedMinimumSessions  = 60

	TrappedObserveVolumeRatio         = 0.20
	TrappedTriggerVolumeRatio         = 0.30
	TrappedObserveOverheadDistancePct = 1.0
	TrappedTriggerOverheadDistancePct = 2.0
	TrappedShallowMaxDistancePct      = 5.0
	TrappedModerateMaxDistancePct     = 15.0

	TrappedObserveNearTermShare = 0.40
	TrappedTriggerNearTermShare = 0.50

	trappedIntradayBuckets = 24
	trappedEpsilon         = 1e-9
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

// TrappedZone describes one distance band above the signal-day close. SharePct
// is the largest-remainder integer share of overhead volume; the three bands
// always sum to 100 when trapped volume exists.
type TrappedZone struct {
	Level       string  `json:"level"`
	Label       string  `json:"label"`
	Low         float64 `json:"low,omitempty"`
	High        float64 `json:"high,omitempty"`
	VolumeShare float64 `json:"volume_share"`
	SharePct    int     `json:"share_pct"`
}

// TrappedSignal estimates overhead supply from unadjusted daily bars. Turnover
// rates decay older chips before today's triangular intraday distribution is
// added. It remains a diagnostic, not a record of actual holdings.
type TrappedSignal struct {
	Version                     int           `json:"schema_version"`
	Method                      string        `json:"method"`
	Status                      string        `json:"status"`
	LookbackSessions            int           `json:"lookback_sessions"`
	Sessions                    int           `json:"sessions"`
	VolumeSessions              int           `json:"volume_sessions"`
	ZeroVolumeSessions          int           `json:"zero_volume_sessions"`
	TurnoverSessions            int           `json:"turnover_sessions,omitempty"`
	TurnoverCoveragePct         float64       `json:"turnover_coverage_pct,omitempty"`
	TurnoverSource              string        `json:"turnover_source,omitempty"`
	VolumeSource                string        `json:"volume_source,omitempty"`
	VolumeUnit                  string        `json:"volume_unit,omitempty"`
	TotalVolumeShares           float64       `json:"total_volume_shares,omitempty"`
	DecayApplied                bool          `json:"decay_applied"`
	CurrentPrice                float64       `json:"current_price,omitempty"`
	RecentHigh                  float64       `json:"recent_high,omitempty"`
	DrawdownFromHighPct         float64       `json:"drawdown_from_high_pct,omitempty"`
	TrappedVolumeRatio          float64       `json:"trapped_volume_ratio"`
	BaselineTrappedVolumeRatio  float64       `json:"baseline_trapped_volume_ratio"`
	OverheadCost                float64       `json:"overhead_cost,omitempty"`
	BaselineOverheadCost        float64       `json:"baseline_overhead_cost,omitempty"`
	OverheadLow                 float64       `json:"overhead_low,omitempty"`
	OverheadHigh                float64       `json:"overhead_high,omitempty"`
	OverheadDistancePct         float64       `json:"overhead_distance_pct,omitempty"`
	BaselineOverheadDistancePct float64       `json:"baseline_overhead_distance_pct,omitempty"`
	CurrentZone                 string        `json:"current_zone,omitempty"`
	DominantZone                string        `json:"dominant_zone,omitempty"`
	NearTermShare               float64       `json:"near_term_share"`
	Zones                       []TrappedZone `json:"zones,omitempty"`
	ObserveVolumeRatio          float64       `json:"observe_volume_ratio"`
	TriggerVolumeRatio          float64       `json:"trigger_volume_ratio"`
	ObserveOverheadDistancePct  float64       `json:"observe_overhead_distance_pct"`
	TriggerOverheadDistancePct  float64       `json:"trigger_overhead_distance_pct"`
	ObserveNearTermShare        float64       `json:"observe_near_term_share"`
	TriggerNearTermShare        float64       `json:"trigger_near_term_share"`
	ShallowMaxDistancePct       float64       `json:"shallow_max_distance_pct"`
	ModerateMaxDistancePct      float64       `json:"moderate_max_distance_pct"`
	Reasons                     []string      `json:"reasons,omitempty"`
	Limitation                  string        `json:"limitation,omitempty"`
}

type trappedPricedVolume struct {
	price  float64
	low    float64
	high   float64
	volume float64
}

type trappedWindow struct {
	bars         []market.Bar
	firstDate    string
	lastPositive string
	volumeBars   int
	zeroVolume   int
}

// AnalyzeTrappedSignalV3 derives a turnover-adjusted v3 observation from the
// raw (unadjusted) history. A missing turnover history produces an explicit
// unknown signal rather than silently falling back to v2 weights.
func AnalyzeTrappedSignalV3(history market.History, metrics *market.DailyMetricHistory) *TrappedSignal {
	signal := newTrappedSignal()
	if history.Adjust != "none" {
		signal.Reasons = []string{"unadjusted_history_required"}
		return signal
	}
	if history.VolumeUnit != "provider_raw" || !trappedVolumeSourceSupported(history.Source) {
		signal.Reasons = []string{"unsupported_volume_source_or_unit"}
		return signal
	}
	signal.VolumeSource = history.Source
	signal.VolumeUnit = "shares"
	window := trappedWindowFor(history)
	signal.Sessions = len(window.bars)
	signal.VolumeSessions = window.volumeBars
	signal.ZeroVolumeSessions = window.zeroVolume
	if len(window.bars) == 0 {
		signal.Reasons = []string{"no_daily_bars"}
		return signal
	}
	signal.CurrentPrice = window.bars[len(window.bars)-1].Close
	for _, bar := range window.bars {
		if validTrappedBar(bar) {
			signal.RecentHigh = math.Max(signal.RecentHigh, bar.High)
		}
	}
	if signal.RecentHigh > 0 && signal.CurrentPrice > 0 {
		signal.DrawdownFromHighPct = (signal.CurrentPrice/signal.RecentHigh - 1) * 100
	}
	if len(window.bars) < TrappedMinimumSessions {
		signal.Reasons = []string{"insufficient_daily_bars"}
		return signal
	}
	if window.volumeBars == 0 {
		signal.Reasons = []string{"no_volume_data"}
		return signal
	}

	turnover, ok := turnoverByDate(metrics, window)
	if !ok {
		signal.Reasons = []string{"turnover_history_unavailable"}
		signal.BaselineTrappedVolumeRatio, signal.BaselineOverheadCost, signal.BaselineOverheadDistancePct, _ = trappedSummary(history, window)
		return signal
	}
	signal.TurnoverSource = metrics.Source
	signal.TurnoverSessions = len(turnover)
	if window.volumeBars > 0 {
		signal.TurnoverCoveragePct = float64(len(turnover)) / float64(window.volumeBars) * 100
	}

	baseline, baselineTotal := trappedObservations(history, window, nil)
	signal.TotalVolumeShares = baselineTotal
	current, _ := trappedObservations(history, window, turnover)
	baselineRatio, baselineCost, baselineDistance, _ := summarizeTrapped(baseline, signal.CurrentPrice)
	currentRatio, currentCost, currentDistance, _ := summarizeTrapped(current, signal.CurrentPrice)
	trapped := trappedOverhead(current, signal.CurrentPrice)
	signal.BaselineTrappedVolumeRatio = baselineRatio
	signal.BaselineOverheadCost = baselineCost
	signal.BaselineOverheadDistancePct = baselineDistance
	signal.TrappedVolumeRatio = currentRatio
	signal.OverheadCost = currentCost
	signal.OverheadDistancePct = currentDistance
	signal.DecayApplied = true

	if len(trapped) == 0 {
		signal.Status = TrappedStatusNone
		signal.Reasons = []string{"no_overhead_volume"}
		signal.Zones = emptyTrappedZones()
		return signal
	}
	signal.OverheadLow = weightedQuantile(trapped, 0.20)
	signal.OverheadHigh = weightedQuantile(trapped, 0.80)
	signal.Zones, signal.NearTermShare, signal.DominantZone = buildTrappedZones(trapped, signal.CurrentPrice)
	signal.CurrentZone = trappedZoneForDistance(signal.OverheadDistancePct)

	triggered := signal.TrappedVolumeRatio >= TrappedTriggerVolumeRatio &&
		signal.OverheadDistancePct >= TrappedTriggerOverheadDistancePct &&
		signal.NearTermShare >= TrappedTriggerNearTermShare
	watch := signal.TrappedVolumeRatio >= TrappedObserveVolumeRatio &&
		signal.OverheadDistancePct >= TrappedObserveOverheadDistancePct &&
		signal.NearTermShare >= TrappedObserveNearTermShare
	switch {
	case triggered:
		signal.Status = TrappedStatusTriggered
		signal.Reasons = []string{"trapped_volume_ratio_ge_30pct", "overhead_distance_ge_2pct", "near_term_share_ge_50pct"}
	case watch:
		signal.Status = TrappedStatusWatch
		signal.Reasons = []string{"trapped_volume_ratio_ge_20pct", "overhead_distance_ge_1pct", "near_term_share_ge_40pct"}
	case signal.TrappedVolumeRatio >= TrappedObserveVolumeRatio && signal.OverheadDistancePct >= TrappedObserveOverheadDistancePct && signal.NearTermShare < TrappedObserveNearTermShare:
		signal.Status = TrappedStatusNone
		signal.Reasons = []string{"near_term_share_below_threshold"}
	default:
		signal.Status = TrappedStatusNone
		signal.Reasons = []string{"below_observation_threshold"}
	}
	return signal
}

// TrappedSignalUnavailable returns a v3 unknown signal when the caller cannot
// supply unadjusted history. It keeps every recommendation renderable.
func TrappedSignalUnavailable(reason string) *TrappedSignal {
	signal := newTrappedSignal()
	if reason == "" {
		reason = "unadjusted_history_unavailable"
	}
	signal.Reasons = []string{reason}
	return signal
}

func newTrappedSignal() *TrappedSignal {
	return &TrappedSignal{
		Version:                    TrappedSignalVersion,
		Method:                     TrappedSignalMethod,
		Status:                     TrappedStatusUnknown,
		LookbackSessions:           TrappedLookbackSessions,
		ObserveVolumeRatio:         TrappedObserveVolumeRatio,
		TriggerVolumeRatio:         TrappedTriggerVolumeRatio,
		ObserveOverheadDistancePct: TrappedObserveOverheadDistancePct,
		TriggerOverheadDistancePct: TrappedTriggerOverheadDistancePct,
		ObserveNearTermShare:       TrappedObserveNearTermShare,
		TriggerNearTermShare:       TrappedTriggerNearTermShare,
		ShallowMaxDistancePct:      TrappedShallowMaxDistancePct,
		ModerateMaxDistancePct:     TrappedModerateMaxDistancePct,
		Limitation:                 "按不复权日线与换手率衰减估算上方成交密集区；日内量按低-收-高三角分布展开，成交量仍不等同真实持仓成本，不构成交易信号。",
	}
}

func trappedVolumeSourceSupported(source string) bool {
	switch source {
	case "tencent", "sina", "baostock", "tushare", "eastmoney":
		return true
	default:
		return false
	}
}

func trappedWindowFor(history market.History) trappedWindow {
	start := max(0, len(history.Bars)-TrappedLookbackSessions)
	window := trappedWindow{bars: history.Bars[start:]}
	for _, bar := range window.bars {
		if !validTrappedBar(bar) {
			continue
		}
		if bar.Volume > 0 {
			window.volumeBars++
			if bar.Date > window.lastPositive {
				window.lastPositive = bar.Date
			}
		} else {
			window.zeroVolume++
		}
	}
	if len(window.bars) > 0 {
		window.firstDate = window.bars[0].Date
	}
	return window
}

func turnoverByDate(metrics *market.DailyMetricHistory, window trappedWindow) (map[string]float64, bool) {
	if metrics == nil || metrics.Validate() != nil {
		return nil, false
	}
	byDate := make(map[string]float64, len(metrics.Metrics))
	for _, metric := range metrics.Metrics {
		byDate[metric.Date] = metric.TurnoverRatePct
	}
	out := make(map[string]float64, window.volumeBars)
	for _, bar := range window.bars {
		if !validTrappedBar(bar) || bar.Volume <= 0 {
			continue
		}
		rate, ok := byDate[bar.Date]
		if !ok || math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 {
			return nil, false
		}
		out[bar.Date] = rate
	}
	return out, true
}

func trappedObservations(history market.History, window trappedWindow, turnover map[string]float64) ([]trappedPricedVolume, float64) {
	chips := make([]trappedPricedVolume, 0, len(window.bars)*trappedIntradayBuckets)
	var total float64
	for _, bar := range window.bars {
		if !validTrappedBar(bar) {
			continue
		}
		if turnover != nil {
			rate := 0.0
			if bar.Volume > 0 {
				rate = turnover[bar.Date]
			}
			decay := 1 - min(max(rate/100, 0), 1)
			for i := range chips {
				chips[i].volume *= decay
			}
		}
		if bar.Volume <= 0 {
			continue
		}
		shares := market.ProviderVolumeShares(history.Source, history.Code, bar.Volume)
		if shares <= 0 {
			continue
		}
		for _, point := range triangularDistribution(bar, shares) {
			chips = append(chips, point)
			total += point.volume
		}
	}
	return chips, total
}

func trappedSummary(history market.History, window trappedWindow) (float64, float64, float64, float64) {
	baseline, _ := trappedObservations(history, window, nil)
	if len(window.bars) == 0 {
		return 0, 0, 0, 0
	}
	return summarizeTrapped(baseline, window.bars[len(window.bars)-1].Close)
}

func summarizeTrapped(observations []trappedPricedVolume, currentPrice float64) (ratio, cost, distance, trappedVolume float64) {
	if currentPrice <= 0 {
		return 0, 0, 0, 0
	}
	var total float64
	trapped := make([]trappedPricedVolume, 0, len(observations))
	for _, observation := range observations {
		total += observation.volume
		if observation.price > currentPrice+trappedEpsilon {
			trapped = append(trapped, observation)
			trappedVolume += observation.volume
		}
	}
	if total <= trappedEpsilon || trappedVolume <= trappedEpsilon {
		return 0, 0, 0, trappedVolume
	}
	var weighted float64
	for _, observation := range trapped {
		weighted += observation.price * observation.volume
	}
	cost = weighted / trappedVolume
	return trappedVolume / total, cost, (cost/currentPrice - 1) * 100, trappedVolume
}

func trappedOverhead(observations []trappedPricedVolume, currentPrice float64) []trappedPricedVolume {
	if currentPrice <= 0 {
		return nil
	}
	trapped := make([]trappedPricedVolume, 0, len(observations))
	for _, observation := range observations {
		if observation.price > currentPrice+trappedEpsilon {
			trapped = append(trapped, observation)
		}
	}
	return trapped
}

func triangularDistribution(bar market.Bar, shares float64) []trappedPricedVolume {
	low, high, mode := bar.Low, bar.High, bar.Close
	if high-low <= trappedEpsilon {
		return []trappedPricedVolume{{price: mode, low: low, high: high, volume: shares}}
	}
	bounds := make([]float64, trappedIntradayBuckets+1)
	for i := 0; i <= trappedIntradayBuckets; i++ {
		bounds[i] = low + (high-low)*float64(i)/float64(trappedIntradayBuckets)
	}
	out := make([]trappedPricedVolume, 0, trappedIntradayBuckets)
	var massSum float64
	for i := 0; i < trappedIntradayBuckets; i++ {
		mass := triangularCDF(bounds[i+1], low, high, mode) - triangularCDF(bounds[i], low, high, mode)
		if mass < 0 || math.IsNaN(mass) || math.IsInf(mass, 0) {
			mass = 0
		}
		massSum += mass
		price := (bounds[i] + bounds[i+1]) / 2
		out = append(out, trappedPricedVolume{price: price, low: bounds[i], high: bounds[i+1], volume: shares * mass})
	}
	if massSum <= trappedEpsilon {
		return []trappedPricedVolume{{price: mode, low: low, high: high, volume: shares}}
	}
	if math.Abs(massSum-1) > 1e-9 {
		for i := range out {
			out[i].volume /= massSum
		}
	}
	return out
}

func triangularCDF(x, low, high, mode float64) float64 {
	if x <= low {
		return 0
	}
	if x >= high {
		return 1
	}
	if mode <= low {
		return 1 - math.Pow((high-x)/(high-low), 2)
	}
	if mode >= high {
		return math.Pow((x-low)/(high-low), 2)
	}
	if x < mode {
		return math.Pow(x-low, 2) / ((high - low) * (mode - low))
	}
	return 1 - math.Pow(high-x, 2)/((high-low)*(high-mode))
}

// buildTrappedZones splits overhead volume into the three distance bands and
// normalizes integer shares with the largest-remainder method.
func buildTrappedZones(observations []trappedPricedVolume, currentPrice float64) ([]TrappedZone, float64, string) {
	zones := emptyTrappedZones()
	if currentPrice <= 0 {
		return zones, 0, ""
	}
	var trappedVolume float64
	for _, observation := range observations {
		if observation.price > currentPrice+trappedEpsilon {
			trappedVolume += observation.volume
		}
	}
	if trappedVolume <= trappedEpsilon {
		return zones, 0, ""
	}
	for _, observation := range observations {
		if observation.price <= currentPrice+trappedEpsilon {
			continue
		}
		distance := (observation.price/currentPrice - 1) * 100
		index := 0
		switch {
		case distance > TrappedModerateMaxDistancePct:
			index = 2
		case distance > TrappedShallowMaxDistancePct:
			index = 1
		}
		zone := &zones[index]
		low, high := observation.price, observation.price
		if observation.low > 0 && observation.high > 0 {
			low, high = observation.low, observation.high
		}
		// Clip each bucket to the band that classified it so the published
		// ranges stay adjacent instead of overlapping at 5%/15% boundaries.
		lower, upper := currentPrice, math.Inf(1)
		switch index {
		case 0:
			upper = currentPrice * (1 + TrappedShallowMaxDistancePct/100)
		case 1:
			lower = currentPrice * (1 + TrappedShallowMaxDistancePct/100)
			upper = currentPrice * (1 + TrappedModerateMaxDistancePct/100)
		case 2:
			lower = currentPrice * (1 + TrappedModerateMaxDistancePct/100)
		}
		if low < lower {
			low = lower
		}
		if high > upper {
			high = upper
		}
		if high < low {
			high = low
		}
		if zone.Low <= 0 || low < zone.Low {
			zone.Low = low
		}
		if high > zone.High {
			zone.High = high
		}
		zone.VolumeShare += observation.volume / trappedVolume
	}
	for i := range zones {
		if zones[i].VolumeShare < 0 || math.IsNaN(zones[i].VolumeShare) {
			zones[i].VolumeShare = 0
		}
	}
	largestRemainderShares(zones)
	nearTerm := zones[0].VolumeShare + zones[1].VolumeShare
	dominant := ""
	best := -1.0
	for _, zone := range zones {
		if zone.VolumeShare > best+trappedEpsilon {
			best = zone.VolumeShare
			dominant = zone.Level
		}
	}
	return zones, nearTerm, dominant
}

func emptyTrappedZones() []TrappedZone {
	return []TrappedZone{
		{Level: TrappedZoneShallow, Label: "浅度"},
		{Level: TrappedZoneModerate, Label: "中度"},
		{Level: TrappedZoneDeep, Label: "深度"},
	}
}

func largestRemainderShares(zones []TrappedZone) {
	total := 0.0
	for _, zone := range zones {
		total += zone.VolumeShare
	}
	if total <= trappedEpsilon {
		return
	}
	sum := 0
	remainders := make([]struct {
		index int
		value float64
	}, 0, len(zones))
	for i := range zones {
		share := zones[i].VolumeShare / total
		zones[i].VolumeShare = share
		floor := int(math.Floor(share*100 + trappedEpsilon))
		if floor < 0 {
			floor = 0
		}
		zones[i].SharePct = floor
		sum += floor
		remainders = append(remainders, struct {
			index int
			value float64
		}{index: i, value: share*100 - float64(floor)})
	}
	for sum < 100 {
		sort.SliceStable(remainders, func(i, j int) bool {
			if math.Abs(remainders[i].value-remainders[j].value) <= trappedEpsilon {
				return remainders[i].index < remainders[j].index
			}
			return remainders[i].value > remainders[j].value
		})
		for i := range remainders {
			if sum >= 100 {
				break
			}
			zones[remainders[i].index].SharePct++
			sum++
		}
	}
	for sum > 100 {
		index := -1
		for i := range zones {
			if zones[i].SharePct > 0 && (index < 0 || zones[i].SharePct > zones[index].SharePct) {
				index = i
			}
		}
		if index < 0 {
			break
		}
		zones[index].SharePct--
		sum--
	}
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
	if bar.Close <= 0 || bar.High < math.Max(bar.Open, bar.Close) || bar.Low > math.Min(bar.Open, bar.Close) || bar.Low <= 0 {
		return false
	}
	for _, value := range []float64{bar.Open, bar.Close, bar.High, bar.Low, bar.Volume} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return false
		}
	}
	return true
}

func weightedQuantile(observations []trappedPricedVolume, quantile float64) float64 {
	if len(observations) == 0 {
		return 0
	}
	var total float64
	for _, observation := range observations {
		total += observation.volume
	}
	if total <= 0 {
		return 0
	}
	if quantile <= 0 {
		return observations[0].price
	}
	if quantile >= 1 {
		return observations[len(observations)-1].price
	}
	target := total * quantile
	var cumulative float64
	for _, observation := range observations {
		cumulative += observation.volume
		if cumulative+trappedEpsilon >= target {
			return observation.price
		}
	}
	return observations[len(observations)-1].price
}
