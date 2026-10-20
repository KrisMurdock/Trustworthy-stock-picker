package strategy

import "math"

const (
	ContinuationVersion    = 1
	ContinuationLabel      = "next_close_above_signal_close"
	ContinuationMinSamples = 30
	ContinuationMinDates   = 30
)

// ContinuationStats is a conservative, rule-level observation of whether an
// up day is followed by a higher next-trading-day close. It is intentionally
// separate from the opportunity score until its incremental value is validated.
type ContinuationStats struct {
	Version    int     `json:"schema_version"`
	Label      string  `json:"label"`
	Rule       string  `json:"rule,omitempty"`
	Samples    int     `json:"samples"`
	Dates      int     `json:"signal_dates"`
	Hits       int     `json:"hits"`
	Rate       float64 `json:"rate"`
	LowerBound float64 `json:"lower_bound"`
	Score      float64 `json:"score"`
	Status     string  `json:"status"`
}

// NextDay records the raw signal-day close to next-trading-day close move.
// Corporate-action windows and missing sessions leave it unset.
type NextDay struct {
	Date     string
	ClosePct float64
	Higher   bool
}

func newContinuationStats() *ContinuationStats {
	return &ContinuationStats{Version: ContinuationVersion, Label: ContinuationLabel, Status: "insufficient"}
}

func finalizeContinuation(c *ContinuationStats) {
	if c == nil {
		return
	}
	if c.Version == 0 {
		c.Version = ContinuationVersion
	}
	if c.Label == "" {
		c.Label = ContinuationLabel
	}
	if c.Samples > 0 {
		c.Rate = float64(c.Hits) / float64(c.Samples)
		c.LowerBound = wilsonLower(c.Rate, max(1, min(c.Samples, c.Dates)))
		c.Score = c.LowerBound * 100
	}
	if c.Samples >= ContinuationMinSamples && c.Dates >= ContinuationMinDates {
		c.Status = "ready"
	} else {
		c.Status = "insufficient"
	}
}

func wilsonLower(p float64, n int) float64 {
	if n <= 0 {
		return 0
	}
	z := 1.96
	fn := float64(n)
	return (p + z*z/(2*fn) - z*math.Sqrt(p*(1-p)/fn+z*z/(4*fn*fn))) / (1 + z*z/fn)
}
