package market

import (
	"context"
	"errors"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

type providerHealth struct {
	failures int
	retryAt  time.Time
	probing  bool
}

func (s *Sources) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Sources) available(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.health == nil {
		s.health = make(map[string]*providerHealth)
	}
	h := s.health[name]
	if h == nil {
		s.health[name] = &providerHealth{}
		return true
	}
	if h.retryAt.IsZero() {
		return true
	}
	if h.probing || s.clock().Before(h.retryAt) {
		return false
	}
	h.probing = true // One recovery probe after the cooldown, across all stocks.
	return true
}

func (s *Sources) record(name string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.health[name]
	h.probing = false
	var failure *Failure
	var invalid *DataError
	if errors.As(err, &failure) && failure.Retryable() {
		h.failures++
		threshold, cooldown := s.failureThreshold, s.failureCooldown
		if threshold == 0 {
			threshold = 3
		}
		if cooldown == 0 {
			cooldown = 5 * time.Minute
		}
		if h.failures >= threshold {
			h.retryAt = s.clock().Add(cooldown)
			if s.Log != nil {
				s.Log.Warn("Market provider cooling down", "source", name, "failures", h.failures, "retry_at", h.retryAt)
			}
		}
		return
	}
	// Missing history/adjustment for one stock does not imply a network outage.
	// Cancellation and storage failures neither penalize nor recover a provider.
	if err == nil || failure != nil || errors.As(err, &invalid) {
		if !h.retryAt.IsZero() && s.Log != nil {
			s.Log.Info("Market provider recovered", "source", name)
		}
		*h = providerHealth{}
	}
}

type windowFetcher interface {
	FetchWindow(context.Context, config.Stock, string, string, string, int) (History, error)
}

// Bind the same budget to all network pages, including incremental fallback.
// The caller keeps its original context for storage reads and validation.
type budgetSource struct {
	Source
	budget context.Context
}
type budgetWindowSource struct {
	*budgetSource
	window windowFetcher
}

func withBudget(source Source, ctx context.Context) Source {
	b := &budgetSource{source, ctx}
	if w, ok := source.(windowFetcher); ok {
		return &budgetWindowSource{b, w}
	}
	return b
}

func (b *budgetSource) result(ctx context.Context, h History, err error) (History, error) {
	if ctx.Err() != nil {
		return History{}, ctx.Err()
	}
	if errors.Is(b.budget.Err(), context.DeadlineExceeded) {
		return History{}, &Failure{Kind: "attempt_timeout"}
	}
	return h, err
}

func (b *budgetSource) Fetch(ctx context.Context, stock config.Stock, start, end, adjust string) (History, error) {
	request, done := b.requestContext(ctx)
	defer done()
	h, err := b.Source.Fetch(request, stock, start, end, adjust)
	return b.result(ctx, h, err)
}

func (b *budgetWindowSource) FetchWindow(ctx context.Context, stock config.Stock, start, end, adjust string, count int) (History, error) {
	request, done := b.requestContext(ctx)
	defer done()
	h, err := b.window.FetchWindow(request, stock, start, end, adjust, count)
	return b.result(ctx, h, err)
}

func (b *budgetSource) requestContext(caller context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(b.budget)
	stop := context.AfterFunc(caller, cancel)
	if caller.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}
