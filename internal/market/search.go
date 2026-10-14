package market

import (
	"context"
	"errors"
	"golang.org/x/text/encoding/simplifiedchinese"
	"io"
	"net/http"
	"net/url"
	"stock-watcher/internal/config"
	"strings"
)

// Search reuses the existing Sina suggest protocol, decoding legacy GB18030 names.
func (c *Client) Search(ctx context.Context, name string) ([]config.Stock, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://suggest3.sinajs.cn/suggest/type=11,12&key="+url.QueryEscape(name), nil)
	if err != nil {
		return nil, errors.New("invalid stock search")
	}
	req.Header.Set("Referer", "https://finance.sina.com.cn/")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Failure{Kind: "search_network"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, &Failure{Status: resp.StatusCode, Kind: "search_status"}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		return nil, errors.New("invalid search response")
	}
	// GBK byte sequences can coincidentally be valid UTF-8 (e.g. 壹石通).
	// The Sina suggest endpoint uses GB18030 unless it explicitly advertises UTF-8.
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "utf-8") {
		body, err = simplifiedchinese.GB18030.NewDecoder().Bytes(body)
		if err != nil {
			return nil, errors.New("invalid search encoding")
		}
	}
	return ParseSearch(string(body))
}
func ParseSearch(text string) ([]config.Stock, error) {
	start := strings.Index(text, "=\"")
	if start < 0 {
		return nil, errors.New("invalid search envelope")
	}
	text = text[start+2:]
	end := strings.Index(text, "\"")
	if end < 0 {
		return nil, errors.New("invalid search envelope")
	}
	stocks := []config.Stock{}
	seen := map[string]bool{}
	for _, entry := range strings.Split(text[:end], ";") {
		fields := strings.Split(entry, ",")
		if len(fields) < 5 {
			continue
		}
		code := strings.TrimSpace(fields[3])
		if !config.ValidCode(code) || seen[code] {
			continue
		}
		seen[code] = true
		stocks = append(stocks, config.Stock{Code: code, Name: strings.TrimSpace(fields[4])})
	}
	return stocks, nil
}
