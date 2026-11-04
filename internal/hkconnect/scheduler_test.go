package hkconnect

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

type scheduledSyncFunc func(context.Context, string) (Result, error)

func (f scheduledSyncFunc) SyncThrough(ctx context.Context, through string) (Result, error) {
	return f(ctx, through)
}

func TestScheduledThroughUsesLastWeekdayAfterClose(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		want string
	}{
		{"monday before close", time.Date(2026, 11, 2, 18, 49, 59, 0, market.Shanghai), "2026-10-30"},
		{"monday at close", time.Date(2026, 11, 2, 18, 50, 0, 0, market.Shanghai), "2026-11-02"},
		{"tuesday before close", time.Date(2026, 11, 3, 12, 0, 0, 0, market.Shanghai), "2026-11-02"},
		{"saturday", time.Date(2026, 11, 7, 12, 0, 0, 0, market.Shanghai), "2026-11-06"},
		{"sunday", time.Date(2026, 11, 8, 20, 0, 0, 0, market.Shanghai), "2026-11-06"},
		{"utc clock", time.Date(2026, 11, 3, 10, 50, 0, 0, time.UTC), "2026-11-03"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := scheduledThrough(test.now); got != test.want {
				t.Fatalf("scheduledThrough(%s) = %s, want %s", test.now, got, test.want)
			}
		})
	}
}

func TestDailyMonitorCatchesUpAndRunsOncePerSlot(t *testing.T) {
	now := time.Date(2026, 11, 2, 18, 49, 0, 0, market.Shanghai)
	var calls []string
	monitor := NewDailyMonitor(scheduledSyncFunc(func(_ context.Context, through string) (Result, error) {
		calls = append(calls, through)
		return Result{Status: "complete", AsOf: through}, nil
	}), nil)
	monitor.Now = func() time.Time { return now }
	monitor.tick(context.Background())
	monitor.tick(context.Background())
	now = now.Add(time.Minute)
	monitor.tick(context.Background())
	monitor.tick(context.Background())
	now = time.Date(2026, 11, 3, 12, 0, 0, 0, market.Shanghai)
	monitor.tick(context.Background())
	if want := []string{"2026-10-30", "2026-11-02"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestDailyMonitorRetriesPartialAndTimeoutWithBackoff(t *testing.T) {
	now := time.Date(2026, 11, 2, 18, 50, 0, 0, market.Shanghai)
	attempts := 0
	monitor := NewDailyMonitor(scheduledSyncFunc(func(_ context.Context, _ string) (Result, error) {
		attempts++
		switch attempts {
		case 1:
			return Result{Status: "partial", Partial: 1}, nil
		case 2:
			return Result{}, context.DeadlineExceeded
		default:
			return Result{Status: "complete"}, nil
		}
	}), nil)
	monitor.Now = func() time.Time { return now }
	monitor.tick(context.Background())
	now = now.Add(4 * time.Minute)
	monitor.tick(context.Background())
	if attempts != 1 {
		t.Fatalf("partial retry was not delayed: %d", attempts)
	}
	now = now.Add(time.Minute)
	monitor.tick(context.Background())
	now = now.Add(9 * time.Minute)
	monitor.tick(context.Background())
	if attempts != 2 {
		t.Fatalf("timeout retry was not backed off: %d", attempts)
	}
	now = now.Add(time.Minute)
	monitor.tick(context.Background())
	monitor.tick(context.Background())
	if attempts != 3 {
		t.Fatalf("successful retry did not settle the slot: %d", attempts)
	}
}

func TestDailyMonitorCancelsInFlightSync(t *testing.T) {
	started := make(chan struct{})
	monitor := NewDailyMonitor(scheduledSyncFunc(func(ctx context.Context, _ string) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}), nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- monitor.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("scheduled sync did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduled sync did not stop after cancellation")
	}
}
