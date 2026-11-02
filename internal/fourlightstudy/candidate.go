package fourlightstudy

import (
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	moderateVolumeGroup = "first_moderate_volume"
	highVolumeGroup     = "first_high_volume"
)

var boardNames = []string{"star", "chinext", "sh_main", "sz_main"}

type PairSourceAudit struct {
	Board          string `json:"board"`
	AdjustedSource string `json:"adjusted_source"`
	RawSource      string `json:"raw_source"`
	Stocks         int    `json:"stocks"`
}

type CandidateRetention struct {
	Group                string   `json:"group"`
	Reference            string   `json:"reference"`
	Days                 int      `json:"days"`
	SelectedFraction     *float64 `json:"selected_fraction"`
	KnownFraction        *float64 `json:"known_fraction"`
	EffectiveCoverage    *float64 `json:"effective_coverage"`
	EntryUnknownFraction *float64 `json:"entry_unknown_fraction"`
}

func boardForCode(code string) string {
	switch {
	case strings.HasPrefix(code, "sh68"):
		return "star"
	case strings.HasPrefix(code, "sz30"):
		return "chinext"
	case strings.HasPrefix(code, "sh"):
		return "sh_main"
	default:
		return "sz_main"
	}
}

func recordPairSource(rows map[string]*PairSourceAudit, board string, pair Pair) {
	key := board + "/" + pair.AdjustedSource + "/" + pair.RawSource
	if rows[key] == nil {
		rows[key] = &PairSourceAudit{Board: board, AdjustedSource: pair.AdjustedSource, RawSource: pair.RawSource}
	}
	rows[key].Stocks++
}

func sortedPairSources(rows map[string]*PairSourceAudit) []PairSourceAudit {
	keys := make([]string, 0, len(rows))
	for key := range rows {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]PairSourceAudit, 0, len(keys))
	for _, key := range keys {
		result = append(result, *rows[key])
	}
	return result
}

// Signal arithmetic uses the untouched source-volume history. Only the
// evaluator's copies use normalized shares, so unit conversion cannot flip a
// four-light threshold through floating-point rounding.
func normalizedPair(pair Pair) ([]market.Bar, []market.Bar, error) {
	if len(pair.AdjustedShares) != len(pair.Adjusted) || len(pair.RawShares) != len(pair.Raw) {
		return nil, nil, errors.New("v2 source must provide parallel normalized share volumes")
	}
	copyBars := func(bars []market.Bar, shares []float64) []market.Bar {
		result := append([]market.Bar(nil), bars...)
		for i := range result {
			result[i].Volume = shares[i]
		}
		return result
	}
	return copyBars(pair.Adjusted, pair.AdjustedShares), copyBars(pair.Raw, pair.RawShares), nil
}

func signalVolumeRatio(bars []market.Bar) *float64 {
	if len(bars) < 6 {
		return nil
	}
	sum := 0.0
	for _, bar := range bars[len(bars)-6 : len(bars)-1] {
		sum += bar.Volume
	}
	if sum <= 0 {
		return nil
	}
	ratio := bars[len(bars)-1].Volume / (sum / 5)
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio <= 0 {
		return nil
	}
	return &ratio
}

func classifyVolume(groups []string, ratio *float64) []string {
	if ratio == nil {
		return groups
	}
	for _, name := range groups {
		if name == "first_all_red" {
			if *ratio >= 1.2 && *ratio < 2 {
				return append(groups, moderateVolumeGroup)
			}
			if *ratio >= 2 {
				return append(groups, highVolumeGroup)
			}
			break
		}
	}
	return groups
}

func candidateRetentions(rows []GroupStats) []CandidateRetention {
	var base GroupStats
	for _, row := range rows {
		if row.Name == "first_all_red" {
			base = row
		}
	}
	if base.Wave == nil {
		return nil
	}
	result := []CandidateRetention{}
	ratio := func(n, d int) *float64 {
		if d == 0 {
			return nil
		}
		value := float64(n) / float64(d)
		return &value
	}
	for _, row := range rows {
		if row.Name != moderateVolumeGroup && row.Name != highVolumeGroup {
			continue
		}
		for i, w := range row.Wave.Windows {
			b := base.Wave.Windows[i]
			result = append(result, CandidateRetention{Group: row.Name, Reference: base.Name, Days: w.Days,
				SelectedFraction: ratio(w.Selected, b.Selected), KnownFraction: ratio(w.Known, b.Known),
				EffectiveCoverage: ratio(w.Known, w.Selected), EntryUnknownFraction: ratio(w.EntryUnknown, w.Selected)})
		}
	}
	return result
}
