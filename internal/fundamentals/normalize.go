package fundamentals

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

type normalizationDigest struct {
	AsOf           string         `json:"as_of"`
	Version        int            `json:"normalization_version"`
	InputMode      string         `json:"input_mode"`
	VisibilityMode VisibilityMode `json:"visibility_mode"`
	SourceVersion  string         `json:"source_version"`
	Code           string         `json:"code"`
	Source         string         `json:"source"`
	Periods        []Period       `json:"periods"`
}

// Normalize derives a deterministic 5-10 year annual surface from reports that
// were both published and actually observed by asOf. Missing optional
// observations are left missing; nil is never treated as zero. Insufficient
// core history produces an explicit unavailable result rather than a partial
// estimate.
func Normalize(series Series, asOf string) Normalized {
	return NormalizeWithMode(series, asOf, VisibilityObserved)
}

// NormalizeWithMode is the explicit escape hatch for historical reconstruction.
// Observed mode is the default and the only mode accepted by walk-forward
// calibration. VendorBackfill mode can use a fact fetched after asOf, so it is
// always marked low confidence with a warning and must not be presented as
// strict point-in-time evidence.
func NormalizeWithMode(series Series, asOf string, mode VisibilityMode) Normalized {
	out := Normalized{
		SchemaVersion:  NormalizationVersion,
		Code:           series.Code,
		AsOf:           asOf,
		Source:         series.Source,
		SourceVersion:  normalizedSourceVersion(series.Source),
		InputMode:      InputModeNormalized,
		VisibilityMode: mode,
	}
	if !mode.Valid() {
		return unavailableNormalized(out, "invalid_visibility_mode")
	}
	asOfDate, err := parseDate(asOf)
	if err != nil {
		return unavailableNormalized(out, "invalid_as_of")
	}
	if mode == VisibilityVendorBackfill {
		out.Warnings = appendWarning(out.Warnings, "vendor_backfill_mode")
	}
	if len(series.Periods) == 0 {
		if digest, err := inputDigest(out, nil); err == nil {
			out.InputDigest = digest
		}
		return unavailableNormalized(out, "empty_series")
	}
	if err := series.Validate(); err != nil {
		return unavailableNormalized(out, "invalid_series")
	}

	visible := series.Visible(asOf, mode)
	if len(visible) == 0 {
		if digest, err := inputDigest(out, nil); err == nil {
			out.InputDigest = digest
		}
		if mode == VisibilityObserved {
			return unavailableNormalized(out, "no_observed_annual_reports")
		}
		return unavailableNormalized(out, "no_visible_annual_reports")
	}
	selected := lastWindow(visible, MaxWindowYears)
	out.WindowStart = fiscalYear(selected[0].ReportEnd)
	out.WindowEnd = fiscalYear(selected[len(selected)-1].ReportEnd)
	out.FiscalYears = fiscalYears(selected)
	out.MissingYears = missingFiscalYears(out.WindowStart, out.WindowEnd, out.FiscalYears)

	latest := selected[len(selected)-1]
	out.LatestReportEnd = latest.ReportEnd
	out.LatestAnnouncementDate = latest.AnnouncementDate
	out.LatestAvailableFrom = latest.AvailableFrom
	out.LatestFetchedAt = latest.FetchedAt
	digest, err := inputDigest(out, selected)
	if err != nil {
		return unavailableNormalized(out, "digest_failed")
	}
	out.InputDigest = digest
	if len(out.MissingYears) > 0 {
		out.Warnings = appendWarning(out.Warnings, "missing_annual_reports")
	}

	if len(selected) < MinWindowYears {
		return unavailableNormalized(out, "insufficient_visible_years")
	}
	if latest.TotalShares == nil {
		return unavailableNormalized(out, "missing_latest_total_shares")
	}
	if *latest.TotalShares <= 0 {
		return unavailableNormalized(out, "non_positive_latest_total_shares")
	}
	out.TotalShares = *latest.TotalShares
	if latest.Equity == nil {
		return unavailableNormalized(out, "missing_latest_equity")
	}
	if *latest.Equity <= 0 {
		return unavailableNormalized(out, "non_positive_latest_equity")
	}
	out.LatestEquity = *latest.Equity

	profits := make([]float64, 0, len(selected))
	equityYears := 0
	for _, period := range selected {
		if period.NetProfit != nil {
			profits = append(profits, *period.NetProfit)
		}
		if period.Equity != nil {
			equityYears++
		}
	}
	if len(profits) < MinWindowYears {
		return unavailableNormalized(out, "insufficient_profit_years")
	}
	if equityYears < MinWindowYears {
		return unavailableNormalized(out, "insufficient_equity_years")
	}
	out.NetProfitAnnualMedian = median(profits)
	if out.NetProfitAnnualMedian <= 0 {
		return unavailableNormalized(out, "non_positive_normalized_earnings")
	}

	roeValues, averageEquities := annualROEs(visible, selected)
	if len(roeValues) < MinWindowYears || len(averageEquities) < MinWindowYears {
		return unavailableNormalized(out, "insufficient_roe_years")
	}
	out.ROEPct = median(roeValues)
	out.AverageEquityMedian = median(averageEquities)
	out.EPSNormalized = out.NetProfitAnnualMedian / out.TotalShares
	out.BVPS = out.LatestEquity / out.TotalShares
	if !finite(out.EPSNormalized) || out.EPSNormalized <= 0 || !finite(out.BVPS) || out.BVPS <= 0 {
		return unavailableNormalized(out, "non_positive_per_share_value")
	}

	payouts := make([]float64, 0, len(selected))
	for _, period := range selected {
		if period.CashDividend == nil || period.NetProfit == nil || *period.NetProfit <= 0 {
			continue
		}
		dividendAvailable, err := parseDate(period.CashDividendAvailableFrom)
		if err != nil || dividendAvailable.After(asOfDate) {
			if err == nil {
				out.Warnings = appendWarning(out.Warnings, "payout_not_yet_visible")
			}
			continue
		}
		ratio := *period.CashDividend / *period.NetProfit * 100
		if finite(ratio) {
			payouts = append(payouts, ratio)
		}
	}
	out.PayoutYears = len(payouts)
	if len(payouts) == 0 {
		out.Warnings = appendWarning(out.Warnings, "payout_missing")
	} else {
		out.PayoutPct = clamp(median(payouts), 0, 100)
		if median(payouts) > 100 {
			out.Warnings = appendWarning(out.Warnings, "payout_above_100_capped")
		}
		if len(payouts) < 3 {
			out.Warnings = appendWarning(out.Warnings, "payout_years_lt_3")
		}
	}

	out.Confidence = ConfidenceMedium
	if out.PayoutYears < 3 || len(out.MissingYears) > 0 {
		out.Confidence = ConfidenceLow
	}
	if staleReport(asOf, latest.ReportEnd) {
		out.Warnings = appendWarning(out.Warnings, "stale_report")
		out.Confidence = ConfidenceLow
	}
	if mode == VisibilityVendorBackfill {
		out.Confidence = ConfidenceLow
	}
	out.Available = true
	return out
}

func normalizedSourceVersion(source string) string {
	return fmt.Sprintf("%s/%s/v%d", source, StatementAnnualConsolidated, Version)
}

func unavailableNormalized(out Normalized, reason string) Normalized {
	out.Available = false
	out.Confidence = ConfidenceUnavailable
	out.Reason = reason
	return out
}

func lastWindow(periods []Period, max int) []Period {
	if max <= 0 || len(periods) <= max {
		return append([]Period(nil), periods...)
	}
	return append([]Period(nil), periods[len(periods)-max:]...)
}

func fiscalYears(periods []Period) []int {
	years := make([]int, 0, len(periods))
	for _, period := range periods {
		years = append(years, fiscalYear(period.ReportEnd))
	}
	return years
}

func fiscalYear(reportEnd string) int {
	parsed, err := parseDate(reportEnd)
	if err != nil {
		return 0
	}
	return parsed.Year()
}

func missingFiscalYears(start, end int, present []int) []int {
	if start <= 0 || end < start {
		return nil
	}
	seen := make(map[int]bool, len(present))
	for _, year := range present {
		seen[year] = true
	}
	missing := make([]int, 0)
	for year := start; year <= end; year++ {
		if !seen[year] {
			missing = append(missing, year)
		}
	}
	return missing
}

func annualROEs(visible, selected []Period) ([]float64, []float64) {
	index := make(map[string]int, len(visible))
	for i, period := range visible {
		index[period.ReportEnd] = i
	}
	roes := make([]float64, 0, len(selected))
	equities := make([]float64, 0, len(selected))
	for _, period := range selected {
		if period.NetProfit == nil || period.Equity == nil {
			continue
		}
		position, ok := index[period.ReportEnd]
		if !ok || position == 0 {
			continue
		}
		previous := visible[position-1]
		if fiscalYear(previous.ReportEnd) != fiscalYear(period.ReportEnd)-1 {
			// A missing annual report breaks the average-equity pair. Do not
			// bridge across a gap and present a multi-year equity change as a
			// single-year ROE.
			continue
		}
		if previous.Equity == nil || *previous.Equity <= 0 || *period.Equity <= 0 {
			continue
		}
		average := (*previous.Equity + *period.Equity) / 2
		if !finite(average) || average <= 0 {
			continue
		}
		roe := *period.NetProfit / average * 100
		if !finite(roe) {
			continue
		}
		roes = append(roes, roe)
		equities = append(equities, average)
	}
	return roes, equities
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func inputDigest(out Normalized, periods []Period) (string, error) {
	canonical := make([]Period, 0, len(periods))
	for _, period := range periods {
		period.FetchedAt = period.FetchedAt.UTC()
		canonical = append(canonical, period)
	}
	raw, err := json.Marshal(normalizationDigest{
		AsOf:           out.AsOf,
		Version:        out.SchemaVersion,
		InputMode:      out.InputMode,
		VisibilityMode: out.VisibilityMode,
		SourceVersion:  out.SourceVersion,
		Code:           out.Code,
		Source:         out.Source,
		Periods:        canonical,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func staleReport(asOf, reportEnd string) bool {
	asOfDate, err := parseDate(asOf)
	if err != nil {
		return false
	}
	endDate, err := parseDate(reportEnd)
	if err != nil {
		return false
	}
	return endDate.Before(asOfDate.AddDate(0, -18, 0))
}

func appendWarning(warnings []string, warning string) []string {
	for _, existing := range warnings {
		if existing == warning {
			return warnings
		}
	}
	return append(warnings, warning)
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

// Validate enforces the normalized surface's identity, arithmetic, date and
// availability invariants. An unavailable result must explain why it is not
// usable; an available result must have positive core per-share values.
func (n Normalized) Validate() error {
	if n.SchemaVersion != NormalizationVersion || !config.ValidCode(n.Code) {
		return errors.New("invalid normalized identity")
	}
	asOf, err := parseDate(n.AsOf)
	if err != nil {
		return errors.New("invalid normalized as_of")
	}
	if strings.TrimSpace(n.Source) == "" || strings.TrimSpace(n.SourceVersion) == "" {
		return errors.New("invalid normalized source")
	}
	if n.InputMode != InputModeNormalized {
		return errors.New("invalid normalized input mode")
	}
	if !n.VisibilityMode.Valid() {
		return errors.New("invalid normalized visibility mode")
	}
	if !validDigest(n.InputDigest) {
		return errors.New("invalid normalized input digest")
	}
	switch n.Confidence {
	case ConfidenceMedium, ConfidenceLow, ConfidenceUnavailable:
	default:
		return errors.New("invalid normalized confidence")
	}
	for _, value := range []float64{
		n.TotalShares, n.NetProfitAnnualMedian, n.LatestEquity, n.AverageEquityMedian,
		n.ROEPct, n.EPSNormalized, n.BVPS, n.PayoutPct,
	} {
		if !finite(value) {
			return errors.New("non-finite normalized value")
		}
	}
	if n.PayoutYears < 0 {
		return errors.New("negative payout years")
	}
	if n.PayoutPct < 0 || n.PayoutPct > 100 {
		return errors.New("payout outside range")
	}
	if err := validateYearWindow(n); err != nil {
		return err
	}
	if n.LatestReportEnd != "" || n.LatestAnnouncementDate != "" || n.LatestAvailableFrom != "" {
		reportEnd, err := parseDate(n.LatestReportEnd)
		if err != nil || reportEnd.Month() != time.December || reportEnd.Day() != 31 {
			return errors.New("invalid latest report end")
		}
		announced, err := parseDate(n.LatestAnnouncementDate)
		if err != nil || announced.Before(reportEnd) {
			return errors.New("invalid latest announcement date")
		}
		available, err := parseDate(n.LatestAvailableFrom)
		if err != nil || !available.After(announced) || available.After(asOf) {
			return errors.New("invalid latest available date")
		}
	}

	if !n.Available {
		if n.Confidence != ConfidenceUnavailable || strings.TrimSpace(n.Reason) == "" {
			return errors.New("unavailable normalized result requires confidence and reason")
		}
		return nil
	}
	if n.Confidence == ConfidenceUnavailable {
		return errors.New("available normalized result has unavailable confidence")
	}
	if n.TotalShares <= 0 || n.LatestEquity <= 0 || n.AverageEquityMedian <= 0 {
		return errors.New("available normalized result has non-positive capital")
	}
	if n.EPSNormalized <= 0 || n.BVPS <= 0 {
		return errors.New("available normalized result has non-positive per-share value")
	}
	if n.NetProfitAnnualMedian <= 0 {
		return errors.New("available normalized result has non-positive earnings")
	}
	if n.WindowStart <= 0 || n.WindowEnd < n.WindowStart || len(n.FiscalYears) < MinWindowYears {
		return errors.New("insufficient normalized fiscal years")
	}
	if n.LatestFetchedAt.IsZero() {
		return errors.New("available normalized result has no fetch time")
	}
	if n.VisibilityMode == VisibilityObserved && !periodFetchedBy(n.LatestFetchedAt, asOf) {
		return errors.New("observed normalized result uses a future fetch")
	}
	if n.LatestReportEnd == "" || n.LatestAnnouncementDate == "" || n.LatestAvailableFrom == "" {
		return errors.New("available normalized result has no latest disclosure dates")
	}
	if fiscalYear(n.LatestReportEnd) != n.WindowEnd {
		return errors.New("latest report does not match window end")
	}
	if n.PayoutYears > len(n.FiscalYears) {
		return errors.New("payout years exceed fiscal years")
	}
	if n.PayoutYears == 0 {
		if n.PayoutPct != 0 || !containsWarning(n.Warnings, "payout_missing") {
			return errors.New("missing payout requires warning and zero value")
		}
	}
	if n.PayoutYears > 0 && n.PayoutYears < 3 && n.Confidence == ConfidenceMedium {
		return errors.New("payout history below three years requires low confidence")
	}
	if !almostEqual(n.EPSNormalized, n.NetProfitAnnualMedian/n.TotalShares) {
		return errors.New("normalized EPS does not match earnings and shares")
	}
	if !almostEqual(n.BVPS, n.LatestEquity/n.TotalShares) {
		return errors.New("normalized BVPS does not match equity and shares")
	}
	return nil
}

func validateYearWindow(n Normalized) error {
	if len(n.FiscalYears) == 0 {
		if n.WindowStart != 0 || n.WindowEnd != 0 || len(n.MissingYears) != 0 {
			return errors.New("normalized window has no fiscal years")
		}
		return nil
	}
	if n.WindowStart <= 0 || n.WindowEnd < n.WindowStart {
		return errors.New("invalid normalized window")
	}
	if n.FiscalYears[0] != n.WindowStart || n.FiscalYears[len(n.FiscalYears)-1] != n.WindowEnd {
		return errors.New("normalized window does not match fiscal years")
	}
	if !strictlyIncreasing(n.FiscalYears) {
		return errors.New("fiscal years are not strictly increasing")
	}
	for _, year := range n.FiscalYears {
		if year < n.WindowStart || year > n.WindowEnd {
			return errors.New("fiscal year outside normalized window")
		}
	}
	if !strictlyIncreasing(n.MissingYears) {
		return errors.New("missing years are not strictly increasing")
	}
	fiscal := make(map[int]bool, len(n.FiscalYears))
	for _, year := range n.FiscalYears {
		fiscal[year] = true
	}
	for _, year := range n.MissingYears {
		if year < n.WindowStart || year > n.WindowEnd || fiscal[year] {
			return errors.New("invalid missing year")
		}
	}
	return nil
}

func strictlyIncreasing(values []int) bool {
	for i := 1; i < len(values); i++ {
		if values[i] <= values[i-1] {
			return false
		}
	}
	return true
}

func containsWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if warning == want {
			return true
		}
	}
	return false
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == sha256.Size
}

func almostEqual(a, b float64) bool {
	scale := math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
	return math.Abs(a-b) <= 1e-9*scale
}
