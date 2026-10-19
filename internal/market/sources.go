package market

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
)

type Provider struct {
	Name   string
	Source Source
}

type StatusProvider struct {
	Name   string
	Source StatusSource
}

type CalendarProvider struct {
	Name   string
	Source TradingCalendarSource
}

// Sources tries whole windows in configured order. The caller supplies its
// calendar/coverage validation, so HTTP success with unusable data also falls
// back. Cancellation never starts another provider.
type Sources struct {
	Providers         []Provider
	Log               *logging.Logger
	attemptTimeout    time.Duration
	failureThreshold  int
	failureCooldown   time.Duration
	mu                sync.Mutex
	health            map[string]*providerHealth
	now               func() time.Time
	listings          *SinaListingClient
	statusProviders   []StatusProvider
	calendarProviders []CalendarProvider
}

func NewSources(c config.Config, log *logging.Logger) (*Sources, error) {
	if err := c.Market.Validate(); err != nil {
		return nil, err
	}
	s := &Sources{Log: log, attemptTimeout: config.Duration(c.Market.AttemptTimeout), failureThreshold: c.Market.FailureThreshold, failureCooldown: config.Duration(c.Market.FailureCooldown)}
	if s.Log == nil {
		s.Log = logging.Nop()
	}
	timeout, interval := config.Duration(c.Timeout), config.Duration(c.Market.RequestInterval)
	s.listings = NewSinaListingClient(timeout, interval)
	s.listings.Endpoint = c.Market.ListingEndpoint
	s.Log.RegisterSecret(c.Market.TushareToken)
	for _, name := range c.Market.Providers {
		var source Source
		switch name {
		case "tencent":
			client := NewClient(timeout)
			client.Endpoint = c.Market.TencentEndpoint
			client.pacer.interval = interval
			source = client
		case "baostock":
			client := NewBaoStockClient(timeout, interval)
			client.Address = c.Market.BaostockAddress
			source = client
		case "tushare":
			if c.Market.TushareToken == "" {
				s.Log.Warn("Market provider disabled: token environment variable is not configured", "source", name, "environment", c.Market.TushareTokenEnv)
				continue
			}
			client := NewTushareClient(timeout, interval, c.Market.TushareToken)
			client.Endpoint = c.Market.TushareEndpoint
			source = client
		case "eastmoney":
			client := NewEastmoneyClient(timeout, interval)
			client.Endpoint = c.Market.EastmoneyEndpoint
			source = client
		case "sina":
			client := NewSinaClient(timeout, interval)
			client.Endpoint = c.Market.SinaEndpoint
			source = client
		}
		s.Providers = append(s.Providers, Provider{Name: name, Source: source})
	}
	baoStatus := NewBaoStockClient(timeout, interval)
	baoStatus.Address = c.Market.BaostockAddress
	s.statusProviders = append(s.statusProviders, StatusProvider{Name: "baostock_status", Source: baoStatus})
	s.calendarProviders = append(s.calendarProviders, CalendarProvider{Name: "baostock_calendar", Source: baoStatus})
	if c.Market.TushareToken != "" {
		tushareStatus := NewTushareClient(timeout, interval, c.Market.TushareToken)
		tushareStatus.Endpoint = c.Market.TushareEndpoint
		s.statusProviders = append(s.statusProviders, StatusProvider{Name: "tushare_status", Source: tushareStatus})
		s.calendarProviders = append(s.calendarProviders, CalendarProvider{Name: "tushare_calendar", Source: tushareStatus})
	}
	s.Log.Info("Market providers configured", "sources", s.Names())
	return s, nil
}

func (s *Sources) Names() []string {
	names := make([]string, 0, len(s.Providers))
	for _, p := range s.Providers {
		names = append(names, p.Name)
	}
	return names
}
func (s *Sources) Close() {
	if s.listings != nil {
		s.listings.Close()
	}
	for _, p := range s.Providers {
		if closer, ok := p.Source.(interface{ Close() }); ok {
			closer.Close()
		}
	}
	for _, p := range s.statusProviders {
		if closer, ok := p.Source.(interface{ Close() }); ok {
			closer.Close()
		}
	}
}

func (s *Sources) FetchStatus(ctx context.Context, stock config.Stock, start, end string) (StatusWindow, error) {
	all := &sourceFailures{}
	answers := []statusAnswer{}
	for _, p := range s.statusProviders {
		if err := ctx.Err(); err != nil {
			return StatusWindow{}, err
		}
		if !s.available(p.Name) {
			all.names = append(all.names, p.Name+":cooldown")
			all.failures = append(all.failures, &Failure{Kind: "cooldown"})
			continue
		}
		budget := s.attemptTimeout
		if budget == 0 {
			budget = time.Minute
		}
		attempt, cancel := context.WithTimeout(ctx, budget)
		window, err := p.Source.FetchStatus(attempt, stock, start, end)
		err = classifyAttemptError(ctx, attempt, err)
		cancel()
		s.record(p.Name, err)
		if ctx.Err() != nil {
			return StatusWindow{}, ctx.Err()
		}
		if err == nil {
			if validateErr := window.Validate(); validateErr != nil {
				err = &DataError{Reason: validateErr.Error()}
			}
		}
		if err == nil && window.Code != stock.Code {
			err = &DataError{Reason: "status_code_mismatch"}
		}
		base := strings.TrimSuffix(p.Name, "_status")
		if err == nil && window.Source != base {
			err = &DataError{Reason: "status_source_mismatch"}
		}
		if err == nil {
			answers = append(answers, statusAnswer{name: base, window: window})
			if s.Log != nil {
				s.Log.Debug("Market status source answered", "code", stock.Code, "source", base, "days", len(window.Statuses))
			}
			continue
		}
		var failure *Failure
		var invalid *DataError
		if !errors.As(err, &failure) && !errors.As(err, &invalid) {
			return StatusWindow{}, err
		}
		reason := "invalid_status"
		if failure != nil {
			reason = fmt.Sprintf("%s:%d", failure.Kind, failure.Status)
		}
		if invalid != nil {
			reason = invalid.Reason
		}
		all.names = append(all.names, p.Name+":"+reason)
		all.failures = append(all.failures, err)
		if s.Log != nil {
			s.Log.Warn("Market status source failed", "code", stock.Code, "source", p.Name, "reason", reason)
		}
	}
	if len(answers) == 0 {
		if len(all.failures) == 0 {
			return StatusWindow{}, &DataError{Reason: "status_source_unavailable"}
		}
		return StatusWindow{}, all
	}
	return mergeStatusWindows(stock, start, end, answers), nil
}

// mergeStatusWindows applies the fail-closed rule: an explicit traded answer
// always wins over a suspension claim, and an explicit conflict becomes
// unknown so the missing bar is treated as a data gap rather than a holiday.
type statusAnswer struct {
	name   string
	window StatusWindow
}

func mergeStatusWindows(stock config.Stock, start, end string, answers []statusAnswer) StatusWindow {
	merged := StatusWindow{Code: stock.Code, Start: start, End: end, FetchedAt: time.Now().In(Shanghai)}
	type claim struct {
		sources  map[string]bool
		conflict bool
		detail   string
	}
	byDate := map[string]*claim{}
	names := []string{}
	seenNames := map[string]bool{}
	for _, answer := range answers {
		if !seenNames[answer.name] {
			names = append(names, answer.name)
			seenNames[answer.name] = true
		}
		for _, status := range answer.window.Statuses {
			entry := byDate[status.Date]
			if entry == nil {
				entry = &claim{sources: map[string]bool{}}
				byDate[status.Date] = entry
			}
			entry.sources[answer.name] = true
			switch status.State {
			case SessionTraded:
				if entry.detail == "suspended" {
					entry.conflict = true
				}
			case SessionSuspended:
				if entry.detail == "traded" {
					entry.conflict = true
				}
			default:
				entry.conflict = true
			}
			if status.State == SessionTraded && (entry.detail == "" || entry.detail == "suspended") {
				entry.detail = "traded"
			}
			if status.State == SessionSuspended && entry.detail == "" {
				entry.detail = "suspended"
			}
		}
	}
	sort.Strings(names)
	merged.Source = strings.Join(names, "+")
	for _, answer := range answers {
		merged.Full = merged.Full || answer.window.Full
	}
	dates := make([]string, 0, len(byDate))
	for date := range byDate {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	for _, date := range dates {
		entry := byDate[date]
		state := SessionUnknown
		detail := ""
		switch {
		case entry.conflict:
			state, detail = SessionUnknown, "status_conflict"
		case entry.detail == "traded":
			state, detail = SessionTraded, "bar_missing"
		case entry.detail == "suspended":
			state = SessionSuspended
		}
		sources := make([]string, 0, len(entry.sources))
		for name := range entry.sources {
			sources = append(sources, name)
		}
		sort.Strings(sources)
		merged.Statuses = append(merged.Statuses, SessionStatus{Code: stock.Code, Date: date, State: state, Source: strings.Join(sources, "+"), FetchedAt: merged.FetchedAt, Detail: detail})
	}
	return merged
}

func (s *Sources) FetchTradingDays(ctx context.Context, start, end string) ([]string, error) {
	all := &sourceFailures{}
	for _, p := range s.calendarProviders {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !s.available(p.Name) {
			all.names = append(all.names, p.Name+":cooldown")
			all.failures = append(all.failures, &Failure{Kind: "cooldown"})
			continue
		}
		budget := s.attemptTimeout
		if budget == 0 {
			budget = time.Minute
		}
		attempt, cancel := context.WithTimeout(ctx, budget)
		days, err := p.Source.FetchTradingDays(attempt, start, end)
		err = classifyAttemptError(ctx, attempt, err)
		cancel()
		s.record(p.Name, err)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil {
			sort.Strings(days)
			previous := ""
			for _, day := range days {
				if day < start || day > end || day <= previous {
					err = &DataError{Reason: "invalid_calendar_days"}
					break
				}
				previous = day
			}
			if len(days) == 0 {
				err = &DataError{Reason: "empty_calendar"}
			}
		}
		if err == nil {
			return days, nil
		}
		var failure *Failure
		var invalid *DataError
		if !errors.As(err, &failure) && !errors.As(err, &invalid) {
			return nil, err
		}
		reason := "invalid_calendar"
		if failure != nil {
			reason = fmt.Sprintf("%s:%d", failure.Kind, failure.Status)
		}
		if invalid != nil {
			reason = invalid.Reason
		}
		all.names = append(all.names, p.Name+":"+reason)
		all.failures = append(all.failures, err)
	}
	if len(all.failures) == 0 {
		return nil, &DataError{Reason: "calendar_source_unavailable"}
	}
	return nil, all
}

// classifyAttemptError turns a per-provider budget expiry into a retryable
// provider failure. A caller cancellation still propagates unchanged.
func classifyAttemptError(parent, attempt context.Context, err error) error {
	if err == nil || parent.Err() != nil {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(attempt.Err(), context.DeadlineExceeded) {
		return &Failure{Kind: "attempt_timeout"}
	}
	if errors.Is(err, context.Canceled) {
		return &Failure{Kind: "attempt_interrupted"}
	}
	return err
}

func (s *Sources) Fetch(ctx context.Context, stock config.Stock, start, end, adjust string) (History, error) {
	return s.Attempt(ctx, stock, adjust, func(source Source) (History, error) {
		h, err := source.Fetch(ctx, stock, start, end, adjust)
		if err == nil {
			if e := h.Validate(); e != nil {
				err = &DataError{Reason: e.Error()}
			}
		}
		return h, err
	})
}

type sourceFailures struct {
	names    []string
	failures []error
}

func (e *sourceFailures) Error() string {
	return "all market providers failed (" + strings.Join(e.names, ", ") + ")"
}

// ProviderFailureSummary exposes sanitized per-provider reasons for persisted
// stock status; it never includes response bodies, endpoints or credentials.
func ProviderFailureSummary(err error) string {
	var all *sourceFailures
	if errors.As(err, &all) {
		return all.Error()
	}
	return ""
}
func (e *sourceFailures) isolatable() bool {
	if len(e.failures) == 0 {
		return false
	}
	for _, err := range e.failures {
		if !IsIsolatable(err) {
			return false
		}
	}
	return true
}

func (s *Sources) Attempt(ctx context.Context, stock config.Stock, adjust string, fetch func(Source) (History, error)) (History, error) {
	all := &sourceFailures{}
	for _, p := range s.Providers {
		if err := ctx.Err(); err != nil {
			return History{}, err
		}
		if !s.available(p.Name) {
			all.names = append(all.names, p.Name+":cooldown")
			all.failures = append(all.failures, &Failure{Kind: "cooldown"})
			continue
		}
		started := time.Now()
		budget := s.attemptTimeout
		if budget == 0 {
			budget = time.Minute
		}
		attempt, cancel := context.WithTimeout(ctx, budget)
		h, err := fetch(withBudget(p.Source, attempt))
		cancel()
		s.record(p.Name, err)
		if ctx.Err() != nil {
			return History{}, ctx.Err()
		}
		if err == nil && h.Source != p.Name {
			err = &DataError{Reason: "provider_metadata_mismatch"}
		}
		if err == nil {
			if s.Log != nil && len(all.failures) > 0 {
				s.Log.Info("Market provider fallback succeeded", "code", stock.Code, "adjust", adjust, "source", p.Name)
			}
			return h, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return History{}, err
		}
		// Storage and caller failures are not provider failures. In particular,
		// never retry a broken shared disk through another provider.
		var failure *Failure
		var invalid *DataError
		if !errors.As(err, &failure) && !errors.As(err, &invalid) {
			return History{}, err
		}
		reason := "invalid_data"
		if failure != nil {
			reason = fmt.Sprintf("%s:%d", failure.Kind, failure.Status)
		}
		if invalid != nil {
			reason = invalid.Reason
		}
		all.names = append(all.names, p.Name+":"+reason)
		all.failures = append(all.failures, err)
		if s.Log != nil {
			s.Log.Warn("Market provider attempt failed", "code", stock.Code, "adjust", adjust, "source", p.Name, "reason", reason, "elapsed_ms", time.Since(started).Milliseconds())
		}
	}
	return History{}, all
}
