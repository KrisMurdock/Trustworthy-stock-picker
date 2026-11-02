package fairvalue

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	MethodV2  = "residual_income_pb_roe_v2"
	VersionV2 = 1
)

const (
	// DataQualityTTMOnly marks a valuation calculated from a current trailing
	// snapshot. DataQualityUnavailable marks a record where no usable snapshot
	// or price was available.
	DataQualityTTMOnly     = "ttm_only"
	DataQualityUnavailable = "unavailable"
)

var costOfEquityPctsV2 = [...]float64{8.5, 9.5, 10.5}

const centralCostOfEquityPctV2 = 9.5

// ScenarioV2 is one auditable residual-income PB scenario. All percentage
// fields use percentage points, so 9.5 means 9.5%.
type ScenarioV2 struct {
	CostOfEquityPct float64 `json:"cost_of_equity_pct"`
	GrowthPct       float64 `json:"growth_pct"`
	FairPB          float64 `json:"fair_pb"`
	FairPE          float64 `json:"fair_pe"`
	Price           float64 `json:"price"`
}

// EstimateV2 is the schema-stable, residual-income valuation result. It is a
// research band rather than a target price or a recommendation.
type EstimateV2 struct {
	Version              int          `json:"schema_version"`
	Code                 string       `json:"code"`
	AsOf                 string       `json:"as_of"`
	Source               string       `json:"source"`
	FetchedAt            time.Time    `json:"fetched_at"`
	Method               string       `json:"method"`
	StatementBasis       string       `json:"statement_basis"`
	Confidence           string       `json:"confidence"`
	DataQuality          string       `json:"data_quality"`
	Reason               string       `json:"reason"`
	Warnings             []string     `json:"warnings"`
	CurrentPrice         float64      `json:"current_price"`
	EPS                  float64      `json:"eps_ttm"`
	BookValuePerShare    float64      `json:"book_value_per_share"`
	GrowthPct            float64      `json:"eps_growth_pct"`
	ROEPct               float64      `json:"roe_pct"`
	CurrentPE            float64      `json:"current_pe"`
	CurrentPB            float64      `json:"current_pb"`
	SustainableGrowthPct float64      `json:"sustainable_growth_pct"`
	PayoutPct            float64      `json:"payout_pct"`
	CostOfEquityPct      float64      `json:"cost_of_equity_pct"`
	FairPB               float64      `json:"fair_pb"`
	FairPE               float64      `json:"fair_pe"`
	Lower                float64      `json:"lower"`
	Central              float64      `json:"central"`
	Upper                float64      `json:"upper"`
	UpsidePct            float64      `json:"upside_pct"`
	Scenarios            []ScenarioV2 `json:"scenarios"`
}

func (e EstimateV2) Available() bool {
	return e.Lower > 0 && e.Central >= e.Lower && e.Upper >= e.Central
}

// CalculateV2 is deterministic and does not fetch data. It applies the
// residual-income identity PB = (ROE - g) / (r - g), keeps a bounded
// non-financial/financial PB range, and derives the central value and band from
// the same auditable scenario set.
func CalculateV2(stock config.Stock, price float64, asOf string, snapshot market.FundamentalSnapshot) EstimateV2 {
	out := EstimateV2{
		Version: VersionV2, Code: stock.Code, AsOf: asOf, Source: snapshot.Source,
		FetchedAt: snapshot.FetchedAt, Method: MethodV2,
		StatementBasis: snapshot.FinancialStatementBasis, DataQuality: DataQualityTTMOnly,
		CurrentPrice: price, EPS: snapshot.EPS, BookValuePerShare: snapshot.BookValuePerShare,
	}
	if snapshot.EPS <= 0 {
		out.Warnings = appendWarning(out.Warnings, "non_positive_earnings")
	}
	if snapshot.BookValuePerShare <= 0 {
		out.Warnings = appendWarning(out.Warnings, "book_value_non_positive")
	}
	growthKnown := snapshot.GrowthKnown && snapshot.EPSPrior > 0
	if !growthKnown {
		out.Warnings = appendWarning(out.Warnings, "growth_unknown")
	} else {
		out.GrowthPct = (snapshot.EPS/snapshot.EPSPrior - 1) * 100
	}

	if err := snapshot.Validate(); err != nil {
		out.DataQuality = DataQualityUnavailable
		out.Reason = "财务快照或收盘价无效"
		return out
	}
	if !config.ValidCode(stock.Code) || price <= 0 || !finite(price) {
		out.DataQuality = DataQualityUnavailable
		out.Reason = "收盘价或股票代码无效"
		return out
	}
	if snapshot.EPS <= 0 {
		out.Reason = "最近四季度每股收益非正，剩余收益估值不适用"
		return out
	}
	if snapshot.BookValuePerShare <= 0 {
		out.Reason = "最近四季度每股净资产非正，市净率估值不适用"
		return out
	}

	out.ROEPct = snapshot.EPS / snapshot.BookValuePerShare * 100
	if !finite(out.ROEPct) || out.ROEPct <= 0 {
		out.Reason = "净资产收益率为非正，剩余收益估值不适用"
		return out
	}
	out.CurrentPE = price / snapshot.EPS
	out.CurrentPB = price / snapshot.BookValuePerShare
	out.SustainableGrowthPct = clamp(out.GrowthPct, -2, 6)
	if out.SustainableGrowthPct >= out.ROEPct {
		out.Reason = "增长率不低于净资产收益率，无法生成有效剩余收益情景"
		return out
	}
	out.PayoutPct = clamp((1-out.SustainableGrowthPct/out.ROEPct)*100, 0, 100)
	out.CostOfEquityPct = centralCostOfEquityPctV2

	financial := isFinancialSector(stock.Sector)
	for _, costOfEquityPct := range costOfEquityPctsV2 {
		seenGrowth := make(map[float64]bool, 3)
		for _, growthPct := range [...]float64{
			clamp(out.SustainableGrowthPct-1, -2, 6),
			out.SustainableGrowthPct,
			clamp(out.SustainableGrowthPct+1, -2, 6),
		} {
			if seenGrowth[growthPct] {
				continue
			}
			seenGrowth[growthPct] = true
			scenario, ok := makeScenarioV2(out.ROEPct, growthPct, costOfEquityPct, snapshot.BookValuePerShare, financial)
			if ok {
				out.Scenarios = append(out.Scenarios, scenario)
			}
		}
	}
	if len(out.Scenarios) == 0 {
		if out.SustainableGrowthPct > out.ROEPct {
			out.Reason = "增长率高于净资产收益率，无法生成有效剩余收益情景"
			return out
		}
		out.Reason = "无有效估值情景"
		return out
	}

	central, ok := selectCentralScenarioV2(out.Scenarios, out.SustainableGrowthPct)
	if !ok {
		out.Reason = "无法确定中心估值情景"
		return out
	}
	prices := scenarioPricesV2(out.Scenarios)
	sort.Float64s(prices)
	out.Lower = scenarioPricePercentileV2(prices, 20)
	out.Upper = scenarioPricePercentileV2(prices, 80)
	out.FairPB = central.FairPB
	out.FairPE = central.FairPE
	out.Central = central.Price
	out.UpsidePct = (out.Central/price - 1) * 100
	out.Confidence = "low"

	if !finite(out.Lower) || !finite(out.Central) || !finite(out.Upper) ||
		out.Lower <= 0 || out.Central <= 0 || out.Upper <= 0 ||
		out.Lower > out.Central || out.Central > out.Upper {
		out.Reason = "估值计算结果无效"
		out.Lower, out.Central, out.Upper, out.UpsidePct = 0, 0, 0, 0
		out.FairPB, out.FairPE = 0, 0
	}
	return out
}

func makeScenarioV2(roePct, growthPct, costOfEquityPct, bookValuePerShare float64, financial bool) (ScenarioV2, bool) {
	if !finite(roePct) || roePct <= 0 || !finite(growthPct) || !finite(costOfEquityPct) ||
		!finite(bookValuePerShare) || bookValuePerShare <= 0 {
		return ScenarioV2{}, false
	}
	if costOfEquityPct-growthPct < 1 {
		return ScenarioV2{}, false
	}
	fairPB := (roePct - growthPct) / (costOfEquityPct - growthPct)
	if !finite(fairPB) || fairPB <= 0 {
		return ScenarioV2{}, false
	}
	upper := 8.0
	if financial {
		upper = 3.0
	}
	if fairPB > upper {
		fairPB = upper
	}
	fairPE := fairPB / (roePct / 100)
	price := bookValuePerShare * fairPB
	if fairPB <= 0 || fairPE <= 0 || price <= 0 || !finite(fairPB) || !finite(fairPE) || !finite(price) {
		return ScenarioV2{}, false
	}
	return ScenarioV2{
		CostOfEquityPct: costOfEquityPct,
		GrowthPct:       growthPct,
		FairPB:          fairPB,
		FairPE:          fairPE,
		Price:           price,
	}, true
}

func selectCentralScenarioV2(scenarios []ScenarioV2, growthPct float64) (ScenarioV2, bool) {
	for _, scenario := range scenarios {
		if scenario.CostOfEquityPct == centralCostOfEquityPctV2 && scenario.GrowthPct == growthPct {
			return scenario, true
		}
	}
	prices := scenarioPricesV2(scenarios)
	sort.Float64s(prices)
	median := prices[len(prices)/2]
	for _, scenario := range scenarios {
		if nearlyEqualV2(scenario.Price, median) {
			return scenario, true
		}
	}
	return ScenarioV2{}, false
}

func scenarioPricesV2(scenarios []ScenarioV2) []float64 {
	prices := make([]float64, 0, len(scenarios))
	for _, scenario := range scenarios {
		prices = append(prices, scenario.Price)
	}
	return prices
}

func scenarioPricePercentileV2(sortedPrices []float64, percentile int) float64 {
	if len(sortedPrices) == 0 {
		return 0
	}
	rank := int(math.Ceil(float64(percentile) * float64(len(sortedPrices)) / 100))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sortedPrices) {
		rank = len(sortedPrices)
	}
	return sortedPrices[rank-1]
}

func appendWarning(warnings []string, warning string) []string {
	if !containsString(warnings, warning) {
		return append(warnings, warning)
	}
	return warnings
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func nearlyEqualV2(a, b float64) bool {
	scale := math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
	return math.Abs(a-b) <= 1e-9*scale
}

// Validate is intentionally strict so a partial or inconsistent v2 estimate
// cannot be mistaken for a usable research valuation.
func (e EstimateV2) Validate() error {
	if e.Version != VersionV2 || !config.ValidCode(e.Code) || strings.TrimSpace(e.AsOf) == "" || e.Method != MethodV2 {
		return errors.New("invalid fair value v2 identity")
	}
	if e.DataQuality != DataQualityTTMOnly && e.DataQuality != DataQualityUnavailable {
		return errors.New("invalid fair value v2 data quality")
	}
	values := []float64{
		e.CurrentPrice, e.EPS, e.BookValuePerShare, e.GrowthPct, e.ROEPct,
		e.CurrentPE, e.CurrentPB, e.SustainableGrowthPct, e.PayoutPct,
		e.CostOfEquityPct, e.FairPB, e.FairPE, e.Lower, e.Central, e.Upper, e.UpsidePct,
	}
	for _, value := range values {
		if !finite(value) {
			return errors.New("non-finite fair value v2 number")
		}
	}
	for _, scenario := range e.Scenarios {
		if !finite(scenario.CostOfEquityPct) || !finite(scenario.GrowthPct) ||
			!finite(scenario.FairPB) || !finite(scenario.FairPE) || !finite(scenario.Price) {
			return errors.New("non-finite fair value v2 scenario")
		}
	}
	if e.Lower > e.Central || e.Central > e.Upper {
		return fmt.Errorf("invalid fair value v2 range")
	}
	if !e.Available() {
		if strings.TrimSpace(e.Reason) == "" {
			return errors.New("unavailable fair value v2 requires a reason")
		}
		return nil
	}
	if e.DataQuality != DataQualityTTMOnly {
		return errors.New("invalid fair value v2 data quality")
	}
	if e.CurrentPrice <= 0 || e.EPS <= 0 || e.BookValuePerShare <= 0 || e.ROEPct <= 0 ||
		e.FairPB <= 0 || e.FairPE <= 0 || e.Lower <= 0 || e.Central <= 0 || e.Upper <= 0 {
		return errors.New("non-positive fair value v2 value")
	}
	for _, scenario := range e.Scenarios {
		if scenario.CostOfEquityPct <= 0 || scenario.FairPB <= 0 || scenario.FairPE <= 0 || scenario.Price <= 0 {
			return errors.New("non-positive fair value v2 scenario")
		}
		if scenario.GrowthPct >= e.ROEPct || scenario.CostOfEquityPct-scenario.GrowthPct < 1 {
			return errors.New("invalid fair value v2 scenario constraints")
		}
		if !nearlyEqualV2(scenario.FairPE, scenario.FairPB/(e.ROEPct/100)) ||
			!nearlyEqualV2(scenario.Price, e.BookValuePerShare*scenario.FairPB) {
			return errors.New("inconsistent fair value v2 scenario")
		}
	}
	if len(e.Scenarios) == 0 {
		return errors.New("available fair value v2 requires scenarios")
	}
	matched := false
	for _, scenario := range e.Scenarios {
		if nearlyEqualV2(scenario.Price, e.Central) && nearlyEqualV2(scenario.FairPB, e.FairPB) && nearlyEqualV2(scenario.FairPE, e.FairPE) {
			matched = true
			break
		}
	}
	if !matched {
		return errors.New("central fair value v2 does not match a scenario")
	}
	return nil
}
