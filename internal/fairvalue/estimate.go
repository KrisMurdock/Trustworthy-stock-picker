// Package fairvalue converts a point-in-time fundamental snapshot into a
// conservative research valuation band. It is not a target price, a DCF, or a
// trading recommendation.
package fairvalue

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	Method  = "ttm_eps_pb_relative_v1"
	Version = 1
)

type FundamentalSource interface {
	FetchFundamentals(context.Context, config.Stock) (market.FundamentalSnapshot, error)
}

// Estimate is the article-facing valuation result. A zero Lower bound means the
// snapshot was insufficient; Reason then explains why without fabricating a
// range. All percentage fields use percentage points (12.5 means 12.5%).
type Estimate struct {
	Version           int       `json:"schema_version"`
	Code              string    `json:"code"`
	AsOf              string    `json:"as_of"`
	Source            string    `json:"source"`
	FetchedAt         time.Time `json:"fetched_at"`
	Method            string    `json:"method"`
	StatementBasis    string    `json:"statement_basis"`
	Confidence        string    `json:"confidence,omitempty"`
	Reason            string    `json:"reason,omitempty"`
	Warnings          []string  `json:"warnings,omitempty"`
	CurrentPrice      float64   `json:"current_price,omitempty"`
	EPS               float64   `json:"eps_ttm,omitempty"`
	BookValuePerShare float64   `json:"book_value_per_share,omitempty"`
	GrowthPct         float64   `json:"eps_growth_pct,omitempty"`
	GrowthKnown       bool      `json:"growth_known"`
	ROEPct            float64   `json:"roe_pct,omitempty"`
	PE                float64   `json:"current_pe,omitempty"`
	PB                float64   `json:"current_pb,omitempty"`
	FairPE            float64   `json:"fair_pe,omitempty"`
	FairPB            float64   `json:"fair_pb,omitempty"`
	Lower             float64   `json:"lower,omitempty"`
	Central           float64   `json:"central,omitempty"`
	Upper             float64   `json:"upper,omitempty"`
	UpsidePct         float64   `json:"upside_pct,omitempty"`
}

func (e Estimate) Available() bool {
	return e.Lower > 0 && e.Central >= e.Lower && e.Upper >= e.Central
}

type Estimator struct {
	Source FundamentalSource
	Now    func() time.Time
}

func NewEstimator(source FundamentalSource) *Estimator {
	return &Estimator{Source: source, Now: time.Now}
}

// Estimate fetches one fundamental snapshot and calculates a band using
// trailing EPS and book value. Price must be the unadjusted signal-date close.
func (e *Estimator) Estimate(ctx context.Context, stock config.Stock, price float64, asOf string) (Estimate, error) {
	out := Estimate{Version: Version, Code: stock.Code, AsOf: asOf, Source: "sina", Method: Method}
	if e == nil || e.Source == nil {
		out.Reason = "财务估值数据源未配置"
		return out, nil
	}
	if !config.ValidCode(stock.Code) || price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		out.Reason = "收盘价或股票代码无效"
		return out, nil
	}
	snapshot, err := e.Source.FetchFundamentals(ctx, stock)
	if err != nil {
		return out, err
	}
	out.Source = snapshot.Source
	out.FetchedAt = snapshot.FetchedAt
	out.StatementBasis = snapshot.FinancialStatementBasis
	return Calculate(stock, price, asOf, snapshot), nil
}

// Calculate is deterministic and does not fetch data. The fair multiple starts
// from a neutral market multiple and is adjusted only for trailing growth and
// ROE; the range widens with growth uncertainty. Both estimates are averaged so
// no single accounting identity determines the result.
func Calculate(stock config.Stock, price float64, asOf string, snapshot market.FundamentalSnapshot) Estimate {
	out := Estimate{Version: Version, Code: stock.Code, AsOf: asOf, Source: snapshot.Source,
		FetchedAt: snapshot.FetchedAt, Method: Method, StatementBasis: snapshot.FinancialStatementBasis,
		CurrentPrice: price, EPS: snapshot.EPS, BookValuePerShare: snapshot.BookValuePerShare}
	if err := snapshot.Validate(); err != nil || price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		out.Reason = "财务快照或收盘价无效"
		return out
	}
	if snapshot.EPS <= 0 {
		out.Reason = "最近四季度每股收益为负，盈利与净资产相对估值不适用"
		out.Warnings = append(out.Warnings, "negative_earnings")
		return out
	}

	out.GrowthKnown = snapshot.GrowthKnown
	if snapshot.GrowthKnown && snapshot.EPSPrior > 0 {
		out.GrowthPct = (snapshot.EPS/snapshot.EPSPrior - 1) * 100
	} else {
		out.Warnings = append(out.Warnings, "prior_year_eps_missing")
	}
	if snapshot.BookValuePerShare > 0 {
		out.ROEPct = snapshot.EPS / snapshot.BookValuePerShare * 100
		out.PB = price / snapshot.BookValuePerShare
	} else {
		out.Warnings = append(out.Warnings, "book_value_non_positive")
	}
	out.PE = price / snapshot.EPS

	financial := isFinancialSector(stock.Sector)
	growth := clamp(out.GrowthPct, -15, 35)
	quality := clamp(out.ROEPct-8, -8, 25)
	if financial {
		out.FairPE = clamp(5, 10, 5.5+0.15*growth+0.20*quality)
	} else {
		out.FairPE = clamp(8, 30, 11+0.35*growth+0.45*quality)
	}
	pePrice := snapshot.EPS * out.FairPE

	pbPrice := pePrice
	if snapshot.BookValuePerShare > 0 && out.ROEPct > 0 {
		if financial {
			out.FairPB = clamp(0.35, 1.20, out.ROEPct/15)
		} else {
			out.FairPB = clamp(0.50, 8, out.ROEPct/5)
		}
		pbPrice = snapshot.BookValuePerShare * out.FairPB
	}
	out.Central = (pePrice + pbPrice) / 2
	width := 0.15 + math.Min(math.Abs(out.GrowthPct), 25)/100*0.20
	out.Lower = out.Central * (1 - width)
	out.Upper = out.Central * (1 + width)
	out.UpsidePct = (out.Central/price - 1) * 100
	out.Confidence = "medium"
	if !snapshot.GrowthKnown || snapshot.BookValuePerShare <= 0 || out.ROEPct < 0 || out.ROEPct > 50 {
		out.Confidence = "low"
	}
	if !finite(out.Lower) || !finite(out.Central) || !finite(out.Upper) || out.Lower <= 0 || out.Central <= 0 || out.Upper <= 0 {
		return Estimate{Version: Version, Code: stock.Code, AsOf: asOf, Source: snapshot.Source,
			FetchedAt: snapshot.FetchedAt, Method: Method, Reason: "估值计算结果无效"}
	}
	return out
}

func isFinancialSector(sector string) bool {
	for _, marker := range []string{"银行", "保险", "证券", "信托", "多元金融"} {
		if strings.Contains(sector, marker) {
			return true
		}
	}
	return false
}

func clamp(value, lower, upper float64) float64 {
	if value < lower {
		return lower
	}
	if value > upper {
		return upper
	}
	return value
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// Validate is intentionally strict so callers cannot accidentally model a
// partial or inconsistent snapshot as a complete valuation.
func (e Estimate) Validate() error {
	if e.Version != Version || !config.ValidCode(e.Code) || strings.TrimSpace(e.AsOf) == "" || e.Method != Method {
		return errors.New("invalid fair value identity")
	}
	if !e.Available() {
		if e.Reason == "" {
			return errors.New("unavailable fair value requires a reason")
		}
		return nil
	}
	if e.EPS <= 0 || e.CurrentPrice <= 0 || e.Lower > e.Central || e.Central > e.Upper {
		return fmt.Errorf("invalid fair value range")
	}
	return nil
}
