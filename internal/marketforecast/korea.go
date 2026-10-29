package marketforecast

import (
	"math"
	"sort"
)

// KoreaSeries supplies one Korean index history. KOSPI and KOSDAQ are modeled
// as lagged, pre-open features: only bars strictly before the Chinese target
// date are visible to the model.
type KoreaSeries struct {
	Code string
	Name string
	Bars []Bar
}

var koreaFeatureNames = []string{
	"kospi_ret1", "kospi_ret5", "kospi_ret20", "kospi_vol5", "kospi_vol20", "kospi_range", "kospi_gap",
	"kosdaq_ret1", "kosdaq_ret5", "kosdaq_ret20", "kosdaq_vol5", "kosdaq_vol20", "kosdaq_range", "kosdaq_gap",
}

func KoreaFeatureNames() []string {
	return append([]string(nil), koreaFeatureNames...)
}

type koreaContext struct {
	kospi  []Bar
	kosdaq []Bar
}

func newKoreaContext(series []KoreaSeries) (koreaContext, bool) {
	var out koreaContext
	for _, item := range series {
		bars := normalizeBars(item.Bars, "")
		switch item.Code {
		case "KOSPI":
			out.kospi = bars
		case "KOSDAQ":
			out.kosdaq = bars
		}
	}
	return out, len(out.kospi) >= 61 && len(out.kosdaq) >= 61
}

func (k koreaContext) features(target string) ([]float64, bool) {
	if target == "" {
		return nil, false
	}
	kospi, ok := koreaFeaturesFor(k.kospi, target)
	if !ok {
		return nil, false
	}
	kosdaq, ok := koreaFeaturesFor(k.kosdaq, target)
	if !ok {
		return nil, false
	}
	out := append(kospi, kosdaq...)
	for _, value := range out {
		if !finite(value) {
			return nil, false
		}
	}
	return out, true
}

func koreaFeaturesFor(bars []Bar, target string) ([]float64, bool) {
	index := sort.Search(len(bars), func(i int) bool { return bars[i].Date >= target }) - 1
	if index < 60 || bars[index].Close <= 0 {
		return nil, false
	}
	close := bars[index].Close
	values := make([]float64, 7)
	for position, horizon := range []int{1, 5, 20} {
		previous := bars[index-horizon].Close
		if previous <= 0 {
			return nil, false
		}
		values[position] = close/previous - 1
	}
	for position, horizon := range []int{5, 20} {
		volatility, ok := returnVolatility(bars, index, horizon)
		if !ok {
			return nil, false
		}
		values[3+position] = volatility
	}
	previous := bars[index-1].Close
	if previous <= 0 {
		return nil, false
	}
	values[5] = (bars[index].High - bars[index].Low) / previous
	values[6] = bars[index].Open/previous - 1
	for _, value := range values {
		if !finite(value) || math.Abs(value) > 1e6 {
			return nil, false
		}
	}
	return values, true
}
