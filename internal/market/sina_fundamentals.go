package market

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

const (
	sinaEPSHardRelativeTolerance    = 0.10
	sinaEPSHardAbsoluteTolerance    = 0.02
	sinaEPSCautionRelativeTolerance = 0.05
	sinaEPSCautionAbsoluteTolerance = 0.01
)

// FundamentalSnapshot is the small, point-in-time financial surface used by
// relative valuation. Sina's jsvar payload exposes trailing-four-quarter EPS,
// prior-year EPS, book value per share and trailing net profit. The fields are
// deliberately kept separate from OHLCV contracts so a failed valuation fetch
// never changes the daily history pipeline.
type FundamentalSnapshot struct {
	Version                 int       `json:"schema_version"`
	Code                    string    `json:"code"`
	Source                  string    `json:"source"`
	FetchedAt               time.Time `json:"fetched_at"`
	EPS                     float64   `json:"eps_ttm"`
	EPSPrior                float64   `json:"eps_prior_year"`
	BookValuePerShare       float64   `json:"book_value_per_share"`
	TTMNetProfitYi          float64   `json:"ttm_net_profit_yi"`
	Shares                  float64   `json:"shares"`
	GrowthKnown             bool      `json:"growth_known"`
	FinancialStatementBasis string    `json:"statement_basis"`
}

func (s FundamentalSnapshot) Validate() error {
	if s.Version != 1 || !config.ValidCode(s.Code) || s.Source != "sina" || s.FetchedAt.IsZero() {
		return &DataError{Reason: "invalid_sina_fundamental_metadata"}
	}
	if s.FinancialStatementBasis != "trailing_four_quarters" {
		return &DataError{Reason: "invalid_sina_fundamental_basis"}
	}
	values := []float64{s.EPS, s.EPSPrior, s.BookValuePerShare, s.TTMNetProfitYi, s.Shares}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return &DataError{Reason: "invalid_sina_fundamental_value"}
		}
	}
	if s.Shares < 0 || s.TTMNetProfitYi < 0 && s.EPS > 0 {
		return &DataError{Reason: "invalid_sina_fundamental_value"}
	}
	if difference, ok := s.epsReconciliationDifference(); ok {
		tolerance := math.Max(sinaEPSHardAbsoluteTolerance, math.Abs(s.EPS)*sinaEPSHardRelativeTolerance)
		if difference > tolerance {
			return &DataError{Reason: "inconsistent_sina_fundamental_eps"}
		}
	}
	return nil
}

// EPSReconciliationNearTolerance reports that Sina's TTM EPS and the EPS
// implied by trailing net profit and total shares are still within the hard
// rejection bound, but close enough to warrant a displayed low-confidence
// warning. The raw values remain available for audit.
func (s FundamentalSnapshot) EPSReconciliationNearTolerance() bool {
	difference, ok := s.epsReconciliationDifference()
	if !ok {
		return false
	}
	caution := math.Max(sinaEPSCautionAbsoluteTolerance, math.Abs(s.EPS)*sinaEPSCautionRelativeTolerance)
	hard := math.Max(sinaEPSHardAbsoluteTolerance, math.Abs(s.EPS)*sinaEPSHardRelativeTolerance)
	return difference > caution && difference <= hard
}

func (s FundamentalSnapshot) epsReconciliationDifference() (float64, bool) {
	if s.Shares <= 0 || s.TTMNetProfitYi == 0 || s.EPS == 0 {
		return 0, false
	}
	impliedEPS := s.TTMNetProfitYi * 100000000 / s.Shares
	return math.Abs(impliedEPS - s.EPS), true
}

var sinaJSNumber = regexp.MustCompile(`(?m)^\s*var\s+(fourQ_mgsy|lastyear_mgsy|mgjzc|profit_four|totalcapital|stock_state)\s*=\s*([-+0-9.eE]+)\s*;`)
var sinaJSString = regexp.MustCompile(`(?m)^\s*var\s+(papercode|a_code|stockname)\s*=\s*'([^']*)'\s*;`)

// FetchFundamentals downloads the public Sina company snapshot. The response is
// a JavaScript assignment file rather than executable code; only the expected
// scalar assignments are parsed, required identity fields are enforced, and
// optional financial fields degrade explicitly instead of being fabricated.
func (c *SinaClient) FetchFundamentals(ctx context.Context, stock config.Stock) (FundamentalSnapshot, error) {
	snapshot := FundamentalSnapshot{Version: 1, Code: stock.Code, Source: "sina",
		FetchedAt: time.Now().In(Shanghai), FinancialStatementBasis: "trailing_four_quarters"}
	if c == nil || c.HTTP == nil || !config.ValidCode(stock.Code) {
		return snapshot, &DataError{Reason: "invalid_sina_fundamental_request"}
	}
	raw, err := c.get(ctx, stock.Code, "jsvar.js")
	if err != nil {
		return snapshot, err
	}
	numbers := map[string]float64{}
	for _, match := range sinaJSNumber.FindAllSubmatch(raw, -1) {
		name := string(match[1])
		if _, exists := numbers[name]; exists {
			return snapshot, &DataError{Reason: "duplicate_sina_fundamental_field"}
		}
		value, parseErr := strconv.ParseFloat(strings.TrimSpace(string(match[2])), 64)
		if parseErr != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return snapshot, &DataError{Reason: "invalid_sina_fundamental_value"}
		}
		numbers[name] = value
	}
	stringsByName := map[string]string{}
	for _, match := range sinaJSString.FindAllSubmatch(raw, -1) {
		name := string(match[1])
		if _, exists := stringsByName[name]; exists {
			return snapshot, &DataError{Reason: "duplicate_sina_fundamental_field"}
		}
		stringsByName[name] = strings.TrimSpace(string(match[2]))
	}
	papercode := stringsByName["papercode"]
	if papercode == "" {
		papercode = stringsByName["a_code"]
	}
	if papercode == "" {
		return snapshot, &DataError{Reason: "missing_sina_fundamental_code"}
	}
	if !strings.EqualFold(papercode, stock.Code) {
		return snapshot, &DataError{Reason: "sina_fundamental_stock_mismatch"}
	}
	if stringsByName["stockname"] == "" {
		return snapshot, &DataError{Reason: "missing_sina_fundamental_name"}
	}
	if _, ok := numbers["fourQ_mgsy"]; !ok {
		return snapshot, &DataError{Reason: "missing_sina_fundamental_field"}
	}
	if _, ok := numbers["stock_state"]; !ok {
		return snapshot, &DataError{Reason: "missing_sina_fundamental_field"}
	}
	if numbers["stock_state"] != 1 {
		return snapshot, &DataError{Reason: "sina_stock_not_normally_listed"}
	}
	snapshot.EPS = numbers["fourQ_mgsy"]
	snapshot.EPSPrior = numbers["lastyear_mgsy"]
	snapshot.BookValuePerShare = numbers["mgjzc"]
	snapshot.TTMNetProfitYi = numbers["profit_four"]
	snapshot.Shares = numbers["totalcapital"] * 10000
	snapshot.GrowthKnown = snapshot.EPSPrior > 0
	if err := snapshot.Validate(); err != nil {
		return FundamentalSnapshot{}, err
	}
	return snapshot, nil
}
