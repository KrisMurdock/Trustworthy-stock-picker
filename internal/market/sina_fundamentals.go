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
	TTMNetProfitCNY         float64   `json:"ttm_net_profit_cny"`
	Shares                  float64   `json:"shares"`
	GrowthKnown             bool      `json:"growth_known"`
	FinancialStatementBasis string    `json:"statement_basis"`
}

func (s FundamentalSnapshot) Validate() error {
	if s.Version != 1 || !config.ValidCode(s.Code) || s.Source != "sina" || s.FetchedAt.IsZero() {
		return &DataError{Reason: "invalid_sina_fundamental_metadata"}
	}
	if s.FinancialStatementBasis != "trailing_four_quarters" || s.Shares <= 0 {
		return &DataError{Reason: "invalid_sina_fundamental_basis"}
	}
	values := []float64{s.EPS, s.EPSPrior, s.BookValuePerShare, s.TTMNetProfitCNY, s.Shares}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return &DataError{Reason: "invalid_sina_fundamental_value"}
		}
	}
	if s.EPS == 0 || s.TTMNetProfitCNY == 0 {
		return &DataError{Reason: "invalid_sina_fundamental_value"}
	}
	impliedEPS := s.TTMNetProfitCNY * 100000000 / s.Shares
	tolerance := math.Max(0.05, math.Abs(s.EPS)*0.20)
	if math.Abs(impliedEPS-s.EPS) > tolerance {
		return &DataError{Reason: "inconsistent_sina_fundamental_eps"}
	}
	return nil
}

var sinaJSNumber = regexp.MustCompile(`(?m)^\s*var\s+(fourQ_mgsy|lastyear_mgsy|mgjzc|profit_four|totalcapital|stock_state)\s*=\s*([-+0-9.eE]+)\s*;`)
var sinaJSString = regexp.MustCompile(`(?m)^\s*var\s+(papercode|a_code|stockname)\s*=\s*'([^']*)'\s*;`)

// FetchFundamentals downloads the public Sina company snapshot. The response is
// a JavaScript assignment file rather than executable code; only the expected
// scalar assignments are parsed and all missing, duplicated or malformed fields
// are rejected before a snapshot reaches the valuation model.
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
	if papercode != "" && !strings.EqualFold(papercode, stock.Code) {
		return snapshot, &DataError{Reason: "sina_fundamental_stock_mismatch"}
	}
	if stringsByName["stockname"] == "" {
		return snapshot, &DataError{Reason: "missing_sina_fundamental_name"}
	}
	for _, name := range []string{"fourQ_mgsy", "lastyear_mgsy", "mgjzc", "profit_four", "totalcapital", "stock_state"} {
		if _, ok := numbers[name]; !ok {
			return snapshot, &DataError{Reason: "missing_sina_fundamental_field"}
		}
	}
	if numbers["stock_state"] != 1 {
		return snapshot, &DataError{Reason: "sina_stock_not_normally_listed"}
	}
	snapshot.EPS = numbers["fourQ_mgsy"]
	snapshot.EPSPrior = numbers["lastyear_mgsy"]
	snapshot.BookValuePerShare = numbers["mgjzc"]
	snapshot.TTMNetProfitCNY = numbers["profit_four"]
	snapshot.Shares = numbers["totalcapital"] * 10000
	snapshot.GrowthKnown = snapshot.EPSPrior > 0
	if err := snapshot.Validate(); err != nil {
		return FundamentalSnapshot{}, err
	}
	return snapshot, nil
}
