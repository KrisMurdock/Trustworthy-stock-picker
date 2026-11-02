package fairvalue

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fundamentals"
)

const (
	MethodV3  = "residual_income_normalized_v3"
	VersionV3 = 1
)

const (
	DataQualityNormalized = "normalized"
)

// EstimateV3 is a research valuation built from a point-in-time normalized
// annual fundamental surface. It deliberately keeps the result outside the
// daily selection and WeChat publication paths until walk-forward calibration
// and a manual release review are complete.
type EstimateV3 struct {
	Version                int                         `json:"schema_version"`
	Code                   string                      `json:"code"`
	Sector                 string                      `json:"sector,omitempty"`
	AsOf                   string                      `json:"as_of"`
	Source                 string                      `json:"source"`
	SourceVersion          string                      `json:"source_version"`
	InputMode              string                      `json:"input_mode"`
	VisibilityMode         fundamentals.VisibilityMode `json:"visibility_mode"`
	Method                 string                      `json:"method"`
	StatementBasis         string                      `json:"statement_basis"`
	Confidence             string                      `json:"confidence"`
	DataQuality            string                      `json:"data_quality"`
	Reason                 string                      `json:"reason,omitempty"`
	Warnings               []string                    `json:"warnings,omitempty"`
	InputDigest            string                      `json:"input_digest"`
	WindowStart            int                         `json:"window_start,omitempty"`
	WindowEnd              int                         `json:"window_end,omitempty"`
	FiscalYears            []int                       `json:"fiscal_years,omitempty"`
	PayoutYears            int                         `json:"payout_years"`
	LatestReportEnd        string                      `json:"latest_report_end,omitempty"`
	LatestAnnouncementDate string                      `json:"latest_announcement_date,omitempty"`
	LatestAvailableFrom    string                      `json:"latest_available_from,omitempty"`
	LatestFetchedAt        time.Time                   `json:"latest_fetched_at,omitempty"`
	CurrentPrice           float64                     `json:"current_price"`
	EPSNormalized          float64                     `json:"eps_normalized"`
	BookValuePerShare      float64                     `json:"book_value_per_share"`
	ROEPct                 float64                     `json:"roe_norm_pct"`
	CurrentPE              float64                     `json:"current_pe"`
	CurrentPB              float64                     `json:"current_pb"`
	PayoutPct              float64                     `json:"payout_norm_pct"`
	SustainableGrowthPct   float64                     `json:"sustainable_growth_pct"`
	CostOfEquityPct        float64                     `json:"cost_of_equity_pct"`
	FairPB                 float64                     `json:"fair_pb"`
	FairPE                 float64                     `json:"fair_pe"`
	Lower                  float64                     `json:"lower"`
	Central                float64                     `json:"central"`
	Upper                  float64                     `json:"upper"`
	UpsidePct              float64                     `json:"upside_pct"`
	Scenarios              []ScenarioV2                `json:"scenarios"`
}

func (e EstimateV3) Available() bool {
	return e.Lower > 0 && e.Central >= e.Lower && e.Upper >= e.Central
}

// CalculateV3 derives sustainable growth from normalized ROE and the observed
// cash-dividend payout. A missing payout is not replaced with an assumed
// payout: without that evidence the valuation remains unavailable. The model
// is intentionally marked low confidence until it has passed walk-forward
// calibration and release review.
func CalculateV3(stock config.Stock, price float64, asOf string, normalized fundamentals.Normalized) EstimateV3 {
	out := EstimateV3{
		Version: VersionV3, Code: stock.Code, Sector: stock.Sector, AsOf: asOf, Source: normalized.Source,
		SourceVersion: normalized.SourceVersion, InputMode: normalized.InputMode,
		VisibilityMode: normalized.VisibilityMode,
		Method:         MethodV3, StatementBasis: fundamentals.StatementAnnualConsolidated,
		DataQuality: DataQualityNormalized, Confidence: ConfidenceLowV3,
		Warnings: append([]string(nil), normalized.Warnings...), CurrentPrice: price,
		FiscalYears: append([]int(nil), normalized.FiscalYears...), WindowStart: normalized.WindowStart,
		WindowEnd: normalized.WindowEnd, PayoutYears: normalized.PayoutYears,
		LatestReportEnd: normalized.LatestReportEnd, LatestAnnouncementDate: normalized.LatestAnnouncementDate,
		LatestAvailableFrom: normalized.LatestAvailableFrom, LatestFetchedAt: normalized.LatestFetchedAt,
		InputDigest: normalized.InputDigest,
	}
	if err := normalized.Validate(); err != nil {
		out.DataQuality = DataQualityUnavailable
		out.Confidence = ConfidenceUnavailableV3
		out.Reason = "标准化财务输入无效"
		return out
	}
	if stock.Code != normalized.Code || asOf != normalized.AsOf {
		out.DataQuality = DataQualityUnavailable
		out.Confidence = ConfidenceUnavailableV3
		out.Reason = "标准化财务输入与股票或 as-of 不一致"
		return out
	}
	if !normalized.Available {
		out.DataQuality = DataQualityUnavailable
		out.Confidence = ConfidenceUnavailableV3
		out.Reason = normalized.Reason
		return out
	}
	if !config.ValidCode(stock.Code) || !finite(price) || price <= 0 {
		out.DataQuality = DataQualityUnavailable
		out.Confidence = ConfidenceUnavailableV3
		out.Reason = "收盘价或股票代码无效"
		return out
	}
	if normalized.PayoutYears == 0 || containsString(out.Warnings, "payout_missing") {
		out.DataQuality = DataQualityUnavailable
		out.Confidence = ConfidenceUnavailableV3
		out.Reason = "缺少已实施现金分红，无法推导可持续增长"
		return out
	}
	if normalized.EPSNormalized <= 0 || normalized.BVPS <= 0 || normalized.ROEPct <= 0 {
		out.DataQuality = DataQualityUnavailable
		out.Confidence = ConfidenceUnavailableV3
		out.Reason = "标准化盈利、净资产或 ROE 非正，剩余收益估值不适用"
		return out
	}

	out.EPSNormalized = normalized.EPSNormalized
	out.BookValuePerShare = normalized.BVPS
	out.ROEPct = normalized.ROEPct
	out.PayoutPct = normalized.PayoutPct
	out.CurrentPE = price / out.EPSNormalized
	out.CurrentPB = price / out.BookValuePerShare
	out.SustainableGrowthPct = clamp(out.ROEPct*(1-out.PayoutPct/100), -2, 6)
	if !finite(out.SustainableGrowthPct) || out.SustainableGrowthPct >= out.ROEPct {
		out.DataQuality = DataQualityUnavailable
		out.Confidence = ConfidenceUnavailableV3
		out.Reason = "可持续增长率不低于标准化 ROE，无法生成有效剩余收益情景"
		return out
	}
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
			scenario, ok := makeScenarioV2(out.ROEPct, growthPct, costOfEquityPct, out.BookValuePerShare, financial)
			if ok {
				out.Scenarios = append(out.Scenarios, scenario)
			}
		}
	}
	if len(out.Scenarios) == 0 {
		out.DataQuality = DataQualityUnavailable
		out.Confidence = ConfidenceUnavailableV3
		out.Reason = "无有效估值情景"
		return out
	}
	central, ok := selectCentralScenarioV2(out.Scenarios, out.SustainableGrowthPct)
	if !ok {
		out.DataQuality = DataQualityUnavailable
		out.Confidence = ConfidenceUnavailableV3
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
	out.Warnings = appendWarning(out.Warnings, "model_uncalibrated")
	if !finite(out.Lower) || !finite(out.Central) || !finite(out.Upper) ||
		out.Lower <= 0 || out.Central <= 0 || out.Upper <= 0 ||
		out.Lower > out.Central || out.Central > out.Upper {
		out.Lower, out.Central, out.Upper, out.UpsidePct = 0, 0, 0, 0
		out.FairPB, out.FairPE = 0, 0
		out.DataQuality = DataQualityUnavailable
		out.Confidence = ConfidenceUnavailableV3
		out.Reason = "估值计算结果无效"
	}
	return out
}

const (
	ConfidenceLowV3         = "low"
	ConfidenceUnavailableV3 = "unavailable"
)

// Validate keeps the v3 result distinct from the TTM-only v2 contract. A
// normalized result must retain the exact input digest and normalized window
// that produced it, while unavailable results must explain the downgrade.
func (e EstimateV3) Validate() error {
	if e.Version != VersionV3 || !config.ValidCode(e.Code) || strings.TrimSpace(e.AsOf) == "" || e.Method != MethodV3 ||
		e.InputMode != fundamentals.InputModeNormalized || e.StatementBasis != fundamentals.StatementAnnualConsolidated {
		return errors.New("invalid normalized fair value identity")
	}
	if !e.VisibilityMode.Valid() {
		return errors.New("invalid normalized fair value visibility mode")
	}
	if len([]rune(e.Sector)) > 128 {
		return errors.New("normalized fair value sector is too long")
	}
	if e.VisibilityMode == fundamentals.VisibilityObserved && containsString(e.Warnings, "vendor_backfill_mode") {
		return errors.New("observed fair value contains vendor backfill evidence")
	}
	if e.VisibilityMode == fundamentals.VisibilityVendorBackfill && !containsString(e.Warnings, "vendor_backfill_mode") {
		return errors.New("vendor backfill fair value is missing its warning")
	}
	if e.DataQuality != DataQualityNormalized && e.DataQuality != DataQualityUnavailable {
		return errors.New("invalid normalized fair value data quality")
	}
	if e.Confidence != ConfidenceLowV3 && e.Confidence != ConfidenceUnavailableV3 {
		return errors.New("invalid normalized fair value confidence")
	}
	if !validHash(e.InputDigest) {
		return errors.New("invalid normalized fair value input digest")
	}
	if e.PayoutYears < 0 {
		return errors.New("negative normalized payout years")
	}
	if e.LatestAvailableFrom != "" {
		available, err := time.Parse("2006-01-02", e.LatestAvailableFrom)
		asOf, asOfErr := time.Parse("2006-01-02", e.AsOf)
		if err != nil || asOfErr != nil || available.After(asOf) {
			return errors.New("invalid normalized latest available date")
		}
	}
	for _, value := range []float64{
		e.CurrentPrice, e.EPSNormalized, e.BookValuePerShare, e.ROEPct, e.CurrentPE,
		e.CurrentPB, e.PayoutPct, e.SustainableGrowthPct, e.CostOfEquityPct,
		e.FairPB, e.FairPE, e.Lower, e.Central, e.Upper, e.UpsidePct,
	} {
		if !finite(value) {
			return errors.New("non-finite normalized fair value number")
		}
	}
	for _, scenario := range e.Scenarios {
		if !finite(scenario.CostOfEquityPct) || !finite(scenario.GrowthPct) ||
			!finite(scenario.FairPB) || !finite(scenario.FairPE) || !finite(scenario.Price) {
			return errors.New("non-finite normalized fair value scenario")
		}
	}
	if e.Lower > e.Central || e.Central > e.Upper {
		return fmt.Errorf("invalid normalized fair value range")
	}
	if !e.Available() {
		if e.DataQuality != DataQualityUnavailable || e.Confidence != ConfidenceUnavailableV3 || strings.TrimSpace(e.Reason) == "" {
			return errors.New("unavailable normalized fair value requires reason and unavailable quality")
		}
		return nil
	}
	if e.DataQuality != DataQualityNormalized || e.Confidence != ConfidenceLowV3 {
		return errors.New("available normalized fair value requires normalized quality and low confidence")
	}
	if e.CurrentPrice <= 0 || e.EPSNormalized <= 0 || e.BookValuePerShare <= 0 || e.ROEPct <= 0 ||
		e.Lower <= 0 || e.Central <= 0 || e.Upper <= 0 || e.FairPB <= 0 || e.FairPE <= 0 {
		return errors.New("non-positive normalized fair value")
	}
	if e.LatestReportEnd == "" || e.LatestAnnouncementDate == "" || e.LatestAvailableFrom == "" || e.LatestFetchedAt.IsZero() {
		return errors.New("available normalized fair value is missing disclosure evidence")
	}
	if e.VisibilityMode == fundamentals.VisibilityObserved && !(fundamentals.Period{FetchedAt: e.LatestFetchedAt}).ObservedBy(e.AsOf) {
		return errors.New("observed fair value has no usable latest fetched time")
	}
	if e.WindowStart <= 0 || e.WindowEnd < e.WindowStart || len(e.FiscalYears) < fundamentals.MinWindowYears {
		return errors.New("insufficient normalized fair value window")
	}
	if e.WindowEnd < e.WindowStart || e.FiscalYears[0] != e.WindowStart || e.FiscalYears[len(e.FiscalYears)-1] != e.WindowEnd {
		return errors.New("normalized fair value window mismatch")
	}
	if e.PayoutYears < 1 || e.PayoutPct < 0 || e.PayoutPct > 100 {
		return errors.New("invalid normalized payout")
	}
	if e.SustainableGrowthPct < -2 || e.SustainableGrowthPct > 6 || e.SustainableGrowthPct >= e.ROEPct {
		return errors.New("invalid normalized sustainable growth")
	}
	if e.CostOfEquityPct <= e.SustainableGrowthPct || e.CostOfEquityPct-e.SustainableGrowthPct < 1 {
		return errors.New("invalid normalized cost of equity")
	}
	if len(e.Scenarios) == 0 {
		return errors.New("available normalized fair value requires scenarios")
	}
	for _, scenario := range e.Scenarios {
		if scenario.CostOfEquityPct <= 0 || scenario.FairPB <= 0 || scenario.FairPE <= 0 || scenario.Price <= 0 ||
			scenario.GrowthPct >= e.ROEPct || scenario.CostOfEquityPct-scenario.GrowthPct < 1 {
			return errors.New("invalid normalized fair value scenario constraints")
		}
		if !nearlyEqualV2(scenario.FairPE, scenario.FairPB/(e.ROEPct/100)) ||
			!nearlyEqualV2(scenario.Price, e.BookValuePerShare*scenario.FairPB) {
			return errors.New("inconsistent normalized fair value scenario")
		}
	}
	matched := false
	for _, scenario := range e.Scenarios {
		if nearlyEqualV2(scenario.Price, e.Central) && nearlyEqualV2(scenario.FairPB, e.FairPB) && nearlyEqualV2(scenario.FairPE, e.FairPE) {
			matched = true
			break
		}
	}
	if !matched {
		return errors.New("central normalized fair value does not match a scenario")
	}
	return nil
}

func validHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32
}
