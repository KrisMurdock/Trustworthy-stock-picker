package hkconnect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type recommendationHistorySource struct{}

func (recommendationHistorySource) Fetch(_ context.Context, stock config.Stock, start, asOf, adjust string) (market.History, error) {
	history := recommendationHistory(stock.Code, stock.Name, asOf, true, 2000)
	history.Adjust = adjust
	if adjust == "none" {
		history.DataField = "day"
	}
	history.FetchedAt = time.Date(2026, 11, 3, 18, 50, 0, 0, market.Shanghai)
	var bars []market.Bar
	for _, bar := range history.Bars {
		if bar.Date >= start && bar.Date <= asOf {
			bars = append(bars, bar)
		}
	}
	history.Bars = bars
	return history, nil
}

type cancelableHistorySource struct {
	started chan struct{}
}

func (source cancelableHistorySource) Fetch(ctx context.Context, _ config.Stock, _, _, _ string) (market.History, error) {
	select {
	case source.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return market.History{}, ctx.Err()
}

func productionFixture(t *testing.T, source *fakeHKHistorySource, codes ...string) *Production {
	t.Helper()
	configuration := hkTestConfig()
	configuration.DataDir = t.TempDir()
	configuration.HKConnect.Enabled = false
	return &Production{
		config: configuration, history: source, universe: hkTestUniverse(codes...), calendar: hkTestTradingDays(),
		log: logging.Nop(), now: func() time.Time { return time.Date(2026, 11, 3, 18, 50, 0, 0, market.Shanghai) },
	}
}

func TestProductionScheduledSyncUsesHKCalendarAndDoesNotMistakeSingleStockForFullRun(t *testing.T) {
	source := &fakeHKHistorySource{failures: map[string]error{}}
	production := productionFixture(t, source, "hk00700", "hk09988")
	partial, err := production.Sync(context.Background(), Options{AsOf: "2026-11-02", Codes: []string{"hk00700"}, Workers: 1})
	if err != nil || partial.Status != "complete" || partial.Eligible != 1 {
		t.Fatalf("single-stock sync = %+v, %v", partial, err)
	}
	result, err := production.SyncThrough(context.Background(), "2026-11-03")
	if err != nil || result.Status != "complete" || result.AsOf != "2026-11-02" || result.Eligible != 2 || result.Succeeded != 1 || result.Skipped != 1 {
		t.Fatalf("scheduled sync = %+v, %v", result, err)
	}
	if calls := source.Calls(); len(calls) != 4 {
		t.Fatalf("scheduled sync should fetch only the missing stock: %+v", calls)
	}
	db, err := storage.OpenHKMarketView(production.config.DataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bars, err := db.LoadBars(context.Background(), "hk09988", "none", "2026-10-28", "2026-11-02")
	if err != nil || len(bars) == 0 {
		t.Fatalf("scheduled stock bars = %v, %v", bars, err)
	}
}

func TestProductionRespectsWriterLockAndCancelsWithoutLeakingIt(t *testing.T) {
	source := &fakeHKHistorySource{failures: map[string]error{}}
	production := productionFixture(t, source, "hk00700")
	locked, err := storage.OpenHKMarket(production.config.DataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := production.SyncThrough(context.Background(), "2026-11-02"); err == nil {
		locked.Close()
		t.Fatal("scheduled sync ignored the HK writer lock")
	}
	if err := locked.Close(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	production.history = cancelableHistorySource{started: started}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := production.SyncThrough(ctx, "2026-11-02")
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("stock request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled sync returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled stock request did not stop")
	}
	opened, err := storage.OpenHKMarket(production.config.DataDir, false)
	if err != nil {
		t.Fatalf("writer lock was not released: %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProductionDoesNotCreateMissingCIFSRoot(t *testing.T) {
	production := productionFixture(t, &fakeHKHistorySource{failures: map[string]error{}}, "hk00700")
	production.config.DataDir = filepath.Join(t.TempDir(), "missing-share")
	production.config.RequireCIFS = true
	if _, err := production.SyncThrough(context.Background(), "2026-11-02"); err == nil {
		t.Fatal("scheduled sync accepted a missing CIFS share")
	}
	if _, err := os.Stat(production.config.DataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scheduled sync created a local fallback: %v", err)
	}
}

func TestProductionPersistsRecommendationAfterScheduledSync(t *testing.T) {
	production := productionFixture(t, &fakeHKHistorySource{failures: map[string]error{}}, "hk00700")
	production.config.HKConnect.Enabled = true
	production.history = recommendationHistorySource{}
	result, err := production.SyncThrough(context.Background(), "2026-11-02")
	if err != nil || result.Status != "complete" || result.RecommendationID == "" {
		t.Fatalf("scheduled sync and recommendation = %+v, %v", result, err)
	}
	recommendation, err := LoadLatestRecommendation(context.Background(), production.config.DataDir, false)
	if err != nil || recommendation.ID != result.RecommendationID || recommendation.AsOf != result.AsOf {
		t.Fatalf("stored recommendation = %+v, %v", recommendation, err)
	}
}

func TestProductionRetriesScheduledRecommendationFailureWithoutChangingManualContract(t *testing.T) {
	production := productionFixture(t, &fakeHKHistorySource{failures: map[string]error{}}, "hk00700")
	production.config.HKConnect.Enabled = true
	manual, err := production.Sync(context.Background(), Options{AsOf: "2026-11-02", Workers: 1})
	if err != nil || manual.Status != "complete" || manual.RecommendationID != "" {
		t.Fatalf("manual sync contract changed: %+v, %v", manual, err)
	}
	scheduled, err := production.SyncThrough(context.Background(), "2026-11-02")
	if err == nil || scheduled.Status != "complete" {
		t.Fatalf("scheduled recommendation failure must be retried: %+v, %v", scheduled, err)
	}
}

func TestProductionRejectsUnknownHongKongCalendar(t *testing.T) {
	source := &fakeHKHistorySource{failures: map[string]error{}}
	production := productionFixture(t, source, "hk00700")
	production.calendar = fakeTradingDays{}
	if _, err := production.SyncThrough(context.Background(), "2026-11-02"); err == nil {
		t.Fatal("scheduled sync accepted an unknown Hong Kong trading day")
	}
	if calls := source.Calls(); len(calls) != 0 {
		t.Fatalf("unknown calendar triggered stock downloads: %+v", calls)
	}
}

func TestScheduledSyncDoesNotReplaceNewerDailyBars(t *testing.T) {
	source := &fakeHKHistorySource{failures: map[string]error{}}
	production := productionFixture(t, source, "hk00700")
	result, err := production.Sync(context.Background(), Options{AsOf: "2026-11-02", Workers: 1})
	if err != nil || result.Status != "complete" {
		t.Fatalf("initial sync = %+v, %v", result, err)
	}
	before := len(source.Calls())
	if _, err := production.SyncThrough(context.Background(), "2026-10-30"); err == nil {
		t.Fatal("scheduled sync accepted a deadline older than saved bars")
	}
	if calls := len(source.Calls()); calls != before {
		t.Fatalf("older sync fetched stock history: got %d calls, want %d", calls, before)
	}
	db, err := storage.OpenHKMarketView(production.config.DataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, adjust := range []string{"none", "qfq"} {
		latest, err := db.LastBarDate(context.Background(), "hk00700", adjust)
		if err != nil || latest != "2026-11-02" {
			t.Fatalf("latest %s bar = %s, %v", adjust, latest, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	result, err = production.SyncThrough(context.Background(), "2026-11-02")
	if err != nil || result.Status != "complete" || result.Skipped != 1 {
		t.Fatalf("current scheduled slot did not recover: %+v, %v", result, err)
	}
}
