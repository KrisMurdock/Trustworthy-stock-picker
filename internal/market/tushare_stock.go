package market

import (
	"context"
	"sort"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

// StockBasic is the small listing identity and industry surface needed by
// full-market valuation. The industry is a current vendor snapshot and is not
// used to reconstruct historical peer groups.
type StockBasic struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Industry string `json:"industry,omitempty"`
}

// FetchStockBasics returns currently listed Shanghai/Shenzhen A shares with
// their Tushare industry labels. It deliberately filters Beijing, B-share and
// non-standard symbols so the result matches the full-market daily universe.
func (c *TushareClient) FetchStockBasics(ctx context.Context) ([]StockBasic, error) {
	rows, err := c.queryParams(ctx, "stock_basic", map[string]string{"list_status": "L"}, "ts_code,name,industry", false)
	if err != nil {
		return nil, err
	}
	out := make([]StockBasic, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		tsCode := strings.TrimSpace(rawText(row["ts_code"]))
		name := strings.TrimSpace(rawText(row["name"]))
		parts := strings.Split(tsCode, ".")
		if len(parts) != 2 || len(parts[0]) != 6 || name == "" {
			return nil, &DataError{Reason: "invalid_tushare_stock_basic_row"}
		}
		code := strings.ToLower(parts[1]) + parts[0]
		if !config.ValidCode(code) {
			continue
		}
		if seen[code] {
			return nil, &DataError{Reason: "duplicate_tushare_stock_basic"}
		}
		seen[code] = true
		out = append(out, StockBasic{Code: code, Name: name, Industry: strings.TrimSpace(rawText(row["industry"]))})
	}
	if len(out) == 0 {
		return nil, &DataError{Reason: "empty_tushare_stock_basic"}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}
