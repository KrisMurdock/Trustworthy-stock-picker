package market

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"stock-watcher/internal/config"
)

const listingHTML = `<html><title>联讯仪器(688808)新股发行_新浪财经_新浪网</title><table><tr><td>发行日期</td><td>2026-04-03</td></tr><tr><td width="170"><strong>上市日期</strong></td><td>2026-04-24</td></tr></table></html>`

func TestSinaListingParsesIdentityAndListingDate(t *testing.T) {
	gb, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(listingHTML))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{[]byte(listingHTML), gb} {
		l, err := parseSinaListing(raw, "sh688808")
		if err != nil || l.ListedFrom != "2026-04-24" || l.Source != "sina_ipo" || l.Validate() != nil {
			t.Fatalf("wrong listing date: %+v %v", l, err)
		}
	}
	for _, raw := range []string{
		strings.ReplaceAll(listingHTML, "688808", "600036"),
		strings.ReplaceAll(listingHTML, "上市日期", "发行日期"),
		strings.ReplaceAll(listingHTML, "2026-04-24", "2026-02-30"),
		strings.ReplaceAll(listingHTML, "2026-04-24", "1900-01-01"),
		listingHTML + `<td>上市日期</td><td>2026-04-25</td>`,
		`<title>验证码</title><td>上市日期</td><td>2026-04-24</td>`,
	} {
		if _, err := parseSinaListing([]byte(raw), "sh688808"); err == nil {
			t.Fatal("accepted missing/ambiguous/mismatched listing metadata")
		}
	}
	if _, err := parseSinaListing([]byte(listingHTML), ""); err == nil {
		t.Fatal("accepted missing stock code")
	}
}

func TestSinaListingHTTPFailuresAndCancellation(t *testing.T) {
	for _, mode := range []string{"success", "403", "429", "timeout", "cancel", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/688808.phtml" {
					t.Error("incorrect listing path")
				}
				switch mode {
				case "success":
					fmt.Fprint(w, listingHTML)
				case "403":
					w.WriteHeader(403)
				case "429":
					w.WriteHeader(429)
				case "redirect":
					http.Redirect(w, r, "http://127.0.0.1:1/", 302)
				default:
					if mode == "cancel" {
						cancel()
					}
					<-r.Context().Done()
				}
			}))
			defer srv.Close()
			c := NewSinaListingClient(30*time.Millisecond, 0)
			c.Endpoint = srv.URL
			defer c.Close()
			l, err := c.Listing(ctx, config.Stock{Code: "sh688808"})
			if mode == "success" {
				if err != nil || l.ListedFrom != "2026-04-24" {
					t.Fatalf("bad listing: %+v %v", l, err)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted failing metadata request")
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if (mode == "timeout" || mode == "429") && IsIsolatable(err) {
				t.Fatalf("lost retryability: %v", err)
			}
		})
	}
}

func TestListingCooldownIsIndependentOfDailySource(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) }))
	defer srv.Close()
	c := NewSinaListingClient(time.Second, 0)
	c.Endpoint = srv.URL
	defer c.Close()
	s := &Sources{listings: c, Providers: []Provider{{"sina", goodSource("sina")}}}
	for range 5 {
		if _, err := s.Listing(context.Background(), providerStock); !IsProviderFailure(err) {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("unavailable metadata source requested %d times", calls)
	}
	if _, err := s.Fetch(context.Background(), providerStock, "2026-10-13", "2026-10-13", "none"); err != nil {
		t.Fatalf("metadata outage disabled daily prices: %v", err)
	}
}
