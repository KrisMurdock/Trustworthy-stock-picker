package marketforecast

import (
	"sort"
	"time"
)

const hangSengMaxGapDays = 10

var hangSengFeatureNames = []string{
	"hsi_ret1", "hsi_ret5", "hsi_ret20", "hsi_vol20", "hsi_range", "hsi_gap", "hsi_close_loc",
}

// HangSengFeatureNames returns the shadow-only Hang Seng feature order. These
// features are never appended to the formal morning or afternoon tree.
func HangSengFeatureNames() []string {
	return append([]string(nil), hangSengFeatureNames...)
}

type hangSengContext struct {
	bars []Bar
}

func newHangSengContext(input []Bar) (hangSengContext, bool) {
	bars := normalizeBars(input, "")
	return hangSengContext{bars: bars}, len(bars) >= 21
}

func (h hangSengContext) features(target string) ([]float64, string) {
	if target == "" {
		return nil, "insufficient_history"
	}
	index := sort.Search(len(h.bars), func(i int) bool { return h.bars[i].Date >= target }) - 1
	if index < 20 || h.bars[index].Close <= 0 {
		return nil, "insufficient_history"
	}
	targetDate, err := time.Parse("2006-01-02", target)
	if err != nil {
		return nil, "invalid_target"
	}
	lastDate, err := time.Parse("2006-01-02", h.bars[index].Date)
	if err != nil || targetDate.Sub(lastDate) > hangSengMaxGapDays*24*time.Hour {
		return nil, "stale"
	}
	closeValue := h.bars[index].Close
	values := make([]float64, len(hangSengFeatureNames))
	for position, horizon := range []int{1, 5, 20} {
		previous := h.bars[index-horizon].Close
		if previous <= 0 {
			return nil, "insufficient_history"
		}
		values[position] = closeValue/previous - 1
	}
	volatility, ok := returnVolatility(h.bars, index, 20)
	if !ok {
		return nil, "insufficient_history"
	}
	values[3] = volatility
	previous := h.bars[index-1].Close
	if previous <= 0 {
		return nil, "insufficient_history"
	}
	values[4] = (h.bars[index].High - h.bars[index].Low) / previous
	values[5] = h.bars[index].Open/previous - 1
	span := h.bars[index].High - h.bars[index].Low
	if span <= 0 {
		values[6] = 0.5
	} else {
		values[6] = (closeValue - h.bars[index].Low) / span
	}
	for _, value := range values {
		if !finite(value) {
			return nil, "insufficient_history"
		}
	}
	return values, ""
}
