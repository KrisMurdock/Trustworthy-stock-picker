package market

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

// Source returns one complete window from one provider. Adjusted windows must
// never be assembled from different providers or different fetches.
type Source interface {
	Fetch(context.Context, config.Stock, string, string, string) (History, error)
}

type requestPacer struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

func (p *requestPacer) wait(ctx context.Context) error {
	p.mu.Lock()
	at := p.next
	if at.Before(time.Now()) {
		at = time.Now()
	}
	p.next = at.Add(p.interval)
	p.mu.Unlock()
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Only stable local errors are exposed: response bodies and URLs can contain
// provider credentials, so they must not be included in errors or logs.
func readResponse(ctx context.Context, client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Failure{Kind: "network"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &Failure{Status: resp.StatusCode, Kind: "status"}
	}
	const limit = 8 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, &Failure{Kind: "body"}
	}
	if len(body) > limit {
		return nil, &DataError{Reason: "response_too_large"}
	}
	return body, nil
}

func requestHistory(stock config.Stock, start, end, adjust, source string) (History, string, error) {
	h := History{Version: 1, Code: stock.Code, Name: stock.Name, Source: source, Adjust: adjust,
		DataField: "day", VolumeUnit: "provider_raw", AsOf: end, FetchedAt: time.Now().In(Shanghai)}
	if h.Name == "" {
		h.Name = stock.Code
	}
	first, e1 := time.Parse("2006-01-02", start)
	_, e2 := time.Parse("2006-01-02", end)
	if !config.ValidCode(stock.Code) || e1 != nil || e2 != nil || start > end {
		return h, "", errors.New("invalid history request")
	}
	if adjust != "none" && adjust != "qfq" && adjust != "hfq" {
		return h, "", errors.New("invalid adjustment")
	}
	if adjust != "none" {
		h.DataField = adjust + "day"
	}
	// Request a preceding anchor so ten-year coverage can be proved even when
	// the first requested date was a holiday outside the configured calendar.
	return h, first.AddDate(0, 0, -31).Format("2006-01-02"), nil
}

func finishHistory(h History, start string) (History, error) {
	sort.Slice(h.Bars, func(i, j int) bool { return h.Bars[i].Date < h.Bars[j].Date })
	if err := h.Validate(); err != nil {
		return History{}, &DataError{Reason: err.Error()}
	}
	i := sort.Search(len(h.Bars), func(i int) bool { return h.Bars[i].Date >= start })
	if i == len(h.Bars) {
		return History{}, &DataError{Reason: "empty_requested_window"}
	}
	if i > 0 && h.Bars[i].Date != start {
		i--
	}
	h.Bars = h.Bars[i:]
	return h, nil
}

// numeric accepts JSON numbers and numeric strings, but never null, booleans,
// missing fields or non-finite values.
func numeric(raw json.RawMessage) (float64, error) {
	text := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if json.Unmarshal(raw, &text) != nil {
			return 0, &DataError{Reason: "invalid_numeric_field"}
		}
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, &DataError{Reason: "invalid_numeric_field"}
	}
	return n, nil
}

func compactDate(date string) string { return strings.ReplaceAll(date, "-", "") }

func parseCompactDate(date string) (string, error) {
	t, err := time.Parse("20060102", date)
	if err != nil {
		return "", &DataError{Reason: "invalid_trade_date"}
	}
	return t.Format("2006-01-02"), nil
}

// ProviderVolumeShares preserves the schema-v1 provider_raw values while
// recording consistent share counts in SQLite. Tencent's STAR board is the
// legacy exception; BaoStock/Sina report shares, Tushare and Eastmoney report lots.
func ProviderVolumeShares(source, code string, raw float64) float64 {
	if source == "baostock" || source == "sina" {
		return raw
	}
	if source == "tencent" {
		return VolumeShares(code, raw)
	}
	return raw * 100
}

// TencentMinuteVolumeRaw converts canonical share counts to the provider_raw
// unit persisted by the realtime minute collector. intraday_bar stores raw
// Tencent values: hands for the main boards and shares for STAR.
func TencentMinuteVolumeRaw(code string, shares float64) float64 {
	if strings.HasPrefix(code, "sh68") {
		return shares
	}
	return shares / 100
}
