package trappedscan

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func loadMetricCache(path string) (map[string]market.DailyMetricHistory, error) {
	cache := map[string]market.DailyMetricHistory{}
	if path == "" {
		return cache, nil
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cache, nil
		}
		return nil, errors.New("cannot read turnover metric cache")
	}
	if err := json.Unmarshal(payload, &cache); err != nil {
		return nil, errors.New("cannot decode turnover metric cache")
	}
	for code, history := range cache {
		if history.Code != code || history.Validate() != nil {
			delete(cache, code)
		}
	}
	return cache, nil
}

func writeMetricCacheLocked(path string, histories map[string]market.DailyMetricHistory) error {
	payload, err := json.Marshal(histories)
	if err != nil {
		return errors.New("cannot encode turnover metric cache")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("cannot create turnover metric cache directory")
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, payload, 0o600); err != nil {
		return errors.New("cannot write turnover metric cache")
	}
	if err := os.Rename(temporary, path); err != nil {
		return errors.New("cannot replace turnover metric cache")
	}
	return nil
}

func metricsCover(history market.DailyMetricHistory, required []string) bool {
	if len(required) == 0 || history.Validate() != nil {
		return false
	}
	byDate := make(map[string]float64, len(history.Metrics))
	for _, metric := range history.Metrics {
		byDate[metric.Date] = metric.TurnoverRatePct
	}
	for _, date := range required {
		rate, ok := byDate[date]
		if !ok || rate <= 0 {
			return false
		}
	}
	return true
}
