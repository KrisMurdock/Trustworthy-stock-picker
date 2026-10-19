package market

import (
	"context"
	"errors"
	"html"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// Listing records a separately verified listing date, never a first price bar.
type Listing struct {
	Version    int       `json:"schema_version"`
	Code       string    `json:"code"`
	ListedFrom string    `json:"listed_from"`
	Source     string    `json:"source"`
	FetchedAt  time.Time `json:"fetched_at"`
}

func (l Listing) Validate() error {
	date, err := time.Parse("2006-01-02", l.ListedFrom)
	if l.Version != 1 || !config.ValidCode(l.Code) || l.Source != "sina_ipo" || l.FetchedAt.IsZero() || err != nil || date.Year() < 1970 || date.After(l.FetchedAt.AddDate(1, 0, 0)) {
		return errors.New("invalid listing metadata")
	}
	return nil
}

type ListingSource interface {
	Listing(context.Context, config.Stock) (Listing, error)
}

// SinaListingClient reads the IPO-information page used for listing metadata.
// It is independent of daily-provider ordering and has no credentials.
type SinaListingClient struct {
	HTTP     *http.Client
	Endpoint string
	pacer    requestPacer
}

func NewSinaListingClient(timeout, interval time.Duration) *SinaListingClient {
	return &SinaListingClient{HTTP: newHTTPClient(timeout), Endpoint: "https://vip.stock.finance.sina.com.cn/corp/go.php/vISSUE_NewStock/stockid", pacer: requestPacer{interval: interval}}
}

func (c *SinaListingClient) Close() { c.HTTP.CloseIdleConnections() }

var listingTitle = regexp.MustCompile(`(?is)<title>\s*([^<]+)\s*</title>`)
var listingDateCell = regexp.MustCompile(`(?is)<td\b[^>]*>\s*(?:<strong>\s*)?上市日期\s*(?:</strong>\s*)?</td>\s*<td\b[^>]*>\s*(\d{4}-\d{2}-\d{2})\s*</td>`)

func parseSinaListing(raw []byte, code string) (Listing, error) {
	if !config.ValidCode(code) {
		return Listing{}, &DataError{Reason: "invalid_listing_stock_code"}
	}
	if !utf8.Valid(raw) {
		var err error
		raw, err = simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
		if err != nil {
			return Listing{}, &DataError{Reason: "invalid_listing_encoding"}
		}
	}
	title := listingTitle.FindSubmatch(raw)
	if len(title) != 2 || !strings.Contains(html.UnescapeString(string(title[1])), "("+code[2:]+")新股发行") {
		return Listing{}, &DataError{Reason: "listing_stock_mismatch"}
	}
	values := listingDateCell.FindAllSubmatch(raw, -1)
	if len(values) != 1 {
		return Listing{}, &DataError{Reason: "listing_date_missing_or_ambiguous"}
	}
	l := Listing{Version: 1, Code: code, ListedFrom: string(values[0][1]), Source: "sina_ipo", FetchedAt: time.Now().In(Shanghai)}
	if l.Validate() != nil {
		return Listing{}, &DataError{Reason: "invalid_listing_date"}
	}
	return l, nil
}

func (c *SinaListingClient) Listing(ctx context.Context, stock config.Stock) (Listing, error) {
	if !config.ValidCode(stock.Code) {
		return Listing{}, errors.New("invalid listing stock code")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Endpoint, "/")+"/"+stock.Code[2:]+".phtml", nil)
	if err != nil {
		return Listing{}, &Failure{Status: 400, Kind: "listing_request"}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	if err = c.pacer.wait(ctx); err != nil {
		return Listing{}, err
	}
	raw, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return Listing{}, err
	}
	return parseSinaListing(raw, stock.Code)
}

func (s *Sources) Listing(ctx context.Context, stock config.Stock) (Listing, error) {
	if err := ctx.Err(); err != nil {
		return Listing{}, err
	}
	if s.listings == nil {
		return Listing{}, &DataError{Reason: "listing_date_unavailable"}
	}
	const name = "sina_listing"
	if !s.available(name) {
		return Listing{}, &Failure{Kind: "listing_cooldown"}
	}
	l, err := s.listings.Listing(ctx, stock)
	s.record(name, err)
	if ctx.Err() != nil {
		return Listing{}, ctx.Err()
	}
	return l, err
}
