package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fundamentals"
)

type tushareFundamentalsRequest struct {
	API    string            `json:"api_name"`
	Token  string            `json:"token"`
	Params map[string]string `json:"params"`
	Fields string            `json:"fields"`
}

func newTushareFundamentalsClient(t *testing.T, respond func(tushareFundamentalsRequest) any) *TushareClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("unexpected query string %q", r.URL.RawQuery)
		}
		defer r.Body.Close()
		var req tushareFundamentalsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Token != "test-token" {
			t.Errorf("token = %q, want test-token", req.Token)
		}
		response := respond(req)
		if response == nil {
			t.Errorf("nil response for %s", req.API)
			response = map[string]any{"code": 0, "data": nil}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode %s response: %v", req.API, err)
		}
	}))
	c := NewTushareClient(time.Second, 0, "test-token")
	c.Endpoint = srv.URL
	t.Cleanup(func() {
		c.Close()
		srv.Close()
	})
	return c
}

func assertTushareFundamentalsAPI(t *testing.T, req tushareFundamentalsRequest, code, start, end string) {
	t.Helper()
	if req.Params["ts_code"] != code {
		t.Errorf("%s ts_code = %q, want %q", req.API, req.Params["ts_code"], code)
	}
	switch req.API {
	case "income":
		assertTushareFundamentalsStatementRequest(t, req, start, end, tushareIncomeFields)
	case "balancesheet":
		assertTushareFundamentalsStatementRequest(t, req, start, end, tushareBalanceFields)
	case "cashflow":
		assertTushareFundamentalsStatementRequest(t, req, start, end, tushareCashflowFields)
	case "dividend":
		if len(req.Params) != 1 {
			t.Errorf("dividend params = %#v, want ts_code only", req.Params)
		}
		if req.Fields != tushareDividendFields {
			t.Errorf("dividend fields = %q, want %q", req.Fields, tushareDividendFields)
		}
	default:
		t.Errorf("unexpected API %q", req.API)
	}
}

func assertTushareFundamentalsStatementRequest(t *testing.T, req tushareFundamentalsRequest, start, end, fields string) {
	t.Helper()
	if len(req.Params) != 3 || req.Params["start_date"] != start || req.Params["end_date"] != end {
		t.Errorf("%s params = %#v, want ts_code/start_date=%s/end_date=%s", req.API, req.Params, start, end)
	}
	if req.Fields != fields {
		t.Errorf("%s fields = %q, want %q", req.API, req.Fields, fields)
	}
}

func tushareFinancialDataFor(req tushareFundamentalsRequest, rows []map[string]any) map[string]any {
	fields := strings.Split(req.Fields, ",")
	items := make([][]any, 0, len(rows))
	for _, row := range rows {
		values := make([]any, len(fields))
		for i, field := range fields {
			values[i] = row[field]
		}
		items = append(items, values)
	}
	return map[string]any{"code": 0, "data": map[string]any{"fields": fields, "items": items}}
}

func testTushareIncomeRow(reportEnd string, fAnnDate, annDate any, compType, updateFlag int, basicEPS, netProfit, totalRevenue, revenue any) map[string]any {
	return map[string]any{
		"ts_code":         "600036.SH",
		"ann_date":        annDate,
		"f_ann_date":      fAnnDate,
		"end_date":        reportEnd,
		"report_type":     "1",
		"comp_type":       compType,
		"basic_eps":       basicEPS,
		"n_income_attr_p": netProfit,
		"total_revenue":   totalRevenue,
		"revenue":         revenue,
		"update_flag":     updateFlag,
	}
}

func testTushareBalanceRow(reportEnd string, fAnnDate, annDate any, compType, updateFlag int, totalShares, totalAssets, totalLiabilities, equity any) map[string]any {
	return map[string]any{
		"ts_code":                    "600036.SH",
		"ann_date":                   annDate,
		"f_ann_date":                 fAnnDate,
		"end_date":                   reportEnd,
		"report_type":                "1",
		"comp_type":                  compType,
		"total_share":                totalShares,
		"total_assets":               totalAssets,
		"total_liab":                 totalLiabilities,
		"total_hldr_eqy_exc_min_int": equity,
		"update_flag":                updateFlag,
	}
}

func testTushareCashflowRow(reportEnd string, fAnnDate, annDate any, compType, updateFlag int, operatingCashFlow any) map[string]any {
	return map[string]any{
		"ts_code":        "600036.SH",
		"ann_date":       annDate,
		"f_ann_date":     fAnnDate,
		"end_date":       reportEnd,
		"report_type":    "1",
		"comp_type":      compType,
		"n_cashflow_act": operatingCashFlow,
		"update_flag":    updateFlag,
	}
}

func testTushareDividendRow(reportEnd, divProc string, impAnnDate, annDate any, cashDividendTax, cashDividend, baseShares any) map[string]any {
	return map[string]any{
		"ts_code":      "600036.SH",
		"end_date":     reportEnd,
		"div_proc":     divProc,
		"cash_div_tax": cashDividendTax,
		"cash_div":     cashDividend,
		"base_share":   baseShares,
		"imp_ann_date": impAnnDate,
		"ann_date":     annDate,
	}
}

func requireFundamentalPeriod(t *testing.T, series fundamentals.Series, reportEnd string) fundamentals.Period {
	t.Helper()
	for _, period := range series.Periods {
		if period.ReportEnd == reportEnd {
			return period
		}
	}
	t.Fatalf("missing fundamental period %s in %#v", reportEnd, series.Periods)
	return fundamentals.Period{}
}

func requireFundamentalNumber(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %v", name, want)
	}
	if *got != want {
		t.Fatalf("%s = %v, want %v", name, *got, want)
	}
}

func requireFundamentalDataError(t *testing.T, err error, reason string) {
	t.Helper()
	var dataErr *DataError
	if !errors.As(err, &dataErr) {
		t.Fatalf("error = %T %v, want DataError", err, err)
	}
	if dataErr.Reason != reason {
		t.Fatalf("DataError reason = %q, want %q", dataErr.Reason, reason)
	}
}

func TestTushareFundamentalsMergesAnnualReportsDividendsAndPIT(t *testing.T) {
	stock := config.Stock{Code: "sh600036", Name: "招商银行"}
	nextCalls := map[string]int{}
	nextTradingDay := func(date string) (string, error) {
		nextCalls[date]++
		switch date {
		case "2024-06-20":
			return "2024-06-21", nil
		case "2025-06-20":
			return "2025-06-23", nil
		default:
			return "", fmt.Errorf("unexpected announcement date %s", date)
		}
	}

	c := newTushareFundamentalsClient(t, func(req tushareFundamentalsRequest) any {
		assertTushareFundamentalsAPI(t, req, "600036.SH", "20230101", "20241231")
		switch req.API {
		case "income":
			rows := []map[string]any{
				testTushareIncomeRow("20231231", "20240410", "20240412", 1, 0, 1, 100, 1000, 900),
				testTushareIncomeRow("20241231", "20250421", "20250425", 1, 1, 0, nil, 5000, 0),
			}
			rows = append(rows, testTushareIncomeRow("20241231", "20250421", "20250425", 1, 1, 0, nil, 5000, 0))
			return tushareFinancialDataFor(req, rows)
		case "balancesheet":
			return tushareFinancialDataFor(req, []map[string]any{
				testTushareBalanceRow("20231231", "20240411", "20240413", 1, 0, 50, 800, 300, 500),
				testTushareBalanceRow("20241231", nil, "20250422", 1, 0, 100, 2000, 800, 1200),
			})
		case "cashflow":
			return tushareFinancialDataFor(req, []map[string]any{
				testTushareCashflowRow("20231231", "20240409", "20240410", 1, 0, 70),
				testTushareCashflowRow("20241231", "20250423", "20250420", 1, 0, nil),
			})
		case "dividend":
			return tushareFinancialDataFor(req, []map[string]any{
				testTushareDividendRow("20231231", "实施", "20240620", "20240601", 0.25, nil, 4000),
				testTushareDividendRow("20241231", "实施", "20250620", "20250601", 0.5, 0.9, 100000),
				testTushareDividendRow("20241231", "实施", nil, "20250610", nil, 0.25, 40000),
				testTushareDividendRow("20241231", "预案", "20250625", "20250602", 100, nil, 999999),
			})
		default:
			return nil
		}
	})

	series, err := c.FetchAnnualFundamentals(context.Background(), stock, "2023-01-01", "2024-12-31", nextTradingDay)
	if err != nil {
		t.Fatal(err)
	}
	if err := series.Validate(); err != nil {
		t.Fatalf("series validation: %v", err)
	}
	if len(series.Periods) != 2 || series.Periods[0].ReportEnd != "2023-12-31" || series.Periods[1].ReportEnd != "2024-12-31" {
		t.Fatalf("periods = %#v, want sorted 2023 and 2024", series.Periods)
	}
	if series.SchemaVersion != fundamentals.Version || series.Code != stock.Code || series.Source != fundamentals.SourceTushare || series.FetchedAt.IsZero() {
		t.Fatalf("invalid series identity: %#v", series)
	}

	period2023 := requireFundamentalPeriod(t, series, "2023-12-31")
	period2024 := requireFundamentalPeriod(t, series, "2024-12-31")
	if !period2023.FetchedAt.Equal(series.FetchedAt) || !period2024.FetchedAt.Equal(series.FetchedAt) {
		t.Fatalf("period fetched_at values are not unified: %#v", series.Periods)
	}
	if period2023.AnnouncementDate != "2024-06-20" || period2023.AvailableFrom != "2024-06-21" {
		t.Fatalf("2023 PIT dates = %q/%q", period2023.AnnouncementDate, period2023.AvailableFrom)
	}
	if period2024.AnnouncementDate != "2025-06-20" || period2024.AvailableFrom != "2025-06-23" {
		t.Fatalf("2024 PIT dates = %q/%q", period2024.AnnouncementDate, period2024.AvailableFrom)
	}
	if period2024.CompType != 1 || period2024.UpdateFlag != 1 || period2024.Revision == "" || len(period2024.RowHash) != 64 {
		t.Fatalf("invalid revision metadata: %#v", period2024)
	}
	if period2024.StatementBasis != fundamentals.StatementAnnualConsolidated || period2024.ReportType != "1" {
		t.Fatalf("invalid statement classification: %#v", period2024)
	}

	requireFundamentalNumber(t, "2024 revenue", period2024.Revenue, 0)
	requireFundamentalNumber(t, "2024 basic EPS", period2024.BasicEPS, 0)
	if period2024.NetProfit != nil || period2024.OperatingCashFlow != nil {
		t.Fatalf("nullable 2024 values were filled: net=%v cashflow=%v", period2024.NetProfit, period2024.OperatingCashFlow)
	}
	requireFundamentalNumber(t, "2024 total shares", period2024.TotalShares, 100)
	requireFundamentalNumber(t, "2024 total assets", period2024.TotalAssets, 2000)
	requireFundamentalNumber(t, "2024 total liabilities", period2024.TotalLiabilities, 800)
	requireFundamentalNumber(t, "2024 equity", period2024.Equity, 1200)
	requireFundamentalNumber(t, "2024 cash dividend", period2024.CashDividend, 600000000)
	requireFundamentalNumber(t, "2023 net profit", period2023.NetProfit, 100)
	requireFundamentalNumber(t, "2023 operating cash flow", period2023.OperatingCashFlow, 70)
	requireFundamentalNumber(t, "2023 cash dividend", period2023.CashDividend, 10000000)

	if nextCalls["2024-06-20"] != 1 || nextCalls["2025-06-20"] != 1 || len(nextCalls) != 2 {
		t.Fatalf("next trading day calls = %#v", nextCalls)
	}
}

func TestTushareFundamentalsSelectsLatestRevisionAndPrefersFinalAnnouncement(t *testing.T) {
	stock := config.Stock{Code: "sh600036", Name: "招商银行"}
	nextCalls := 0
	nextTradingDay := func(date string) (string, error) {
		nextCalls++
		if date != "2025-04-15" {
			return "", fmt.Errorf("announcement = %s, want 2025-04-15", date)
		}
		return "2025-04-16", nil
	}

	c := newTushareFundamentalsClient(t, func(req tushareFundamentalsRequest) any {
		assertTushareFundamentalsAPI(t, req, "600036.SH", "20240101", "20241231")
		switch req.API {
		case "income":
			return tushareFinancialDataFor(req, []map[string]any{
				testTushareIncomeRow("20241231", "20250401", "20250402", 1, 0, 1, 10, 100, 90),
				testTushareIncomeRow("20241231", "20250414", "20250416", 1, 0, 2, 20, 200, 180),
				testTushareIncomeRow("20241231", "20250414", "20250417", 1, 1, 3, 30, 300, 270),
				testTushareIncomeRow("20241231", "20250414", "20250417", 1, 1, 3, 30, 300, 270),
			})
		case "balancesheet":
			return tushareFinancialDataFor(req, []map[string]any{
				testTushareBalanceRow("20241231", nil, "20250415", 1, 0, 100, 1000, 400, 600),
			})
		case "cashflow":
			return tushareFinancialDataFor(req, []map[string]any{
				testTushareCashflowRow("20241231", "20250413", "20250418", 1, 0, 55),
			})
		case "dividend":
			return tushareFinancialDataFor(req, nil)
		default:
			return nil
		}
	})

	series, err := c.FetchAnnualFundamentals(context.Background(), stock, "2024-01-01", "2024-12-31", nextTradingDay)
	if err != nil {
		t.Fatal(err)
	}
	period := requireFundamentalPeriod(t, series, "2024-12-31")
	if period.AnnouncementDate != "2025-04-15" || period.AvailableFrom != "2025-04-16" {
		t.Fatalf("PIT dates = %q/%q, want 2025-04-15/2025-04-16", period.AnnouncementDate, period.AvailableFrom)
	}
	if period.UpdateFlag != 1 {
		t.Fatalf("update flag = %d, want 1", period.UpdateFlag)
	}
	requireFundamentalNumber(t, "net profit", period.NetProfit, 30)
	requireFundamentalNumber(t, "revenue", period.Revenue, 270)
	requireFundamentalNumber(t, "basic EPS", period.BasicEPS, 3)
	if period.Revision == "" || len(period.RowHash) != 64 {
		t.Fatalf("invalid revision metadata: %#v", period)
	}

	second, err := c.FetchAnnualFundamentals(context.Background(), stock, "2024-01-01", "2024-12-31", nextTradingDay)
	if err != nil {
		t.Fatal(err)
	}
	secondPeriod := requireFundamentalPeriod(t, second, "2024-12-31")
	if secondPeriod.RowHash != period.RowHash || secondPeriod.Revision != period.Revision {
		t.Fatalf("revision is not deterministic: first=%q/%q second=%q/%q", period.Revision, period.RowHash, secondPeriod.Revision, secondPeriod.RowHash)
	}
	if nextCalls != 2 {
		t.Fatalf("next trading day calls = %d, want 2", nextCalls)
	}
}

func TestTushareFundamentalsFiltersNonAnnualAndNonConsolidatedRows(t *testing.T) {
	stock := config.Stock{Code: "sh600036", Name: "招商银行"}
	c := newTushareFundamentalsClient(t, func(req tushareFundamentalsRequest) any {
		assertTushareFundamentalsAPI(t, req, "600036.SH", "20240101", "20241231")
		switch req.API {
		case "income":
			parent := testTushareIncomeRow("20241231", "20250425", "20250426", 1, 0, 8, 8888, 8888, 8888)
			parent["report_type"] = "6"
			singleQuarter := testTushareIncomeRow("20241231", "20250426", "20250427", 1, 0, 5, 5555, 5555, 5555)
			singleQuarter["report_type"] = "2"
			return tushareFinancialDataFor(req, []map[string]any{
				testTushareIncomeRow("20241231", "20250410", "20250412", 1, 0, 1, 100, 1000, 1000),
				testTushareIncomeRow("20240930", "20250420", "20250421", 1, 0, 9, 9999, 9999, 9999),
				parent,
				singleQuarter,
				testTushareIncomeRow("20241231", "20250430", "20250501", 8, 0, 7, 7777, 7777, 7777),
			})
		case "balancesheet":
			parent := testTushareBalanceRow("20241231", "20250425", "20250426", 1, 0, 100, 2000, 900, 1100)
			parent["report_type"] = "6"
			return tushareFinancialDataFor(req, []map[string]any{
				testTushareBalanceRow("20241231", "20250410", "20250412", 1, 0, 100, 1000, 400, 600),
				testTushareBalanceRow("20240930", "20250420", "20250421", 1, 0, 100, 1000, 400, 600),
				parent,
			})
		case "cashflow":
			return tushareFinancialDataFor(req, nil)
		case "dividend":
			return tushareFinancialDataFor(req, nil)
		default:
			return nil
		}
	})

	series, err := c.FetchAnnualFundamentals(context.Background(), stock, "2024-01-01", "2024-12-31", func(date string) (string, error) {
		if date != "2025-04-10" {
			return "", fmt.Errorf("announcement = %s, want 2025-04-10", date)
		}
		return "2025-04-11", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Periods) != 1 || series.Periods[0].ReportEnd != "2024-12-31" {
		t.Fatalf("periods = %#v, want only the consolidated annual report", series.Periods)
	}
	period := series.Periods[0]
	requireFundamentalNumber(t, "net profit", period.NetProfit, 100)
	requireFundamentalNumber(t, "revenue", period.Revenue, 1000)
	requireFundamentalNumber(t, "equity", period.Equity, 600)
}

func TestTushareFundamentalsRejectsMalformedResponses(t *testing.T) {
	stock := config.Stock{Code: "sh600036", Name: "招商银行"}
	tests := []struct {
		name          string
		respond       func(tushareFundamentalsRequest) any
		wantReason    string
		wantHTTPState int
	}{
		{
			name:       "invalid envelope",
			respond:    func(tushareFundamentalsRequest) any { return map[string]any{"msg": "missing code"} },
			wantReason: "invalid_tushare_envelope",
		},
		{
			name:          "api error",
			respond:       func(tushareFundamentalsRequest) any { return map[string]any{"code": 2002, "msg": "denied"} },
			wantHTTPState: http.StatusForbidden,
		},
		{
			name: "missing requested field",
			respond: func(req tushareFundamentalsRequest) any {
				fields := strings.Split(req.Fields, ",")
				fields = fields[1:]
				return map[string]any{"code": 0, "data": map[string]any{
					"fields": fields,
					"items":  [][]any{make([]any, len(fields))},
				}}
			},
			wantReason: "missing_tushare_field",
		},
		{
			name: "stock code mismatch",
			respond: func(req tushareFundamentalsRequest) any {
				row := testTushareIncomeRow("20241231", "20250410", "20250412", 1, 0, 1, 100, 1000, 1000)
				row["ts_code"] = "000001.SZ"
				return tushareFinancialDataFor(req, []map[string]any{row})
			},
			wantReason: "tushare_fundamentals_stock_mismatch",
		},
		{
			name: "empty required statement",
			respond: func(req tushareFundamentalsRequest) any {
				return tushareFinancialDataFor(req, nil)
			},
			wantReason: "empty_tushare_income",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := newTushareFundamentalsClient(t, test.respond)
			_, err := c.FetchAnnualFundamentals(context.Background(), stock, "2024-01-01", "2024-12-31", func(string) (string, error) {
				return "2025-04-16", nil
			})
			if test.wantHTTPState != 0 {
				var failure *Failure
				if !errors.As(err, &failure) || failure.Status != test.wantHTTPState {
					t.Fatalf("error = %T %v, want HTTP %d Failure", err, err, test.wantHTTPState)
				}
				return
			}
			requireFundamentalDataError(t, err, test.wantReason)
		})
	}
}

func TestTushareFundamentalsRejectsCompTypeConflicts(t *testing.T) {
	stock := config.Stock{Code: "sh600036", Name: "招商银行"}
	tests := []struct {
		name    string
		income  []map[string]any
		balance []map[string]any
	}{
		{
			name: "same statement table",
			income: []map[string]any{
				testTushareIncomeRow("20241231", "20250410", "20250412", 1, 0, 1, 100, 1000, 1000),
				testTushareIncomeRow("20241231", "20250411", "20250413", 2, 0, 1, 100, 1000, 1000),
			},
			balance: []map[string]any{
				testTushareBalanceRow("20241231", "20250410", "20250412", 1, 0, 100, 1000, 400, 600),
			},
		},
		{
			name: "across statement tables",
			income: []map[string]any{
				testTushareIncomeRow("20241231", "20250410", "20250412", 1, 0, 1, 100, 1000, 1000),
			},
			balance: []map[string]any{
				testTushareBalanceRow("20241231", "20250411", "20250413", 2, 0, 100, 1000, 400, 600),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := newTushareFundamentalsClient(t, func(req tushareFundamentalsRequest) any {
				assertTushareFundamentalsAPI(t, req, "600036.SH", "20240101", "20241231")
				switch req.API {
				case "income":
					return tushareFinancialDataFor(req, test.income)
				case "balancesheet":
					return tushareFinancialDataFor(req, test.balance)
				case "cashflow", "dividend":
					return tushareFinancialDataFor(req, nil)
				default:
					return nil
				}
			})
			_, err := c.FetchAnnualFundamentals(context.Background(), stock, "2024-01-01", "2024-12-31", func(string) (string, error) {
				return "2025-04-14", nil
			})
			requireFundamentalDataError(t, err, "tushare_fundamentals_comp_type_conflict")
		})
	}
}

func TestTushareFundamentalsRequiresValidNextTradingDay(t *testing.T) {
	stock := config.Stock{Code: "sh600036", Name: "招商银行"}
	tests := []struct {
		name   string
		next   NextTradingDayFunc
		reason string
	}{
		{
			name:   "missing callback",
			next:   nil,
			reason: "missing_next_trading_day_callback",
		},
		{
			name: "callback error",
			next: func(string) (string, error) {
				return "", errors.New("calendar unavailable")
			},
			reason: "next_trading_day_callback_failed",
		},
		{
			name: "not after announcement",
			next: func(string) (string, error) {
				return "2025-04-10", nil
			},
			reason: "invalid_next_trading_day",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := newTushareFundamentalsClient(t, func(req tushareFundamentalsRequest) any {
				assertTushareFundamentalsAPI(t, req, "600036.SH", "20240101", "20241231")
				switch req.API {
				case "income":
					return tushareFinancialDataFor(req, []map[string]any{
						testTushareIncomeRow("20241231", "20250410", "20250412", 1, 0, 1, 100, 1000, 1000),
					})
				case "balancesheet":
					return tushareFinancialDataFor(req, []map[string]any{
						testTushareBalanceRow("20241231", "20250410", "20250412", 1, 0, 100, 1000, 400, 600),
					})
				case "cashflow", "dividend":
					return tushareFinancialDataFor(req, nil)
				default:
					return nil
				}
			})
			_, err := c.FetchAnnualFundamentals(context.Background(), stock, "2024-01-01", "2024-12-31", test.next)
			requireFundamentalDataError(t, err, test.reason)
		})
	}
}

func TestTushareFundamentalsAllowsMissingCashflowAndDividendAmount(t *testing.T) {
	stock := config.Stock{Code: "sh600036", Name: "招商银行"}
	nextCalls := map[string]int{}
	nextTradingDay := func(date string) (string, error) {
		nextCalls[date]++
		switch date {
		case "2024-05-20":
			return "2024-05-21", nil
		case "2025-04-10":
			return "2025-04-11", nil
		default:
			return "", fmt.Errorf("unexpected announcement date %s", date)
		}
	}
	c := newTushareFundamentalsClient(t, func(req tushareFundamentalsRequest) any {
		assertTushareFundamentalsAPI(t, req, "600036.SH", "20230101", "20241231")
		switch req.API {
		case "income":
			return tushareFinancialDataFor(req, []map[string]any{
				testTushareIncomeRow("20231231", "20240410", "20240411", 1, 0, 1, 77, 1234, nil),
				testTushareIncomeRow("20241231", "20250410", "20250411", 1, 0, 2, 88, 2000, 2000),
			})
		case "balancesheet":
			return tushareFinancialDataFor(req, []map[string]any{
				testTushareBalanceRow("20231231", "20240410", "20240411", 1, 0, 100, 1000, 400, 600),
				testTushareBalanceRow("20241231", "20250410", "20250411", 1, 0, 100, 1100, 400, 700),
			})
		case "cashflow":
			return tushareFinancialDataFor(req, nil)
		case "dividend":
			return tushareFinancialDataFor(req, []map[string]any{
				testTushareDividendRow("20231231", "实施", "20240520", "20240501", nil, nil, 10000),
			})
		default:
			return nil
		}
	})

	series, err := c.FetchAnnualFundamentals(context.Background(), stock, "2023-01-01", "2024-12-31", nextTradingDay)
	if err != nil {
		t.Fatal(err)
	}
	period2023 := requireFundamentalPeriod(t, series, "2023-12-31")
	period2024 := requireFundamentalPeriod(t, series, "2024-12-31")
	requireFundamentalNumber(t, "2023 revenue fallback", period2023.Revenue, 1234)
	if period2023.OperatingCashFlow != nil || period2023.CashDividend != nil {
		t.Fatalf("missing cashflow or dividend amount was filled: %#v", period2023)
	}
	if period2024.OperatingCashFlow != nil || period2024.CashDividend != nil {
		t.Fatalf("missing cashflow or dividend row was filled: %#v", period2024)
	}
	if period2023.AnnouncementDate != "2024-05-20" || period2023.AvailableFrom != "2024-05-21" {
		t.Fatalf("2023 PIT dates = %q/%q", period2023.AnnouncementDate, period2023.AvailableFrom)
	}
	if period2024.AnnouncementDate != "2025-04-10" || period2024.AvailableFrom != "2025-04-11" {
		t.Fatalf("2024 PIT dates = %q/%q", period2024.AnnouncementDate, period2024.AvailableFrom)
	}
	if nextCalls["2024-05-20"] != 1 || nextCalls["2025-04-10"] != 1 || len(nextCalls) != 2 {
		t.Fatalf("next trading day calls = %#v", nextCalls)
	}
}

func TestTushareFundamentalsConvertsShanghaiAndShenzhenCodes(t *testing.T) {
	tests := []struct {
		stock       config.Stock
		tushareCode string
	}{
		{stock: config.Stock{Code: "sh600036", Name: "招商银行"}, tushareCode: "600036.SH"},
		{stock: config.Stock{Code: "sz000001", Name: "平安银行"}, tushareCode: "000001.SZ"},
	}
	for _, test := range tests {
		t.Run(test.stock.Code, func(t *testing.T) {
			c := newTushareFundamentalsClient(t, func(req tushareFundamentalsRequest) any {
				assertTushareFundamentalsAPI(t, req, test.tushareCode, "20240101", "20241231")
				switch req.API {
				case "income":
					row := testTushareIncomeRow("20241231", "20250410", "20250411", 1, 0, 1, 100, 1000, 1000)
					row["ts_code"] = test.tushareCode
					return tushareFinancialDataFor(req, []map[string]any{row})
				case "balancesheet":
					row := testTushareBalanceRow("20241231", "20250410", "20250411", 1, 0, 100, 1000, 400, 600)
					row["ts_code"] = test.tushareCode
					return tushareFinancialDataFor(req, []map[string]any{row})
				case "cashflow", "dividend":
					return tushareFinancialDataFor(req, nil)
				default:
					return nil
				}
			})
			series, err := c.FetchAnnualFundamentals(context.Background(), test.stock, "2024-01-01", "2024-12-31", func(string) (string, error) {
				return "2025-04-14", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if series.Code != test.stock.Code || len(series.Periods) != 1 || series.Periods[0].Code != test.stock.Code {
				t.Fatalf("series code = %q, periods = %#v", series.Code, series.Periods)
			}
		})
	}
}
