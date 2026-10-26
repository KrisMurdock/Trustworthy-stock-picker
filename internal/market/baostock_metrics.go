package market

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

const baoMetricFields = "date,code,turn,amount,tradestatus"

// DailyMetric is the turnover and amount observation that is deliberately kept
// outside the OHLCV Bar contract. TurnoverRatePct follows BaoStock's percentage
// unit (for example, 2.35 means 2.35%).
type DailyMetric struct {
	Date            string  `json:"date"`
	TurnoverRatePct float64 `json:"turnover_rate_pct"`
	AmountCNY       float64 `json:"amount_cny"`
}

type DailyMetricHistory struct {
	Version   int           `json:"schema_version"`
	Code      string        `json:"code"`
	Source    string        `json:"source"`
	FetchedAt time.Time     `json:"fetched_at"`
	Metrics   []DailyMetric `json:"metrics"`
}

func (h DailyMetricHistory) Validate() error {
	if h.Version != 1 || !config.ValidCode(h.Code) || (h.Source != "baostock" && h.Source != "sohu") || h.FetchedAt.IsZero() || len(h.Metrics) == 0 {
		return fmt.Errorf("invalid daily metric history metadata")
	}
	previous := ""
	for _, metric := range h.Metrics {
		parsed, err := time.Parse("2006-01-02", metric.Date)
		if err != nil || parsed.Format("2006-01-02") != metric.Date || metric.Date <= previous {
			return fmt.Errorf("invalid daily metric date order")
		}
		if math.IsNaN(metric.TurnoverRatePct) || math.IsInf(metric.TurnoverRatePct, 0) || metric.TurnoverRatePct < 0 ||
			math.IsNaN(metric.AmountCNY) || math.IsInf(metric.AmountCNY, 0) || metric.AmountCNY < 0 {
			return fmt.Errorf("invalid daily metric value")
		}
		previous = metric.Date
	}
	return nil
}

// FetchDailyMetrics loads the historical turnover rate and traded amount in
// Yuan. It uses a separate BaoStock query so the existing OHLCV contract and
// source fallback behavior remain unchanged.
func (c *BaoStockClient) FetchDailyMetrics(ctx context.Context, stock config.Stock, start, end string) (DailyMetricHistory, error) {
	history := DailyMetricHistory{Version: 1, Code: stock.Code, Source: "baostock", FetchedAt: time.Now().In(Shanghai)}
	if !config.ValidCode(stock.Code) {
		return history, &DataError{Reason: "invalid_daily_metric_code"}
	}
	first, err := time.Parse("2006-01-02", start)
	if err != nil {
		return history, &DataError{Reason: "invalid_daily_metric_window"}
	}
	last, err := time.Parse("2006-01-02", end)
	if err != nil || first.After(last) {
		return history, &DataError{Reason: "invalid_daily_metric_window"}
	}
	code := stock.Code[:2] + "." + stock.Code[2:]
	for from := first; !from.After(last); {
		to := time.Date(from.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
		if to.After(last) {
			to = last
		}
		metrics, err := c.fetchDailyMetricYear(ctx, code, from.Format("2006-01-02"), to.Format("2006-01-02"))
		if err != nil {
			return DailyMetricHistory{}, err
		}
		history.Metrics = append(history.Metrics, metrics...)
		from = to.AddDate(0, 0, 1)
	}
	sort.Slice(history.Metrics, func(i, j int) bool { return history.Metrics[i].Date < history.Metrics[j].Date })
	if err := history.Validate(); err != nil {
		return DailyMetricHistory{}, &DataError{Reason: err.Error()}
	}
	return history, nil
}

func (c *BaoStockClient) fetchDailyMetricYear(ctx context.Context, code, pageStart, pageEnd string) ([]DailyMetric, error) {
	session, cleanup, err := c.openSession(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var out []DailyMetric
	complete := false
	for page := 1; page <= 2; page++ {
		if err := c.pacer.wait(ctx); err != nil {
			return nil, err
		}
		body := strings.Join([]string{"query_history_k_data_plus", "anonymous", strconv.Itoa(page), strconv.Itoa(baoPageSize), code, baoMetricFields, pageStart, pageEnd, "d", "3"}, baoSeparator)
		parts, err := session.exchange(ctx, "95", "96", body)
		if err != nil {
			return nil, fmt.Errorf("baostock metric year %s page %d: %w", pageStart[:4], page, err)
		}
		if len(parts) < 13 || parts[2] != "query_history_k_data_plus" || parts[3] != "anonymous" || parts[4] != strconv.Itoa(page) || parts[5] != strconv.Itoa(baoPageSize) || parts[7] != code || parts[8] != baoMetricFields || parts[9] != pageStart || parts[10] != pageEnd || parts[11] != "d" || parts[12] != "3" {
			return nil, &DataError{Reason: "baostock_metric_query_mismatch"}
		}
		var data struct {
			Records [][]string `json:"record"`
		}
		if parts[6] != "" && json.Unmarshal([]byte(parts[6]), &data) != nil {
			return nil, &DataError{Reason: "invalid_baostock_metric_records"}
		}
		if len(data.Records) > baoPageSize {
			return nil, &DataError{Reason: "invalid_baostock_metric_page_size"}
		}
		for _, row := range data.Records {
			if len(row) != 5 || row[1] != code || row[0] < pageStart || row[0] > pageEnd {
				return nil, &DataError{Reason: "invalid_baostock_metric_row"}
			}
			switch row[4] {
			case "0":
				continue
			case "1":
			default:
				return nil, &DataError{Reason: "invalid_baostock_metric_trade_status"}
			}
			turnover, err := numeric(json.RawMessage(row[2]))
			if err != nil {
				return nil, err
			}
			amount, err := numeric(json.RawMessage(row[3]))
			if err != nil {
				return nil, err
			}
			out = append(out, DailyMetric{Date: row[0], TurnoverRatePct: turnover, AmountCNY: amount})
		}
		if len(data.Records) < baoPageSize {
			complete = true
			break
		}
	}
	if !complete {
		return nil, &DataError{Reason: "baostock_metric_exceeds_limit"}
	}
	return out, nil
}
