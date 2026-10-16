package monitor

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"stock-watcher/internal/storage"
)

type listingFunc func(context.Context, config.Stock) (market.Listing, error)

func (f listingFunc) Listing(ctx context.Context, stock config.Stock) (market.Listing, error) {
	return f(ctx, stock)
}

func listingFor(stock config.Stock, date string) market.Listing {
	return market.Listing{Version: 1, Code: stock.Code, ListedFrom: date, Source: "sina_ipo", FetchedAt: time.Now()}
}

func TestRequiredHistoryStartsAtListingOrDecade(t *testing.T) {
	for _, tc := range []struct{ end, listed, want string }{
		{"2026-10-15", "2002-04-09", "2016-10-15"},
		{"2026-10-15", "2016-10-15", "2016-10-15"},
		{"2026-10-15", "2022-01-12", "2022-01-12"},
		{"2026-10-15", "2026-04-24", "2026-04-24"},
		{"2024-02-29", "2002-04-09", "2014-02-28"},
	} {
		end, _ := time.Parse("2006-01-02", tc.end)
		if got := requiredHistoryStart(end, tc.listed); got != tc.want {
			t.Errorf("%+v: got %s", tc, got)
		}
	}
}

func TestListingCacheAndConfiguredOverride(t *testing.T) {
	m, _, _ := fixture(t)
	stock := m.Config.Stocks[0]
	stock.ListedFrom = ""
	calls := 0
	m.Listings = listingFunc(func(context.Context, config.Stock) (market.Listing, error) {
		calls++
		return listingFor(stock, "2020-07-16"), nil
	})
	resolved, err := m.listingStock(context.Background(), stock)
	if err != nil || resolved.ListedFrom != "2020-07-16" {
		t.Fatalf("resolve failed: %+v %v", resolved, err)
	}
	// A fresh monitor reuses the durable metadata even with the upstream offline.
	restarted := New(m.Config, m.Store, m.Source, nil, nil)
	restarted.Listings = listingFunc(func(context.Context, config.Stock) (market.Listing, error) {
		calls++
		return market.Listing{}, &market.Failure{Kind: "network"}
	})
	resolved, err = restarted.listingStock(context.Background(), stock)
	if err != nil || calls != 1 || resolved.ListedFrom != "2020-07-16" {
		t.Fatalf("cache was not reused: %+v %v calls=%d", resolved, err, calls)
	}
	stock.ListedFrom = "2010-01-04"
	resolved, err = restarted.listingStock(context.Background(), stock)
	if err != nil || resolved.ListedFrom != stock.ListedFrom || calls != 1 {
		t.Fatal("explicit listing date did not override cache")
	}
	if m.Config.Stocks[0].ListedFrom != "2026-07-15" {
		t.Fatal("mutated shared configuration")
	}
}

func TestMissingListingSkipsStockBeforePriceRequests(t *testing.T) {
	for _, mode := range []string{"network", "invalid", "unavailable", "cancel", "corrupt_cache"} {
		t.Run(mode, func(t *testing.T) {
			m, prices, channel := fixture(t)
			m.Config.Stocks[0].ListedFrom = ""
			stock := m.Config.Stocks[0]
			m.Listings = listingFunc(func(context.Context, config.Stock) (market.Listing, error) {
				if mode == "invalid" {
					return listingFor(config.Stock{Code: "sh600036"}, "2002-04-09"), nil
				}
				if mode == "cancel" {
					return market.Listing{}, context.Canceled
				}
				return market.Listing{}, &market.Failure{Kind: "network"}
			})
			if mode == "unavailable" {
				m.Listings = nil
			}
			if mode == "corrupt_cache" {
				if err := m.Store.Write("metadata/listings/"+stock.Code+".json", listingFor(config.Stock{Code: "sh600036"}, "2002-04-09")); err != nil {
					t.Fatal(err)
				}
			}
			_, err := m.Sync(context.Background(), "2026-10-13")
			if err == nil || prices.calls != 0 || channel.calls != 0 {
				t.Fatalf("unknown listing downloaded/published: err=%v calls=%d sends=%d", err, prices.calls, channel.calls)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("lost cancellation")
			}
			if mode == "corrupt_cache" && strings.Contains(err.Error(), "coverage") {
				t.Fatal("storage corruption treated as a provider failure")
			}
		})
	}
}

func TestSyncResolvesListingBeforeDownloadingAllAvailableHistory(t *testing.T) {
	m, prices, channel := fixture(t)
	listingDatabase(t, m)
	m.Config.Stocks[0].ListedFrom = ""
	stock := m.Config.Stocks[0]
	calls := 0
	m.Listings = listingFunc(func(context.Context, config.Stock) (market.Listing, error) {
		if prices.calls != 0 {
			t.Error("prices requested before listing lookup")
		}
		calls++
		return listingFor(stock, "2026-07-15"), nil
	})
	report, err := m.Sync(context.Background(), "2026-10-13")
	if err != nil || calls != 1 || report.AsOf != "2026-10-13" {
		t.Fatalf("new IPO sync failed: %+v %v", report, err)
	}
	h, err := m.loadPreviousHistory(context.Background(), stock.Code, "none", "2026-10-13")
	if err != nil || len(h.Bars) == 0 || h.Bars[0].Date != "2026-07-15" {
		t.Fatalf("did not save entire post-IPO history: %+v %v", h, err)
	}
	nextRun := m.Now().Add(time.Second)
	m.Now = func() time.Time { return nextRun }
	if _, err = m.Sync(context.Background(), "2026-10-13"); err != nil || calls != 1 {
		t.Fatalf("repeat sync did not reuse metadata: %v", err)
	}
	if channel.calls != 0 {
		t.Fatal("sync unexpectedly sent a notification")
	}
}

func listingDatabase(t *testing.T, m *Monitor) {
	t.Helper()
	db, err := storage.InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	m.Market = db
	t.Cleanup(func() { db.Close() })
}

func TestSyncCapsOldStockHistoryAtTenYears(t *testing.T) {
	m, _, _ := fixture(t)
	listingDatabase(t, m)
	m.Config.Daily.Adjust = "qfq"
	m.Config.Daily.HistoryDays = 7300
	m.Config.Stocks[0].ListedFrom = ""
	m.Listings = listingFunc(func(_ context.Context, s config.Stock) (market.Listing, error) {
		return listingFor(s, "1991-04-03"), nil
	})
	if _, err := m.Sync(context.Background(), "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	for _, adjust := range []string{"none", "qfq"} {
		h, err := m.loadPreviousHistory(context.Background(), m.Config.Stocks[0].Code, adjust, "2026-10-13")
		if err != nil || len(h.Bars) == 0 || h.Bars[0].Date != "2016-10-13" || h.Bars[len(h.Bars)-1].Date != "2026-10-13" {
			t.Fatalf("%s history did not stay within the decade: %v", adjust, err)
		}
	}
}

func TestListingFailureContinuesPoolAndPreservesCoverageGate(t *testing.T) {
	m, prices, channel := fixture(t)
	listingDatabase(t, m)
	good := m.Config.Stocks[0]
	m.Config.Stocks = []config.Stock{{Code: "sh600036", Name: "unknown listing"}, good}
	m.Listings = listingFunc(func(context.Context, config.Stock) (market.Listing, error) {
		return market.Listing{}, &market.Failure{Status: 403, Kind: "permission"}
	})
	ctx := context.Background()
	_, err := m.Sync(ctx, "2026-10-13")
	if err == nil || !strings.Contains(err.Error(), "50.00% is below 95.00%") || prices.calls != 1 || channel.calls != 0 {
		t.Fatalf("lookup failure stopped the pool or bypassed the gate: %v prices=%d", err, prices.calls)
	}
	h, err := m.loadPreviousHistory(ctx, good.Code, "none", "2026-10-13")
	if err != nil || len(h.Bars) == 0 {
		t.Fatalf("valid stock was not saved: %v", err)
	}
	view, err := m.Market.LoadDashboard(ctx, m.Config.Stocks, "none")
	if err != nil || view.Summary.Available != 1 || view.Summary.CoveragePct != 50 || view.LatestSync.ErrorKind != "coverage" {
		t.Fatalf("dashboard state did not reflect isolated lookup: %v", err)
	}
	if _, err := m.Market.LoadReport(ctx, "2026-10-13"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("failed gate published report: %v", err)
	}
}

func TestFutureListingSkipsPriceRequests(t *testing.T) {
	m, prices, _ := fixture(t)
	m.Config.Stocks[0].ListedFrom = ""
	m.Listings = listingFunc(func(_ context.Context, s config.Stock) (market.Listing, error) {
		return listingFor(s, "2026-10-14"), nil
	})
	if _, err := m.Sync(context.Background(), "2026-10-13"); err == nil || prices.calls != 0 {
		t.Fatalf("downloaded before listing: %v calls=%d", err, prices.calls)
	}
}

func TestListingCacheWriteFailureAborts(t *testing.T) {
	m, prices, _ := fixture(t)
	m.Config.Stocks[0].ListedFrom = ""
	stock := m.Config.Stocks[0]
	if err := m.Store.Write("metadata", map[string]string{"fixture": "blocks directory creation"}); err != nil {
		t.Fatal(err)
	}
	m.Listings = listingFunc(func(context.Context, config.Stock) (market.Listing, error) {
		return listingFor(stock, "2026-07-15"), nil
	})
	_, err := m.Sync(context.Background(), "2026-10-13")
	if err == nil || market.IsProviderFailure(err) || prices.calls != 0 {
		t.Fatalf("storage failure ignored: %v", err)
	}
	var l market.Listing
	if err := m.Store.Read("metadata/listings/"+stock.Code+".json", &l); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatal("expected blocked metadata directory")
	}
}
