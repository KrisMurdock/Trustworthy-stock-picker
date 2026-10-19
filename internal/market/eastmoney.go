package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

// EastmoneyClient uses the upstream HTTP endpoint used by AKShare's
// stock_zh_a_hist. It neither imports AKShare nor invokes a Python service.
type EastmoneyClient struct {
	HTTP     *http.Client
	Endpoint string
	pacer    requestPacer
}

func NewEastmoneyClient(timeout, interval time.Duration) *EastmoneyClient {
	return &EastmoneyClient{HTTP: newHTTPClient(timeout), Endpoint: "https://push2his.eastmoney.com/api/qt/stock/kline/get", pacer: requestPacer{interval: interval}}
}
func (c *EastmoneyClient) Close() { c.HTTP.CloseIdleConnections() }

func (c *EastmoneyClient) Fetch(ctx context.Context, stock config.Stock, start, end, adjust string) (History, error) {
	h, probe, err := requestHistory(stock, start, end, adjust, "eastmoney")
	if err != nil {
		return h, err
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return h, &Failure{Status: 400, Kind: "request"}
	}
	exchange := "0"
	if strings.HasPrefix(stock.Code, "sh") {
		exchange = "1"
	}
	q := u.Query()
	q.Set("fields1", "f1,f2,f3,f4,f5,f6")
	q.Set("fields2", "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61")
	q.Set("ut", "7eea3edcaed734bea9cbfc24409ed989") // Public endpoint identifier, not a user credential.
	q.Set("klt", "101")
	q.Set("fqt", map[string]string{"none": "0", "qfq": "1", "hfq": "2"}[adjust])
	q.Set("secid", exchange+"."+stock.Code[2:])
	q.Set("beg", compactDate(probe))
	q.Set("end", compactDate(end))
	q.Set("lmt", "6000")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return h, &Failure{Status: 400, Kind: "request"}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	if err := c.pacer.wait(ctx); err != nil {
		return h, err
	}
	raw, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return h, err
	}
	var response struct {
		RC   *int `json:"rc"`
		Data *struct {
			Code   string   `json:"code"`
			Market *int     `json:"market"`
			Name   string   `json:"name"`
			Klines []string `json:"klines"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil || response.RC == nil || *response.RC != 0 || response.Data == nil {
		return History{}, &DataError{Reason: "invalid_eastmoney_envelope"}
	}
	d := response.Data
	if d.Code != stock.Code[2:] || d.Market == nil || (*d.Market == 1) != (exchange == "1") || (*d.Market != 0 && *d.Market != 1) {
		return History{}, &DataError{Reason: "eastmoney_stock_mismatch"}
	}
	if len(d.Klines) >= MaxHistoryBars {
		return History{}, &DataError{Reason: "eastmoney_history_may_be_truncated"}
	}
	if d.Name != "" {
		h.Name = d.Name
	}
	for _, line := range d.Klines {
		row := strings.Split(line, ",")
		if len(row) != 11 || row[0] < probe || row[0] > end {
			return History{}, &DataError{Reason: "invalid_eastmoney_row"}
		}
		bar := Bar{Date: row[0]}
		for i, dest := range []*float64{&bar.Open, &bar.Close, &bar.High, &bar.Low, &bar.Volume} {
			*dest, err = numeric(json.RawMessage(row[i+1]))
			if err != nil {
				return History{}, err
			}
		}
		h.Bars = append(h.Bars, bar)
	}
	return finishHistory(h, start)
}

func (c *EastmoneyClient) FetchWindow(ctx context.Context, stock config.Stock, start, end, adjust string, count int) (History, error) {
	return recentWindow(ctx, c, stock, start, end, adjust, count)
}
