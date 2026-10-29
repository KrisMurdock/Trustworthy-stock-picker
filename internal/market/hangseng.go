package market

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"time"
)

const (
	HangSengCode         = "hkHSI"
	HangSengHistoryLimit = 640
)

// HangSengBar is one daily Hang Seng Index row. The model treats it as a
// shadow-only afternoon feature and always reads bars strictly before the
// Chinese target date.
type HangSengBar struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume_raw"`
}

type TencentHangSengClient struct {
	HTTP     *http.Client
	Endpoint string
	pacer    requestPacer
}

func NewTencentHangSengClient(timeout, interval time.Duration) *TencentHangSengClient {
	return &TencentHangSengClient{
		HTTP: newHTTPClient(timeout), Endpoint: "https://web.ifzq.gtimg.cn/appstock/app/fqkline/get",
		pacer: requestPacer{interval: interval},
	}
}

func (c *TencentHangSengClient) Close() { c.HTTP.CloseIdleConnections() }

func (c *TencentHangSengClient) FetchDaily(ctx context.Context, through string) ([]HangSengBar, error) {
	if through != "" {
		if parsed, err := time.Parse("2006-01-02", through); err != nil || parsed.Format("2006-01-02") != through {
			return nil, &DataError{Reason: "invalid_hang_seng_through"}
		}
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, errors.New("invalid hang seng endpoint")
	}
	q := u.Query()
	q.Set("param", HangSengCode+",day,,,"+"640"+",qfq")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid hang seng request")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	if err := c.pacer.wait(ctx); err != nil {
		return nil, err
	}
	raw, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return nil, err
	}
	return parseTencentHangSeng(raw, through)
}

func parseTencentHangSeng(raw []byte, through string) ([]HangSengBar, error) {
	var envelope struct {
		Code *int                       `json:"code"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if !json.Valid(raw) || json.Unmarshal(raw, &envelope) != nil || envelope.Code == nil || *envelope.Code != 0 {
		return nil, &DataError{Reason: "invalid_hang_seng_envelope"}
	}
	encoded, ok := envelope.Data[HangSengCode]
	if !ok {
		return nil, &DataError{Reason: "hang_seng_code_mismatch"}
	}
	var stock map[string]json.RawMessage
	if json.Unmarshal(encoded, &stock) != nil {
		return nil, &DataError{Reason: "invalid_hang_seng_response"}
	}
	key := "qfqday"
	if _, ok := stock[key]; !ok {
		key = "day"
	}
	var rows [][]json.RawMessage
	if json.Unmarshal(stock[key], &rows) != nil || len(rows) == 0 || len(rows) > HangSengHistoryLimit+1 {
		return nil, &DataError{Reason: "missing_hang_seng_history"}
	}
	out := make([]HangSengBar, 0, len(rows))
	previous := ""
	for _, row := range rows {
		if len(row) < 6 {
			return nil, &DataError{Reason: "invalid_hang_seng_row"}
		}
		var date string
		if json.Unmarshal(row[0], &date) != nil {
			return nil, &DataError{Reason: "invalid_hang_seng_date"}
		}
		parsed, err := time.Parse("2006-01-02", date)
		if err != nil || parsed.Format("2006-01-02") != date || parsed.Weekday() == time.Saturday || parsed.Weekday() == time.Sunday {
			return nil, &DataError{Reason: "invalid_hang_seng_date"}
		}
		if date <= previous {
			return nil, &DataError{Reason: "invalid_hang_seng_ordering"}
		}
		values := make([]float64, 5)
		for index := range values {
			value, err := numericRaw(row[index+1])
			if err != nil {
				return nil, &DataError{Reason: "invalid_hang_seng_numeric"}
			}
			values[index] = value
		}
		bar := HangSengBar{Date: date, Open: values[0], Close: values[1], High: values[2], Low: values[3], Volume: values[4]}
		if bar.Open <= 0 || bar.Close <= 0 || bar.Low <= 0 || bar.High < bar.Open || bar.High < bar.Close || bar.Low > bar.Open || bar.Low > bar.Close || bar.Volume < 0 {
			return nil, &DataError{Reason: "invalid_hang_seng_row"}
		}
		if through == "" || date <= through {
			out = append(out, bar)
		}
		previous = date
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	if len(out) == 0 {
		return nil, &DataError{Reason: "empty_hang_seng_history"}
	}
	return out, nil
}
