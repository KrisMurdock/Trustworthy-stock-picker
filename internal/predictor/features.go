package predictor

import (
	"errors"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

// Data is the immutable local snapshot used by training and inference. Minutes
// contain only native five-minute bars; Daily contains qfq daily bars.
type Data struct {
	Stocks  []config.Stock
	Minutes map[string][]market.MinuteBar
	Daily   map[string][]market.Bar
}

// FeatureNames returns the stable feature order. Changing this list requires a
// FeatureVersion bump and a fresh model artifact.
func FeatureNames() []string {
	return featureNames
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
}

func BuildTrainingSamples(data Data) ([]Sample, error) {
	return nil, errors.New("predictor feature builder is not implemented")
}

func BuildInferenceFeature(data Data, code string, inputEnd time.Time) (FeatureVector, error) {
	return FeatureVector{}, errors.New("predictor inference features are not implemented")
}
