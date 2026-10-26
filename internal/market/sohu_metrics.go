package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

// SohuMetricsClient is a research-only daily metric source. Sohu's historical
// quote feed returns turnover rate and traded amount in one response, which
// keeps multi-year feature studies independent from BaoStock's short-lived
// sessions. It is not used by the production sync pipeline.
type SohuMetricsClient struct {
	HTTP     *http.Client
	Endpoint string
	pacer    requestPacer
}

func NewSohuMetricsClient(timeout, interval time.Duration) *SohuMetricsClient {
	return &SohuMetricsClient{
		HTTP:     newHTTPClient(timeout),
		Endpoint: "https://q.stock.sohu.com/hisHq",
		pacer:    requestPacer{interval: interval},
	}
}

func (c *SohuMetricsClient) Close() { c.HTTP.CloseIdleConnections() }

func (c *SohuMetricsClient) FetchDailyMetrics(ctx context.Context, stock config.Stock, start, end string) (DailyMetricHistory, error) {
	history := DailyMetricHistory{Version: 1, Code: stock.Code, Source: "sohu", FetchedAt: time.Now().In(Shanghai)}
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
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return history, &Failure{Status: 400, Kind: "request"}
	}
	query := u.Query()
	query.Set("code", "cn_"+stock.Code[2:])
	query.Set("start", compactDate(start))
	query.Set("end", compactDate(end))
	query.Set("stat", "1")
	query.Set("order", "D")
	query.Set("period", "d")
	query.Set("rt", "jsonp")
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return history, &Failure{Status: 400, Kind: "request"}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	if err := c.pacer.wait(ctx); err != nil {
		return history, err
	}
	raw, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return history, err
	}
	history.Metrics, err = parseSohuMetrics(raw, stock.Code, start, end)
	if err != nil {
		return DailyMetricHistory{}, err
	}
	if err := history.Validate(); err != nil {
		return DailyMetricHistory{}, &DataError{Reason: err.Error()}
	}
	return history, nil
}

func parseSohuMetrics(raw []byte, code, start, end string) ([]DailyMetric, error) {
	text := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(text, "callback(") || !strings.HasSuffix(text, ")") {
		return nil, &DataError{Reason: "invalid_sohu_metric_envelope"}
	}
	text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(text[len("callback("):], ")"), ";"))
	var payload []struct {
		Status int        `json:"status"`
		Code   string     `json:"code"`
		Rows   [][]string `json:"hq"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil || len(payload) != 1 {
		return nil, &DataError{Reason: "invalid_sohu_metric_payload"}
	}
	item := payload[0]
	if item.Status != 0 || item.Code != "cn_"+code[2:] || len(item.Rows) == 0 {
		return nil, &DataError{Reason: "sohu_metric_stock_mismatch"}
	}
	metrics := make([]DailyMetric, 0, len(item.Rows))
	seen := make(map[string]bool, len(item.Rows))
	for _, row := range item.Rows {
		if len(row) < 10 || row[0] < start || row[0] > end || seen[row[0]] {
			return nil, &DataError{Reason: "invalid_sohu_metric_row"}
		}
		amount, amountOK, amountErr := sohuMetricNumber(row[8])
		turnover, turnoverOK, turnoverErr := sohuMetricNumber(row[9])
		if amountErr != nil || turnoverErr != nil {
			return nil, &DataError{Reason: "invalid_sohu_metric_value"}
		}
		// New listings publish '-' for turnover on their first session.
		if !amountOK || !turnoverOK || amount == 0 || turnover == 0 {
			seen[row[0]] = true
			continue
		}
		amount *= 10000 // Sohu reports traded amount in ten-thousand yuan.
		metrics = append(metrics, DailyMetric{Date: row[0], TurnoverRatePct: turnover, AmountCNY: amount})
		seen[row[0]] = true
	}
	if len(metrics) == 0 {
		return nil, &DataError{Reason: "empty_sohu_metric_history"}
	}
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].Date < metrics[j].Date })
	return metrics, nil
}

func sohuMetricNumber(raw string) (float64, bool, error) {
	value := strings.TrimSpace(strings.TrimSuffix(raw, "%"))
	if value == "" || value == "-" {
		return 0, false, nil
	}
	number, err := numeric(json.RawMessage(value))
	if err != nil || number < 0 {
		return 0, false, &DataError{Reason: "invalid_sohu_metric_value"}
	}
	return number, true, nil
}
