package market

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

const (
	hkDailyPageSize = 1_000
	hkQFQPageSize   = 100
	hkDailyMaxPages = 12
)

// HKDailyClient reads Hong Kong daily bars from Tencent's public endpoint. Raw
// and forward-adjusted windows use different upstream paths but share the same
// paging and validation rules.
type HKDailyClient struct {
	HTTP      *http.Client
	endpoints []string
	pacer     requestPacer
	attempts  int
	backoff   time.Duration
}

type hkDailyPage struct {
	Bars  []Bar
	Field string
}

func NewHKDailyClient(timeout, interval time.Duration, endpoints ...string) *HKDailyClient {
	seen := map[string]bool{}
	unique := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
		if endpoint == "" || seen[endpoint] {
			continue
		}
		seen[endpoint] = true
		unique = append(unique, endpoint)
	}
	return &HKDailyClient{HTTP: newHTTPClient(timeout), endpoints: unique, pacer: requestPacer{interval: interval}, attempts: 6, backoff: time.Second}
}

func (c *HKDailyClient) Close() { c.HTTP.CloseIdleConnections() }

func (c *HKDailyClient) Fetch(ctx context.Context, stock config.Stock, start, end, adjust string) (History, error) {
	h := History{
		Version: 1, Code: stock.Code, Name: stock.Name, Source: "tencent_hk", Adjust: adjust,
		DataField: "day", VolumeUnit: "provider_raw", AsOf: end, FetchedAt: time.Now().In(Shanghai),
	}
	if !config.ValidHKStockCode(stock.Code) || stock.Name == "" || (adjust != "none" && adjust != "qfq") {
		return h, errors.New("invalid HK daily request")
	}
	first, e1 := time.Parse("2006-01-02", start)
	_, e2 := time.Parse("2006-01-02", end)
	if e1 != nil || e2 != nil || start > end {
		return h, errors.New("invalid HK daily range")
	}
	if adjust == "qfq" {
		h.DataField = "qfqday"
	}
	probeStart := first
	if adjust == "none" {
		probeStart = first.AddDate(0, 0, -31)
	}
	probeStartText := probeStart.Format("2006-01-02")
	cursor := end
	var history []Bar
	field := ""
	pageSize := hkDailyPageSize
	if adjust == "qfq" {
		pageSize = hkQFQPageSize
	}
	for pageNumber := 0; pageNumber < hkDailyMaxPages; pageNumber++ {
		page, err := c.fetchPage(ctx, stock.Code, probeStartText, cursor, pageSize, adjust)
		if err != nil {
			return History{}, err
		}
		if field == "" {
			field = page.Field
		} else if page.Field != field {
			return History{}, &DataError{Reason: "hk_daily_field_changed"}
		}
		if len(page.Bars) == 0 {
			return History{}, &DataError{Reason: "empty_hk_daily_window"}
		}
		if len(history) > 0 {
			boundary := history[0]
			if page.Bars[len(page.Bars)-1].Date != boundary.Date || page.Bars[len(page.Bars)-1] != boundary {
				return History{}, &DataError{Reason: "hk_daily_page_overlap_mismatch"}
			}
			if len(page.Bars) == 1 {
				break
			}
			history = append(page.Bars[:len(page.Bars)-1], history...)
		} else {
			history = append(history, page.Bars...)
		}
		if history[0].Date <= start || len(page.Bars) < pageSize {
			break
		}
		cursor = history[0].Date
		if pageNumber == hkDailyMaxPages-1 {
			return History{}, &DataError{Reason: "hk_daily_history_exceeds_page_limit"}
		}
	}
	h.DataField = field
	h.Bars = history
	return finishHistory(h, start)
}

func (c *HKDailyClient) fetchPage(ctx context.Context, code, start, through string, count int, adjust string) (hkDailyPage, error) {
	if c == nil || c.HTTP == nil || len(c.endpoints) == 0 {
		return hkDailyPage{}, errors.New("HK daily endpoint is unavailable")
	}
	attempts := c.attempts
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		endpoint := c.endpoints[(attempt-1)%len(c.endpoints)]
		page, err := c.fetchPageOnce(ctx, endpoint, code, start, through, count, adjust)
		if err == nil {
			return page, nil
		}
		last = err
		if ctx.Err() != nil || !retryableHKDailyError(err) || attempt == attempts {
			return hkDailyPage{}, err
		}
		delay := c.backoff * time.Duration(1<<(attempt-1))
		if delay <= 0 {
			delay = time.Second
		}
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return hkDailyPage{}, ctx.Err()
		case <-timer.C:
		}
	}
	return hkDailyPage{}, last
}

func retryableHKDailyError(err error) bool {
	var failure *Failure
	return errors.As(err, &failure) && failure.Retryable()
}

func (c *HKDailyClient) fetchPageOnce(ctx context.Context, endpoint, code, start, through string, count int, adjust string) (hkDailyPage, error) {
	if count < 1 || count > hkDailyPageSize {
		return hkDailyPage{}, errors.New("invalid HK daily page size")
	}
	path := "/appstock/app/fqkline/get"
	field := "day"
	if adjust == "qfq" {
		path = "/appstock/app/hkfqkline/get"
		field = "qfqday"
	}
	u, err := url.Parse(endpoint + path)
	if err != nil {
		return hkDailyPage{}, errors.New("invalid HK daily endpoint")
	}
	q := u.Query()
	q.Set("param", code+",day,"+start+","+through+","+strconv.Itoa(count)+","+adjust)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return hkDailyPage{}, &Failure{Status: 400, Kind: "request"}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	if err := c.pacer.wait(ctx); err != nil {
		return hkDailyPage{}, err
	}
	raw, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return hkDailyPage{}, err
	}
	var envelope struct {
		Code *int                       `json:"code"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Code == nil || *envelope.Code != 0 {
		return hkDailyPage{}, &DataError{Reason: "invalid_hk_daily_envelope"}
	}
	encoded, ok := envelope.Data[code]
	if !ok {
		return hkDailyPage{}, &DataError{Reason: "hk_daily_code_mismatch"}
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(encoded, &payload) != nil {
		return hkDailyPage{}, &DataError{Reason: "invalid_hk_daily_response"}
	}
	encodedRows, ok := payload[field]
	if !ok && adjust == "qfq" {
		field = "day"
		encodedRows, ok = payload[field]
	}
	if !ok {
		return hkDailyPage{}, &DataError{Reason: "missing_or_invalid_hk_daily_rows"}
	}
	var rows [][]json.RawMessage
	if json.Unmarshal(encodedRows, &rows) != nil || len(rows) == 0 || len(rows) > count+1 {
		return hkDailyPage{}, &DataError{Reason: "missing_or_invalid_hk_daily_rows"}
	}
	out := make([]Bar, 0, len(rows))
	previous := ""
	for _, row := range rows {
		if len(row) < 6 {
			return hkDailyPage{}, &DataError{Reason: "invalid_hk_daily_row"}
		}
		var date string
		if json.Unmarshal(row[0], &date) != nil {
			return hkDailyPage{}, &DataError{Reason: "invalid_hk_daily_date"}
		}
		parsed, err := time.Parse("2006-01-02", date)
		if err != nil || parsed.Format("2006-01-02") != date || date > through || parsed.Weekday() == time.Saturday || parsed.Weekday() == time.Sunday {
			return hkDailyPage{}, &DataError{Reason: "invalid_hk_daily_date"}
		}
		if date <= previous {
			return hkDailyPage{}, &DataError{Reason: "invalid_hk_daily_ordering"}
		}
		previous = date
		if date < start {
			continue
		}
		values := make([]float64, 5)
		for index := range values {
			value, err := numericRaw(row[index+1])
			if err != nil {
				return hkDailyPage{}, &DataError{Reason: "invalid_hk_daily_numeric"}
			}
			values[index] = value
		}
		bar := Bar{Date: date, Open: values[0], Close: values[1], High: values[2], Low: values[3], Volume: values[4]}
		if bar.Open <= 0 || bar.Close <= 0 || bar.Low <= 0 || bar.High < bar.Open || bar.High < bar.Close || bar.Low > bar.Open || bar.Low > bar.Close || bar.Volume < 0 {
			return hkDailyPage{}, &DataError{Reason: "invalid_hk_daily_row"}
		}
		out = append(out, bar)
	}
	return hkDailyPage{Bars: out, Field: field}, nil
}

// TradingDays derives Hong Kong sessions from the Hang Seng Index's complete
// daily series. The index is used only as a calendar, never as stock data.
func (c *HKDailyClient) TradingDays(ctx context.Context, from, through string) ([]string, error) {
	first, e1 := time.Parse("2006-01-02", from)
	last, e2 := time.Parse("2006-01-02", through)
	if e1 != nil || e2 != nil || from > through || last.Sub(first) > 2*366*24*time.Hour {
		return nil, errors.New("invalid HK trading-day range")
	}
	page, err := c.fetchPage(ctx, "hkHSI", from, through, hkDailyPageSize, "none")
	if err != nil {
		return nil, err
	}
	days := make([]string, 0, len(page.Bars))
	for _, bar := range page.Bars {
		if bar.Date >= from && bar.Date <= through {
			days = append(days, bar.Date)
		}
	}
	if len(days) == 0 {
		return nil, &DataError{Reason: "empty_hk_trading_days"}
	}
	return days, nil
}
