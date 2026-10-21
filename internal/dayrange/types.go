// Package dayrange evaluates frozen daily price-extreme forecasts. It does not
// train models, load market data, infer outcomes, or execute trades.
package dayrange

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
	"time"
)

const EvaluationSchemaVersion = 1

type Mode string

const (
	ModePremarket Mode = "premarket"
	ModeRemaining Mode = "remaining"
	ModeFullDay   Mode = "full_day"
)

type Extremes struct {
	High float64 `json:"high"`
	Low  float64 `json:"low"`
}
type Quantile struct {
	Level float64 `json:"level"`
	Price float64 `json:"price"`
}

// Interval is a central marginal prediction interval for one extreme. Its
// coverage is not the nominal joint coverage of high AND low.
type Interval struct {
	Lower           float64 `json:"lower"`
	Upper           float64 `json:"upper"`
	NominalCoverage float64 `json:"nominal_coverage"`
	Method          string  `json:"method"`
}
type Estimate struct {
	Point     float64    `json:"point"`
	Quantiles []Quantile `json:"quantiles,omitempty"`
	Interval  *Interval  `json:"interval,omitempty"`
}
type Forecast struct {
	High Estimate `json:"high"`
	Low  Estimate `json:"low"`
}

// Observation describes a single frozen forecast. Prices must share the same
// unadjusted basis. The caller must verify Actual's complete target window and
// ensure the reference, scale, forecast and slices were available at AsOf.
// Actual=nil means unresolved, not a zero return. Baseline, when present, must
// be a forecast for this exact observation, never a separately sampled average.
type Observation struct {
	Code           string    `json:"code"`
	Date           string    `json:"date"`
	Mode           Mode      `json:"mode"`
	AsOf           time.Time `json:"as_of"`
	ReferencePrice float64   `json:"reference_price"`
	// ScalePrice is an optional, strictly historical volatility scale in CNY.
	// Zero means unavailable; it is never estimated from the target session.
	ScalePrice       float64                    `json:"scale_price,omitempty"`
	Forecast         Forecast                   `json:"forecast"`
	Actual           *Extremes                  `json:"actual"`
	UnresolvedReason string                     `json:"unresolved_reason,omitempty"`
	Baseline         *Extremes                  `json:"baseline,omitempty"`
	Slices           []modeleval.SliceDimension `json:"slices,omitempty"`
}
type Options struct {
	Subject  string
	Baseline string
}
type Evidence struct {
	Slice             []modeleval.SliceDimension `json:"slice"`
	Submitted         int                        `json:"submitted"`
	Resolved          int                        `json:"resolved"`
	Unresolved        int                        `json:"unresolved"`
	UnresolvedReasons map[string]int             `json:"unresolved_reasons,omitempty"`
}
type Report struct {
	SchemaVersion int                      `json:"schema_version"`
	Subject       string                   `json:"subject"`
	Status        string                   `json:"status"`
	Submitted     int                      `json:"submitted"`
	Resolved      int                      `json:"resolved"`
	Unresolved    int                      `json:"unresolved"`
	Groups        []Evidence               `json:"groups"`
	Metrics       []modeleval.MetricRecord `json:"metrics"`
}
