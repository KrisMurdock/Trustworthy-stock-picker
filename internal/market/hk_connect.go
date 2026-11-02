package market

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

const (
	hkConnectPageSize  = 100
	maxHKConnectStocks = 2_000
)

// HKConnectUniverseClient discovers the current southbound Stock Connect stock
// list from Eastmoney's public quote board. The list is deliberately kept
// separate from Config.Stocks: it is a data universe, not a recommendation pool.
type HKConnectUniverseClient struct {
	HTTP     *http.Client
	Endpoint string
	pacer    requestPacer
	attempts int
	backoff  time.Duration
}

func NewHKConnectUniverseClient(timeout, interval time.Duration, endpoint string) *HKConnectUniverseClient {
	return &HKConnectUniverseClient{
		HTTP: newHTTPClient(timeout), Endpoint: endpoint,
		pacer: requestPacer{interval: interval}, attempts: 3, backoff: time.Second,
	}
}

func (c *HKConnectUniverseClient) Close() { c.HTTP.CloseIdleConnections() }

type hkUniverseResponse struct {
	RC   *int `json:"rc"`
	Data *struct {
		Total int `json:"total"`
		Diff  []struct {
			Code string `json:"f12"`
			Name string `json:"f14"`
		} `json:"diff"`
	} `json:"data"`
}

func (c *HKConnectUniverseClient) Discover(ctx context.Context) (Universe, error) {
	out := Universe{Version: 1, Market: "hk_connect", Source: "eastmoney", FetchedAt: time.Now().In(Shanghai), Stocks: []config.Stock{}}
	if c == nil || c.HTTP == nil || strings.TrimSpace(c.Endpoint) == "" {
		return out, errors.New("HK Connect universe endpoint is unavailable")
	}
	seen := map[string]bool{}
	total := 0
	rawRows := 0
	for page := 1; ; page++ {
		if page > maxHKConnectStocks/hkConnectPageSize+2 {
			return out, errors.New("HK Connect universe exceeded page limit")
		}
		response, err := c.fetchPage(ctx, page)
		if err != nil {
			return out, err
		}
		if response.Data == nil || response.RC == nil || *response.RC != 0 {
			return out, errors.New("invalid HK Connect universe envelope")
		}
		if page == 1 {
			total = response.Data.Total
			if total < 1 || total > maxHKConnectStocks {
				return out, errors.New("HK Connect universe returned an invalid total")
			}
		} else if response.Data.Total != total {
			return out, errors.New("HK Connect universe total changed during pagination")
		}
		if len(response.Data.Diff) == 0 {
			break
		}
		rawRows += len(response.Data.Diff)
		for _, row := range response.Data.Diff {
			code, ok := hkConnectCode(row.Code)
			name := strings.TrimSpace(row.Name)
			if !ok || name == "" {
				out.ExcludedOther++
				continue
			}
			if seen[code] {
				continue
			}
			seen[code] = true
			out.Stocks = append(out.Stocks, config.Stock{Code: code, Name: name})
		}
		if page*hkConnectPageSize >= total {
			break
		}
	}
	if rawRows < total || len(out.Stocks) == 0 {
		return out, errors.New("HK Connect universe response is incomplete")
	}
	out.Total = total
	sort.Slice(out.Stocks, func(i, j int) bool { return out.Stocks[i].Code < out.Stocks[j].Code })
	return out, nil
}

func (c *HKConnectUniverseClient) fetchPage(ctx context.Context, page int) (hkUniverseResponse, error) {
	var out hkUniverseResponse
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return out, errors.New("invalid HK Connect universe endpoint")
	}
	q := u.Query()
	q.Set("pn", strconv.Itoa(page))
	q.Set("pz", strconv.Itoa(hkConnectPageSize))
	q.Set("po", "1")
	q.Set("np", "1")
	q.Set("ut", "bd1d9ddb04089700cf9c27f6f7426281")
	q.Set("fltt", "2")
	q.Set("invt", "2")
	q.Set("fid", "f12")
	q.Set("fs", "b:DLMK0146,b:DLMK0144")
	q.Set("fields", "f12,f14,f13")
	u.RawQuery = q.Encode()
	attempts := c.attempts
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return out, &Failure{Status: 400, Kind: "request"}
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
		if err := c.pacer.wait(ctx); err != nil {
			return out, err
		}
		raw, err := readResponse(ctx, c.HTTP, req)
		if err == nil {
			if json.Unmarshal(raw, &out) == nil {
				return out, nil
			}
			err = &DataError{Reason: "invalid_hk_connect_universe"}
		}
		if ctx.Err() != nil || !retryableHKUniverseError(err) || attempt == attempts {
			return out, err
		}
		timer := time.NewTimer(c.backoff * time.Duration(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return out, ctx.Err()
		case <-timer.C:
		}
	}
	return out, errors.New("HK Connect universe request failed")
}

func retryableHKUniverseError(err error) bool {
	var failure *Failure
	if !errors.As(err, &failure) {
		return false
	}
	return failure.Retryable()
}

func hkConnectCode(raw string) (string, bool) {
	code := strings.TrimSpace(raw)
	if len(code) != 5 {
		return "", false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	code = "hk" + code
	return code, config.ValidHKStockCode(code)
}

// HKTradingDaysSource supplies the Hong Kong exchange sessions used to choose
// the default as-of date. It is intentionally independent from the mainland
// calendar because Stock Connect holidays differ.
type HKTradingDaysSource interface {
	TradingDays(context.Context, string, string) ([]string, error)
}

var _ HKTradingDaysSource = (*HKDailyClient)(nil)
