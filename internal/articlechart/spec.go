// Package articlechart defines the deterministic chart attached to article
// cards and renders it to PNG without external assets or a browser.
package articlechart

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"
)

const (
	Version = 1
	// MaxBars bounds the persisted and uploaded chart payload. The article
	// only needs a recent structure window, not the full analysis history.
	MaxBars = 120
	// MaxPNGBytes matches the WeChat media/uploadimg limit.
	MaxPNGBytes = 1 << 20
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Spec is a compact, serialisable chart description. It contains no image
// bytes, so a delivery ledger can retain it and re-render after a restart.
type Spec struct {
	Version             int      `json:"schema_version"`
	ID                  string   `json:"id"`
	Code                string   `json:"code"`
	AsOf                string   `json:"as_of"`
	Adjust              string   `json:"adjust,omitempty"`
	Caption             string   `json:"caption,omitempty"`
	Bars                []Bar    `json:"bars"`
	Strokes             []Stroke `json:"strokes,omitempty"`
	Fractals            []Point  `json:"fractals,omitempty"`
	Zone                *Zone    `json:"zone,omitempty"`
	Levels              []Level  `json:"levels,omitempty"`
	Stage               string   `json:"stage,omitempty"`
	RetracementPct      float64  `json:"retracement_pct,omitempty"`
	RetracementExceeded bool     `json:"retracement_exceeded,omitempty"`
	CompletedStrokes    int      `json:"completed_strokes,omitempty"`
}

type Bar struct {
	Date  string  `json:"date"`
	Open  float64 `json:"open"`
	High  float64 `json:"high"`
	Low   float64 `json:"low"`
	Close float64 `json:"close"`
}

type Stroke struct {
	FromDate  string  `json:"from_date"`
	ToDate    string  `json:"to_date"`
	From      float64 `json:"from"`
	To        float64 `json:"to"`
	Direction string  `json:"direction,omitempty"`
	Finished  bool    `json:"finished"`
}

type Point struct {
	Date  string  `json:"date"`
	Price float64 `json:"price"`
	Mark  string  `json:"mark,omitempty"`
}

type Zone struct {
	FromDate string  `json:"from_date,omitempty"`
	ToDate   string  `json:"to_date,omitempty"`
	Upper    float64 `json:"upper"`
	Lower    float64 `json:"lower"`
}

type Level struct {
	Kind  string  `json:"kind"`
	Label string  `json:"label"`
	Price float64 `json:"price"`
}

// Validate rejects malformed chart payloads before they can reach WeChat.
func (s Spec) Validate() error {
	if s.Version != Version {
		return errors.New("unsupported chart schema version")
	}
	if !idPattern.MatchString(s.ID) {
		return errors.New("chart id is invalid")
	}
	if s.Code == "" {
		return errors.New("chart stock code is required")
	}
	if _, err := time.Parse("2006-01-02", s.AsOf); err != nil {
		return errors.New("chart as-of date is invalid")
	}
	if len(s.Bars) < 2 || len(s.Bars) > MaxBars {
		return fmt.Errorf("chart requires 2..%d bars", MaxBars)
	}
	previous := ""
	for _, bar := range s.Bars {
		if _, err := time.Parse("2006-01-02", bar.Date); err != nil {
			return errors.New("chart bar date is invalid")
		}
		if previous != "" && bar.Date <= previous {
			return errors.New("chart bars must be strictly ordered")
		}
		previous = bar.Date
		if !validPrice(bar.Open) || !validPrice(bar.High) || !validPrice(bar.Low) || !validPrice(bar.Close) {
			return errors.New("chart bar contains an invalid price")
		}
		if bar.High < math.Max(bar.Open, bar.Close) || bar.Low > math.Min(bar.Open, bar.Close) {
			return errors.New("chart bar has invalid OHLC")
		}
	}
	if s.Zone != nil {
		if !validPrice(s.Zone.Upper) || !validPrice(s.Zone.Lower) || s.Zone.Upper < s.Zone.Lower {
			return errors.New("chart zone is invalid")
		}
	}
	for _, stroke := range s.Strokes {
		if !validPrice(stroke.From) || !validPrice(stroke.To) {
			return errors.New("chart stroke contains an invalid price")
		}
	}
	for _, point := range s.Fractals {
		if !validPrice(point.Price) {
			return errors.New("chart fractal contains an invalid price")
		}
	}
	for _, level := range s.Levels {
		if level.Label == "" || !validPrice(level.Price) {
			return errors.New("chart level is invalid")
		}
	}
	if math.IsNaN(s.RetracementPct) || math.IsInf(s.RetracementPct, 0) || s.RetracementPct < 0 {
		return errors.New("chart retracement is invalid")
	}
	if s.RetracementExceeded && s.RetracementPct != 100 {
		return errors.New("chart retracement breakdown must be normalized to 100")
	}
	return nil
}

func validPrice(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
}
