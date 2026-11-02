// Package hkrecommend defines the durable Stock Connect recommendation
// contract shared by the recommendation builder, storage and article renderer.
// It must stay free of storage or delivery dependencies to avoid import cycles.
package hkrecommend

import (
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/articlechart"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/indicators"
)

const (
	SchemaVersion = 1
	Market        = "hk_connect"
)

type RecommendationCandidate struct {
	Rank      int                `json:"rank"`
	Code      string             `json:"code"`
	Name      string             `json:"name"`
	Close     float64            `json:"close"`
	ChangePct float64            `json:"change_pct"`
	Metrics   indicators.Metrics `json:"metrics"`
	Chart     *articlechart.Spec `json:"chart,omitempty"`
}

type RecommendationReason struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// Recommendation is the durable, deterministic Stock Connect rule report. It
// intentionally does not reuse A-share first-stage or second-stage artifacts.
type Recommendation struct {
	Version       int                       `json:"schema_version"`
	ID            string                    `json:"id"`
	Market        string                    `json:"market"`
	AsOf          string                    `json:"as_of"`
	Target        string                    `json:"target"`
	GeneratedAt   time.Time                 `json:"generated_at"`
	UniverseTotal int                       `json:"universe_total"`
	Eligible      int                       `json:"eligible"`
	Passed        int                       `json:"passed"`
	Selected      int                       `json:"selected"`
	TopN          int                       `json:"top_n"`
	MinScore      int                       `json:"min_score"`
	Rules         []string                  `json:"rules"`
	Candidates    []RecommendationCandidate `json:"candidates"`
	Filtered      []RecommendationReason    `json:"filtered,omitempty"`
	Limitations   []string                  `json:"limitations"`
}
