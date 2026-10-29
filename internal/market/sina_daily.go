package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

const sinaKLineLimit = 5001

type sinaKLineRow struct {
	Day    string `json:"day"`
	Open   string `json:"open"`
	High   string `json:"high"`
	Low    string `json:"low"`
	Close  string `json:"close"`
	Volume string `json:"volume"`
}

// SinaKLineClient reads the public Sina K-line JSON endpoint. The same
// endpoint serves daily bars (used to derive session status) and native
// five-minute bars (used by the offline minute backfill). It never executes
// JavaScript and never follows redirects.
type SinaKLineClient struct {
	HTTP     *http.Client
	Endpoint string
	pacer    requestPacer

	mu        sync.Mutex
	indexDays []string
}

func NewSinaKLineClient(timeout, interval time.Duration) *SinaKLineClient {
	return &SinaKLineClient{
		HTTP:     newHTTPClient(timeout),
		Endpoint: "https://money.finance.sina.com.cn/quotes_service/api/json_v2.php/CN_MarketData.getKLineData",
		pacer:    requestPacer{interval: interval},
	}
}

func (c *SinaKLineClient) Close() { c.HTTP.CloseIdleConnections() }

func (c *SinaKLineClient) fetchRows(ctx context.Context, symbol string, scale, limit int) ([]sinaKLineRow, error) {
	if !config.ValidInstrumentCode(symbol) || (scale != 5 && scale != 240) || limit < 1 || limit > sinaKLineLimit {
		return nil, &DataError{Reason: "invalid_sina_kline_request"}
	}
	query := url.Values{}
	query.Set("symbol", symbol)
	query.Set("scale", strconv.Itoa(scale))
	query.Set("ma", "no")
	query.Set("datalen", strconv.Itoa(limit))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Endpoint, "/")+"?"+query.Encode(), nil)
	if err != nil {
		return nil, &Failure{Kind: "request"}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	if err := c.pacer.wait(ctx); err != nil {
		return nil, err
	}
	body, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return nil, err
	}
	var rows []sinaKLineRow
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, &DataError{Reason: "invalid_sina_kline_json"}
	}
	if len(rows) == 0 {
		return nil, &DataError{Reason: "empty_sina_kline"}
	}
	return rows, nil
}

func (c *SinaKLineClient) fetchDailyDates(ctx context.Context, symbol string) ([]string, error) {
	rows, err := c.fetchRows(ctx, symbol, 240, sinaKLineLimit)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(rows))
	dates := make([]string, 0, len(rows))
	for _, row := range rows {
		day := strings.TrimSpace(row.Day)
		if _, err := time.ParseInLocation("2006-01-02", day, Shanghai); err != nil {
			return nil, &DataError{Reason: "invalid_sina_kline_date"}
		}
		if seen[day] {
			return nil, &DataError{Reason: "duplicate_sina_kline_date"}
		}
		seen[day] = true
		dates = append(dates, day)
	}
	sort.Strings(dates)
	return dates, nil
}

func (c *SinaKLineClient) cachedIndexDays(ctx context.Context, end string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.indexDays) > 0 && c.indexDays[len(c.indexDays)-1] >= end {
		return append([]string(nil), c.indexDays...), nil
	}
	days, err := c.fetchDailyDates(ctx, "sh000001")
	if err != nil {
		return nil, err
	}
	c.indexDays = append(c.indexDays[:0], days...)
	return append([]string(nil), c.indexDays...), nil
}

// FetchStatus derives a complete session-status window from the exchange
// calendar (Shanghai composite daily bars) and the stock's complete Sina daily
// history. A missing stock bar on a trading session is recorded as suspended;
// truncated history is rejected instead of being mistaken for suspension.
func (c *SinaKLineClient) FetchStatus(ctx context.Context, stock config.Stock, start, end string) (StatusWindow, error) {
	window := StatusWindow{Code: stock.Code, Start: start, End: end, Source: "sina_daily", FetchedAt: time.Now().In(Shanghai), Full: true}
	first, e1 := time.ParseInLocation("2006-01-02", start, Shanghai)
	last, e2 := time.ParseInLocation("2006-01-02", end, Shanghai)
	if !config.ValidCode(stock.Code) || e1 != nil || e2 != nil || first.After(last) || last.Sub(first) > 20*366*24*time.Hour {
		return StatusWindow{}, &DataError{Reason: "invalid_sina_status_request"}
	}
	index, err := c.cachedIndexDays(ctx, end)
	if err != nil {
		return StatusWindow{}, err
	}
	if len(index) == 0 || index[0] > start || index[len(index)-1] < end {
		return StatusWindow{}, &DataError{Reason: "sina_status_calendar_short"}
	}
	stockDays, err := c.fetchDailyDates(ctx, stock.Code)
	if err != nil {
		return StatusWindow{}, err
	}
	firstSession := ""
	for _, day := range index {
		if day >= start {
			firstSession = day
			break
		}
	}
	if firstSession == "" {
		return StatusWindow{}, &DataError{Reason: "sina_status_empty_window"}
	}
	firstNeeded, _ := time.ParseInLocation("2006-01-02", firstSession, Shanghai)
	earliestAllowed := firstNeeded.AddDate(0, 0, 31).Format("2006-01-02")
	if stockDays[0] > earliestAllowed || (len(stockDays) >= sinaKLineLimit && stockDays[0] > start) {
		return StatusWindow{}, &DataError{Reason: "sina_status_history_short"}
	}
	traded := make(map[string]bool, len(stockDays))
	for _, day := range stockDays {
		traded[day] = true
	}
	for _, day := range index {
		if day < start || day > end {
			continue
		}
		state := SessionSuspended
		detail := "absent from complete Sina daily history while the Shanghai composite traded"
		if traded[day] {
			state = SessionTraded
			detail = ""
		}
		window.Statuses = append(window.Statuses, SessionStatus{
			Code: stock.Code, Date: day, State: state, Source: "sina_daily", FetchedAt: window.FetchedAt, Detail: detail,
		})
	}
	if err := window.Validate(); err != nil {
		return StatusWindow{}, &DataError{Reason: err.Error()}
	}
	return window, nil
}

// FetchMinuteRange downloads the provider's native five-minute bars. The
// public endpoint returns at most 5001 rows, so callers must keep the requested
// range within roughly 100 complete sessions.
func (c *SinaKLineClient) FetchMinuteRange(ctx context.Context, stock config.Stock, from, through string) (MinuteArchive, error) {
	archive := MinuteArchive{
		Version: 1, Code: stock.Code, Name: stock.Name, Source: "sina", Period: 5,
		BasePeriod: 5, Adjust: "none", VolumeUnit: "shares", From: from, Through: through,
		FetchedAt: time.Now().In(Shanghai),
	}
	first, e1 := time.ParseInLocation("2006-01-02", from, Shanghai)
	last, e2 := time.ParseInLocation("2006-01-02", through, Shanghai)
	if !config.ValidInstrumentCode(stock.Code) || e1 != nil || e2 != nil || first.After(last) || last.Sub(first) > 20*366*24*time.Hour {
		return MinuteArchive{}, &DataError{Reason: "invalid_sina_minute_range"}
	}
	rows, err := c.fetchRows(ctx, stock.Code, 5, sinaKLineLimit)
	if err != nil {
		return MinuteArchive{}, err
	}
	previous := time.Time{}
	for _, row := range rows {
		end, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(row.Day), Shanghai)
		if err != nil {
			return MinuteArchive{}, &DataError{Reason: "invalid_sina_minute_date"}
		}
		day := end.Format("2006-01-02")
		if day < from || day > through {
			continue
		}
		if !end.After(previous) {
			return MinuteArchive{}, &DataError{Reason: "invalid_sina_minute_ordering"}
		}
		values := []string{row.Open, row.High, row.Low, row.Close, row.Volume}
		numbers := make([]float64, len(values))
		for i, value := range values {
			n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil {
				return MinuteArchive{}, &DataError{Reason: "invalid_sina_minute_numeric"}
			}
			numbers[i] = n
		}
		archive.Bars = append(archive.Bars, MinuteBar{
			End: end, Open: numbers[0], High: numbers[1], Low: numbers[2], Close: numbers[3], Volume: numbers[4], Final: true,
		})
		previous = end
	}
	if len(archive.Bars) == 0 {
		return MinuteArchive{}, &DataError{Reason: "empty_sina_minute_window"}
	}
	if err := archive.Validate(); err != nil {
		return MinuteArchive{}, &DataError{Reason: err.Error()}
	}
	return archive, nil
}
