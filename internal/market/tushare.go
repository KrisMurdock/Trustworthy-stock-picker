package market

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"stock-watcher/internal/config"
)

// TushareClient uses the official REST API directly. The token is never part
// of the URL, error messages, configuration serialization or returned history.
type TushareClient struct {
	HTTP     *http.Client
	Endpoint string
	token    string
	pacer    requestPacer
}

func NewTushareClient(timeout, interval time.Duration, token string) *TushareClient {
	return &TushareClient{HTTP: newHTTPClient(timeout), Endpoint: "https://api.tushare.pro", token: token, pacer: requestPacer{interval: interval}}
}
func (c *TushareClient) Close() { c.HTTP.CloseIdleConnections() }

func (c *TushareClient) query(ctx context.Context, api, code, start, end, fields string) ([]map[string]json.RawMessage, error) {
	if strings.TrimSpace(c.token) == "" {
		return nil, &Failure{Status: 401, Kind: "missing_token"}
	}
	if err := c.pacer.wait(ctx); err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"api_name": api, "token": c.token,
		"params": map[string]string{"ts_code": code, "start_date": compactDate(start), "end_date": compactDate(end)}, "fields": fields})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &Failure{Status: 400, Kind: "request"}
	}
	req.Header.Set("Content-Type", "application/json")
	raw, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return nil, err
	}
	var response struct {
		Code *int `json:"code"`
		Data *struct {
			Fields []string            `json:"fields"`
			Items  [][]json.RawMessage `json:"items"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Code == nil {
		return nil, &DataError{Reason: "invalid_tushare_envelope"}
	}
	if *response.Code != 0 {
		// Only classify documented permission failures as permanent. Unknown
		// upstream failures must not be mistaken for missing stock history.
		status := 503
		if *response.Code == 2002 {
			status = 403
		}
		if *response.Code == 40101 {
			status = 401
		}
		if *response.Code == -2001 {
			status = 429
		}
		return nil, &Failure{Status: status, Kind: "tushare_api"}
	}
	if response.Data == nil || len(response.Data.Items) >= 6000 {
		return nil, &DataError{Reason: "missing_or_truncated_tushare_data"}
	}
	seen := map[string]bool{}
	for _, name := range response.Data.Fields {
		if seen[name] {
			return nil, &DataError{Reason: "duplicate_tushare_field"}
		}
		seen[name] = true
	}
	for _, name := range strings.Split(fields, ",") {
		if !seen[name] {
			return nil, &DataError{Reason: "missing_tushare_field"}
		}
	}
	rows := make([]map[string]json.RawMessage, 0, len(response.Data.Items))
	for _, values := range response.Data.Items {
		if len(values) != len(response.Data.Fields) {
			return nil, &DataError{Reason: "invalid_tushare_row"}
		}
		row := make(map[string]json.RawMessage, len(values))
		for i, field := range response.Data.Fields {
			row[field] = values[i]
		}
		var actual, date string
		if json.Unmarshal(row["ts_code"], &actual) != nil || actual != code || json.Unmarshal(row["trade_date"], &date) != nil {
			return nil, &DataError{Reason: "tushare_stock_mismatch"}
		}
		parsed, err := parseCompactDate(date)
		if err != nil || parsed < start || parsed > end {
			return nil, &DataError{Reason: "tushare_date_outside_request"}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (c *TushareClient) Fetch(ctx context.Context, stock config.Stock, start, end, adjust string) (History, error) {
	h, probe, err := requestHistory(stock, start, end, adjust, "tushare")
	if err != nil {
		return h, err
	}
	code := stock.Code[2:] + "." + strings.ToUpper(stock.Code[:2])
	// Calendar-year chunks stay well below the API's 6000-row cap, including
	// the optional 20-year archive. Adjustment is applied after all chunks.
	first, _ := time.Parse("2006-01-02", probe)
	last, _ := time.Parse("2006-01-02", end)
	factors := map[string]float64{}
	for from := first; !from.After(last); {
		to := time.Date(from.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
		if to.After(last) {
			to = last
		}
		a, b := from.Format("2006-01-02"), to.Format("2006-01-02")
		rows, err := c.query(ctx, "daily", code, a, b, "ts_code,trade_date,open,high,low,close,vol")
		if err != nil {
			return History{}, err
		}
		for _, row := range rows {
			var date string
			_ = json.Unmarshal(row["trade_date"], &date)
			date, _ = parseCompactDate(date)
			bar := Bar{Date: date}
			for key, dest := range map[string]*float64{"open": &bar.Open, "high": &bar.High, "low": &bar.Low, "close": &bar.Close, "vol": &bar.Volume} {
				*dest, err = numeric(row[key])
				if err != nil {
					return History{}, err
				}
			}
			h.Bars = append(h.Bars, bar)
		}
		if adjust != "none" {
			rows, err = c.query(ctx, "adj_factor", code, a, b, "ts_code,trade_date,adj_factor")
			if err != nil {
				return History{}, err
			}
			for _, row := range rows {
				var date string
				_ = json.Unmarshal(row["trade_date"], &date)
				date, _ = parseCompactDate(date)
				factor, err := numeric(row["adj_factor"])
				if err != nil || factor <= 0 || factors[date] != 0 {
					return History{}, &DataError{Reason: "invalid_adjustment_factor"}
				}
				factors[date] = factor
			}
		}
		from = to.AddDate(0, 0, 1)
	}
	if len(h.Bars) == 0 {
		return History{}, &DataError{Reason: "empty_history"}
	}
	sort.Slice(h.Bars, func(i, j int) bool { return h.Bars[i].Date < h.Bars[j].Date })
	if adjust != "none" {
		anchor := 1.0
		if adjust == "qfq" {
			anchor = factors[h.Bars[len(h.Bars)-1].Date]
		}
		if anchor <= 0 {
			return History{}, &DataError{Reason: "missing_adjustment_anchor"}
		}
		for i := range h.Bars {
			bar := &h.Bars[i]
			factor := factors[bar.Date] / anchor
			if factor <= 0 {
				return History{}, &DataError{Reason: "missing_adjustment_factor"}
			}
			bar.Open *= factor
			bar.High *= factor
			bar.Low *= factor
			bar.Close *= factor
		}
	}
	return finishHistory(h, start)
}

func (c *TushareClient) FetchWindow(ctx context.Context, stock config.Stock, start, end, adjust string, count int) (History, error) {
	return recentWindow(ctx, c, stock, start, end, adjust, count)
}

func recentWindow(ctx context.Context, source Source, stock config.Stock, start, end, adjust string, count int) (History, error) {
	if count < 1 || count > 640 {
		return History{}, &DataError{Reason: "invalid_window_count"}
	}
	last, err := time.Parse("2006-01-02", end)
	if err != nil {
		return History{}, &DataError{Reason: "invalid_end_date"}
	}
	from := last.AddDate(0, 0, -(count*2 + 31)).Format("2006-01-02")
	if from < start {
		from = start
	}
	h, err := source.Fetch(ctx, stock, from, end, adjust)
	if err == nil && len(h.Bars) > count {
		h.Bars = h.Bars[len(h.Bars)-count:]
	}
	return h, err
}
