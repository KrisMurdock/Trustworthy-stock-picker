package monitor

import (
	"context"
	"errors"
	"io/fs"
	"time"

	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
)

// listingStock never infers an IPO date from a provider's first available bar.
// Only explicit configuration or separately verified metadata may shorten a
// stock's required history. Durable metadata uses the same guarded Windows store.
func (m *Monitor) listingStock(ctx context.Context, stock config.Stock) (config.Stock, error) {
	if err := ctx.Err(); err != nil {
		return stock, err
	}
	if stock.ListedFrom != "" {
		return stock, nil
	}
	path := "metadata/listings/" + stock.Code + ".json"
	var listing market.Listing
	err := m.Store.Read(path, &listing)
	if err == nil {
		if listing.Validate() != nil || listing.Code != stock.Code {
			return stock, errors.New("stored listing metadata is invalid or belongs to another stock")
		}
		stock.ListedFrom = listing.ListedFrom
		return stock, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return stock, err
	}
	if m.Listings == nil {
		return stock, &market.DataError{Reason: "listing_date_unavailable"}
	}
	listing, err = m.Listings.Listing(ctx, stock)
	if err != nil {
		return stock, err
	}
	if err = ctx.Err(); err != nil {
		return stock, err
	}
	if listing.Validate() != nil || listing.Code != stock.Code {
		return stock, &market.DataError{Reason: "invalid_listing_metadata"}
	}
	if err = m.Store.Write(path, listing); err != nil {
		return stock, err
	}
	stock.ListedFrom = listing.ListedFrom
	m.Log.Info("Stock listing date confirmed", "code", stock.Code, "listed_from", stock.ListedFrom, "source", listing.Source)
	return stock, nil
}

func requiredHistoryStart(end time.Time, listedFrom string) string {
	start := historyStart(end)
	if listedFrom > start {
		return listedFrom
	}
	return start
}
