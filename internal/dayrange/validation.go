package dayrange

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
)

var shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

func finite(v float64) bool         { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func positive(v float64) bool       { return finite(v) && v > 0 }
func validExtremes(x Extremes) bool { return positive(x.High) && positive(x.Low) && x.High >= x.Low }

func validateObservation(s Observation, opts Options) error {
	if strings.TrimSpace(s.Code) == "" {
		return fmt.Errorf("stock code is required")
	}
	date, err := time.Parse(time.DateOnly, s.Date)
	if err != nil || date.Format(time.DateOnly) != s.Date {
		return fmt.Errorf("invalid date")
	}
	at := s.AsOf.In(shanghai)
	if s.AsOf.IsZero() || at.Format(time.DateOnly) != s.Date || at.Second() != 0 || at.Nanosecond() != 0 {
		return fmt.Errorf("as_of must be a minute boundary on the target date")
	}
	minute := at.Hour()*60 + at.Minute()
	switch s.Mode {
	case ModePremarket:
		if minute >= 570 {
			return fmt.Errorf("premarket as_of must precede 09:30")
		}
	case ModeRemaining, ModeFullDay:
		if minute%5 != 0 || !((minute >= 575 && minute <= 690) || (minute >= 785 && minute < 900)) {
			return fmt.Errorf("intraday as_of must be a finalized five-minute boundary before close")
		}
	default:
		return fmt.Errorf("unsupported forecast mode")
	}
	if !positive(s.ReferencePrice) || !finite(s.ScalePrice) || s.ScalePrice < 0 {
		return fmt.Errorf("invalid reference or volatility scale")
	}
	if err := validateForecast(s.Forecast); err != nil {
		return err
	}
	if s.Actual == nil {
		if strings.TrimSpace(s.UnresolvedReason) == "" {
			return fmt.Errorf("unresolved outcome requires a reason")
		}
	} else {
		if !validExtremes(*s.Actual) || s.UnresolvedReason != "" {
			return fmt.Errorf("invalid resolved outcome")
		}
	}
	if s.Baseline != nil && (!validExtremes(*s.Baseline) || strings.TrimSpace(opts.Baseline) == "") {
		return fmt.Errorf("baseline requires valid extremes and a name")
	}
	seen := map[string]bool{}
	for _, d := range s.Slices {
		switch d.Name {
		case "mode", "as_of", "month", "coverage", "interval_method", "quantile", "normalization":
			return fmt.Errorf("reserved slice dimension %q", d.Name)
		}
		if strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.Value) == "" || seen[d.Name] {
			return fmt.Errorf("invalid or duplicate slice dimension")
		}
		seen[d.Name] = true
	}
	return nil
}

func validateForecast(f Forecast) error {
	for _, e := range []Estimate{f.High, f.Low} {
		if !positive(e.Point) {
			return fmt.Errorf("invalid point price")
		}
		lastLevel, lastPrice := 0.0, 0.0
		for _, q := range e.Quantiles {
			if !finite(q.Level) || q.Level <= lastLevel || q.Level >= 1 || !positive(q.Price) || q.Price < lastPrice {
				return fmt.Errorf("quantiles must have increasing levels and nondecreasing prices")
			}
			lastLevel, lastPrice = q.Level, q.Price
		}
		if v := e.Interval; v != nil {
			if !positive(v.Lower) || !positive(v.Upper) || v.Lower > e.Point || v.Upper < e.Point || !finite(v.NominalCoverage) || v.NominalCoverage <= 0 || v.NominalCoverage >= 1 || strings.TrimSpace(v.Method) == "" {
				return fmt.Errorf("invalid central prediction interval")
			}
		}
	}
	if f.High.Point < f.Low.Point || len(f.High.Quantiles) != len(f.Low.Quantiles) {
		return fmt.Errorf("inconsistent high/low forecast")
	}
	for i, q := range f.High.Quantiles {
		l := f.Low.Quantiles[i]
		if q.Level != l.Level || q.Price < l.Price {
			return fmt.Errorf("inconsistent high/low quantiles")
		}
	}
	h, l := f.High.Interval, f.Low.Interval
	if (h == nil) != (l == nil) {
		return fmt.Errorf("high and low intervals must both be present or absent")
	}
	if h != nil && (h.NominalCoverage != l.NominalCoverage || h.Method != l.Method || h.Lower < l.Lower || h.Upper < l.Upper) {
		return fmt.Errorf("inconsistent high/low intervals")
	}
	return nil
}

func dimensions(s Observation) [][]modeleval.SliceDimension {
	base := []modeleval.SliceDimension{{Name: "as_of", Value: s.AsOf.In(shanghai).Format("15:04")}, {Name: "mode", Value: string(s.Mode)}}
	out := [][]modeleval.SliceDimension{base, appendCopy(base, modeleval.SliceDimension{Name: "month", Value: s.Date[:7]})}
	for _, d := range s.Slices {
		out = append(out, appendCopy(base, d))
	}
	return out
}
func appendCopy(base []modeleval.SliceDimension, extra ...modeleval.SliceDimension) []modeleval.SliceDimension {
	out := append(append([]modeleval.SliceDimension(nil), base...), extra...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func dimensionKey(d []modeleval.SliceDimension) string { b, _ := json.Marshal(d); return string(b) }
func observationKey(s Observation) string {
	b, _ := json.Marshal([]string{s.Date, s.Code, string(s.Mode), s.AsOf.UTC().Format(time.RFC3339)})
	return string(b)
}

// Different output levels or calibration methods must be evaluated separately,
// rather than quietly averaging incompatible coverage or pinball metrics.
func forecastSchema(f Forecast) string {
	v := struct {
		Levels   []float64
		Coverage float64
		Method   string
	}{}
	for _, q := range f.High.Quantiles {
		v.Levels = append(v.Levels, q.Level)
	}
	if i := f.High.Interval; i != nil {
		v.Coverage = i.NominalCoverage
		v.Method = i.Method
	}
	b, _ := json.Marshal(v)
	return string(b)
}
