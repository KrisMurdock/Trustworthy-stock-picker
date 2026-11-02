package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

var errFourLightArchiveClosed = errors.New("four-light archive is closed")

// fourLightArchive opens the optional all-market database lazily. The main
// runtime may start before the independent full-market timer has created the
// database, so a startup read failure must not permanently disable the page.
type fourLightArchive struct {
	mu          sync.Mutex
	root        string
	requireCIFS bool
	db          *storage.AllMarketDB
	closed      bool
}

func newFourLightArchive(root string, requireCIFS bool) *fourLightArchive {
	return &fourLightArchive{root: root, requireCIFS: requireCIFS}
}

func (a *fourLightArchive) reader() (*storage.AllMarketDB, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errFourLightArchiveClosed
	}
	if a.db != nil {
		return a.db, nil
	}
	db, err := storage.OpenAllMarketView(a.root, a.requireCIFS)
	if err != nil {
		return nil, fmt.Errorf("four-light archive is unavailable: %w", err)
	}
	a.db = db
	return db, nil
}

func (a *fourLightArchive) LatestFourLightSummary(ctx context.Context, asOf, rule string, page, pageSize int) (storage.FourLightSummary, error) {
	db, err := a.reader()
	if err != nil {
		return storage.FourLightSummary{}, err
	}
	return db.LatestFourLightSummary(ctx, asOf, rule, page, pageSize)
}

func (a *fourLightArchive) FourLightHistory(ctx context.Context, code, rule, from, through string) ([]storage.FourLightRecord, error) {
	db, err := a.reader()
	if err != nil {
		return nil, err
	}
	return db.FourLightHistory(ctx, code, rule, from, through)
}

func (a *fourLightArchive) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	if a.db == nil {
		return nil
	}
	err := a.db.Close()
	a.db = nil
	return err
}
