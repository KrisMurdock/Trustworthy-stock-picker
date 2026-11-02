package market

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fundamentals"
)

// NextTradingDayFunc returns the first trading day after an announcement date.
// It is required so every fundamental period has an explicit point-in-time
// availability boundary instead of using the announcement date itself.
type NextTradingDayFunc func(announcementDate string) (string, error)

const (
	tushareIncomeFields   = "ts_code,ann_date,f_ann_date,end_date,report_type,comp_type,basic_eps,n_income_attr_p,total_revenue,revenue,update_flag"
	tushareBalanceFields  = "ts_code,ann_date,f_ann_date,end_date,report_type,comp_type,total_share,total_assets,total_liab,total_hldr_eqy_exc_min_int,update_flag"
	tushareCashflowFields = "ts_code,ann_date,f_ann_date,end_date,report_type,comp_type,n_cashflow_act,update_flag"
	tushareDividendFields = "ts_code,end_date,div_proc,cash_div_tax,cash_div,base_share,imp_ann_date,ann_date"
)

type annualStatementRow struct {
	reportEnd        string
	announcementDate string
	compType         int
	updateFlag       int
	rowHash          string
	fetchedAt        time.Time
	raw              map[string]json.RawMessage
}

type annualDividendRows struct {
	announcementDate string
	total            *float64
	rows             []map[string]json.RawMessage
}

// FetchAnnualFundamentals fetches annual consolidated reports and implemented
// cash dividends for one stock. It selects one disclosure per report period,
// requires income and balance-sheet coverage, and makes cashflow optional.
func (c *TushareClient) FetchAnnualFundamentals(ctx context.Context, stock config.Stock, startReportDate, endReportDate string, nextTradingDay NextTradingDayFunc) (fundamentals.Series, error) {
	if c == nil || c.HTTP == nil || !config.ValidCode(stock.Code) {
		return fundamentals.Series{}, &DataError{Reason: "invalid_tushare_fundamentals_request"}
	}
	if nextTradingDay == nil {
		return fundamentals.Series{}, &DataError{Reason: "missing_next_trading_day_callback"}
	}
	start, err := normalizeFundamentalDate(startReportDate)
	if err != nil {
		return fundamentals.Series{}, &DataError{Reason: "invalid_tushare_fundamentals_start"}
	}
	end, err := normalizeFundamentalDate(endReportDate)
	if err != nil || start > end {
		return fundamentals.Series{}, &DataError{Reason: "invalid_tushare_fundamentals_end"}
	}

	code := stock.Code[2:] + "." + strings.ToUpper(stock.Code[:2])
	params := map[string]string{"ts_code": code, "start_date": compactDate(start), "end_date": compactDate(end)}
	incomeRows, err := c.queryParams(ctx, "income", params, tushareIncomeFields, true)
	if err != nil {
		return fundamentals.Series{}, err
	}
	balanceRows, err := c.queryParams(ctx, "balancesheet", params, tushareBalanceFields, true)
	if err != nil {
		return fundamentals.Series{}, err
	}
	cashflowRows, err := c.queryParams(ctx, "cashflow", params, tushareCashflowFields, true)
	if err != nil {
		return fundamentals.Series{}, err
	}
	dividendRows, err := c.queryParams(ctx, "dividend", map[string]string{"ts_code": code}, tushareDividendFields, true)
	if err != nil {
		return fundamentals.Series{}, err
	}
	if err := ctx.Err(); err != nil {
		return fundamentals.Series{}, err
	}

	fetchedAt := time.Now().In(Shanghai)
	income, err := selectAnnualStatementRows("income", incomeRows, code, start, end, fetchedAt, true)
	if err != nil {
		return fundamentals.Series{}, err
	}
	balance, err := selectAnnualStatementRows("balancesheet", balanceRows, code, start, end, fetchedAt, true)
	if err != nil {
		return fundamentals.Series{}, err
	}
	cashflow, err := selectAnnualStatementRows("cashflow", cashflowRows, code, start, end, fetchedAt, false)
	if err != nil {
		return fundamentals.Series{}, err
	}

	reportEnds := make(map[string]bool, len(income))
	for reportEnd := range income {
		if _, ok := balance[reportEnd]; ok {
			reportEnds[reportEnd] = true
		}
	}
	if len(reportEnds) == 0 {
		return fundamentals.Series{}, &DataError{Reason: "no_complete_tushare_annual_fundamentals"}
	}
	dividends, err := aggregateAnnualDividends(dividendRows, code, reportEnds, fetchedAt)
	if err != nil {
		return fundamentals.Series{}, err
	}

	periods := make([]fundamentals.Period, 0, len(reportEnds))
	for reportEnd := range reportEnds {
		incomeRow := income[reportEnd]
		balanceRow := balance[reportEnd]
		cashflowRow, hasCashflow := cashflow[reportEnd]
		if incomeRow.compType != balanceRow.compType || hasCashflow && cashflowRow.compType != incomeRow.compType {
			return fundamentals.Series{}, &DataError{Reason: "tushare_fundamentals_comp_type_conflict"}
		}
		dividend := dividends[reportEnd]
		announcementDate := maxFundamentalDate(incomeRow.announcementDate, balanceRow.announcementDate)
		updateFlag := maxInt(incomeRow.updateFlag, balanceRow.updateFlag)
		if hasCashflow {
			announcementDate = maxFundamentalDate(announcementDate, cashflowRow.announcementDate)
			updateFlag = maxInt(updateFlag, cashflowRow.updateFlag)
		}
		if dividend != nil {
			announcementDate = maxFundamentalDate(announcementDate, dividend.announcementDate)
		}
		availableFrom, err := fundamentalAvailableFrom(announcementDate, nextTradingDay)
		if err != nil {
			return fundamentals.Series{}, err
		}

		revenue, err := optionalFundamentalNumber(incomeRow.raw, "revenue")
		if err != nil {
			return fundamentals.Series{}, err
		}
		if revenue == nil {
			revenue, err = optionalFundamentalNumber(incomeRow.raw, "total_revenue")
			if err != nil {
				return fundamentals.Series{}, err
			}
		}
		netProfit, err := optionalFundamentalNumber(incomeRow.raw, "n_income_attr_p")
		if err != nil {
			return fundamentals.Series{}, err
		}
		basicEPS, err := optionalFundamentalNumber(incomeRow.raw, "basic_eps")
		if err != nil {
			return fundamentals.Series{}, err
		}
		totalShares, err := optionalFundamentalNumber(balanceRow.raw, "total_share")
		if err != nil {
			return fundamentals.Series{}, err
		}
		totalAssets, err := optionalFundamentalNumber(balanceRow.raw, "total_assets")
		if err != nil {
			return fundamentals.Series{}, err
		}
		totalLiabilities, err := optionalFundamentalNumber(balanceRow.raw, "total_liab")
		if err != nil {
			return fundamentals.Series{}, err
		}
		equity, err := optionalFundamentalNumber(balanceRow.raw, "total_hldr_eqy_exc_min_int")
		if err != nil {
			return fundamentals.Series{}, err
		}
		var operatingCashFlow *float64
		if hasCashflow {
			operatingCashFlow, err = optionalFundamentalNumber(cashflowRow.raw, "n_cashflow_act")
			if err != nil {
				return fundamentals.Series{}, err
			}
		}
		var cashDividend *float64
		var dividendRowsForHash []map[string]json.RawMessage
		if dividend != nil {
			cashDividend = cloneFundamentalNumber(dividend.total)
			dividendRowsForHash = dividend.rows
		}
		rowHash, err := annualFundamentalRowHash(incomeRow.raw, balanceRow.raw, cashflowRow.raw, hasCashflow, dividendRowsForHash)
		if err != nil {
			return fundamentals.Series{}, err
		}
		periods = append(periods, fundamentals.Period{
			SchemaVersion:     fundamentals.Version,
			Code:              stock.Code,
			Source:            fundamentals.SourceTushare,
			ReportEnd:         reportEnd,
			AnnouncementDate:  announcementDate,
			AvailableFrom:     availableFrom,
			StatementBasis:    fundamentals.StatementAnnualConsolidated,
			ReportType:        "1",
			CompType:          incomeRow.compType,
			UpdateFlag:        updateFlag,
			Revision:          annualFundamentalRevision(incomeRow, balanceRow, cashflowRow, hasCashflow, dividend),
			FetchedAt:         fetchedAt,
			RowHash:           rowHash,
			Revenue:           revenue,
			NetProfit:         netProfit,
			Equity:            equity,
			OperatingCashFlow: operatingCashFlow,
			TotalAssets:       totalAssets,
			TotalLiabilities:  totalLiabilities,
			TotalShares:       totalShares,
			BasicEPS:          basicEPS,
			CashDividend:      cashDividend,
		})
	}
	sort.Slice(periods, func(i, j int) bool { return periods[i].ReportEnd < periods[j].ReportEnd })
	series := fundamentals.Series{
		SchemaVersion: fundamentals.Version,
		Code:          stock.Code,
		Source:        fundamentals.SourceTushare,
		FetchedAt:     fetchedAt,
		Periods:       periods,
	}
	if err := series.Validate(); err != nil {
		return fundamentals.Series{}, &DataError{Reason: "invalid_tushare_annual_fundamentals"}
	}
	return series, nil
}

func selectAnnualStatementRows(api string, rows []map[string]json.RawMessage, code, start, end string, fetchedAt time.Time, required bool) (map[string]annualStatementRow, error) {
	byReportEnd := map[string][]annualStatementRow{}
	seen := map[string]bool{}
	for _, raw := range rows {
		row, ok, err := parseAnnualStatementRow(api, raw, code, start, end, fetchedAt)
		if err != nil {
			return nil, err
		}
		if !ok || seen[row.rowHash] {
			continue
		}
		seen[row.rowHash] = true
		byReportEnd[row.reportEnd] = append(byReportEnd[row.reportEnd], row)
	}
	if len(byReportEnd) == 0 {
		if required {
			return nil, &DataError{Reason: "empty_tushare_" + api}
		}
		return map[string]annualStatementRow{}, nil
	}
	selected := make(map[string]annualStatementRow, len(byReportEnd))
	for reportEnd, candidates := range byReportEnd {
		compType := candidates[0].compType
		for _, candidate := range candidates[1:] {
			if candidate.compType != compType {
				return nil, &DataError{Reason: "tushare_fundamentals_comp_type_conflict"}
			}
		}
		latest := candidates[0]
		for _, candidate := range candidates[1:] {
			if newerAnnualStatementRow(candidate, latest) {
				latest = candidate
			}
		}
		selected[reportEnd] = latest
	}
	return selected, nil
}

func parseAnnualStatementRow(api string, raw map[string]json.RawMessage, code, start, end string, fetchedAt time.Time) (annualStatementRow, bool, error) {
	if rawText(raw["ts_code"]) != code {
		return annualStatementRow{}, false, &DataError{Reason: "tushare_fundamentals_stock_mismatch"}
	}
	reportEnd, err := normalizeFundamentalDate(rawText(raw["end_date"]))
	if err != nil {
		return annualStatementRow{}, false, &DataError{Reason: "invalid_tushare_" + api + "_report_end"}
	}
	if reportEnd < start || reportEnd > end {
		return annualStatementRow{}, false, &DataError{Reason: "tushare_fundamentals_report_outside_request"}
	}
	parsedEnd, _ := time.Parse("2006-01-02", reportEnd)
	if parsedEnd.Month() != time.December || parsedEnd.Day() != 31 {
		return annualStatementRow{}, false, nil
	}
	if strings.TrimSpace(rawText(raw["report_type"])) != "1" {
		return annualStatementRow{}, false, nil
	}
	compType, err := parseFundamentalInt(raw["comp_type"], 0)
	if err != nil || compType < 1 || compType > 7 {
		return annualStatementRow{}, false, nil
	}
	announcementDate, err := preferredFundamentalDate(raw, "f_ann_date", "ann_date")
	if err != nil {
		return annualStatementRow{}, false, &DataError{Reason: "invalid_tushare_" + api + "_announcement"}
	}
	announced, err := time.Parse("2006-01-02", announcementDate)
	if err != nil || announced.Before(parsedEnd) {
		return annualStatementRow{}, false, &DataError{Reason: "invalid_tushare_" + api + "_announcement"}
	}
	updateFlag, err := parseFundamentalInt(raw["update_flag"], 0)
	if err != nil || updateFlag < 0 || updateFlag > 1 {
		return annualStatementRow{}, false, &DataError{Reason: "invalid_tushare_" + api + "_update_flag"}
	}
	rowHash, err := fundamentalRawHash(raw)
	if err != nil {
		return annualStatementRow{}, false, &DataError{Reason: "invalid_tushare_" + api + "_row"}
	}
	return annualStatementRow{
		reportEnd:        reportEnd,
		announcementDate: announcementDate,
		compType:         compType,
		updateFlag:       updateFlag,
		rowHash:          rowHash,
		fetchedAt:        fetchedAt,
		raw:              raw,
	}, true, nil
}

func newerAnnualStatementRow(candidate, current annualStatementRow) bool {
	if candidate.announcementDate != current.announcementDate {
		return candidate.announcementDate > current.announcementDate
	}
	if candidate.updateFlag != current.updateFlag {
		return candidate.updateFlag > current.updateFlag
	}
	if !candidate.fetchedAt.Equal(current.fetchedAt) {
		return candidate.fetchedAt.After(current.fetchedAt)
	}
	return candidate.rowHash > current.rowHash
}

func aggregateAnnualDividends(rows []map[string]json.RawMessage, code string, reportEnds map[string]bool, fetchedAt time.Time) (map[string]*annualDividendRows, error) {
	out := map[string]*annualDividendRows{}
	seen := map[string]bool{}
	for _, raw := range rows {
		if rawText(raw["ts_code"]) != code {
			return nil, &DataError{Reason: "tushare_fundamentals_dividend_stock_mismatch"}
		}
		if strings.TrimSpace(rawText(raw["div_proc"])) != "实施" {
			continue
		}
		reportEnd, err := normalizeFundamentalDate(rawText(raw["end_date"]))
		if err != nil {
			return nil, &DataError{Reason: "invalid_tushare_dividend_report_end"}
		}
		if !reportEnds[reportEnd] {
			continue
		}
		rowHash, err := fundamentalRawHash(raw)
		if err != nil || seen[rowHash] {
			if err != nil {
				return nil, &DataError{Reason: "invalid_tushare_dividend_row"}
			}
			continue
		}
		seen[rowHash] = true
		announcementDate, err := preferredFundamentalDate(raw, "imp_ann_date", "ann_date")
		if err != nil {
			return nil, &DataError{Reason: "invalid_tushare_dividend_announcement"}
		}
		aggregate := out[reportEnd]
		if aggregate == nil {
			aggregate = &annualDividendRows{}
			out[reportEnd] = aggregate
		}
		aggregate.announcementDate = maxFundamentalDate(aggregate.announcementDate, announcementDate)
		aggregate.rows = append(aggregate.rows, raw)

		cashPerShare, err := optionalFundamentalNumber(raw, "cash_div_tax")
		if err != nil {
			return nil, err
		}
		if cashPerShare == nil {
			cashPerShare, err = optionalFundamentalNumber(raw, "cash_div")
			if err != nil {
				return nil, err
			}
		}
		baseShares, err := optionalFundamentalNumber(raw, "base_share")
		if err != nil {
			return nil, err
		}
		if cashPerShare == nil || baseShares == nil {
			continue
		}
		amount := *cashPerShare * *baseShares * 10000
		if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
			return nil, &DataError{Reason: "invalid_tushare_dividend_amount"}
		}
		if aggregate.total == nil {
			value := 0.0
			aggregate.total = &value
		}
		*aggregate.total += amount
	}
	return out, nil
}

func fundamentalAvailableFrom(announcementDate string, nextTradingDay NextTradingDayFunc) (string, error) {
	available, err := nextTradingDay(announcementDate)
	if err != nil {
		return "", &DataError{Reason: "next_trading_day_callback_failed"}
	}
	normalized, err := normalizeFundamentalDate(available)
	if err != nil || normalized <= announcementDate {
		return "", &DataError{Reason: "invalid_next_trading_day"}
	}
	return normalized, nil
}

func annualFundamentalRevision(income, balance, cashflow annualStatementRow, hasCashflow bool, dividend *annualDividendRows) string {
	cashflowRevision := "-/0"
	if hasCashflow {
		cashflowRevision = fmt.Sprintf("%s/%d", cashflow.announcementDate, cashflow.updateFlag)
	}
	dividendDate := "-"
	if dividend != nil {
		dividendDate = dividend.announcementDate
	}
	return fmt.Sprintf("income=%s/%d;balance=%s/%d;cashflow=%s;dividend=%s",
		income.announcementDate, income.updateFlag, balance.announcementDate, balance.updateFlag, cashflowRevision, dividendDate)
}

func annualFundamentalRowHash(income, balance, cashflow map[string]json.RawMessage, hasCashflow bool, dividends []map[string]json.RawMessage) (string, error) {
	sortedDividends := append([]map[string]json.RawMessage(nil), dividends...)
	sort.Slice(sortedDividends, func(i, j int) bool {
		left, _ := fundamentalRawHash(sortedDividends[i])
		right, _ := fundamentalRawHash(sortedDividends[j])
		return left < right
	})
	payload := struct {
		Income    map[string]json.RawMessage   `json:"income"`
		Balance   map[string]json.RawMessage   `json:"balance"`
		Cashflow  map[string]json.RawMessage   `json:"cashflow,omitempty"`
		Dividends []map[string]json.RawMessage `json:"dividends,omitempty"`
	}{Income: income, Balance: balance, Dividends: sortedDividends}
	if hasCashflow {
		payload.Cashflow = cashflow
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", &DataError{Reason: "invalid_tushare_fundamentals_row_hash"}
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func fundamentalRawHash(raw map[string]json.RawMessage) (string, error) {
	value, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:]), nil
}

func optionalFundamentalNumber(raw map[string]json.RawMessage, field string) (*float64, error) {
	value, ok := raw[field]
	if !ok || isNullOrBlankJSON(value) {
		return nil, nil
	}
	number, err := numeric(value)
	if err != nil {
		return nil, &DataError{Reason: "invalid_tushare_fundamental_" + field}
	}
	return &number, nil
}

func cloneFundamentalNumber(value *float64) *float64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func preferredFundamentalDate(raw map[string]json.RawMessage, preferred, fallback string) (string, error) {
	for _, field := range []string{preferred, fallback} {
		value := strings.TrimSpace(rawText(raw[field]))
		if value == "" {
			continue
		}
		normalized, err := normalizeFundamentalDate(value)
		if err != nil {
			return "", err
		}
		return normalized, nil
	}
	return "", &DataError{Reason: "missing_tushare_fundamental_announcement"}
}

func parseFundamentalInt(raw json.RawMessage, fallback int) (int, error) {
	value := strings.TrimSpace(rawText(raw))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, &DataError{Reason: "invalid_tushare_fundamental_integer"}
	}
	return parsed, nil
}

func normalizeFundamentalDate(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", &DataError{Reason: "invalid_tushare_fundamental_date"}
	}
	layouts := []string{"20060102", "2006-01-02"}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.Format("2006-01-02"), nil
		}
	}
	return "", &DataError{Reason: "invalid_tushare_fundamental_date"}
}

func rawText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	if trimmed[0] == '"' {
		var value string
		if json.Unmarshal(trimmed, &value) == nil {
			return value
		}
		return ""
	}
	return string(trimmed)
}

func isNullOrBlankJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`))
}

func maxFundamentalDate(left, right string) string {
	if left > right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
