package market

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

// SinaClient implements the upstream used by AKShare stock_zh_a_daily in Go.
// It decodes KLC2 OHLCV and applies separately fetched adjustment factors.
type SinaClient struct {
	HTTP     *http.Client
	Endpoint string
	pacer    requestPacer
}

func NewSinaClient(timeout, interval time.Duration) *SinaClient {
	return &SinaClient{HTTP: newHTTPClient(timeout), Endpoint: "https://finance.sina.com.cn/realstock/company", pacer: requestPacer{interval: interval}}
}

func (c *SinaClient) Close() { c.HTTP.CloseIdleConnections() }

func (c *SinaClient) get(ctx context.Context, code, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Endpoint, "/")+"/"+code+"/"+path, nil)
	if err != nil {
		return nil, &Failure{Status: 400, Kind: "request"}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	if err := c.pacer.wait(ctx); err != nil {
		return nil, err
	}
	return readResponse(ctx, c.HTTP, req)
}

func sinaAssignment(raw []byte, name string) ([]byte, error) {
	s := strings.TrimSpace(string(raw))
	prefix := "var " + name + "="
	if !strings.HasPrefix(s, prefix) {
		return nil, &DataError{Reason: "invalid_sina_envelope"}
	}
	s = strings.TrimSpace(s[len(prefix):])
	decoder := json.NewDecoder(strings.NewReader(s))
	var payload json.RawMessage
	if decoder.Decode(&payload) != nil {
		return nil, &DataError{Reason: "invalid_sina_envelope"}
	}
	tail := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s[decoder.InputOffset():]), ";"))
	if tail != "" && !(len(tail) >= 4 && strings.HasPrefix(tail, "/*") && strings.HasSuffix(tail, "*/") && !strings.Contains(tail[2:len(tail)-2], "*/")) {
		return nil, &DataError{Reason: "invalid_sina_envelope"}
	}
	return payload, nil
}

func (c *SinaClient) Fetch(ctx context.Context, stock config.Stock, start, end, adjust string) (History, error) {
	h, _, err := requestHistory(stock, start, end, adjust, "sina")
	if err != nil {
		return h, err
	}
	raw, err := c.get(ctx, stock.Code, "hisdata_klc2/klc_kl.js")
	if err != nil {
		return History{}, err
	}
	payload, err := sinaAssignment(raw, "KLC_K2_"+stock.Code)
	if err != nil {
		return History{}, err
	}
	var encoded string
	if json.Unmarshal(payload, &encoded) != nil {
		return History{}, &DataError{Reason: "invalid_sina_payload"}
	}
	bars, err := decodeSinaKLC2(encoded)
	if err != nil {
		return History{}, err
	}
	previous := ""
	for _, bar := range bars {
		if bar.Date <= previous {
			return History{}, &DataError{Reason: "invalid_sina_ordering"}
		}
		previous = bar.Date
	}
	first := sort.Search(len(bars), func(i int) bool { return bars[i].Date >= start })
	if first > 0 && (first == len(bars) || bars[first].Date != start) {
		first--
	}
	last := sort.Search(len(bars), func(i int) bool { return bars[i].Date > end })
	if last <= first {
		return History{}, &DataError{Reason: "empty_requested_window"}
	}
	h.Bars = bars[first:last]
	if adjust != "none" {
		raw, err = c.get(ctx, stock.Code, adjust+".js")
		if err != nil {
			return History{}, err
		}
		payload, err = sinaAssignment(raw, stock.Code+adjust)
		if err != nil {
			return History{}, err
		}
		if err = applySinaFactors(h.Bars, payload, adjust); err != nil {
			return History{}, err
		}
	}
	for i := range h.Bars {
		bar := &h.Bars[i]
		for _, v := range []*float64{&bar.Open, &bar.High, &bar.Low, &bar.Close} {
			*v = math.RoundToEven(*v*100) / 100
		}
	}
	if ctx.Err() != nil {
		return History{}, ctx.Err()
	}
	return finishHistory(h, start)
}

func applySinaFactors(bars []Bar, raw []byte, adjust string) error {
	var factors struct {
		Total int `json:"total"`
		Data  []struct {
			Date   string          `json:"d"`
			Factor json.RawMessage `json:"f"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &factors) != nil || len(factors.Data) == 0 || factors.Total != len(factors.Data) {
		return &DataError{Reason: "invalid_sina_factors"}
	}
	sort.Slice(factors.Data, func(i, j int) bool { return factors.Data[i].Date < factors.Data[j].Date })
	values := make([]float64, len(factors.Data))
	for i, f := range factors.Data {
		_, err := time.Parse("2006-01-02", f.Date)
		v, numErr := numeric(f.Factor)
		if err != nil || numErr != nil || v <= 0 || (i > 0 && factors.Data[i-1].Date == f.Date) {
			return &DataError{Reason: "invalid_sina_factors"}
		}
		values[i] = v
	}
	index := -1
	for i := range bars {
		bar := &bars[i]
		for index+1 < len(values) && factors.Data[index+1].Date <= bar.Date {
			index++
		}
		if index < 0 {
			return &DataError{Reason: "missing_sina_factor"}
		}
		factor := values[index]
		if adjust == "qfq" {
			factor = 1 / factor
		}
		bar.Open *= factor
		bar.High *= factor
		bar.Low *= factor
		bar.Close *= factor
	}
	return nil
}

func (c *SinaClient) FetchWindow(ctx context.Context, stock config.Stock, start, end, adjust string, count int) (History, error) {
	return recentWindow(ctx, c, stock, start, end, adjust, count)
}
