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
// contain only native five-minute bars; Daily contains qfq daily bars; Market
// contains daily bars for the broad-market reference index and may be empty.
type Data struct {
	Stocks  []config.Stock
	Minutes map[string][]market.MinuteBar
	Daily   map[string][]market.Bar
	Market  []market.Bar
}

// MarketIndexCode is the broad-market reference used by the optional market
// context features. It is CSI 300, which is already collected by the
// independent market-context feed.
const MarketIndexCode = "sh000300"

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
	"macd_line", "macd_hist", "boll_pctb", "boll_width",
	"day_macd_hist", "day_boll_pctb", "day_boll_width", "day_volume_z20",
	"mkt_ret1", "mkt_ret5", "mkt_ret20", "mkt_ma20_dist", "mkt_vol20", "mkt_up", "mkt_down",
	"rel_ret5", "rel_ret20", "beta60", "down_beta60",
	"has_macd", "has_daily_macd", "has_market", "has_beta60",
}

var featurePositions = func() map[string]int {
	out := make(map[string]int, len(featureNames))
	for i, name := range featureNames {
		out[name] = i
	}
	return out
}()

type poolSnapshot struct {
	bars  map[string]market.MinuteBar
	codes []string
}

type featureContext struct {
	universe  []string
	minutes   map[string][]market.MinuteBar
	daily     map[string][]market.Bar
	market    []market.Bar
	marketRet map[string]float64
	pools     map[int64]*poolSnapshot
	firstOpen map[string]map[string]float64
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
		minutes:   make(map[string][]market.MinuteBar, len(data.Minutes)),
		daily:     make(map[string][]market.Bar, len(data.Daily)),
		pools:     map[int64]*poolSnapshot{},
		firstOpen: map[string]map[string]float64{},
	}
	codes := make([]string, 0, len(data.Minutes))
	for code, bars := range data.Minutes {
		copied := append([]market.MinuteBar(nil), bars...)
		sort.SliceStable(copied, func(i, j int) bool { return copied[i].End.Before(copied[j].End) })
		ctx.minutes[code] = copied
		codes = append(codes, code)
	}
	for code, bars := range data.Daily {
		copied := append([]market.Bar(nil), bars...)
		sort.SliceStable(copied, func(i, j int) bool { return copied[i].Date < copied[j].Date })
		ctx.daily[code] = copied
	}
	ctx.market = append([]market.Bar(nil), data.Market...)
	sort.SliceStable(ctx.market, func(i, j int) bool { return ctx.market[i].Date < ctx.market[j].Date })
	ctx.marketRet = returnMap(ctx.market)
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
	rawPools := map[int64]map[string]market.MinuteBar{}
	universeCodes := make(map[string]bool, len(ctx.universe))
	for _, code := range ctx.universe {
		universeCodes[code] = true
	}
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
		}
		ctx.firstOpen[code] = opens
	}
	for key, pool := range rawPools {
		snapshot := &poolSnapshot{bars: pool, codes: make([]string, 0, len(pool))}
		for code := range pool {
			snapshot.codes = append(snapshot.codes, code)
		}
		sort.Strings(snapshot.codes)
		ctx.pools[key] = snapshot
	}
	return ctx, nil
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
	for _, name := range []string{"has_macd", "has_daily_macd", "has_market", "has_beta60"} {
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
	if closes, ok := sameDayCloses(bars, idx, 35); ok {
		if line, hist, ok := macdFromCloses(closes); ok {
			values[featurePositions["macd_line"]] = line
			values[featurePositions["macd_hist"]] = hist
			values[featurePositions["has_macd"]] = 1
		}
	}
	if closes, ok := sameDayCloses(bars, idx, 20); ok {
		if pctb, width, ok := bollingerFromCloses(closes); ok {
			values[featurePositions["boll_pctb"]] = pctb
			values[featurePositions["boll_width"]] = width
		}
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
		if zscore, ok := dailyVolumeZScore(daily, dayIdx, 20); ok {
			values[featurePositions["day_volume_z20"]] = zscore
		}
		if closes, ok := dailyCloses(daily, dayIdx, 35); ok {
			if _, hist, ok := macdFromCloses(closes); ok {
				values[featurePositions["day_macd_hist"]] = hist
				values[featurePositions["has_daily_macd"]] = 1
			}
		}
		if closes, ok := dailyCloses(daily, dayIdx, 20); ok {
			if pctb, width, ok := bollingerFromCloses(closes); ok {
				values[featurePositions["day_boll_pctb"]] = pctb
				values[featurePositions["day_boll_width"]] = width
			}
		}
		if first := ctx.firstOpen[code][date]; first > 0 && daily[dayIdx].Close > 0 {
			values[featurePositions["today_gap"]] = first/daily[dayIdx].Close - 1
		}
	}
	marketIdx := dailyIndexBefore(ctx.market, date)
	if marketIdx >= 0 {
		if value, ok := dailyReturn(ctx.market, marketIdx, 1); ok {
			values[featurePositions["mkt_ret1"]] = value
			values[featurePositions["has_market"]] = 1
		}
		if value, ok := dailyReturn(ctx.market, marketIdx, 5); ok {
			values[featurePositions["mkt_ret5"]] = value
		}
		if value, ok := dailyReturn(ctx.market, marketIdx, 20); ok {
			values[featurePositions["mkt_ret20"]] = value
		}
		if mean, ok := dailyMeanClose(ctx.market, marketIdx, 20); ok && mean > 0 {
			delta := ctx.market[marketIdx].Close/mean - 1
			values[featurePositions["mkt_ma20_dist"]] = delta
			switch {
			case delta > 0.01:
				values[featurePositions["mkt_up"]] = 1
				values[featurePositions["mkt_down"]] = 0
			case delta < -0.01:
				values[featurePositions["mkt_up"]] = 0
				values[featurePositions["mkt_down"]] = 1
			default:
				values[featurePositions["mkt_up"]] = 0
				values[featurePositions["mkt_down"]] = 0
			}
		}
		if volatility, ok := dailyVolatility(ctx.market, marketIdx, 20); ok {
			values[featurePositions["mkt_vol20"]] = volatility
		}
		if dayIdx >= 0 {
			if value, ok := relativeReturn(daily, dayIdx, ctx.market, 5); ok {
				values[featurePositions["rel_ret5"]] = value
			}
			if value, ok := relativeReturn(daily, dayIdx, ctx.market, 20); ok {
				values[featurePositions["rel_ret20"]] = value
			}
			if beta, downBeta, ok := rollingBeta(daily, dayIdx, ctx.marketRet, 60); ok {
				values[featurePositions["beta60"]] = beta
				values[featurePositions["down_beta60"]] = downBeta
				values[featurePositions["has_beta60"]] = 1
			}
		}
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

func dailyVolumeZScore(bars []market.Bar, idx, window int) (float64, bool) {
	if idx-window < 0 {
		return 0, false
	}
	volumes := make([]float64, 0, window)
	for j := idx - window; j < idx; j++ {
		volumes = append(volumes, bars[j].Volume)
	}
	mean := mean(volumes)
	std := populationStd(volumes)
	if std <= 0 {
		return 0, false
	}
	return (bars[idx].Volume - mean) / std, true
}

// sameDayCloses permits the lunch break but never mixes sessions or dates.
// MACD and Bollinger bands are structural indicators over the intraday price
// path, so treating the lunch pause as a gap would make them unavailable for
// the whole afternoon session.
func sameDayCloses(bars []market.MinuteBar, idx, count int) ([]float64, bool) {
	if count <= 0 || idx-count+1 < 0 {
		return nil, false
	}
	date := barDate(bars[idx].End)
	out := make([]float64, count)
	for i := 0; i < count; i++ {
		bar := bars[idx-count+1+i]
		if !bar.Final || barDate(bar.End) != date || bar.Close <= 0 {
			return nil, false
		}
		out[i] = bar.Close
	}
	return out, true
}

func dailyCloses(bars []market.Bar, idx, count int) ([]float64, bool) {
	if count <= 0 || idx-count+1 < 0 {
		return nil, false
	}
	out := make([]float64, count)
	for i := 0; i < count; i++ {
		close := bars[idx-count+1+i].Close
		if close <= 0 {
			return nil, false
		}
		out[i] = close
	}
	return out, true
}

func emaSeries(values []float64, period int) []float64 {
	out := make([]float64, len(values))
	if len(values) == 0 || period <= 0 {
		return out
	}
	alpha := 2 / (float64(period) + 1)
	out[0] = values[0]
	for i := 1; i < len(values); i++ {
		out[i] = alpha*values[i] + (1-alpha)*out[i-1]
	}
	return out
}

// macdFromCloses returns the normalized MACD line and histogram at the last
// close. It requires enough history for the slow EMA and signal EMA to settle.
func macdFromCloses(closes []float64) (line, histogram float64, ok bool) {
	if len(closes) < 35 || closes[len(closes)-1] <= 0 {
		return 0, 0, false
	}
	fast := emaSeries(closes, 12)
	slow := emaSeries(closes, 26)
	macd := make([]float64, len(closes))
	for i := range closes {
		macd[i] = fast[i] - slow[i]
	}
	signal := emaSeries(macd, 9)
	last := len(closes) - 1
	line = macd[last] / closes[last]
	histogram = (macd[last] - signal[last]) / closes[last]
	if math.IsNaN(line) || math.IsInf(line, 0) || math.IsNaN(histogram) || math.IsInf(histogram, 0) {
		return 0, 0, false
	}
	return line, histogram, true
}

func bollingerFromCloses(closes []float64) (pctb, width float64, ok bool) {
	if len(closes) < 20 || closes[len(closes)-1] <= 0 {
		return 0, 0, false
	}
	window := closes[len(closes)-20:]
	center := mean(window)
	if center <= 0 {
		return 0, 0, false
	}
	std := populationStd(window)
	upper := center + 2*std
	lower := center - 2*std
	if upper <= lower {
		pctb = 0.5
	} else {
		pctb = (closes[len(closes)-1] - lower) / (upper - lower)
	}
	width = (upper - lower) / center
	if math.IsNaN(pctb) || math.IsInf(pctb, 0) || math.IsNaN(width) || math.IsInf(width, 0) {
		return 0, 0, false
	}
	return pctb, width, true
}

// relativeReturn compares a stock's trailing return with the broad-market
// return over exactly the same calendar dates, so holidays and suspensions do
// not get silently offset.
func relativeReturn(stock []market.Bar, stockIdx int, marketBars []market.Bar, days int) (float64, bool) {
	if days <= 0 || stockIdx-days < 0 {
		return 0, false
	}
	stockRet, ok := dailyReturn(stock, stockIdx, days)
	if !ok {
		return 0, false
	}
	fromDate := stock[stockIdx-days].Date
	toDate := stock[stockIdx].Date
	marketRet, ok := returnBetween(marketBars, fromDate, toDate)
	if !ok {
		return 0, false
	}
	return stockRet - marketRet, true
}

func returnBetween(bars []market.Bar, from, to string) (float64, bool) {
	if from == "" || to == "" || from >= to {
		return 0, false
	}
	fromIdx := sort.Search(len(bars), func(i int) bool { return bars[i].Date >= from })
	toIdx := sort.Search(len(bars), func(i int) bool { return bars[i].Date >= to })
	if fromIdx >= len(bars) || toIdx >= len(bars) || bars[fromIdx].Date != from || bars[toIdx].Date != to {
		return 0, false
	}
	if bars[fromIdx].Close <= 0 || bars[toIdx].Close <= 0 {
		return 0, false
	}
	return bars[toIdx].Close/bars[fromIdx].Close - 1, true
}

// rollingBeta estimates the stock's sensitivity to the broad market and its
// downside sensitivity. It aligns returns by trading date and requires at
// least twenty common observations.
func returnMap(bars []market.Bar) map[string]float64 {
	out := make(map[string]float64, len(bars))
	for i := 1; i < len(bars); i++ {
		if bars[i-1].Close <= 0 || bars[i].Close <= 0 {
			continue
		}
		out[bars[i].Date] = bars[i].Close/bars[i-1].Close - 1
	}
	return out
}

func rollingBeta(stock []market.Bar, stockIdx int, marketReturns map[string]float64, window int) (beta, downBeta float64, ok bool) {
	if stockIdx < 1 || window < 20 {
		return 0, 0, false
	}
	start := stockIdx - window + 1
	if start < 1 {
		start = 1
	}
	xs := make([]float64, 0, window)
	ys := make([]float64, 0, window)
	downX := make([]float64, 0, window)
	downY := make([]float64, 0, window)
	for i := start; i <= stockIdx; i++ {
		if stock[i-1].Close <= 0 || stock[i].Close <= 0 {
			continue
		}
		marketReturn, found := marketReturns[stock[i].Date]
		if !found {
			continue
		}
		stockReturn := stock[i].Close/stock[i-1].Close - 1
		xs = append(xs, marketReturn)
		ys = append(ys, stockReturn)
		if marketReturn < 0 {
			downX = append(downX, marketReturn)
			downY = append(downY, stockReturn)
		}
	}
	if len(xs) < 20 {
		return 0, 0, false
	}
	variance := populationVariance(xs)
	if variance <= 0 {
		return 0, 0, false
	}
	beta = covariance(xs, ys) / variance
	downBeta = beta
	if len(downX) >= 5 {
		if downVariance := populationVariance(downX); downVariance > 0 {
			downBeta = covariance(downX, downY) / downVariance
		}
	}
	if math.IsNaN(beta) || math.IsInf(beta, 0) || math.IsNaN(downBeta) || math.IsInf(downBeta, 0) {
		return 0, 0, false
	}
	return clip(beta, 5), clip(downBeta, 5), true
}

func populationVariance(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	center := mean(values)
	sum := 0.0
	for _, value := range values {
		delta := value - center
		sum += delta * delta
	}
	return sum / float64(len(values))
}

func covariance(xs, ys []float64) float64 {
	if len(xs) == 0 || len(xs) != len(ys) {
		return 0
	}
	meanX := mean(xs)
	meanY := mean(ys)
	sum := 0.0
	for i := range xs {
		sum += (xs[i] - meanX) * (ys[i] - meanY)
	}
	return sum / float64(len(xs))
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
