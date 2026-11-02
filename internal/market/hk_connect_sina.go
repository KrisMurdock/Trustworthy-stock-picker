package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

const (
	hkSinaPageSize     = 60
	hkSinaMaxPages     = 60
	hkSinaMaxStocks    = 2_000
	hkSinaQuoteBatch   = 50
	hkSinaQuoteMaxBody = 2 << 20
)

var hkSinaNodes = []string{"hgt_hk", "sgt_hk"}

// SinaHKConnectUniverseClient discovers the current southbound list from
// Sina's 港股通(沪/深) boards, then uses Tencent quote metadata to keep stocks
// and exclude listed ETFs/products. Eastmoney remains a separate fallback.
type SinaHKConnectUniverseClient struct {
	HTTP          *http.Client
	Endpoint      string
	QuoteEndpoint string
	pacer         requestPacer
}

func NewSinaHKConnectUniverseClient(timeout, interval time.Duration, endpoint, quoteEndpoint string) *SinaHKConnectUniverseClient {
	return &SinaHKConnectUniverseClient{HTTP: newHTTPClient(timeout), Endpoint: endpoint, QuoteEndpoint: quoteEndpoint, pacer: requestPacer{interval: interval}}
}

func (c *SinaHKConnectUniverseClient) Close() { c.HTTP.CloseIdleConnections() }

type hkSinaRow struct {
	Symbol string `json:"symbol"`
	Name   string `json:"name"`
}

func (c *SinaHKConnectUniverseClient) Discover(ctx context.Context) (Universe, error) {
	out := Universe{Version: 1, Market: "hk_connect", Source: "sina", FetchedAt: time.Now().In(Shanghai), Stocks: []config.Stock{}}
	if c == nil || c.HTTP == nil || strings.TrimSpace(c.Endpoint) == "" {
		return out, errors.New("Sina HK Connect universe endpoint is unavailable")
	}
	seen := map[string]bool{}
	for _, node := range hkSinaNodes {
		nodeRows := 0
		for page := 1; page <= hkSinaMaxPages; page++ {
			rows, err := c.fetchPage(ctx, node, page)
			if err != nil {
				return out, fmt.Errorf("Sina HK Connect board %s page %d: %w", node, page, err)
			}
			if len(rows) == 0 {
				break
			}
			nodeRows += len(rows)
			if nodeRows > hkSinaMaxStocks {
				return out, errors.New("Sina HK Connect universe exceeded stock limit")
			}
			for _, row := range rows {
				code, ok := hkConnectCode(row.Symbol)
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
			if page == hkSinaMaxPages {
				return out, errors.New("Sina HK Connect universe exceeded page limit")
			}
		}
		if nodeRows == 0 {
			return out, errors.New("Sina HK Connect universe returned an empty board")
		}
	}
	if len(out.Stocks) == 0 {
		return out, errors.New("Sina HK Connect universe returned no eligible stocks")
	}
	sort.Slice(out.Stocks, func(i, j int) bool { return out.Stocks[i].Code < out.Stocks[j].Code })
	stocks, excluded, err := c.filterEquities(ctx, out.Stocks)
	if err != nil {
		return out, fmt.Errorf("Sina HK Connect security types: %w", err)
	}
	out.Stocks = stocks
	out.ExcludedOther += excluded
	out.Total = len(out.Stocks)
	if len(out.Stocks) == 0 {
		return out, errors.New("Sina HK Connect universe returned no eligible stocks")
	}
	return out, nil
}

func (c *SinaHKConnectUniverseClient) filterEquities(ctx context.Context, stocks []config.Stock) ([]config.Stock, int, error) {
	if c == nil || c.HTTP == nil || strings.TrimSpace(c.QuoteEndpoint) == "" {
		return nil, 0, errors.New("Sina HK Connect quote endpoint is unavailable")
	}
	out := make([]config.Stock, 0, len(stocks))
	excluded := 0
	for start := 0; start < len(stocks); start += hkSinaQuoteBatch {
		end := start + hkSinaQuoteBatch
		if end > len(stocks) {
			end = len(stocks)
		}
		types, err := c.fetchQuoteTypes(ctx, stocks[start:end])
		if err != nil {
			return nil, 0, err
		}
		for _, stock := range stocks[start:end] {
			kind, ok := types[stock.Code]
			if !ok {
				return nil, 0, &DataError{Reason: "missing_hk_connect_quote"}
			}
			switch {
			case kind == "GP":
				out = append(out, stock)
			case strings.HasPrefix(kind, "GP-"):
				excluded++
			default:
				return nil, 0, &DataError{Reason: "invalid_hk_connect_quote_type"}
			}
		}
	}
	return out, excluded, nil
}

func (c *SinaHKConnectUniverseClient) fetchQuoteTypes(ctx context.Context, stocks []config.Stock) (map[string]string, error) {
	u, err := url.Parse(c.QuoteEndpoint)
	if err != nil {
		return nil, errors.New("invalid Sina HK Connect quote endpoint")
	}
	codes := make([]string, 0, len(stocks))
	for _, stock := range stocks {
		codes = append(codes, stock.Code)
	}
	u.RawQuery = "q=" + strings.Join(codes, ",")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &Failure{Status: 400, Kind: "request"}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	if err := c.pacer.wait(ctx); err != nil {
		return nil, err
	}
	raw, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return nil, err
	}
	if len(raw) > hkSinaQuoteMaxBody {
		return nil, &DataError{Reason: "oversized_hk_connect_quote_response"}
	}
	out := make(map[string]string, len(stocks))
	for _, statement := range strings.Split(string(raw), ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		equal := strings.IndexByte(statement, '=')
		if equal < 0 || !strings.HasPrefix(statement[:equal], "v_hk") {
			return nil, &DataError{Reason: "invalid_hk_connect_quote_response"}
		}
		code := strings.TrimPrefix(statement[:equal], "v_")
		if !config.ValidHKStockCode(code) {
			return nil, &DataError{Reason: "invalid_hk_connect_quote_code"}
		}
		payload := strings.Trim(strings.TrimSpace(statement[equal+1:]), `"`)
		kind := ""
		for _, field := range strings.Split(payload, "~") {
			if strings.HasPrefix(field, "GP") {
				kind = field
			}
		}
		if kind == "" {
			return nil, &DataError{Reason: "missing_hk_connect_quote_type"}
		}
		out[code] = kind
	}
	return out, nil
}

func (c *SinaHKConnectUniverseClient) fetchPage(ctx context.Context, node string, page int) ([]hkSinaRow, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, errors.New("invalid Sina HK Connect universe endpoint")
	}
	q := u.Query()
	q.Set("page", strconv.Itoa(page))
	q.Set("num", strconv.Itoa(hkSinaPageSize))
	q.Set("sort", "symbol")
	q.Set("asc", "1")
	q.Set("node", node)
	q.Set("_s_r_a", "page")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &Failure{Status: 400, Kind: "request"}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	req.Header.Set("Referer", "https://vip.stock.finance.sina.com.cn/mkt/")
	if err := c.pacer.wait(ctx); err != nil {
		return nil, err
	}
	raw, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return nil, err
	}
	var rows []hkSinaRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, &DataError{Reason: "invalid_hk_connect_sina_rows"}
	}
	return rows, nil
}
