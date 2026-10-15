package market

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"stock-watcher/internal/config"
	"stock-watcher/internal/logging"
)

type Provider struct {
	Name   string
	Source Source
}

// Sources tries whole windows in configured order. The caller supplies its
// calendar/coverage validation, so HTTP success with unusable data also falls
// back. Cancellation never starts another provider.
type Sources struct {
	Providers []Provider
	Log       *logging.Logger
}

func NewSources(c config.Config, log *logging.Logger) (*Sources, error) {
	if err := c.Market.Validate(); err != nil {
		return nil, err
	}
	s := &Sources{Log: log}
	if s.Log == nil {
		s.Log = logging.Nop()
	}
	timeout, interval := config.Duration(c.Timeout), config.Duration(c.Market.RequestInterval)
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
		}
		s.Providers = append(s.Providers, Provider{Name: name, Source: source})
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
	for _, p := range s.Providers {
		if closer, ok := p.Source.(interface{ Close() }); ok {
			closer.Close()
		}
	}
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
		h, err := fetch(p.Source)
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
			s.Log.Warn("Market provider attempt failed", "code", stock.Code, "adjust", adjust, "source", p.Name, "reason", reason)
		}
	}
	return History{}, all
}
