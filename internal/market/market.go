package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"stock-watcher/internal/config"
)

var Shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

// MaxHistoryBars accommodates twenty years of daily sessions. The provider's
// separate 640-row limit applies to each HTTP request, not the stored history.
const MaxHistoryBars = 6000

type Bar struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	Close  float64 `json:"close"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Volume float64 `json:"volume_raw"`
}
type History struct {
	Version    int       `json:"schema_version"`
	Code       string    `json:"code"`
	Name       string    `json:"name"`
	Source     string    `json:"source"`
	Adjust     string    `json:"adjust"`
	DataField  string    `json:"data_field"`
	VolumeUnit string    `json:"volume_unit"`
	AsOf       string    `json:"as_of"`
	FetchedAt  time.Time `json:"fetched_at"`
	Bars       []Bar     `json:"bars"`
}

// Validate protects the same contract when JSON is read back from Windows.
func (h History) Validate() error {
	if h.Version != 1 || !config.ValidCode(h.Code) || h.Name == "" || h.Source != "tencent" || h.VolumeUnit != "provider_raw" || h.FetchedAt.IsZero() {
		return errors.New("missing or unsupported history metadata")
	}
	if _, err := time.Parse("2006-01-02", h.AsOf); err != nil {
		return errors.New("invalid history as-of date")
	}
	if h.Adjust != "none" && h.Adjust != "qfq" && h.Adjust != "hfq" {
		return errors.New("invalid history adjustment")
	}
	if h.DataField != "day" && (h.Adjust == "none" || h.DataField != h.Adjust+"day") {
		return errors.New("mismatched history adjustment field")
	}
	if len(h.Bars) == 0 || len(h.Bars) > MaxHistoryBars {
		return errors.New("invalid history bar count")
	}
	previous := ""
	for _, b := range h.Bars {
		d, err := time.Parse("2006-01-02", b.Date)
		if err != nil || b.Date <= previous || b.Date > h.AsOf || d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			return errors.New("invalid history bar ordering or date")
		}
		previous = b.Date
		for _, v := range []float64{b.Open, b.Close, b.High, b.Low, b.Volume} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return errors.New("invalid history numeric value")
			}
		}
		if b.Open <= 0 || b.Close <= 0 || b.Low <= 0 || b.High < math.Max(b.Open, b.Close) || b.Low > math.Min(b.Open, b.Close) {
			return errors.New("invalid history OHLC range")
		}
	}
	return nil
}

type Failure struct {
	Status int
	Kind   string
	Reason string
}

func (f *Failure) Error() string {
	if f.Reason != "" {
		return fmt.Sprintf("daily request failed (%s, HTTP %d): %s", f.Kind, f.Status, f.Reason)
	}
	return fmt.Sprintf("daily request failed (%s, HTTP %d)", f.Kind, f.Status)
}
func (f *Failure) Retryable() bool {
	return f.Status == 0 || f.Status == http.StatusRequestTimeout || f.Status == http.StatusTooManyRequests || f.Status >= 500
}

type DataError struct {
	Reason string
}

func (e *DataError) Error() string { return "daily data rejected: " + e.Reason }

func IsIsolatable(err error) bool {
	var failure *Failure
	if errors.As(err, &failure) {
		return !failure.Retryable()
	}
	var dataErr *DataError
	return errors.As(err, &dataErr)
}

type Client struct {
	HTTP     *http.Client
	Endpoint string
}

func NewClient(timeout time.Duration) *Client {
	return &Client{HTTP: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Endpoint: "https://proxy.finance.qq.com/ifzqgtimg/appstock/app/newfqkline/get"}
}
func (c *Client) Close() { c.HTTP.CloseIdleConnections() }

// Fetch replaces the full adjusted window; merging qfq data from different
// fetches is unsafe. Long windows are paged backwards with a shared boundary
// bar, which must agree in every page before any history can be returned.
func (c *Client) Fetch(ctx context.Context, stock config.Stock, start, end, adjust string) (History, error) {
	first, startErr := time.Parse("2006-01-02", start)
	last, endErr := time.Parse("2006-01-02", end)
	if startErr != nil || endErr != nil || last.Sub(first) <= 640*24*time.Hour {
		return c.FetchWindow(ctx, stock, start, end, adjust, 640)
	}
	// A preceding session proves coverage when start falls on a market holiday.
	// Retain that one bar in the stored history instead of guessing holidays or
	// interpreting the provider's truncated history as a stock's listing date.
	probeStart := first.AddDate(0, 0, -31).Format("2006-01-02")
	history, err := c.FetchWindow(ctx, stock, probeStart, end, adjust, 640)
	if err != nil {
		return History{}, err
	}
	for history.Bars[0].Date > start {
		timer := time.NewTimer(350 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return History{}, ctx.Err()
		case <-timer.C:
		}
		boundary := history.Bars[0]
		page, err := c.FetchWindow(ctx, stock, probeStart, boundary.Date, adjust, 640)
		if err != nil {
			return History{}, fmt.Errorf("historical page ending %s: %w", boundary.Date, err)
		}
		if page.DataField != history.DataField || page.Bars[len(page.Bars)-1] != boundary {
			return History{}, &DataError{Reason: "historical page overlap or adjustment mismatch"}
		}
		if page.Bars[0].Date >= boundary.Date {
			return History{}, &DataError{Reason: "historical coverage incomplete; verify listed_from for newer stocks"}
		}
		if len(history.Bars)+len(page.Bars)-1 > MaxHistoryBars {
			return History{}, &DataError{Reason: "historical window exceeds size limit"}
		}
		history.Bars = append(page.Bars[:len(page.Bars)-1], history.Bars...)
	}
	// Keep the closest boundary anchor and the entire requested window.
	i := sort.Search(len(history.Bars), func(i int) bool { return history.Bars[i].Date >= start })
	if i == len(history.Bars) || history.Bars[i].Date != start {
		i--
	}
	history.Bars = history.Bars[i:]
	return history, nil
}

func (c *Client) FetchWindow(ctx context.Context, stock config.Stock, start, end, adjust string, count int) (History, error) {
	h := History{Version: 1, Code: stock.Code, Name: stock.Name, Source: "tencent", Adjust: adjust, AsOf: end, FetchedAt: time.Now().In(Shanghai)}
	h.VolumeUnit = "provider_raw"
	if !config.ValidCode(stock.Code) {
		return h, errors.New("invalid A-share code")
	}
	if _, err := time.Parse("2006-01-02", start); err != nil {
		return h, errors.New("invalid start date")
	}
	if _, err := time.Parse("2006-01-02", end); err != nil || start > end {
		return h, errors.New("invalid end date")
	}
	suffix := adjust
	if adjust == "none" {
		suffix = ""
	} else if adjust != "qfq" && adjust != "hfq" {
		return h, errors.New("invalid adjustment")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return h, &Failure{Kind: "request"}
	}
	if count < 1 || count > 640 {
		return h, errors.New("invalid daily count")
	}
	query := u.Query()
	query.Set("param", strings.Join([]string{stock.Code, "day", start, end, strconv.Itoa(count), suffix}, ","))
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return h, &Failure{Kind: "request"}
	}
	req.Header.Set("User-Agent", "stock-watcher/1.0")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return h, ctx.Err()
		}
		return h, &Failure{Kind: "network"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return h, &Failure{Status: resp.StatusCode, Kind: "status"}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(body) > 2<<20 {
		return h, &Failure{Kind: "body"}
	}
	bars, name, err := ParseWithLimit(stock.Code, body, start, end, adjust, count)
	if err != nil {
		return h, &DataError{Reason: err.Error()}
	}
	h.Bars = bars
	var fieldEnvelope struct {
		Data map[string]map[string]json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(body, &fieldEnvelope)
	h.DataField = adjustmentField(fieldEnvelope.Data[stock.Code], adjust)
	if name != "" {
		h.Name = name
	}
	return h, nil
}

func Parse(code string, body []byte, start, end, adjust string) ([]Bar, string, error) {
	return parseWithLimit(code, body, start, end, adjust, 640)
}

func ParseWithLimit(code string, body []byte, start, end, adjust string, count int) ([]Bar, string, error) {
	return parseWithLimit(code, body, start, end, adjust, count)
}

func parseWithLimit(code string, body []byte, start, end, adjust string, count int) ([]Bar, string, error) {
	var envelope struct {
		Code *int                       `json:"code"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if !json.Valid(body) || json.Unmarshal(body, &envelope) != nil || envelope.Code == nil || *envelope.Code != 0 {
		return nil, "", errors.New("invalid daily response envelope")
	}
	raw, ok := envelope.Data[code]
	if !ok {
		return nil, "", errors.New("daily response code mismatch")
	}
	var stock map[string]json.RawMessage
	if json.Unmarshal(raw, &stock) != nil {
		return nil, "", errors.New("invalid daily stock response")
	}
	key := adjustmentField(stock, adjust)
	var rows [][]json.RawMessage
	// Tencent returns day when an adjusted series uses the base day data; this is
	// also the protocol used by the legacy stock_zh_a_hist_tx adapter. Record the
	// actual field in History rather than disguising this provider behavior.
	// Tencent can append today's live bar to the requested 640 historical rows.
	// Permit that one response row, but keep each parsed page capped at count.
	if json.Unmarshal(stock[key], &rows) != nil || len(rows) == 0 || len(rows) > count+1 {
		return nil, "", errors.New("missing requested daily adjustment data")
	}
	bars := make([]Bar, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if len(row) < 6 {
			return nil, "", errors.New("missing daily fields")
		}
		var date string
		if json.Unmarshal(row[0], &date) != nil {
			return nil, "", errors.New("invalid daily date")
		}
		d, err := time.Parse("2006-01-02", date)
		if err != nil || d.Weekday() == time.Saturday || d.Weekday() == time.Sunday || seen[date] {
			return nil, "", errors.New("invalid or duplicate daily date")
		}
		seen[date] = true
		b := Bar{Date: date}
		values := []*float64{&b.Open, &b.Close, &b.High, &b.Low, &b.Volume}
		for i, target := range values {
			var str string
			if json.Unmarshal(row[i+1], &str) != nil {
				str = string(row[i+1])
			}
			v, err := strconv.ParseFloat(str, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return nil, "", errors.New("invalid daily numeric field")
			}
			*target = v
		}
		if b.Open <= 0 || b.Close <= 0 || b.Low <= 0 || b.High < math.Max(b.Open, b.Close) || b.Low > math.Min(b.Open, b.Close) {
			return nil, "", errors.New("invalid daily price range")
		}
		if date >= start && date <= end {
			bars = append(bars, b)
		}
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
	var qt map[string][]json.RawMessage
	name := ""
	if json.Unmarshal(stock["qt"], &qt) == nil && len(qt[code]) > 1 {
		_ = json.Unmarshal(qt[code][1], &name)
	}
	if len(bars) == 0 {
		return nil, "", errors.New("no daily data in requested window")
	}
	if len(bars) > count {
		return nil, "", errors.New("daily data window exceeds size limit")
	}
	return bars, name, nil
}

func VolumeShares(code string, raw float64) float64 {
	if strings.HasPrefix(code, "sh68") {
		return raw
	}
	return raw * 100
}

func adjustmentField(stock map[string]json.RawMessage, adjust string) string {
	if adjust != "none" {
		if _, ok := stock[adjust+"day"]; ok {
			return adjust + "day"
		}
	}
	return "day"
}
