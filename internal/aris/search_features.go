package aris

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
)

// FeatureTable contains signal-time observations only. Missing observations are
// absent, never zero-filled. Keys include the code and signal date.
type FeatureTable map[string]map[string]float64

type Predicate struct {
	Feature   string  `json:"feature"`
	Op        string  `json:"op"`
	Threshold float64 `json:"threshold"`
}

func signalKey(s maexec.Signal) string { return s.Code + "/" + s.Date }
func featureNames() []string {
	names := []string{"upper_wick", "close_position", "range_pct"}
	for _, w := range []int{5, 20, 60} {
		for _, name := range []string{"return", "volatility", "volume_ratio", "ma_distance", "ma_slope"} {
			names = append(names, fmt.Sprintf("%s_%d", name, w))
		}
	}
	sort.Strings(names)
	return names
}
func (p Predicate) valid() bool {
	names := featureNames()
	i := sort.SearchStrings(names, p.Feature)
	return i < len(names) && names[i] == p.Feature && (p.Op == "le" || p.Op == "ge") && finite(p.Threshold)
}
func matches(values map[string]float64, terms []Predicate) bool {
	for _, p := range terms {
		v, ok := values[p.Feature]
		if !ok || !finite(v) || !p.valid() {
			return false
		}
		if (p.Op == "le" && v > p.Threshold) || (p.Op == "ge" && v < p.Threshold) {
			return false
		}
	}
	return true
}

// SearchFeatures indexes only qfq observations through each signal date. The
// snapshot was validated by the bundle decoder; raw-price rows are not features.
func SearchFeatures(ctx context.Context, s maexec.Snapshot, signals []maexec.Signal) (FeatureTable, error) {
	out := FeatureTable{}
	byCode := map[string][]maexec.Signal{}
	for _, sig := range signals {
		byCode[sig.Code] = append(byCode[sig.Code], sig)
	}
	for code, sigs := range byCode {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bars := []maexec.Price{}
		for _, p := range s.Stocks[code] {
			if p.Adjust == "qfq" {
				bars = append(bars, p)
			}
		}
		sort.Slice(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
		for _, sig := range sigs {
			i := sort.Search(len(bars), func(i int) bool { return bars[i].Date >= sig.Date })
			values := map[string]float64{}
			out[signalKey(sig)] = values
			if i == len(bars) || bars[i].Date != sig.Date {
				continue
			}
			p := bars[i]
			c, h, l, o := float64(p.Close), float64(p.High), float64(p.Low), float64(p.Open)
			if c <= 0 {
				continue
			}
			values["range_pct"] = (h - l) / c
			if h > l {
				values["upper_wick"] = (h - math.Max(o, c)) / (h - l)
				values["close_position"] = (c - l) / (h - l)
			}
			for _, w := range []int{5, 20, 60} {
				if i < w || bars[i-w].Close <= 0 {
					continue
				}
				key := func(name string) string { return fmt.Sprintf("%s_%d", name, w) }
				values[key("return")] = c/float64(bars[i-w].Close) - 1
				sum, prior, volume, mean, square := 0.0, 0.0, 0.0, 0.0, 0.0
				for j := i - w + 1; j <= i; j++ {
					sum += float64(bars[j].Close)
					prior += float64(bars[j-1].Close)
					volume += bars[j-1].Volume
					r := float64(bars[j].Close)/float64(bars[j-1].Close) - 1
					mean += r
					square += r * r
				}
				if sum > 0 && prior > 0 {
					values[key("ma_distance")] = c/(sum/float64(w)) - 1
					values[key("ma_slope")] = sum/prior - 1
				}
				if volume > 0 {
					values[key("volume_ratio")] = p.Volume / (volume / float64(w))
				}
				mean /= float64(w)
				values[key("volatility")] = math.Sqrt(math.Max(0, square/float64(w)-mean*mean))
			}
			for name, v := range values {
				if !finite(v) {
					delete(values, name)
				}
			}
		}
	}
	return out, nil
}

// The complete search grammar and training-only quantiles are frozen before any
// candidate is tested. Validation observations cannot alter this pool.
func searchPool(table FeatureTable, training []maexec.Signal) []Predicate {
	pool := []Predicate{}
	seen := map[string]bool{}
	for _, name := range featureNames() {
		values := []float64{}
		for _, sig := range training {
			if v, ok := table[signalKey(sig)][name]; ok && finite(v) {
				values = append(values, v)
			}
		}
		sort.Float64s(values)
		if len(values) == 0 {
			continue
		}
		for _, q := range []float64{.25, .5, .75} {
			v := values[int(float64(len(values)-1)*q)]
			for _, op := range []string{"le", "ge"} {
				p := Predicate{name, op, v}
				id := Digest(p)
				if !seen[id] {
					seen[id] = true
					pool = append(pool, p)
				}
			}
		}
	}
	return pool
}
func filterSignals(signals []maexec.Signal, table FeatureTable, terms []Predicate) []maexec.Signal {
	selected := []maexec.Signal{}
	for _, sig := range signals {
		if matches(table[signalKey(sig)], terms) {
			selected = append(selected, sig)
		}
	}
	return selected
}
