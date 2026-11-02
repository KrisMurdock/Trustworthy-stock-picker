// Package fundamentals owns point-in-time financial facts and normalized
// annual fundamentals used by research valuation. It deliberately does not
// depend on the market or fair-value packages.
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

const (
	Version = 1

	InputModeNormalized  = "normalized"
	NormalizationVersion = 2

	ConfidenceMedium      = "medium"
	ConfidenceLow         = "low"
	ConfidenceUnavailable = "unavailable"

	SourceTushare = "tushare"

	StatementAnnualConsolidated = "annual_consolidated"

	MinWindowYears = 5
	MaxWindowYears = 10
)

// VisibilityMode controls how a historical as-of view treats fetched_at. The
// observed mode is the only valid mode for strict point-in-time research:
// available_from and the actual fetch date must both be on or before as-of.
// VendorBackfill ignores fetched_at for explicit historical reconstruction and
// is always surfaced as a warning by normalization and valuation.
type VisibilityMode string

const (
	VisibilityObserved       VisibilityMode = "observed"
	VisibilityVendorBackfill VisibilityMode = "vendor_backfill"
)

func ParseVisibilityMode(value string) (VisibilityMode, error) {
	mode := VisibilityMode(strings.TrimSpace(value))
	if !mode.Valid() {
		return "", errors.New("visibility mode must be observed or vendor_backfill")
	}
	return mode, nil
}

func (m VisibilityMode) Valid() bool {
	return m == VisibilityObserved || m == VisibilityVendorBackfill
}

// Period is one disclosed annual consolidated financial statement. Monetary
// values are yuan, per-share values are yuan/share, shares are shares, and
// ratios are percentage points. Pointer fields preserve missing values instead
// of treating them as zero.
type Period struct {
	SchemaVersion    int       `json:"schema_version"`
	Code             string    `json:"code"`
	Source           string    `json:"source"`
	ReportEnd        string    `json:"report_end_date"`
	AnnouncementDate string    `json:"announcement_date"`
	AvailableFrom    string    `json:"available_from"`
	StatementBasis   string    `json:"statement_basis"`
	ReportType       string    `json:"report_type"`
	CompType         int       `json:"comp_type"`
	UpdateFlag       int       `json:"update_flag"`
	Revision         string    `json:"revision"`
	FetchedAt        time.Time `json:"fetched_at"`
	RowHash          string    `json:"row_hash"`

	Revenue           *float64 `json:"revenue,omitempty"`
	NetProfit         *float64 `json:"net_profit_attr,omitempty"`
	Equity            *float64 `json:"equity_attr,omitempty"`
	OperatingCashFlow *float64 `json:"operating_cash_flow,omitempty"`
	TotalAssets       *float64 `json:"total_assets,omitempty"`
	TotalLiabilities  *float64 `json:"total_liabilities,omitempty"`
	TotalShares       *float64 `json:"total_shares,omitempty"`
	BasicEPS          *float64 `json:"basic_eps,omitempty"`
	CashDividend      *float64 `json:"cash_dividend,omitempty"`
}

// Series is the append-only set of disclosures fetched for one stock and
// source. Visible performs the point-in-time cut and revision selection.
type Series struct {
	SchemaVersion int       `json:"schema_version"`
	Code          string    `json:"code"`
	Source        string    `json:"source"`
	FetchedAt     time.Time `json:"fetched_at"`
	Periods       []Period  `json:"periods"`
}

// Normalized is the deterministic, point-in-time annual fundamental surface
// used by research valuation. It contains only values derived from annual
// consolidated reports visible at AsOf. Missing optional observations remain
// distinguishable through the corresponding year counts and warnings.
type Normalized struct {
	SchemaVersion          int            `json:"schema_version"`
	Code                   string         `json:"code"`
	AsOf                   string         `json:"as_of"`
	Source                 string         `json:"source"`
	SourceVersion          string         `json:"source_version"`
	InputMode              string         `json:"input_mode"`
	VisibilityMode         VisibilityMode `json:"visibility_mode"`
	WindowStart            int            `json:"window_start"`
	WindowEnd              int            `json:"window_end"`
	FiscalYears            []int          `json:"fiscal_years,omitempty"`
	MissingYears           []int          `json:"missing_years,omitempty"`
	LatestReportEnd        string         `json:"latest_report_end,omitempty"`
	LatestAnnouncementDate string         `json:"latest_announcement_date,omitempty"`
	LatestAvailableFrom    string         `json:"latest_available_from,omitempty"`
	LatestFetchedAt        time.Time      `json:"latest_fetched_at,omitempty"`
	TotalShares            float64        `json:"total_shares"`
	NetProfitAnnualMedian  float64        `json:"net_profit_annual_median"`
	LatestEquity           float64        `json:"latest_equity"`
	AverageEquityMedian    float64        `json:"average_equity_median"`
	ROEPct                 float64        `json:"roe_pct"`
	EPSNormalized          float64        `json:"eps_normalized"`
	BVPS                   float64        `json:"bvps"`
	PayoutPct              float64        `json:"payout_pct"`
	PayoutYears            int            `json:"payout_years"`
	InputDigest            string         `json:"input_digest"`
	Confidence             string         `json:"confidence"`
	Warnings               []string       `json:"warnings,omitempty"`
	Available              bool           `json:"available"`
	Reason                 string         `json:"reason,omitempty"`
}

func (s Series) Validate() error {
	if s.SchemaVersion != Version || !config.ValidCode(s.Code) || s.Source != SourceTushare || s.FetchedAt.IsZero() {
		return errors.New("invalid fundamental series identity")
	}
	if len(s.Periods) == 0 {
		return errors.New("fundamental series has no periods")
	}
	seen := map[string]bool{}
	var latestFetched time.Time
	for _, period := range s.Periods {
		if err := period.Validate(); err != nil {
			return err
		}
		if period.Code != s.Code || period.Source != s.Source {
			return errors.New("fundamental period identity diverges from series")
		}
		key := periodKey(period)
		if seen[key] {
			return errors.New("duplicate fundamental period")
		}
		seen[key] = true
		if period.FetchedAt.After(latestFetched) {
			latestFetched = period.FetchedAt
		}
	}
	if s.FetchedAt.Before(latestFetched) {
		return errors.New("fundamental series fetched_at precedes a period")
	}
	return nil
}

func (p Period) Validate() error {
	if p.SchemaVersion != Version || !config.ValidCode(p.Code) || p.Source != SourceTushare || p.StatementBasis != StatementAnnualConsolidated ||
		p.ReportType != "1" || p.FetchedAt.IsZero() || strings.TrimSpace(p.RowHash) == "" {
		return errors.New("invalid fundamental period identity")
	}
	reportEnd, err := parseDate(p.ReportEnd)
	if err != nil || reportEnd.Month() != time.December || reportEnd.Day() != 31 {
		return errors.New("fundamental report end must be a fiscal year end")
	}
	announced, err := parseDate(p.AnnouncementDate)
	if err != nil || announced.Before(reportEnd) {
		return errors.New("invalid fundamental announcement date")
	}
	available, err := parseDate(p.AvailableFrom)
	if err != nil || !available.After(announced) {
		return errors.New("fundamental available_from must follow announcement")
	}
	if p.CompType < 1 || p.CompType > 7 || p.UpdateFlag < 0 || p.UpdateFlag > 1 {
		return errors.New("invalid fundamental statement classification")
	}
	for name, value := range map[string]*float64{
		"revenue": p.Revenue, "net_profit": p.NetProfit, "equity": p.Equity,
		"operating_cash_flow": p.OperatingCashFlow, "total_assets": p.TotalAssets,
		"total_liabilities": p.TotalLiabilities, "total_shares": p.TotalShares,
		"basic_eps": p.BasicEPS, "cash_dividend": p.CashDividend,
	} {
		if value != nil && !finite(*value) {
			return fmt.Errorf("non-finite fundamental %s", name)
		}
	}
	if p.TotalShares != nil && *p.TotalShares <= 0 {
		return errors.New("non-positive fundamental total shares")
	}
	if p.TotalAssets != nil && *p.TotalAssets <= 0 {
		return errors.New("non-positive fundamental total assets")
	}
	if p.CashDividend != nil && *p.CashDividend < 0 {
		return errors.New("negative fundamental cash dividend")
	}
	return nil
}

// Visible returns annual consolidated periods visible at asOf. Observed mode
// requires both available_from and the actual fetch date to be on or before
// asOf. VendorBackfill ignores fetched_at and is only for explicit historical
// reconstruction. For a repeated report end it keeps the latest visible
// disclosure revision, so future announcements and revisions cannot change an
// earlier observed result.
func (s Series) Visible(asOf string, mode VisibilityMode) []Period {
	if !mode.Valid() {
		return nil
	}
	cutoff, err := parseDate(asOf)
	if err != nil {
		return nil
	}
	byReportEnd := map[string]Period{}
	for _, period := range s.Periods {
		if period.StatementBasis != StatementAnnualConsolidated || period.ReportType != "1" {
			continue
		}
		available, err := parseDate(period.AvailableFrom)
		if err != nil || available.After(cutoff) {
			continue
		}
		if mode == VisibilityObserved && !periodFetchedBy(period.FetchedAt, cutoff) {
			continue
		}
		current, ok := byReportEnd[period.ReportEnd]
		if !ok || laterPeriod(period, current) {
			byReportEnd[period.ReportEnd] = period
		}
	}
	out := make([]Period, 0, len(byReportEnd))
	for _, period := range byReportEnd {
		out = append(out, period)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReportEnd < out[j].ReportEnd })
	return out
}

// AnnualVisible is the safe default: it applies observed-data visibility.
func (s Series) AnnualVisible(asOf string) []Period {
	return s.Visible(asOf, VisibilityObserved)
}

// AnnualVisibleVendorBackfill is intentionally explicit because it can use
// facts that were fetched after asOf. Callers must surface this limitation.
func (s Series) AnnualVisibleVendorBackfill(asOf string) []Period {
	return s.Visible(asOf, VisibilityVendorBackfill)
}

func laterPeriod(candidate, current Period) bool {
	// available_from is a visibility gate, not a revision rank. A correction
	// may move the date earlier; the later observation must still win once both
	// versions are visible. observed mode independently rejects future fetches.
	if !candidate.FetchedAt.Equal(current.FetchedAt) {
		return candidate.FetchedAt.After(current.FetchedAt)
	}
	if candidate.UpdateFlag != current.UpdateFlag {
		return candidate.UpdateFlag > current.UpdateFlag
	}
	if candidate.AnnouncementDate != current.AnnouncementDate {
		return candidate.AnnouncementDate > current.AnnouncementDate
	}
	if candidate.AvailableFrom != current.AvailableFrom {
		return candidate.AvailableFrom > current.AvailableFrom
	}
	return candidate.RowHash > current.RowHash
}

// BatchID is a deterministic identifier for one complete fetch of a series.
// It includes the fetch time and every period digest, allowing storage to keep
// an auditable append-only record of what was observed on each synchronization.
func (s Series) BatchID() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	digests := make([]string, 0, len(s.Periods))
	for _, period := range s.Periods {
		digest, err := period.Digest()
		if err != nil {
			return "", err
		}
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	raw, err := json.Marshal(struct {
		SchemaVersion int       `json:"schema_version"`
		Code          string    `json:"code"`
		Source        string    `json:"source"`
		FetchedAt     time.Time `json:"fetched_at"`
		PeriodDigests []string  `json:"period_digests"`
	}{
		SchemaVersion: s.SchemaVersion,
		Code:          s.Code,
		Source:        s.Source,
		FetchedAt:     s.FetchedAt.UTC(),
		PeriodDigests: digests,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (p Period) Digest() (string, error) {
	canonical := p
	canonical.FetchedAt = p.FetchedAt.UTC()
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// ObservedBy reports whether the period had actually been fetched by asOf.
// available_from is validated separately when building a point-in-time view.
func (p Period) ObservedBy(asOf string) bool {
	cutoff, err := parseDate(asOf)
	if err != nil {
		return false
	}
	return periodFetchedBy(p.FetchedAt, cutoff)
}

func periodKey(period Period) string {
	// available_from is part of the fact identity: a corrected visibility date is
	// a new append-only observation, not an in-place edit of the earlier one.
	return strings.Join([]string{period.Code, period.Source, period.ReportEnd, period.StatementBasis, period.ReportType, period.AnnouncementDate, period.Revision, period.RowHash, period.AvailableFrom}, "|")
}

func parseDate(value string) (time.Time, error) {
	return time.Parse("2006-01-02", value)
}

var shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

func periodFetchedBy(fetchedAt, cutoff time.Time) bool {
	if fetchedAt.IsZero() {
		return false
	}
	// as-of is a Shanghai calendar-day cutoff. The model does not claim
	// intraday visibility; callers needing a specific cutoff time must add one
	// explicitly rather than inferring it from this date comparison.
	fetchedDate := localDate(fetchedAt)
	return !fetchedDate.After(cutoff)
}

func localDate(value time.Time) time.Time {
	year, month, day := value.In(shanghai).Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
