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
	if len(h.Bars) == 0 || len(h.Bars) > 640 {
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
}

func (f *Failure) Error() string {
	return fmt.Sprintf("daily request failed (%s, HTTP %d)", f.Kind, f.Status)
}
func (f *Failure) Retryable() bool { return f.Status == 0 || f.Status == 429 || f.Status >= 500 }

type Client struct {
	HTTP     *http.Client
	Endpoint string
}

func NewClient(timeout time.Duration) *Client {
	return &Client{HTTP: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Endpoint: "https://proxy.finance.qq.com/ifzqgtimg/appstock/app/newfqkline/get"}
}
func (c *Client) Close() { c.HTTP.CloseIdleConnections() }

// Fetch replaces the full adjusted window; merging qfq data from different base dates is unsafe.
func (c *Client) Fetch(ctx context.Context, stock config.Stock, start, end, adjust string) (History, error) {
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
	query := u.Query()
	query.Set("param", strings.Join([]string{stock.Code, "day", start, end, "640", suffix}, ","))
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
	bars, name, err := Parse(stock.Code, body, start, end, adjust)
	if err != nil {
		return h, err
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
	// Permit that one response row, but keep the persisted window capped at 640.
	if json.Unmarshal(stock[key], &rows) != nil || len(rows) == 0 || len(rows) > 641 {
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
	if len(bars) > 640 {
		return nil, "", errors.New("daily data window exceeds size limit")
	}
	return bars, name, nil
}

func adjustmentField(stock map[string]json.RawMessage, adjust string) string {
	if adjust != "none" {
		if _, ok := stock[adjust+"day"]; ok {
			return adjust + "day"
		}
	}
	return "day"
}
