package experiment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"stock-watcher/internal/market"
)

// WithMinutes freezes the actual paths in the digest; old snapshots with no
// minute fields retain their exact JSON encoding and hash.
func WithMinutes(s Snapshot, histories []market.MinuteHistory) (Snapshot, error) {
	allowed := map[string]bool{}
	for _, h := range s.Raw {
		allowed[h.Code] = true
	}
	s.Minutes = nil
	s.MinuteFrom = ""
	s.MinuteThrough = ""
	s.MinuteStocks = 0
	seen := map[string]bool{}
	for _, h := range histories {
		if !allowed[h.Code] {
			continue
		}
		if seen[h.Code] {
			return s, errors.New("duplicate minute stock")
		}
		seen[h.Code] = true
		if err := h.Validate(); err != nil {
			return s, err
		}
		bars := []market.MinuteBar{}
		for _, b := range h.Bars {
			date := b.End.In(market.Shanghai).Format("2006-01-02")
			if date > s.AsOf {
				continue
			}
			bars = append(bars, b)
			if s.MinuteFrom == "" || date < s.MinuteFrom {
				s.MinuteFrom = date
			}
			if date > s.MinuteThrough {
				s.MinuteThrough = date
			}
		}
		if len(bars) == 0 {
			continue
		}
		h.Bars = bars
		s.Minutes = append(s.Minutes, h)
	}
	sort.Slice(s.Minutes, func(i, j int) bool { return s.Minutes[i].Code < s.Minutes[j].Code })
	s.MinuteStocks = len(s.Minutes)
	s.ID = ""
	data, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	hash := sha256.Sum256(data)
	s.ID = hex.EncodeToString(hash[:])
	return s, nil
}
