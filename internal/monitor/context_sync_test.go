package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/marketcontext"
)

func TestSyncRunsMarketContextAlongsideStockCollection(t *testing.T) {
	m, source, _ := fixture(t)
	stockStarted := make(chan struct{}, 1)
	source.fetchHook = func(config.Stock) {
		select {
		case stockStarted <- struct{}{}:
		default:
		}
	}
	contextStarted := make(chan string, 1)
	releaseContext := make(chan struct{})
	m.MarketContextSync = func(ctx context.Context, asof string) (marketcontext.SyncResult, error) {
		contextStarted <- asof
		select {
		case <-stockStarted:
		case <-time.After(5 * time.Second):
			return marketcontext.SyncResult{}, errors.New("stock collection did not start")
		case <-ctx.Done():
			return marketcontext.SyncResult{}, ctx.Err()
		}
		select {
		case <-releaseContext:
		case <-ctx.Done():
			return marketcontext.SyncResult{}, ctx.Err()
		}
		return marketcontext.SyncResult{AsOf: asof, Status: "complete", Indices: 5, Boards: 421, Succeeded: 426}, nil
	}

	result := make(chan error, 1)
	go func() {
		_, err := m.Sync(context.Background(), "2026-10-13")
		result <- err
	}()
	select {
	case got := <-contextStarted:
		if got != "2026-10-13" {
			t.Fatalf("market context received %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("market context sync did not start")
	}
	select {
	case err := <-result:
		t.Fatalf("sync returned before market context completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseContext)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestMarketContextFailureDoesNotBlockStockSync(t *testing.T) {
	m, _, _ := fixture(t)
	m.MarketContextSync = func(context.Context, string) (marketcontext.SyncResult, error) {
		return marketcontext.SyncResult{}, errors.New("board list unavailable")
	}
	if _, err := m.Sync(context.Background(), "2026-10-13"); err != nil {
		t.Fatalf("market context failure blocked stock sync: %v", err)
	}
}
