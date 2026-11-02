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
	universePageSize  = 100
	maxUniverseStocks = 10_000
)

// universeNodes are the Sina market nodes for Shanghai and Shenzhen A shares.
// They deliberately exclude Beijing and B-share nodes; listed_from is not
// provided by this endpoint and is left empty.
var universeNodes = []string{"sh_a", "sz_a"}

// Universe is the current, tradeable Shanghai/Shenzhen A-share list. It is
// intentionally separate from Config.Stocks: the production pool remains the
// small recommendation universe, while the full-market feed has its own store.
type Universe struct {
	Version          int            `json:"schema_version"`
	FetchedAt        time.Time      `json:"fetched_at"`
	Total            int            `json:"total"`
	Stocks           []config.Stock `json:"stocks"`
	ExcludedST       int            `json:"excluded_st"`
	ExcludedDelisted int            `json:"excluded_delisted"`
	ExcludedOther    int            `json:"excluded_other"`
}

type UniverseClient struct {
	HTTP          *http.Client
	Endpoint      string
	CountEndpoint string
	pacer         requestPacer
}

func NewUniverseClient(timeout, interval time.Duration, endpoint, countEndpoint string) *UniverseClient {
	return &UniverseClient{HTTP: newHTTPClient(timeout), Endpoint: endpoint, CountEndpoint: countEndpoint, pacer: requestPacer{interval: interval}}
}

func (c *UniverseClient) Close() { c.HTTP.CloseIdleConnections() }

type universeRow struct {
	code string
	name string
}

func (c *UniverseClient) Discover(ctx context.Context) (Universe, error) {
	out := Universe{Version: 1, FetchedAt: time.Now().In(Shanghai), Stocks: []config.Stock{}}
	if c == nil || c.HTTP == nil || strings.TrimSpace(c.Endpoint) == "" || strings.TrimSpace(c.CountEndpoint) == "" {
		return out, errors.New("universe endpoints are unavailable")
	}
	rows := []universeRow{}
	for _, node := range universeNodes {
		count, err := c.countNode(ctx, node)
		if err != nil {
			return out, err
		}
		if count < 1 || count > maxUniverseStocks {
			return out, errors.New("stock universe returned an invalid total")
		}
		out.Total += count
		nodeRows := 0
		for page := 1; ; page++ {
			if page > maxUniverseStocks/universePageSize+2 {
				return out, errors.New("stock universe exceeded page limit")
			}
			items, err := c.listPage(ctx, node, page)
			if err != nil {
				return out, err
			}
			if len(items) == 0 {
				break
			}
			rows = append(rows, items...)
			nodeRows += len(items)
			if page*universePageSize >= count {
				break
			}
		}
		if nodeRows < count {
			return out, errors.New("stock universe response is incomplete")
		}
	}
	if len(rows) < out.Total {
		return out, errors.New("stock universe response is incomplete")
	}

	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.name)
		code := universeCode(row.code)
		if code == "" || name == "" {
			out.ExcludedOther++
			continue
		}
		if seen[code] {
			continue
		}
		if IsSTStockName(name) {
			out.ExcludedST++
			continue
		}
		if isDelistedName(name) {
			out.ExcludedDelisted++
			continue
		}
		seen[code] = true
		out.Stocks = append(out.Stocks, config.Stock{Code: code, Name: name})
	}
	if len(out.Stocks) == 0 {
		return out, errors.New("stock universe returned no eligible stocks")
	}
	sort.Slice(out.Stocks, func(i, j int) bool { return out.Stocks[i].Code < out.Stocks[j].Code })
	return out, nil
}

func (c *UniverseClient) countNode(ctx context.Context, node string) (int, error) {
	u, err := url.Parse(c.CountEndpoint)
	if err != nil {
		return 0, errors.New("invalid stock universe count endpoint")
	}
	q := u.Query()
	q.Set("node", node)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, &Failure{Kind: "universe_request"}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	req.Header.Set("Referer", "https://vip.stock.finance.sina.com.cn/")
	if err := c.pacer.wait(ctx); err != nil {
		return 0, err
	}
	raw, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return 0, err
	}
	text := strings.TrimSpace(string(raw))
	text = strings.Trim(text, `"`)
	if text == "" || text == "null" {
		return 0, &DataError{Reason: "invalid_universe_count"}
	}
	count, err := strconv.Atoi(text)
	if err != nil {
		return 0, &DataError{Reason: "invalid_universe_count"}
	}
	return count, nil
}

func (c *UniverseClient) listPage(ctx context.Context, node string, page int) ([]universeRow, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, errors.New("invalid stock universe endpoint")
	}
	q := u.Query()
	q.Set("page", strconv.Itoa(page))
	q.Set("num", strconv.Itoa(universePageSize))
	q.Set("sort", "symbol")
	q.Set("asc", "1")
	q.Set("node", node)
	q.Set("symbol", "")
	q.Set("_s_r_a", "page")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &Failure{Kind: "universe_request"}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	req.Header.Set("Referer", "https://vip.stock.finance.sina.com.cn/")
	if err := c.pacer.wait(ctx); err != nil {
		return nil, err
	}
	raw, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var decoded []struct {
		Symbol string `json:"symbol"`
		Code   string `json:"code"`
		Name   string `json:"name"`
	}
	if json.Unmarshal(raw, &decoded) != nil {
		return nil, &DataError{Reason: "invalid_universe_rows"}
	}
	rows := make([]universeRow, 0, len(decoded))
	for _, item := range decoded {
		symbol := strings.TrimSpace(item.Symbol)
		code := strings.TrimSpace(item.Code)
		if symbol == "" {
			symbol = code
		}
		if code != "" && len(symbol) > 6 && !strings.HasSuffix(symbol, code) {
			rows = append(rows, universeRow{code: "", name: item.Name})
			continue
		}
		rows = append(rows, universeRow{code: symbol, name: item.Name})
	}
	return rows, nil
}

// universeCode accepts only sh60xxxx/sh68xxxx/sz00xxxx/sz30xxxx so Shanghai
// and Shenzhen B shares, Beijing listings and indices are all excluded.
func universeCode(symbol string) string {
	symbol = strings.ToLower(strings.TrimSpace(symbol))
	if len(symbol) != 8 {
		return ""
	}
	prefix, code := symbol[:2], symbol[2:]
	if (prefix != "sh" && prefix != "sz") || !config.ValidCode(symbol) {
		return ""
	}
	switch {
	case prefix == "sh" && (strings.HasPrefix(code, "60") || strings.HasPrefix(code, "68")):
	case prefix == "sz" && (strings.HasPrefix(code, "00") || strings.HasPrefix(code, "30")):
	default:
		return ""
	}
	return symbol
}

// IsSTStockName excludes the current risk-warning prefixes used by Shanghai
// and Shenzhen. It does not attempt to infer historical ST status.
func IsSTStockName(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	// Sina prepends ex-rights markers to the displayed name on the ex-date, so
	// an ST stock can appear as XD*ST..., XRST... or DR*ST... for one session.
	for _, marker := range []string{"XD", "XR", "DR"} {
		if strings.HasPrefix(n, marker) {
			n = strings.TrimSpace(n[len(marker):])
			break
		}
	}
	return strings.HasPrefix(n, "ST") || strings.HasPrefix(n, "*ST") ||
		strings.HasPrefix(n, "SST") || strings.HasPrefix(n, "S*ST")
}

func isDelistedName(name string) bool {
	return strings.Contains(name, "退")
}
