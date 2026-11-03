// Package lowvaluation builds an independent full-market valuation screen.
// Candidates must trade below the conservative lower bound of the existing
// TTM EPS/book-value research range. The ranking is intentionally based on the
// distance to that lower bound, not on a return forecast or trading signal.
package lowvaluation

import (
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
)

const (
	SchemaVersion = 1
	Market        = "low_valuation"
)

// Quote is the unadjusted latest close used by the financial valuation model.
type Quote struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	PriceDate string  `json:"price_date"`
	Close     float64 `json:"close"`
	Volume    float64 `json:"volume_shares"`
}

type Candidate struct {
	Rank               int                `json:"rank"`
	Code               string             `json:"code"`
	Name               string             `json:"name"`
	Sector             string             `json:"sector,omitempty"`
	PriceDate          string             `json:"price_date"`
	Close              float64            `json:"close"`
	GapToLowerPct      float64            `json:"gap_to_lower_pct"`
	UpsideToCentralPct float64            `json:"upside_to_central_pct"`
	Estimate           fairvalue.Estimate `json:"estimate"`
}

type Filtered struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type Recommendation struct {
	Version           int         `json:"schema_version"`
	ID                string      `json:"id"`
	Market            string      `json:"market"`
	AsOf              string      `json:"as_of"`
	Target            string      `json:"target"`
	GeneratedAt       time.Time   `json:"generated_at"`
	UniverseTotal     int         `json:"universe_total"`
	Priced            int         `json:"priced"`
	Valued            int         `json:"valued"`
	Eligible          int         `json:"eligible"`
	Selected          int         `json:"selected"`
	TopN              int         `json:"top_n"`
	SectorCoveragePct float64     `json:"sector_coverage_pct"`
	Candidates        []Candidate `json:"candidates"`
	Filtered          []Filtered  `json:"filtered,omitempty"`
	Limitations       []string    `json:"limitations"`
}
