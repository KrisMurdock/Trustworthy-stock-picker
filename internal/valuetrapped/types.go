// Package valuetrapped builds the independent intersection of the full-market
// low-valuation screen and the turnover-adjusted trapped-supply diagnostic.
// It is a research observation, not a trading recommendation.
package valuetrapped

import (
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const (
	SchemaVersion = 1
	Market        = "value_trapped"

	DefaultTopN               = 20
	DefaultMaxTrappedRatio    = 0.30
	DefaultMinHistorySessions = 250
	DefaultMinAmountCNY       = 20_000_000
)

type Candidate struct {
	Rank               int                     `json:"rank"`
	Code               string                  `json:"code"`
	Name               string                  `json:"name"`
	Sector             string                  `json:"sector,omitempty"`
	PriceDate          string                  `json:"price_date"`
	Close              float64                 `json:"close"`
	GapToLowerPct      float64                 `json:"gap_to_lower_pct"`
	UpsideToCentralPct float64                 `json:"upside_to_central_pct"`
	Estimate           fairvalue.Estimate      `json:"estimate"`
	AvgAmount20CNY     float64                 `json:"avg_amount_20d_cny"`
	Trapped            *strategy.TrappedSignal `json:"trapped_signal"`
}

type Filtered struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type Recommendation struct {
	Version               int            `json:"schema_version"`
	ID                    string         `json:"id"`
	Market                string         `json:"market"`
	AsOf                  string         `json:"as_of"`
	Target                string         `json:"target"`
	GeneratedAt           time.Time      `json:"generated_at"`
	UniverseTotal         int            `json:"universe_total"`
	Priced                int            `json:"priced"`
	Valued                int            `json:"valued"`
	LowValuationEligible  int            `json:"low_valuation_eligible"`
	TrappedValid          int            `json:"trapped_valid"`
	Eligible              int            `json:"eligible"`
	Selected              int            `json:"selected"`
	TopN                  int            `json:"top_n"`
	MaxTrappedRatioPct    float64        `json:"max_trapped_ratio_pct"`
	MinHistorySessions    int            `json:"min_history_sessions"`
	MinAmountCNY          float64        `json:"min_amount_cny"`
	SectorCoveragePct     float64        `json:"sector_coverage_pct"`
	Candidates            []Candidate    `json:"candidates"`
	Filtered              []Filtered     `json:"filtered,omitempty"`
	TrappedUnknownReasons map[string]int `json:"trapped_unknown_reasons,omitempty"`
	Limitations           []string       `json:"limitations"`
}
