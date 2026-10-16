package market

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"stock-watcher/internal/config"
)

func TestUnavailableProviderIsSkippedAcrossStocks(t *testing.T) {
	calls := 0
	s := &Sources{Providers: []Provider{{"eastmoney", sourceFunc(func(context.Context, config.Stock, string, string, string) (History, error) {
		calls++
		return History{}, &Failure{Kind: "network"}
	})}}}
	for range 10 {
		_, err := s.Fetch(context.Background(), providerStock, "2026-10-13", "2026-10-13", "none")
		if !IsProviderFailure(err) {
			t.Fatalf("unavailable source must remain a stock-level failure: %v", err)
		}
	}
	if calls != 3 {
		t.Fatalf("unavailable source requested %d times; want 3 before cooldown", calls)
	}
}

func goodSource(name string) Source {
	return sourceFunc(func(context.Context, config.Stock, string, string, string) (History, error) {
		return History{Version: 1, Code: providerStock.Code, Name: providerStock.Name, Source: name, Adjust: "none", DataField: "day", VolumeUnit: "provider_raw", AsOf: "2026-10-13", FetchedAt: time.Now(), Bars: []Bar{{Date: "2026-10-13", Open: 10, Close: 10, High: 11, Low: 9, Volume: 100}}}, nil
	})
}

func TestAttemptTimeoutFallsBackButParentDeadlineAborts(t *testing.T) {
	for _, parent := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if parent {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
		}
		second := 0
		last := sourceFunc(func(ctx context.Context, stock config.Stock, a, b, c string) (History, error) {
			second++
			return goodSource("sina").Fetch(ctx, stock, a, b, c)
		})
		s := &Sources{attemptTimeout: 30 * time.Millisecond, Providers: []Provider{{"tencent", sourceFunc(func(ctx context.Context, _ config.Stock, _, _, _ string) (History, error) {
			<-ctx.Done()
			return History{}, ctx.Err()
		})}, {"sina", last}}}
		h, err := s.Fetch(ctx, providerStock, "2026-10-13", "2026-10-13", "none")
		cancel()
		if parent {
			if !errors.Is(err, context.DeadlineExceeded) || second != 0 {
				t.Fatalf("parent deadline retried: %v %d", err, second)
			}
		} else if err != nil || h.Source != "sina" || second != 1 {
			t.Fatalf("attempt timeout did not fall back: %v %+v", err, h)
		}
	}
}

func TestCooldownRecoveryAndDataErrors(t *testing.T) {
	now := time.Now()
	calls := 0
	mode := "network"
	first := sourceFunc(func(ctx context.Context, stock config.Stock, a, b, c string) (History, error) {
		calls++
		switch mode {
		case "network":
			return History{}, &Failure{Kind: "network"}
		case "data":
			return History{}, &DataError{Reason: "historical coverage incomplete"}
		default:
			return goodSource("tencent").Fetch(ctx, stock, a, b, c)
		}
	})
	s := &Sources{now: func() time.Time { return now }, Providers: []Provider{{"tencent", first}, {"sina", goodSource("sina")}}}
	fetch := func() {
		t.Helper()
		if _, err := s.Fetch(context.Background(), providerStock, "2026-10-13", "2026-10-13", "none"); err != nil {
			t.Fatal(err)
		}
	}
	for range 4 {
		fetch()
	}
	if calls != 3 {
		t.Fatalf("cooldown calls=%d", calls)
	}
	now = now.Add(5 * time.Minute)
	mode = "success"
	fetch()
	fetch()
	if calls != 5 {
		t.Fatalf("recovery calls=%d", calls)
	}
	mode = "data"
	for range 5 {
		fetch()
	}
	if calls != 10 {
		t.Fatal("stock data errors incorrectly disabled source")
	}
	mode = "network"
	fetch()
	fetch()
	mode = "success"
	fetch()
	mode = "network"
	fetch()
	fetch()
	if calls != 15 {
		t.Fatal("successful response did not reset failure streak")
	}
}

func TestCooldownSingleRecoveryProbe(t *testing.T) {
	now := time.Now()
	s := &Sources{now: func() time.Time { return now }}
	for range 3 {
		if !s.available("sina") {
			t.Fatal("early cooldown")
		}
		s.record("sina", &Failure{Kind: "network"})
	}
	now = now.Add(5 * time.Minute)
	if !s.available("sina") || s.available("sina") {
		t.Fatal("expected exactly one recovery probe")
	}
	s.record("sina", &Failure{Kind: "network"})
	if s.available("sina") {
		t.Fatal("failed recovery did not reopen cooldown")
	}
}

type testWindowSource struct {
	Source
	calls int
}

func (s *testWindowSource) FetchWindow(ctx context.Context, _ config.Stock, _, _, _ string, _ int) (History, error) {
	s.calls++
	<-ctx.Done()
	return History{}, ctx.Err()
}

func TestIncrementalWindowUsesSameBudget(t *testing.T) {
	first := &testWindowSource{Source: goodSource("tencent")}
	s := &Sources{attemptTimeout: 10 * time.Millisecond, Providers: []Provider{{"tencent", first}, {"sina", goodSource("sina")}}}
	h, err := s.Attempt(context.Background(), providerStock, "none", func(source Source) (History, error) {
		if w, ok := source.(windowFetcher); ok {
			return w.FetchWindow(context.Background(), providerStock, "2026-10-13", "2026-10-13", "none", 5)
		}
		return source.Fetch(context.Background(), providerStock, "2026-10-13", "2026-10-13", "none")
	})
	if err != nil || h.Source != "sina" || first.calls != 1 {
		t.Fatalf("incremental timeout lost fallback: %v %+v", err, h)
	}
}

func TestProviderFailureSummaryRetainsCauses(t *testing.T) {
	s := &Sources{Providers: []Provider{{"eastmoney", sourceFunc(func(context.Context, config.Stock, string, string, string) (History, error) {
		return History{}, &Failure{Kind: "network"}
	})}}}
	_, err := s.Fetch(context.Background(), providerStock, "2026-10-13", "2026-10-13", "none")
	if !strings.Contains(ProviderFailureSummary(err), "eastmoney:network") {
		t.Fatal("lost provider failure reason")
	}
}

func TestBudgetHonorsCallbackCancellation(t *testing.T) {
	parent, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	caller, cancel := context.WithCancel(parent)
	defer cancel()
	observed := false
	source := sourceFunc(func(ctx context.Context, _ config.Stock, _, _, _ string) (History, error) {
		cancel()
		select {
		case <-ctx.Done():
			observed = true
			return History{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
			return History{}, errors.New("callback cancellation did not reach provider")
		}
	})
	_, err := withBudget(source, parent).Fetch(caller, providerStock, "2026-10-13", "2026-10-13", "none")
	if !errors.Is(err, context.Canceled) || !observed {
		t.Fatalf("lost callback cancellation: %v", err)
	}
}
