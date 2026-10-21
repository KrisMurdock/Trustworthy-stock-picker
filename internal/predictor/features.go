package predictor

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

// Data is the immutable local snapshot used by training and inference. Minutes
// contain only native five-minute bars; Daily contains qfq daily bars. Reference
// is a fixed broad pool used for market-breadth context, independent from the
// candidate Stocks ranked by the model. MarketDaily contains the completed
// index bars strictly before each sample date.
type Data struct {
	Stocks        []config.Stock
	Reference     []string
	FeatureGroups []string
	Minutes       map[string][]market.MinuteBar
	Daily         map[string][]market.Bar
	MarketDaily   []market.Bar
}

// DefaultMarketIndexCode is the broad-market reference used by the first
// market-context feature set.
const DefaultMarketIndexCode = "sh000300"

const (
	barStep          = 5 * time.Minute
	afternoonOpen    = 780
	morningClose     = 690
	dayClose         = 900
	maxContextBars   = 24
	maxPoolRangeBars = 6
)

// FeatureNames returns the stable feature order. Changing this list requires a
// FeatureVersion bump and a fresh model artifact.
func FeatureNames() []string {
	return append([]string(nil), featureNames...)
}

var featureNames = []string{
	"ret1", "ret2", "ret3", "ret6", "ret12", "ret24",
	"range1", "range3", "range6", "range12",
	"close_pos", "body", "upper_wick", "lower_wick",
	"volume_ratio1", "volume_ratio3", "volume_ratio6",
	"vol12", "vol24",
	"ma5_dist", "ma12_dist", "ma24_dist", "rsi14",
	"minute_of_session", "bar_index", "is_afternoon",
	"pool_ret1", "pool_breadth", "pool_mean_range",
	"day_ret1", "day_ret5", "day_ret20", "day_ma5_dist", "day_ma20_dist",
	"day_vol20", "day_volume_ratio", "today_gap",
	"log_price", "stock_day_vol20",
	"has_24_bars", "has_daily20", "has_volume20",
	"mkt_ret1", "mkt_ret5", "mkt_ret20", "mkt_ma20_dist", "mkt_vol20", "mkt_drawdown20",
	"rel_mkt_ret1", "rel_mkt_ret5", "rel_mkt_ret20",
	"beta60", "corr60", "downside_beta60", "mkt_ret5_x_beta60",
	"ref_ret", "ref_breadth", "ref_dispersion", "ref_ma20_breadth", "ref_coverage",
	"rel_ref_ret1",
	"has_market_context", "has_beta60", "has_ref_breadth",
}

var featurePositions = func() map[string]int {
	out := make(map[string]int, len(featureNames))
	for i, name := range featureNames {
		out[name] = i
	}
	return out
}()

var featureGroupByName = map[string]string{
	"mkt_ret1": "market", "mkt_ret5": "market", "mkt_ret20": "market", "mkt_ma20_dist": "market",
	"mkt_vol20": "market", "mkt_drawdown20": "market", "has_market_context": "market",
	"rel_mkt_ret1": "relative", "rel_mkt_ret5": "relative", "rel_mkt_ret20": "relative",
	"beta60": "beta", "corr60": "beta", "downside_beta60": "beta", "mkt_ret5_x_beta60": "beta", "has_beta60": "beta",
	"ref_ret": "reference", "ref_breadth": "reference", "ref_dispersion": "reference", "ref_ma20_breadth": "reference",
	"ref_coverage": "reference", "rel_ref_ret1": "reference", "has_ref_breadth": "reference",
}

type poolSnapshot struct {
	bars  map[string]market.MinuteBar
	codes []string
}

type featureContext struct {
	universe         []string
	reference        []string
	minutes          map[string][]market.MinuteBar
	daily            map[string][]market.Bar
	marketDaily      []market.Bar
	pools            map[int64]*poolSnapshot
	referencePools   map[int64]*poolSnapshot
	referenceMA20Pct map[string]float64
	firstOpen        map[string]map[string]float64
	featureGroups    map[string]bool
}

// BuildTrainingSamples creates one sample per finalized input bar that has a
// finalized next bar exactly five minutes later in the same trading session.
// Input bars at 11:30 and 15:00 are excluded because they have no next bar in
// the same session.
func BuildTrainingSamples(data Data) ([]Sample, error) {
	ctx, err := newFeatureContext(data)
	if err != nil {
		return nil, err
	}
	out := make([]Sample, 0, 4096)
	for _, code := range ctx.universe {
		bars := ctx.minutes[code]
		for i := range bars {
			bar := bars[i]
			if !bar.Final {
				continue
			}
			if _, ok := validInputEnd(bar.End); !ok {
				continue
			}
			targetEnd := bar.End.Add(barStep)
			if _, ok := market.MinuteSession(targetEnd, 5); !ok {
				continue
			}
			j := i + 1
			if j >= len(bars) {
				continue
			}
			next := bars[j]
			if !next.Final || !next.End.Equal(targetEnd) || bar.Close <= 0 {
				continue
			}
			out = append(out, Sample{
				Code:          code,
				Date:          barDate(bar.End),
				InputEnd:      bar.End,
				TargetEnd:     targetEnd,
				InputOpen:     bar.Open,
				InputHigh:     bar.High,
				InputLow:      bar.Low,
				InputClose:    bar.Close,
				BaselineRange: baselineRange(bars, i),
				Features:      buildFeatureValues(ctx, code, bars, i),
				Labels: map[Target]float64{
					TargetHigh:  next.High/bar.Close - 1,
					TargetLow:   next.Low/bar.Close - 1,
					TargetClose: next.Close/bar.Close - 1,
				},
			})
		}
	}
	sortSamples(out)
	return out, nil
}

// BuildInferenceFeature returns the feature vector for a finalized input bar.
// Only bars ending at or before inputEnd, the same-timestamp cross-section and
// daily bars strictly before the input date are used.
func BuildInferenceFeature(data Data, code string, inputEnd time.Time) (FeatureVector, error) {
	vector, _, err := BuildInference(data, code, inputEnd)
	return vector, err
}

// BuildInference returns the feature vector and the finalized input bar that
// produced it. The target bar has not happened yet at inference time.
func BuildInference(data Data, code string, inputEnd time.Time) (FeatureVector, market.MinuteBar, error) {
	if _, ok := validInputEnd(inputEnd); !ok {
		return FeatureVector{}, market.MinuteBar{}, fmt.Errorf("predictor input end %s is not a valid bar boundary", inputEnd.Format(time.RFC3339))
	}
	if _, ok := market.MinuteSession(inputEnd.Add(barStep), 5); !ok {
		return FeatureVector{}, market.MinuteBar{}, fmt.Errorf("predictor input end %s has no next bar in this session", inputEnd.Format(time.RFC3339))
	}
	ctx, err := newFeatureContext(data)
	if err != nil {
		return FeatureVector{}, market.MinuteBar{}, err
	}
	bars, ok := ctx.minutes[code]
	if !ok || len(bars) == 0 {
		return FeatureVector{}, market.MinuteBar{}, fmt.Errorf("predictor has no minute data for %q", code)
	}
	idx := sort.Search(len(bars), func(i int) bool { return !bars[i].End.Before(inputEnd) })
	if idx >= len(bars) || !bars[idx].End.Equal(inputEnd) {
		return FeatureVector{}, market.MinuteBar{}, fmt.Errorf("predictor has no finalized bar for %q at %s", code, inputEnd.Format(time.RFC3339))
	}
	bar := bars[idx]
	if !bar.Final {
		return FeatureVector{}, market.MinuteBar{}, fmt.Errorf("predictor bar for %q at %s is not finalized", code, inputEnd.Format(time.RFC3339))
	}
	return FeatureVector{Names: FeatureNames(), Values: buildFeatureValues(ctx, code, bars, idx)}, bar, nil
}

func newFeatureContext(data Data) (*featureContext, error) {
	ctx := &featureContext{
		minutes:          make(map[string][]market.MinuteBar, len(data.Minutes)),
		daily:            make(map[string][]market.Bar, len(data.Daily)),
		marketDaily:      append([]market.Bar(nil), data.MarketDaily...),
		pools:            map[int64]*poolSnapshot{},
		referencePools:   map[int64]*poolSnapshot{},
		referenceMA20Pct: map[string]float64{},
		firstOpen:        map[string]map[string]float64{},
	}
	if len(data.FeatureGroups) > 0 {
		ctx.featureGroups = map[string]bool{}
		allGroups := false
	groupLoop:
		for _, group := range data.FeatureGroups {
			switch group {
			case "all":
				allGroups = true
				break groupLoop
			case "market", "relative", "beta", "reference":
				ctx.featureGroups[group] = true
			default:
				return nil, fmt.Errorf("unknown predictor feature group %q", group)
			}
		}
		if allGroups {
			ctx.featureGroups = nil
		}
	}
	codes := make([]string, 0, len(data.Minutes))
	dates := map[string]bool{}
	for code, bars := range data.Minutes {
		copied := append([]market.MinuteBar(nil), bars...)
		sort.SliceStable(copied, func(i, j int) bool { return copied[i].End.Before(copied[j].End) })
		ctx.minutes[code] = copied
		codes = append(codes, code)
		for _, bar := range copied {
			if bar.Final {
				dates[barDate(bar.End)] = true
			}
		}
	}
	sort.SliceStable(ctx.marketDaily, func(i, j int) bool { return ctx.marketDaily[i].Date < ctx.marketDaily[j].Date })
	for code, bars := range data.Daily {
		copied := append([]market.Bar(nil), bars...)
		sort.SliceStable(copied, func(i, j int) bool { return copied[i].Date < copied[j].Date })
		ctx.daily[code] = copied
	}
	if len(data.Stocks) > 0 {
		seen := map[string]bool{}
		for _, stock := range data.Stocks {
			if stock.Code == "" || seen[stock.Code] {
				continue
			}
			seen[stock.Code] = true
			ctx.universe = append(ctx.universe, stock.Code)
		}
		sort.Strings(ctx.universe)
	} else {
		sort.Strings(codes)
		ctx.universe = codes
	}
	seenReference := map[string]bool{}
	for _, code := range data.Reference {
		if code == "" || seenReference[code] {
			continue
		}
		seenReference[code] = true
		ctx.reference = append(ctx.reference, code)
	}
	if len(ctx.reference) == 0 {
		ctx.reference = append([]string(nil), ctx.universe...)
	}
	sort.Strings(ctx.reference)
	for date := range dates {
		if breadth, ok := referenceMA20Breadth(ctx.reference, ctx.daily, date); ok {
			ctx.referenceMA20Pct[date] = breadth
		}
	}
	rawPools := map[int64]map[string]market.MinuteBar{}
	universeCodes := make(map[string]bool, len(ctx.universe))
	for _, code := range ctx.universe {
		universeCodes[code] = true
	}
	referenceCodes := make(map[string]bool, len(ctx.reference))
	for _, code := range ctx.reference {
		referenceCodes[code] = true
	}
	rawReferencePools := map[int64]map[string]market.MinuteBar{}
	for code, bars := range ctx.minutes {
		opens := map[string]float64{}
		for _, bar := range bars {
			if !bar.Final {
				continue
			}
			date := barDate(bar.End)
			if _, ok := opens[date]; !ok {
				opens[date] = bar.Open
			}
			if universeCodes[code] {
				key := bar.End.Unix()
				pool := rawPools[key]
				if pool == nil {
					pool = map[string]market.MinuteBar{}
					rawPools[key] = pool
				}
				pool[code] = bar
			}
			if referenceCodes[code] {
				key := bar.End.Unix()
				pool := rawReferencePools[key]
				if pool == nil {
					pool = map[string]market.MinuteBar{}
					rawReferencePools[key] = pool
				}
				pool[code] = bar
			}
		}
		ctx.firstOpen[code] = opens
	}
	ctx.pools = snapshotsByTimestamp(rawPools)
	ctx.referencePools = snapshotsByTimestamp(rawReferencePools)
	return ctx, nil
}

func snapshotsByTimestamp(raw map[int64]map[string]market.MinuteBar) map[int64]*poolSnapshot {
	out := make(map[int64]*poolSnapshot, len(raw))
	for key, pool := range raw {
		snapshot := &poolSnapshot{bars: pool, codes: make([]string, 0, len(pool))}
		for code := range pool {
			snapshot.codes = append(snapshot.codes, code)
		}
		sort.Strings(snapshot.codes)
		out[key] = snapshot
	}
	return out
}

func referenceMA20Breadth(reference []string, daily map[string][]market.Bar, date string) (float64, bool) {
	if len(reference) == 0 {
		return 0, false
	}
	valid := 0
	above := 0
	for _, code := range reference {
		bars := daily[code]
		idx := dailyIndexBefore(bars, date)
		if idx < 0 {
			continue
		}
		mean, ok := dailyMeanClose(bars, idx, 20)
		if !ok || mean <= 0 {
			continue
		}
		valid++
		if bars[idx].Close > mean {
			above++
		}
	}
	if valid*5 < len(reference)*4 {
		return 0, false
	}
	return float64(above) / float64(valid), true
}

func validInputEnd(t time.Time) (int, bool) {
	start, ok := market.MinuteSession(t, 5)
	if !ok {
		return 0, false
	}
	minute := minuteOfDay(t)
	if minute == morningClose || minute == dayClose {
		return 0, false
	}
	return start, true
}

func minuteOfDay(t time.Time) int {
	local := t.In(market.Shanghai)
	return local.Hour()*60 + local.Minute()
}

func barDate(t time.Time) string {
	return t.In(market.Shanghai).Format("2006-01-02")
}

func sortSamples(samples []Sample) {
	sort.SliceStable(samples, func(i, j int) bool {
		if samples[i].Date != samples[j].Date {
			return samples[i].Date < samples[j].Date
		}
		if !samples[i].InputEnd.Equal(samples[j].InputEnd) {
			return samples[i].InputEnd.Before(samples[j].InputEnd)
		}
		return samples[i].Code < samples[j].Code
	})
}

func buildFeatureValues(ctx *featureContext, code string, bars []market.MinuteBar, idx int) []float64 {
	values := make([]float64, len(featureNames))
	for i := range values {
		values[i] = math.NaN()
	}
	for _, name := range []string{"has_24_bars", "has_daily20", "has_volume20", "has_market_context", "has_beta60", "has_ref_breadth"} {
		values[featurePositions[name]] = 0
	}
	bar := bars[idx]
	end := bar.End
	start, _ := validInputEnd(end)
	minute := minuteOfDay(end)
	if bar.Close > 0 {
		values[featurePositions["log_price"]] = math.Log(bar.Close)
	}
	for _, horizon := range []int{1, 2, 3, 6, 12, 24} {
		if previous, ok := contiguousBar(bars, idx, horizon); ok && previous.Close > 0 && bar.Close > 0 {
			values[featurePositions[fmt.Sprintf("ret%d", horizon)]] = bar.Close/previous.Close - 1
		}
	}
	for _, horizon := range []int{1, 3, 6, 12} {
		if high, low, ok := windowExtremes(bars, idx, horizon); ok && bar.Close > 0 {
			values[featurePositions[fmt.Sprintf("range%d", horizon)]] = (high - low) / bar.Close
		}
	}
	if bar.High > bar.Low {
		values[featurePositions["close_pos"]] = (bar.Close - bar.Low) / (bar.High - bar.Low)
	} else {
		values[featurePositions["close_pos"]] = 0.5
	}
	if bar.Open > 0 {
		values[featurePositions["body"]] = (bar.Close - bar.Open) / bar.Open
		values[featurePositions["upper_wick"]] = (bar.High - math.Max(bar.Open, bar.Close)) / bar.Open
		values[featurePositions["lower_wick"]] = (math.Min(bar.Open, bar.Close) - bar.Low) / bar.Open
	}
	if previous, ok := contiguousBar(bars, idx, 1); ok && previous.Volume > 0 {
		values[featurePositions["volume_ratio1"]] = bar.Volume / previous.Volume
	}
	for _, horizon := range []int{3, 6} {
		if _, ok := contiguousBar(bars, idx, horizon); ok {
			mean := 0.0
			for j := idx - horizon; j < idx; j++ {
				mean += bars[j].Volume
			}
			mean /= float64(horizon)
			if mean > 0 {
				values[featurePositions[fmt.Sprintf("volume_ratio%d", horizon)]] = bar.Volume / mean
			}
		}
	}
	if volatility, ok := recentReturnVolatility(bars, idx, 12); ok {
		values[featurePositions["vol12"]] = volatility
	}
	if volatility, ok := recentReturnVolatility(bars, idx, 24); ok {
		values[featurePositions["vol24"]] = volatility
	}
	for _, horizon := range []int{5, 12, 24} {
		if mean, ok := trailingMeanClose(bars, idx, horizon); ok && mean > 0 && bar.Close > 0 {
			values[featurePositions[fmt.Sprintf("ma%d_dist", horizon)]] = bar.Close/mean - 1
		}
	}
	if rsi, ok := rsi14(bars, idx); ok {
		values[featurePositions["rsi14"]] = rsi
	}
	values[featurePositions["minute_of_session"]] = float64(minute - start)
	values[featurePositions["bar_index"]] = float64((minute - start) / 5)
	if start == afternoonOpen {
		values[featurePositions["is_afternoon"]] = 1
	} else {
		values[featurePositions["is_afternoon"]] = 0
	}
	if pool := ctx.pools[end.Unix()]; pool != nil {
		ret, breadth, meanRange, ok := poolFeatures(pool)
		if ok {
			values[featurePositions["pool_ret1"]] = ret
			values[featurePositions["pool_breadth"]] = breadth
			values[featurePositions["pool_mean_range"]] = meanRange
		}
	}
	date := barDate(end)
	daily := ctx.daily[code]
	dayIdx := dailyIndexBefore(daily, date)
	if dayIdx >= 0 {
		if value, ok := dailyReturn(daily, dayIdx, 1); ok {
			values[featurePositions["day_ret1"]] = value
		}
		if value, ok := dailyReturn(daily, dayIdx, 5); ok {
			values[featurePositions["day_ret5"]] = value
		}
		if value, ok := dailyReturn(daily, dayIdx, 20); ok {
			values[featurePositions["day_ret20"]] = value
		}
		if mean, ok := dailyMeanClose(daily, dayIdx, 5); ok && mean > 0 {
			values[featurePositions["day_ma5_dist"]] = daily[dayIdx].Close/mean - 1
		}
		if mean, ok := dailyMeanClose(daily, dayIdx, 20); ok && mean > 0 {
			values[featurePositions["day_ma20_dist"]] = daily[dayIdx].Close/mean - 1
		}
		if volatility, ok := dailyVolatility(daily, dayIdx, 20); ok {
			values[featurePositions["day_vol20"]] = volatility
		}
		if ratio, ok := dailyVolumeRatio(daily, dayIdx, 20); ok {
			values[featurePositions["day_volume_ratio"]] = ratio
		}
		if first := ctx.firstOpen[code][date]; first > 0 && daily[dayIdx].Close > 0 {
			values[featurePositions["today_gap"]] = first/daily[dayIdx].Close - 1
		}
	}
	marketIdx := dailyIndexBefore(ctx.marketDaily, date)
	marketReady := false
	for _, horizon := range []int{1, 5, 20} {
		if value, ok := dailyReturn(ctx.marketDaily, marketIdx, horizon); ok {
			values[featurePositions[fmt.Sprintf("mkt_ret%d", horizon)]] = value
			if horizon == 20 {
				marketReady = true
			}
		}
	}
	if mean, ok := dailyMeanClose(ctx.marketDaily, marketIdx, 20); ok && mean > 0 {
		values[featurePositions["mkt_ma20_dist"]] = ctx.marketDaily[marketIdx].Close/mean - 1
	}
	if volatility, ok := dailyVolatility(ctx.marketDaily, marketIdx, 20); ok {
		values[featurePositions["mkt_vol20"]] = volatility
	}
	if drawdown, ok := dailyDrawdown(ctx.marketDaily, marketIdx, 20); ok {
		values[featurePositions["mkt_drawdown20"]] = drawdown
	}
	if marketReady {
		values[featurePositions["has_market_context"]] = 1
	}
	if dayIdx >= 0 && marketIdx >= 0 {
		for _, horizon := range []int{1, 5, 20} {
			if stockReturn, stockOK := dailyReturn(daily, dayIdx, horizon); stockOK {
				if marketReturn, marketOK := dailyReturn(ctx.marketDaily, marketIdx, horizon); marketOK {
					values[featurePositions[fmt.Sprintf("rel_mkt_ret%d", horizon)]] = stockReturn - marketReturn
				}
			}
		}
		if beta, correlation, downside, ok := marketSensitivity(daily, dayIdx, ctx.marketDaily, marketIdx, 60); ok {
			values[featurePositions["beta60"]] = beta
			values[featurePositions["corr60"]] = correlation
			values[featurePositions["downside_beta60"]] = downside
			values[featurePositions["has_beta60"]] = 1
			if market5, marketOK := dailyReturn(ctx.marketDaily, marketIdx, 5); marketOK {
				values[featurePositions["mkt_ret5_x_beta60"]] = market5 * beta
			}
		}
	}
	if pool := ctx.referencePools[end.Unix()]; pool != nil {
		ret, breadth, dispersion, coverage, ok := referenceFeatures(pool, len(ctx.reference))
		if ok {
			values[featurePositions["ref_ret"]] = ret
			values[featurePositions["ref_breadth"]] = breadth
			values[featurePositions["ref_dispersion"]] = dispersion
			values[featurePositions["ref_coverage"]] = coverage
			values[featurePositions["has_ref_breadth"]] = 1
			if bar.Open > 0 {
				values[featurePositions["rel_ref_ret1"]] = bar.Close/bar.Open - 1 - ret
			}
		}
	}
	if breadth, ok := ctx.referenceMA20Pct[date]; ok {
		values[featurePositions["ref_ma20_breadth"]] = breadth
	}
	if volatility, ok := sameDayVolatility(bars, idx, 20); ok {
		values[featurePositions["stock_day_vol20"]] = volatility
	}
	if _, ok := contiguousBar(bars, idx, maxContextBars-1); ok {
		values[featurePositions["has_24_bars"]] = 1
	} else {
		values[featurePositions["has_24_bars"]] = 0
	}
	if dayIdx >= 19 {
		values[featurePositions["has_daily20"]] = 1
	} else {
		values[featurePositions["has_daily20"]] = 0
	}
	if _, ok := contiguousBar(bars, idx, 20); ok {
		values[featurePositions["has_volume20"]] = 1
	} else {
		values[featurePositions["has_volume20"]] = 0
	}
	if ctx.featureGroups != nil {
		for name, group := range featureGroupByName {
			if !ctx.featureGroups[group] {
				values[featurePositions[name]] = math.NaN()
			}
		}
	}
	return values
}

func contiguousBar(bars []market.MinuteBar, idx, back int) (market.MinuteBar, bool) {
	j := idx - back
	if j < 0 || j >= len(bars) {
		return market.MinuteBar{}, false
	}
	for k := j; k <= idx; k++ {
		if !bars[k].Final {
			return market.MinuteBar{}, false
		}
		if k > j && !bars[k].End.Equal(bars[k-1].End.Add(barStep)) {
			return market.MinuteBar{}, false
		}
	}
	return bars[j], true
}

func windowExtremes(bars []market.MinuteBar, idx, window int) (float64, float64, bool) {
	if window <= 0 || idx-window+1 < 0 {
		return 0, 0, false
	}
	if _, ok := contiguousBar(bars, idx, window-1); !ok {
		return 0, 0, false
	}
	high := math.Inf(-1)
	low := math.Inf(1)
	for j := idx - window + 1; j <= idx; j++ {
		high = math.Max(high, bars[j].High)
		low = math.Min(low, bars[j].Low)
	}
	if math.IsInf(high, 0) || math.IsInf(low, 0) {
		return 0, 0, false
	}
	return high, low, true
}

func recentReturnVolatility(bars []market.MinuteBar, idx, window int) (float64, bool) {
	if _, ok := contiguousBar(bars, idx, window); !ok {
		return 0, false
	}
	returns := make([]float64, 0, window)
	for j := idx - window + 1; j <= idx; j++ {
		if bars[j-1].Close <= 0 || bars[j].Close <= 0 {
			return 0, false
		}
		returns = append(returns, bars[j].Close/bars[j-1].Close-1)
	}
	return populationStd(returns), true
}

func trailingMeanClose(bars []market.MinuteBar, idx, window int) (float64, bool) {
	if _, ok := contiguousBar(bars, idx, window-1); !ok {
		return 0, false
	}
	sum := 0.0
	for j := idx - window + 1; j <= idx; j++ {
		sum += bars[j].Close
	}
	return sum / float64(window), true
}

func rsi14(bars []market.MinuteBar, idx int) (float64, bool) {
	if _, ok := contiguousBar(bars, idx, 14); !ok {
		return 0, false
	}
	gain := 0.0
	loss := 0.0
	for j := idx - 13; j <= idx; j++ {
		change := bars[j].Close - bars[j-1].Close
		if change > 0 {
			gain += change
		} else {
			loss -= change
		}
	}
	if gain == 0 && loss == 0 {
		return 50, true
	}
	if loss == 0 {
		return 100, true
	}
	return 100 - 100/(1+gain/loss), true
}

func sameDayVolatility(bars []market.MinuteBar, idx, limit int) (float64, bool) {
	returns := make([]float64, 0, limit)
	for j := idx; j > 0 && len(returns) < limit; j-- {
		if !bars[j].End.Equal(bars[j-1].End.Add(barStep)) || bars[j-1].Close <= 0 || bars[j].Close <= 0 {
			break
		}
		returns = append(returns, bars[j].Close/bars[j-1].Close-1)
	}
	if len(returns) < 2 {
		return 0, false
	}
	return populationStd(returns), true
}

func populationStd(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	sum := 0.0
	for _, value := range values {
		delta := value - mean
		sum += delta * delta
	}
	return math.Sqrt(sum / float64(len(values)))
}

func poolFeatures(pool *poolSnapshot) (float64, float64, float64, bool) {
	if pool == nil || len(pool.codes) == 0 {
		return 0, 0, 0, false
	}
	sumRet := 0.0
	sumRange := 0.0
	up := 0
	count := 0
	for _, code := range pool.codes {
		bar := pool.bars[code]
		if bar.Open <= 0 || bar.Close <= 0 {
			continue
		}
		// Same-timestamp cross-section only: pool_ret1 is the mean intrabar
		// close/open return of bars ending exactly at inputEnd.
		sumRet += bar.Close/bar.Open - 1
		sumRange += (bar.High - bar.Low) / bar.Open
		if bar.Close > bar.Open {
			up++
		}
		count++
	}
	if count == 0 {
		return 0, 0, 0, false
	}
	n := float64(count)
	return sumRet / n, float64(up) / n, sumRange / n, true
}

func referenceFeatures(pool *poolSnapshot, referenceSize int) (float64, float64, float64, float64, bool) {
	if pool == nil || len(pool.codes) == 0 || referenceSize <= 0 {
		return 0, 0, 0, 0, false
	}
	returns := make([]float64, 0, len(pool.codes))
	up := 0
	for _, code := range pool.codes {
		bar := pool.bars[code]
		if bar.Open <= 0 || bar.Close <= 0 {
			continue
		}
		ret := bar.Close/bar.Open - 1
		returns = append(returns, ret)
		if ret > 0 {
			up++
		}
	}
	if len(returns) == 0 {
		return 0, 0, 0, 0, false
	}
	mean := 0.0
	for _, ret := range returns {
		mean += ret
	}
	n := float64(len(returns))
	mean /= n
	return mean, float64(up) / n, populationStd(returns), n / float64(referenceSize), true
}

func dailyIndexBefore(bars []market.Bar, date string) int {
	return sort.Search(len(bars), func(i int) bool { return bars[i].Date >= date }) - 1
}

func dailyReturn(bars []market.Bar, idx, days int) (float64, bool) {
	j := idx - days
	if j < 0 || bars[j].Close <= 0 || bars[idx].Close <= 0 {
		return 0, false
	}
	return bars[idx].Close/bars[j].Close - 1, true
}

func dailyMeanClose(bars []market.Bar, idx, window int) (float64, bool) {
	if idx-window+1 < 0 {
		return 0, false
	}
	sum := 0.0
	for j := idx - window + 1; j <= idx; j++ {
		sum += bars[j].Close
	}
	return sum / float64(window), true
}

func dailyVolatility(bars []market.Bar, idx, window int) (float64, bool) {
	if idx-window < 0 {
		return 0, false
	}
	returns := make([]float64, 0, window)
	for j := idx - window + 1; j <= idx; j++ {
		if bars[j-1].Close <= 0 || bars[j].Close <= 0 {
			return 0, false
		}
		returns = append(returns, bars[j].Close/bars[j-1].Close-1)
	}
	return populationStd(returns), true
}

func dailyDrawdown(bars []market.Bar, idx, window int) (float64, bool) {
	if idx < 0 || idx-window+1 < 0 || bars[idx].Close <= 0 {
		return 0, false
	}
	peak := 0.0
	for j := idx - window + 1; j <= idx; j++ {
		if bars[j].Close > peak {
			peak = bars[j].Close
		}
	}
	if peak <= 0 {
		return 0, false
	}
	return bars[idx].Close/peak - 1, true
}

func marketSensitivity(stock []market.Bar, stockIdx int, market []market.Bar, marketIdx, window int) (float64, float64, float64, bool) {
	if stockIdx < 2 || marketIdx < 2 || window < 2 {
		return 0, 0, 0, false
	}
	marketClose := make(map[string]float64, marketIdx+1)
	for j := 0; j <= marketIdx; j++ {
		if market[j].Close > 0 {
			marketClose[market[j].Date] = market[j].Close
		}
	}
	type pair struct{ stock, market float64 }
	pairs := make([]pair, 0, window)
	for j := stockIdx; j >= 1 && len(pairs) < window; j-- {
		if stock[j].Close <= 0 || stock[j-1].Close <= 0 {
			continue
		}
		current, currentOK := marketClose[stock[j].Date]
		previous, previousOK := marketClose[stock[j-1].Date]
		if !currentOK || !previousOK || previous <= 0 || current <= 0 {
			continue
		}
		pairs = append(pairs, pair{stock: stock[j].Close/stock[j-1].Close - 1, market: current/previous - 1})
	}
	if len(pairs) < window*2/3 {
		return 0, 0, 0, false
	}
	stockMean, marketMean := 0.0, 0.0
	for _, item := range pairs {
		stockMean += item.stock
		marketMean += item.market
	}
	n := float64(len(pairs))
	stockMean /= n
	marketMean /= n
	covariance, stockVariance, marketVariance := 0.0, 0.0, 0.0
	for _, item := range pairs {
		stockDelta := item.stock - stockMean
		marketDelta := item.market - marketMean
		covariance += stockDelta * marketDelta
		stockVariance += stockDelta * stockDelta
		marketVariance += marketDelta * marketDelta
	}
	covariance /= n
	stockVariance /= n
	marketVariance /= n
	if marketVariance <= 0 || stockVariance <= 0 {
		return 0, 0, 0, false
	}
	beta := covariance / marketVariance
	correlation := covariance / math.Sqrt(stockVariance*marketVariance)
	downsideCovariance, downsideVariance := 0.0, 0.0
	downsideCount := 0
	for _, item := range pairs {
		if item.market >= 0 {
			continue
		}
		downsideCovariance += (item.stock - stockMean) * (item.market - marketMean)
		downsideVariance += (item.market - marketMean) * (item.market - marketMean)
		downsideCount++
	}
	downsideBeta := math.NaN()
	if downsideCount >= 10 && downsideVariance > 0 {
		downsideBeta = (downsideCovariance / float64(downsideCount)) / (downsideVariance / float64(downsideCount))
	}
	return beta, correlation, downsideBeta, true
}

func dailyVolumeRatio(bars []market.Bar, idx, window int) (float64, bool) {
	if idx-window < 0 {
		return 0, false
	}
	sum := 0.0
	for j := idx - window; j < idx; j++ {
		sum += bars[j].Volume
	}
	mean := sum / float64(window)
	if mean <= 0 {
		return 0, false
	}
	return bars[idx].Volume / mean, true
}

func baselineRange(bars []market.MinuteBar, idx int) float64 {
	window := maxPoolRangeBars
	if idx+1 < window {
		window = idx + 1
	}
	if window <= 0 {
		return 0
	}
	sum := 0.0
	count := 0
	for j := idx - window + 1; j <= idx; j++ {
		if bars[j].Close <= 0 {
			continue
		}
		if _, ok := contiguousBar(bars, idx, idx-j); !ok {
			continue
		}
		sum += (bars[j].High - bars[j].Low) / bars[j].Close
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}
