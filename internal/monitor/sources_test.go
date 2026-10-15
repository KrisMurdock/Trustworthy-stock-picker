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

func TestPoolContinuesAfterTemporaryProviderFailure(t *testing.T) {
	for _, adjust := range []string{"qfq", "none"} {
		t.Run(adjust, func(t *testing.T) {
			m, _, channel := fixture(t)
			m.Config.Daily.Adjust = "qfq"
			m.Config.Stocks = append(m.Config.Stocks, config.Stock{Code: "sz000002", Name: "后续股票", ListedFrom: "2026-07-15"})
			db, err := storage.InitMarket(t.TempDir(), false)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			m.Market = db
			first := &fakeSource{err: &market.DataError{Reason: "stale_or_suspended"}, failCode: "sz000001", failAdjust: adjust}
			second := &fakeSource{err: &market.Failure{Kind: "network"}, failCode: "sz000001", failAdjust: adjust}
			m.Source = &market.Sources{Providers: []market.Provider{{Name: "tencent", Source: first}, {Name: "eastmoney", Source: second}}}
			_, err = m.Sync(context.Background(), "2026-10-13")
			if err == nil || !strings.Contains(err.Error(), "coverage 50.00%") {
				t.Fatalf("whole pool was not attempted: %v", err)
			}
			for _, kind := range []string{"qfq", "none"} {
				h, err := db.LoadLatestHistory(context.Background(), "sz000002", kind, "2026-10-13")
				if err != nil || h.AsOf != "2026-10-13" {
					t.Fatalf("valid later stock was not saved: %s %v", kind, err)
				}
			}
			if _, err := db.LoadReport(context.Background(), "2026-10-13"); !errors.Is(err, fs.ErrNotExist) || channel.calls != 0 {
				t.Fatal("coverage gate was bypassed")
			}
		})
	}
}

func TestPoolStopsOnCancellationAndStorageFailure(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errors.New("shared storage unavailable")} {
		m, source, _ := fixture(t)
		m.Config.Stocks = append(m.Config.Stocks, config.Stock{Code: "sz000002", Name: "后续股票"})
		source.err = failure
		m.Source = &market.Sources{Providers: []market.Provider{{Name: "tencent", Source: source}}}
		_, err := m.Sync(context.Background(), "2026-10-13")
		if !errors.Is(err, failure) || source.calls != 1 {
			t.Fatalf("global failure did not stop sync: %v calls=%d", err, source.calls)
		}
	}
}

type namedTestSource struct {
	base          *fakeSource
	name, defect  string
	full, windows int
}

func (s *namedTestSource) Fetch(ctx context.Context, stock config.Stock, start, end, adjust string) (market.History, error) {
	s.full++
	h, err := s.base.Fetch(ctx, stock, start, end, adjust)
	if err != nil {
		return h, err
	}
	h.Source = s.name
	if s.name == "baostock" {
		for i := range h.Bars {
			h.Bars[i].Volume *= 100
		}
	}
	switch s.defect {
	case "stale":
		h.Bars = h.Bars[:len(h.Bars)-1]
	case "gap":
		h.Bars = append(h.Bars[:5], h.Bars[6:]...)
	case "short":
		h.Bars = h.Bars[1:]
	}
	return h, nil
}
func (s *namedTestSource) FetchWindow(ctx context.Context, stock config.Stock, start, end, adjust string, count int) (market.History, error) {
	s.windows++
	h, err := s.Fetch(ctx, stock, start, end, adjust)
	s.full--
	if len(h.Bars) > count {
		h.Bars = h.Bars[len(h.Bars)-count:]
	}
	return h, err
}

func TestFallbackIncludesCalendarAndCoverageValidation(t *testing.T) {
	for _, defect := range []string{"stale", "gap", "short"} {
		t.Run(defect, func(t *testing.T) {
			m, _, channel := fixture(t)
			db, err := storage.InitMarket(t.TempDir(), false)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			m.Market = db
			first := &namedTestSource{base: &fakeSource{}, name: "tencent", defect: defect}
			second := &namedTestSource{base: &fakeSource{}, name: "baostock"}
			m.Source = &market.Sources{Providers: []market.Provider{{Name: "tencent", Source: first}, {Name: "baostock", Source: second}}}
			if _, err := m.Sync(context.Background(), "2026-10-13"); err != nil {
				t.Fatal(err)
			}
			h, err := db.LoadLatestHistory(context.Background(), m.Config.Stocks[0].Code, "none", "2026-10-13")
			if err != nil || h.Source != "baostock" || second.full != 1 || channel.calls != 0 {
				t.Fatalf("fallback did not validate/persist full history: %v source=%s full=%d pushes=%d", err, h.Source, second.full, channel.calls)
			}
		})
	}
}

func TestProviderSwitchBackfillsInsteadOfMixingVolumes(t *testing.T) {
	m, _, _ := fixture(t)
	db, err := storage.InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m.Market = db
	old := &namedTestSource{base: &fakeSource{}, name: "tencent"}
	m.Source = old
	if _, err := m.Sync(context.Background(), "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	old.base.err = &market.Failure{Kind: "network"}
	backup := &namedTestSource{base: &fakeSource{}, name: "baostock"}
	m.Source = &market.Sources{Providers: []market.Provider{{Name: "tencent", Source: old}, {Name: "baostock", Source: backup}}}
	m.Now = func() time.Time { return time.Date(2026, 10, 14, 19, 0, 0, 0, market.Shanghai) }
	if _, err := m.Sync(context.Background(), "2026-10-14"); err != nil {
		t.Fatal(err)
	}
	h, err := db.LoadLatestHistory(context.Background(), m.Config.Stocks[0].Code, "none", "2026-10-14")
	if err != nil || h.Source != "baostock" || backup.full != 1 || backup.windows != 1 {
		t.Fatalf("source switch skipped backfill: %v full=%d windows=%d", err, backup.full, backup.windows)
	}
	for _, bar := range h.Bars {
		if bar.Volume != 10000 {
			t.Fatalf("mixed providers on %s: volume=%v", bar.Date, bar.Volume)
		}
	}
	// The same provider and matching overlap can use the next incremental window.
	m.Now = func() time.Time { return time.Date(2026, 10, 15, 19, 0, 0, 0, market.Shanghai) }
	if _, err := m.Sync(context.Background(), "2026-10-15"); err != nil {
		t.Fatal(err)
	}
	if backup.full != 1 || backup.windows != 2 {
		t.Fatalf("matching provider lost incremental sync: full=%d windows=%d", backup.full, backup.windows)
	}
}

func TestAllSourceFailureDoesNotPublishOrNotify(t *testing.T) {
	m, _, channel := fixture(t)
	db, err := storage.InitMarket(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m.Market = db
	first := &fakeSource{err: &market.Failure{Kind: "network"}}
	second := &fakeSource{err: &market.DataError{Reason: "missing_data"}}
	m.Source = &market.Sources{Providers: []market.Provider{{Name: "tencent", Source: first}, {Name: "baostock", Source: second}}}
	if _, err := m.Sync(context.Background(), "2026-10-13"); err == nil {
		t.Fatal("published failed multi-source sync")
	}
	if _, err := db.LoadReport(context.Background(), "2026-10-13"); err == nil {
		t.Fatal("stored recommendation after failure")
	}
	if channel.calls != 0 {
		t.Fatal("sent after failed synchronization")
	}
}

func TestIncrementRejectsCorrectedOverlap(t *testing.T) {
	source := &fakeSource{}
	stock := config.Stock{Code: "sz000001", Name: "测试"}
	old, _ := source.Fetch(context.Background(), stock, "2026-10-12", "2026-10-13", "none")
	recent, _ := source.Fetch(context.Background(), stock, "2026-10-12", "2026-10-14", "none")
	if !compatibleIncrement(old, recent) {
		t.Fatal("valid overlap rejected")
	}
	recent.Bars[0].Volume++
	if compatibleIncrement(old, recent) {
		t.Fatal("corrected overlap silently merged")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m, _, _ := fixture(t)
	if _, err := m.fetchHistory(ctx, stock, "2026-10-12", "2026-10-13", "none"); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation")
	}
}
