package market

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// BaoStock paginates the exchange calendar at 2000 rows. Ten years fits in two
// pages, including non-trading dates used to prove the window boundary.
const baoCalendarPageSize = 2000
const baoCalendarMaxPages = 10

func (c *BaoStockClient) TradingDays(ctx context.Context, start, end string) ([]string, error) {
	if err := validCalendarRange(start, end); err != nil {
		return nil, err
	}
	if err := c.pacer.wait(ctx); err != nil {
		return nil, err
	}
	conn, err := (&net.Dialer{Timeout: c.Timeout}).DialContext(ctx, "tcp", c.Address)
	if err != nil {
		return nil, baoNetworkError(ctx)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	session := baoSession{conn: conn, reader: bufio.NewReader(conn), timeout: c.Timeout}
	login, err := session.exchange(ctx, "00", "01", strings.Join([]string{"login", "anonymous", "123456", "0"}, baoSeparator))
	if err != nil {
		return nil, err
	}
	if len(login) < 4 || login[2] != "login" || login[3] != "anonymous" {
		return nil, &DataError{Reason: "invalid_baostock_login"}
	}

	seen := map[string]bool{}
	days := []string{}
	firstSeen, lastSeen := "", ""
	for page := 1; page <= baoCalendarMaxPages; page++ {
		if err := c.pacer.wait(ctx); err != nil {
			return nil, err
		}
		body := strings.Join([]string{
			"query_trade_dates", "anonymous", strconv.Itoa(page),
			strconv.Itoa(baoCalendarPageSize), start, end,
		}, baoSeparator)
		parts, err := session.exchange(ctx, "33", "34", body)
		if err != nil {
			return nil, fmt.Errorf("baostock calendar page %d: %w", page, err)
		}
		if len(parts) < 10 || parts[2] != "query_trade_dates" || parts[3] != "anonymous" ||
			parts[4] != strconv.Itoa(page) || parts[5] != strconv.Itoa(baoCalendarPageSize) ||
			parts[7] != start || parts[8] != end {
			return nil, &DataError{Reason: "baostock_calendar_query_mismatch"}
		}
		fields := strings.Split(parts[9], ",")
		if len(fields) != 2 || fields[0] != "calendar_date" || fields[1] != "is_trading_day" {
			return nil, &DataError{Reason: "invalid_baostock_calendar_fields"}
		}
		var data struct {
			Records [][]string `json:"record"`
		}
		if parts[6] != "" && json.Unmarshal([]byte(parts[6]), &data) != nil {
			return nil, &DataError{Reason: "invalid_baostock_calendar_records"}
		}
		if len(data.Records) > baoCalendarPageSize {
			return nil, &DataError{Reason: "invalid_baostock_calendar_page_size"}
		}
		for _, row := range data.Records {
			if len(row) != 2 || row[0] < start || row[0] > end {
				return nil, &DataError{Reason: "invalid_baostock_calendar_row"}
			}
			if _, err := time.Parse("2006-01-02", row[0]); err != nil || seen[row[0]] {
				return nil, &DataError{Reason: "invalid_baostock_calendar_date"}
			}
			if row[1] != "0" && row[1] != "1" {
				return nil, &DataError{Reason: "invalid_baostock_trade_status"}
			}
			seen[row[0]] = true
			if firstSeen == "" || row[0] < firstSeen {
				firstSeen = row[0]
			}
			if lastSeen == "" || row[0] > lastSeen {
				lastSeen = row[0]
			}
			if row[1] == "1" {
				days = append(days, row[0])
			}
		}
		if len(data.Records) < baoCalendarPageSize {
			break
		}
		if page == baoCalendarMaxPages {
			return nil, &DataError{Reason: "baostock_calendar_exceeds_page_limit"}
		}
	}
	if firstSeen != start || lastSeen != end {
		return nil, &DataError{Reason: "baostock_calendar_incomplete_window"}
	}
	sort.Strings(days)
	return days, nil
}

type tushareCalendarChunk struct {
	days        []string
	first, last string
}

func (c *TushareClient) TradingDays(ctx context.Context, start, end string) ([]string, error) {
	if err := validCalendarRange(start, end); err != nil {
		return nil, err
	}
	first, _ := time.Parse("2006-01-02", start)
	last, _ := time.Parse("2006-01-02", end)
	days := []string{}
	for from := first; !from.After(last); {
		to := time.Date(from.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
		if to.After(last) {
			to = last
		}
		a, b := from.Format("2006-01-02"), to.Format("2006-01-02")
		chunk, err := c.tradingCalendarChunk(ctx, a, b)
		if err != nil {
			return nil, err
		}
		if chunk.first != a || chunk.last != b {
			return nil, &DataError{Reason: "tushare_calendar_incomplete_window"}
		}
		days = append(days, chunk.days...)
		from = to.AddDate(0, 0, 1)
	}
	sort.Strings(days)
	for i := 1; i < len(days); i++ {
		if days[i] == days[i-1] {
			return nil, &DataError{Reason: "duplicate_tushare_calendar_date"}
		}
	}
	return days, nil
}

func (c *TushareClient) tradingCalendarChunk(ctx context.Context, start, end string) (tushareCalendarChunk, error) {
	var chunk tushareCalendarChunk
	if strings.TrimSpace(c.token) == "" {
		return chunk, &Failure{Status: 401, Kind: "missing_token"}
	}
	if err := c.pacer.wait(ctx); err != nil {
		return chunk, err
	}
	const fields = "cal_date,is_open"
	body, _ := json.Marshal(map[string]any{
		"api_name": "trade_cal",
		"token":    c.token,
		"params": map[string]string{
			"exchange":   "SSE",
			"start_date": compactDate(start),
			"end_date":   compactDate(end),
		},
		"fields": fields,
	})
	req, err := newHTTPRequest(ctx, c.Endpoint, body)
	if err != nil {
		return chunk, &Failure{Status: 400, Kind: "request"}
	}
	raw, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return chunk, err
	}
	var response struct {
		Code *int `json:"code"`
		Data *struct {
			Fields []string            `json:"fields"`
			Items  [][]json.RawMessage `json:"items"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Code == nil {
		return chunk, &DataError{Reason: "invalid_tushare_calendar_envelope"}
	}
	if *response.Code != 0 {
		status := 503
		switch *response.Code {
		case 2002:
			status = 403
		case 40101:
			status = 401
		case -2001:
			status = 429
		}
		return chunk, &Failure{Status: status, Kind: "tushare_api"}
	}
	if response.Data == nil || len(response.Data.Items) >= 6000 {
		return chunk, &DataError{Reason: "missing_or_truncated_tushare_calendar"}
	}
	index := map[string]int{}
	for i, name := range response.Data.Fields {
		if _, exists := index[name]; exists {
			return chunk, &DataError{Reason: "duplicate_tushare_calendar_field"}
		}
		index[name] = i
	}
	dateIndex, okDate := index["cal_date"]
	openIndex, okOpen := index["is_open"]
	if !okDate || !okOpen {
		return chunk, &DataError{Reason: "missing_tushare_calendar_field"}
	}
	seen := map[string]bool{}
	for _, values := range response.Data.Items {
		if len(values) != len(response.Data.Fields) {
			return chunk, &DataError{Reason: "invalid_tushare_calendar_row"}
		}
		var rawDate, rawOpen string
		if json.Unmarshal(values[dateIndex], &rawDate) != nil || json.Unmarshal(values[openIndex], &rawOpen) != nil {
			return chunk, &DataError{Reason: "invalid_tushare_calendar_row"}
		}
		date, err := parseCompactDate(rawDate)
		if err != nil || date < start || date > end || seen[date] {
			return chunk, &DataError{Reason: "invalid_tushare_calendar_date"}
		}
		if rawOpen != "0" && rawOpen != "1" {
			return chunk, &DataError{Reason: "invalid_tushare_calendar_status"}
		}
		seen[date] = true
		if chunk.first == "" || date < chunk.first {
			chunk.first = date
		}
		if chunk.last == "" || date > chunk.last {
			chunk.last = date
		}
		if rawOpen == "1" {
			chunk.days = append(chunk.days, date)
		}
	}
	return chunk, nil
}

func validCalendarRange(start, end string) error {
	first, err := time.Parse("2006-01-02", start)
	if err != nil {
		return errors.New("invalid calendar request")
	}
	last, err := time.Parse("2006-01-02", end)
	if err != nil || first.After(last) {
		return errors.New("invalid calendar request")
	}
	return nil
}

func newHTTPRequest(ctx context.Context, endpoint string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}
