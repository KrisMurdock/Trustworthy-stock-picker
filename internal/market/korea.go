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
)

const (
	KoreaKOSPI  = "KOSPI"
	KoreaKOSDAQ = "KOSDAQ"

	KoreaHistoryLimit = 20 * 366
)

// KoreaBar is one daily KOSPI or KOSDAQ row. The Korean market closes before
// the next Chinese session, so the previous Korean bar is available to a
// pre-open Chinese model without look-ahead.
type KoreaBar struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume_raw"`
}

func ValidKoreaCode(code string) bool {
	return code == KoreaKOSPI || code == KoreaKOSDAQ
}

// NaverKoreaClient reads Naver's public daily index endpoint. The response is a
// Python-like list literal with one JSON-compatible array per line, so parsing
// is line-oriented instead of treating the whole body as JSON.
type NaverKoreaClient struct {
	HTTP     *http.Client
	Endpoint string
	pacer    requestPacer
}

func NewNaverKoreaClient(timeout, interval time.Duration) *NaverKoreaClient {
	return &NaverKoreaClient{
		HTTP:     newHTTPClient(timeout),
		Endpoint: "https://api.finance.naver.com/siseJson.naver",
		pacer:    requestPacer{interval: interval},
	}
}

func (c *NaverKoreaClient) Close() { c.HTTP.CloseIdleConnections() }

func (c *NaverKoreaClient) FetchDaily(ctx context.Context, code, from, through string) ([]KoreaBar, error) {
	if !ValidKoreaCode(code) {
		return nil, &DataError{Reason: "invalid_korea_code"}
	}
	first, err := time.Parse("2006-01-02", from)
	if err != nil || first.Format("2006-01-02") != from {
		return nil, &DataError{Reason: "invalid_korea_from"}
	}
	last, err := time.Parse("2006-01-02", through)
	if err != nil || last.Format("2006-01-02") != through || first.After(last) || last.Sub(first) > 20*366*24*time.Hour {
		return nil, &DataError{Reason: "invalid_korea_range"}
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, errors.New("invalid korea endpoint")
	}
	q := u.Query()
	q.Set("symbol", code)
	q.Set("requestType", "1")
	q.Set("startTime", first.Format("20060102"))
	q.Set("endTime", last.Format("20060102"))
	q.Set("timeframe", "day")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid korea request")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	if err := c.pacer.wait(ctx); err != nil {
		return nil, err
	}
	raw, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return nil, err
	}
	bars, err := parseNaverKoreaDaily(raw, from, through)
	if err != nil {
		return nil, err
	}
	return bars, nil
}

func parseNaverKoreaDaily(raw []byte, from, through string) ([]KoreaBar, error) {
	text := strings.TrimPrefix(string(raw), "\ufeff")
	out := make([]KoreaBar, 0, 1024)
	previous := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
		if !strings.HasPrefix(line, `["`) {
			continue
		}
		var fields []json.RawMessage
		if err := json.Unmarshal([]byte(line), &fields); err != nil || len(fields) < 6 {
			continue
		}
		var stamp string
		if err := json.Unmarshal(fields[0], &stamp); err != nil || len(stamp) != 8 {
			continue
		}
		date := stamp[:4] + "-" + stamp[4:6] + "-" + stamp[6:]
		if date < from || date > through {
			continue
		}
		if date <= previous {
			return nil, &DataError{Reason: "invalid_korea_ordering"}
		}
		values := make([]float64, 5)
		for index := range values {
			value, err := numericRaw(fields[index+1])
			if err != nil {
				return nil, &DataError{Reason: "invalid_korea_numeric"}
			}
			values[index] = value
		}
		bar := KoreaBar{Date: date, Open: values[0], High: values[1], Low: values[2], Close: values[3], Volume: values[4]}
		if bar.Open <= 0 || bar.Close <= 0 || bar.Low <= 0 || bar.High < bar.Open || bar.High < bar.Close || bar.Low > bar.Open || bar.Low > bar.Close || bar.Volume < 0 {
			return nil, &DataError{Reason: "invalid_korea_row"}
		}
		out = append(out, bar)
		previous = date
		if len(out) > KoreaHistoryLimit {
			return nil, &DataError{Reason: "korea_history_exceeds_limit"}
		}
	}
	if len(out) == 0 {
		return nil, &DataError{Reason: "empty_korea_history"}
	}
	return out, nil
}

func numericRaw(raw json.RawMessage) (float64, error) {
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(text), 64)
}
