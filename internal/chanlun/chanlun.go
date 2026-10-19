// Package chanlun implements a small, deterministic subset of Chan analysis:
// inclusion merging, strict fractals, strokes and central zones.
//
// It intentionally does not stream or infer unfinished bars. Callers must pass
// completed bars in chronological order, and the same input must always produce
// the same structure.
package chanlun

import (
	"errors"
	"math"
	"time"
)

const (
	Version              = 1
	DefaultMinStrokeBars = 6
)

type Mark string

const (
	Top    Mark = "top"
	Bottom Mark = "bottom"
)

type Direction string

const (
	Up   Direction = "up"
	Down Direction = "down"
)

// Bar is one completed input bar. RawBars records how many source bars were
// folded into a merged bar by inclusion processing.
type Bar struct {
	Time    time.Time `json:"time"`
	Open    float64   `json:"open"`
	High    float64   `json:"high"`
	Low     float64   `json:"low"`
	Close   float64   `json:"close"`
	Volume  float64   `json:"volume"`
	RawBars int       `json:"raw_bars"`
}

type Fractal struct {
	Mark  Mark      `json:"mark"`
	Time  time.Time `json:"time"`
	High  float64   `json:"high"`
	Low   float64   `json:"low"`
	Price float64   `json:"price"`
	Index int       `json:"index"`
	Start int       `json:"start_index"`
	End   int       `json:"end_index"`
}

type Stroke struct {
	Direction  Direction `json:"direction"`
	Start      Fractal   `json:"start"`
	End        Fractal   `json:"end"`
	High       float64   `json:"high"`
	Low        float64   `json:"low"`
	MergedBars int       `json:"merged_bars"`
	Finished   bool      `json:"finished"`
}

type Zone struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Upper   float64   `json:"upper"`
	Lower   float64   `json:"lower"`
	Mid     float64   `json:"mid"`
	High    float64   `json:"high"`
	Low     float64   `json:"low"`
	Strokes int       `json:"strokes"`
}

type Analysis struct {
	Version         int       `json:"schema_version"`
	MergedBars      []Bar     `json:"merged_bars"`
	Fractals        []Fractal `json:"fractals"`
	Strokes         []Stroke  `json:"strokes"`
	FinishedStrokes []Stroke  `json:"finished_strokes"`
	Zones           []Zone    `json:"zones"`
	LastClose       float64   `json:"last_close"`
}

// LatestZone returns the most recent valid zone built from finished strokes.
func (a Analysis) LatestZone() *Zone {
	if len(a.Zones) == 0 {
		return nil
	}
	zone := a.Zones[len(a.Zones)-1]
	return &zone
}

func (a Analysis) LastFinishedStroke() *Stroke {
	if len(a.FinishedStrokes) == 0 {
		return nil
	}
	stroke := a.FinishedStrokes[len(a.FinishedStrokes)-1]
	return &stroke
}

func (a Analysis) LastFractal() *Fractal {
	if len(a.Fractals) == 0 {
		return nil
	}
	fractal := a.Fractals[len(a.Fractals)-1]
	return &fractal
}

// Analyze builds the completed structure over bars. The final stroke is only
// marked finished when at least five merged bars remain from its ending fractal
// window; this mirrors the minimum tail needed to avoid publishing a stroke that
// is likely to be extended or invalidated by the next bars.
func Analyze(bars []Bar, minStrokeBars int) (Analysis, error) {
	out := Analysis{Version: Version}
	if minStrokeBars == 0 {
		minStrokeBars = DefaultMinStrokeBars
	}
	if minStrokeBars < 3 {
		return out, errors.New("minimum stroke length must be at least three merged bars")
	}
	if err := validate(bars); err != nil {
		return out, err
	}
	if len(bars) == 0 {
		return out, nil
	}
	out.MergedBars = mergeInclusions(bars)
	out.Fractals = detectFractals(out.MergedBars)
	out.Strokes = buildStrokes(out.Fractals, out.MergedBars, minStrokeBars)
	for i := range out.Strokes {
		out.Strokes[i].Finished = i < len(out.Strokes)-1
	}
	if n := len(out.Strokes); n > 0 {
		last := &out.Strokes[n-1]
		if len(out.MergedBars)-last.End.Start >= 5 {
			last.Finished = true
		}
	}
	for _, stroke := range out.Strokes {
		if stroke.Finished {
			out.FinishedStrokes = append(out.FinishedStrokes, stroke)
		}
	}
	out.Zones = buildZones(out.FinishedStrokes)
	out.LastClose = bars[len(bars)-1].Close
	return out, nil
}

func validate(bars []Bar) error {
	var previous time.Time
	for i, bar := range bars {
		values := []float64{bar.Open, bar.High, bar.Low, bar.Close, bar.Volume}
		for _, value := range values {
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return errors.New("chanlun bars contain an invalid numeric value")
			}
		}
		if bar.Low <= 0 || bar.High < math.Max(bar.Open, bar.Close) || bar.Low > math.Min(bar.Open, bar.Close) {
			return errors.New("chanlun bar has invalid OHLC")
		}
		if !previous.IsZero() && !bar.Time.After(previous) {
			return errors.New("chanlun bars must be strictly ordered")
		}
		if i == 0 || bar.Time.After(previous) {
			previous = bar.Time
		}
	}
	return nil
}

func mergeInclusions(bars []Bar) []Bar {
	out := make([]Bar, 0, len(bars))
	for _, source := range bars {
		bar := source
		if bar.RawBars <= 0 {
			bar.RawBars = 1
		}
		if len(out) < 2 {
			out = append(out, bar)
			continue
		}
		previous, last := out[len(out)-2], out[len(out)-1]
		direction := 0
		switch {
		case previous.High < last.High:
			direction = 1
		case previous.High > last.High:
			direction = -1
		default:
			out = append(out, bar)
			continue
		}
		included := (last.High <= bar.High && last.Low >= bar.Low) || (last.High >= bar.High && last.Low <= bar.Low)
		if !included {
			out = append(out, bar)
			continue
		}
		merged := last
		merged.RawBars += bar.RawBars
		merged.Volume += bar.Volume
		merged.Close = bar.Close
		if direction > 0 {
			merged.High = math.Max(last.High, bar.High)
			merged.Low = math.Max(last.Low, bar.Low)
			if bar.High > last.High {
				merged.Time = bar.Time
			}
		} else {
			merged.High = math.Min(last.High, bar.High)
			merged.Low = math.Min(last.Low, bar.Low)
			if bar.Low < last.Low {
				merged.Time = bar.Time
			}
		}
		out[len(out)-1] = merged
	}
	return out
}

func detectFractals(bars []Bar) []Fractal {
	out := []Fractal{}
	for i := 1; i+1 < len(bars); i++ {
		left, middle, right := bars[i-1], bars[i], bars[i+1]
		var fractal Fractal
		switch {
		case left.High < middle.High && middle.High > right.High && left.Low < middle.Low && middle.Low > right.Low:
			fractal = Fractal{Mark: Top, Time: middle.Time, High: middle.High, Low: middle.Low, Price: middle.High}
		case left.Low > middle.Low && middle.Low < right.Low && left.High > middle.High && middle.High < right.High:
			fractal = Fractal{Mark: Bottom, Time: middle.Time, High: middle.High, Low: middle.Low, Price: middle.Low}
		default:
			continue
		}
		if len(out) > 0 && out[len(out)-1].Mark == fractal.Mark {
			continue
		}
		fractal.Index, fractal.Start, fractal.End = i, i-1, i+1
		out = append(out, fractal)
	}
	return out
}

func buildStrokes(fractals []Fractal, bars []Bar, minBars int) []Stroke {
	out := []Stroke{}
	start := 0
	for {
		candidates := make([]Fractal, 0, len(fractals))
		for _, fractal := range fractals {
			if fractal.Start >= start {
				candidates = append(candidates, fractal)
			}
		}
		if len(candidates) < 2 {
			break
		}
		first := candidates[0]
		end, ok := selectStrokeEnd(first, candidates[1:])
		if !ok {
			break
		}
		span := end.End - first.Start + 1
		if fractalContains(first, end) || span < minBars {
			break
		}
		stroke := Stroke{Direction: Up, Start: first, End: end}
		if first.Mark == Top {
			stroke.Direction = Down
		}
		stroke.High, stroke.Low = bars[first.Start].High, bars[first.Start].Low
		for i := first.Start + 1; i <= end.End; i++ {
			stroke.High = math.Max(stroke.High, bars[i].High)
			stroke.Low = math.Min(stroke.Low, bars[i].Low)
		}
		stroke.MergedBars = span
		out = append(out, stroke)
		start = end.Start
	}
	return out
}

func selectStrokeEnd(first Fractal, candidates []Fractal) (Fractal, bool) {
	var selected Fractal
	found := false
	for _, candidate := range candidates {
		if candidate.Mark == first.Mark || candidate.Index <= first.Index {
			continue
		}
		if first.Mark == Bottom && candidate.Price <= first.Price {
			continue
		}
		if first.Mark == Top && candidate.Price >= first.Price {
			continue
		}
		if !found {
			selected, found = candidate, true
			continue
		}
		if first.Mark == Bottom && candidate.High > selected.High {
			selected = candidate
		}
		if first.Mark == Top && candidate.Low < selected.Low {
			selected = candidate
		}
	}
	return selected, found
}

func fractalContains(a, b Fractal) bool {
	return (a.High > b.High && a.Low < b.Low) || (a.High < b.High && a.Low > b.Low)
}

func buildZones(strokes []Stroke) []Zone {
	groups := [][]Stroke{}
	for _, stroke := range strokes {
		if len(groups) == 0 {
			groups = append(groups, []Stroke{stroke})
			continue
		}
		last := &groups[len(groups)-1]
		zone := newZone(*last)
		if (stroke.Direction == Up && stroke.High < zone.Lower) || (stroke.Direction == Down && stroke.Low > zone.Upper) {
			groups = append(groups, []Stroke{stroke})
			continue
		}
		*last = append(*last, stroke)
	}
	out := []Zone{}
	for _, group := range groups {
		zone := newZone(group)
		if len(group) >= 3 && zone.Upper >= zone.Lower {
			out = append(out, zone)
		}
	}
	return out
}

func newZone(strokes []Stroke) Zone {
	n := len(strokes)
	if n > 3 {
		n = 3
	}
	zone := Zone{Strokes: len(strokes), Upper: math.Inf(1), Lower: math.Inf(-1), High: math.Inf(-1), Low: math.Inf(1)}
	if len(strokes) > 0 {
		zone.From = strokes[0].Start.Time
		zone.To = strokes[len(strokes)-1].End.Time
	}
	for i, stroke := range strokes {
		zone.High = math.Max(zone.High, stroke.High)
		zone.Low = math.Min(zone.Low, stroke.Low)
		if i < n {
			zone.Upper = math.Min(zone.Upper, stroke.High)
			zone.Lower = math.Max(zone.Lower, stroke.Low)
		}
	}
	zone.Mid = zone.Lower + (zone.Upper-zone.Lower)/2
	return zone
}
