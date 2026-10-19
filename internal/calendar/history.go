package calendar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"stock-watcher/internal/market"
)

const HistoryVersion = 1

const (
	SourceBaoStock = "baostock"
	SourceTushare  = "tushare"
)

const (
	CoverageComplete = "complete"
	CoveragePartial  = "partial"
	CoverageMissing  = "missing"
)

// History is an immutable exchange-calendar window. Digest covers the semantic
// fields (version, source, bounds and trading days), not the fetch timestamp.
type History struct {
	Version   int       `json:"schema_version"`
	Source    string    `json:"source"`
	From      string    `json:"from"`
	Through   string    `json:"through"`
	Days      []string  `json:"days"`
	FetchedAt time.Time `json:"fetched_at"`
	Digest    string    `json:"sha256"`
}

// Span identifies a contiguous date range.
type Span struct {
	From    string `json:"from"`
	Through string `json:"through"`
}

// Coverage describes whether cached histories cover a requested range. Missing
// is always empty for complete coverage.
type Coverage struct {
	From     string `json:"from"`
	Through  string `json:"through"`
	Status   string `json:"status"`
	Complete bool   `json:"complete"`
	Missing  []Span `json:"missing,omitempty"`
}

// CoverageError makes incomplete coverage a hard failure for callers that need
// an exact trading-day list.
type CoverageError struct {
	Coverage Coverage
}

func (e *CoverageError) Error() string {
	return fmt.Sprintf("trading calendar does not cover %s through %s", e.Coverage.From, e.Coverage.Through)
}

// Dataset is a collection of independently fetched calendar windows. Its
// coverage check permits adjacent or overlapping windows while preserving each
// source window in storage.
type Dataset []History

func ValidateRange(from, through string) error {
	first, err := time.Parse("2006-01-02", from)
	if err != nil {
		return errors.New("invalid calendar start date")
	}
	last, err := time.Parse("2006-01-02", through)
	if err != nil || first.After(last) {
		return errors.New("invalid calendar end date")
	}
	return nil
}

// NewHistory validates and canonicalizes a provider response, then computes its
// content digest. Duplicate dates are rejected rather than silently collapsed.
func NewHistory(source, from, through string, days []string, fetchedAt time.Time) (History, error) {
	if err := ValidateRange(from, through); err != nil {
		return History{}, err
	}
	if source == "" || strings.TrimSpace(source) != source {
		return History{}, errors.New("calendar source is required")
	}
	if fetchedAt.IsZero() {
		return History{}, errors.New("calendar fetch time is required")
	}
	normalized := append([]string{}, days...)
	sort.Strings(normalized)
	for i, day := range normalized {
		if _, err := time.Parse("2006-01-02", day); err != nil {
			return History{}, errors.New("invalid trading day")
		}
		if day < from || day > through {
			return History{}, errors.New("trading day outside calendar window")
		}
		if i > 0 && day == normalized[i-1] {
			return History{}, errors.New("duplicate trading day")
		}
	}
	h := History{
		Version:   HistoryVersion,
		Source:    source,
		From:      from,
		Through:   through,
		Days:      normalized,
		FetchedAt: fetchedAt.In(market.Shanghai),
	}
	h.Digest = h.digest()
	return h, nil
}

func (h History) Validate() error {
	if h.Version != HistoryVersion {
		return errors.New("unsupported trading calendar schema")
	}
	if err := ValidateRange(h.From, h.Through); err != nil {
		return err
	}
	if h.Source == "" || strings.TrimSpace(h.Source) != h.Source {
		return errors.New("calendar source is required")
	}
	if h.FetchedAt.IsZero() {
		return errors.New("calendar fetch time is required")
	}
	if h.Days == nil {
		return errors.New("calendar trading days are required")
	}
	for i, day := range h.Days {
		if _, err := time.Parse("2006-01-02", day); err != nil {
			return errors.New("invalid trading day")
		}
		if day < h.From || day > h.Through {
			return errors.New("trading day outside calendar window")
		}
		if i > 0 && day <= h.Days[i-1] {
			return errors.New("trading days must be sorted and unique")
		}
	}
	if h.Digest == "" || h.Digest != h.digest() {
		return errors.New("trading calendar digest mismatch")
	}
	return nil
}

func (h History) digest() string {
	payload := struct {
		Version int      `json:"version"`
		Source  string   `json:"source"`
		From    string   `json:"from"`
		Through string   `json:"through"`
		Days    []string `json:"days"`
	}{
		Version: h.Version,
		Source:  h.Source,
		From:    h.From,
		Through: h.Through,
		Days:    h.Days,
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (h History) Coverage(from, through string) (Coverage, error) {
	return Dataset{h}.Coverage(from, through)
}

func (h History) Covers(from, through string) (bool, error) {
	coverage, err := h.Coverage(from, through)
	return err == nil && coverage.Complete, err
}

// TradingDays returns an exact list or a CoverageError when the window is not
// fully covered. Callers that intentionally tolerate partial history should use
// Coverage instead.
func (h History) TradingDays(from, through string) ([]string, error) {
	return Dataset{h}.TradingDays(from, through)
}

func (d Dataset) Coverage(from, through string) (Coverage, error) {
	if err := ValidateRange(from, through); err != nil {
		return Coverage{}, err
	}
	for _, h := range d {
		if err := h.Validate(); err != nil {
			return Coverage{}, err
		}
	}
	if err := d.validateConsistency(); err != nil {
		return Coverage{}, err
	}
	spans := make([]Span, 0, len(d))
	for _, h := range d {
		a, b := h.From, h.Through
		if b < from || a > through {
			continue
		}
		if a < from {
			a = from
		}
		if b > through {
			b = through
		}
		spans = append(spans, Span{From: a, Through: b})
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].From == spans[j].From {
			return spans[i].Through < spans[j].Through
		}
		return spans[i].From < spans[j].From
	})
	merged := make([]Span, 0, len(spans))
	for _, span := range spans {
		if len(merged) == 0 || span.From > nextDay(merged[len(merged)-1].Through) {
			merged = append(merged, span)
			continue
		}
		if span.Through > merged[len(merged)-1].Through {
			merged[len(merged)-1].Through = span.Through
		}
	}
	missing := missingSpans(merged, from, through)
	status := CoverageComplete
	if len(missing) > 0 {
		status = CoveragePartial
		if len(merged) == 0 {
			status = CoverageMissing
		}
	}
	return Coverage{
		From:     from,
		Through:  through,
		Status:   status,
		Complete: len(missing) == 0,
		Missing:  missing,
	}, nil
}

// validateConsistency rejects overlapping windows that disagree about whether
// a day inside both declared ranges is a trading day. Coverage is a claim about
// the whole window, so an absent day is just as meaningful as a present one.
func (d Dataset) validateConsistency() error {
	for i := 0; i < len(d); i++ {
		for j := i + 1; j < len(d); j++ {
			from, through := d[i].From, d[i].Through
			if d[j].From > from {
				from = d[j].From
			}
			if d[j].Through < through {
				through = d[j].Through
			}
			if from > through {
				continue
			}
			left, right := map[string]bool{}, map[string]bool{}
			for _, day := range d[i].Days {
				if day >= from && day <= through {
					left[day] = true
				}
			}
			for _, day := range d[j].Days {
				if day >= from && day <= through {
					right[day] = true
				}
			}
			conflicts := []string{}
			for day := range left {
				if !right[day] {
					conflicts = append(conflicts, day)
				}
			}
			for day := range right {
				if !left[day] {
					conflicts = append(conflicts, day)
				}
			}
			if len(conflicts) > 0 {
				sort.Strings(conflicts)
				return fmt.Errorf("trading calendar sources disagree on %s", conflicts[0])
			}
		}
	}
	return nil
}

func (d Dataset) TradingDays(from, through string) ([]string, error) {
	coverage, err := d.Coverage(from, through)
	if err != nil {
		return nil, err
	}
	if !coverage.Complete {
		return nil, &CoverageError{Coverage: coverage}
	}
	seen := map[string]bool{}
	for _, h := range d {
		for _, day := range h.Days {
			if day >= from && day <= through {
				seen[day] = true
			}
		}
	}
	days := make([]string, 0, len(seen))
	for day := range seen {
		days = append(days, day)
	}
	sort.Strings(days)
	return days, nil
}

// Freeze produces one canonical history for a snapshot. When the source is a
// union of windows, sources are recorded as a sorted '+' joined provenance.
func (d Dataset) Freeze(from, through string) (History, error) {
	days, err := d.TradingDays(from, through)
	if err != nil {
		return History{}, err
	}
	sources := map[string]bool{}
	var fetchedAt time.Time
	for _, h := range d {
		if h.Through < from || h.From > through {
			continue
		}
		sources[h.Source] = true
		if h.FetchedAt.After(fetchedAt) {
			fetchedAt = h.FetchedAt
		}
	}
	names := make([]string, 0, len(sources))
	for source := range sources {
		names = append(names, source)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return History{}, errors.New("no calendar source covers requested window")
	}
	return NewHistory(strings.Join(names, "+"), from, through, days, fetchedAt)
}

func missingSpans(covered []Span, from, through string) []Span {
	missing := []Span{}
	cursor := from
	for _, span := range covered {
		if span.Through < cursor {
			continue
		}
		if span.From > through {
			break
		}
		if span.From > cursor {
			end := previousDay(span.From)
			if end > through {
				end = through
			}
			missing = append(missing, Span{From: cursor, Through: end})
		}
		if span.Through >= through {
			return missing
		}
		cursor = nextDay(span.Through)
	}
	if cursor <= through {
		missing = append(missing, Span{From: cursor, Through: through})
	}
	return missing
}

func nextDay(day string) string {
	t, _ := time.Parse("2006-01-02", day)
	return t.AddDate(0, 0, 1).Format("2006-01-02")
}

func previousDay(day string) string {
	t, _ := time.Parse("2006-01-02", day)
	return t.AddDate(0, 0, -1).Format("2006-01-02")
}

// Source fetches the complete exchange calendar for a bounded window.
type Source interface {
	TradingDays(context.Context, string, string) ([]string, error)
}

type Provider struct {
	Name   string
	Source Source
}

// Sources tries providers in order. Calendar data is small, so a provider
// failure can use the next source without affecting market-history fallback.
type Sources struct {
	Providers []Provider
	Now       func() time.Time
}

func (s Sources) Fetch(ctx context.Context, from, through string) (History, error) {
	if err := ValidateRange(from, through); err != nil {
		return History{}, err
	}
	if len(s.Providers) == 0 {
		return History{}, errors.New("no trading calendar sources configured")
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	var failures []error
	for _, provider := range s.Providers {
		if err := ctx.Err(); err != nil {
			return History{}, err
		}
		if provider.Name == "" || provider.Source == nil {
			failures = append(failures, errors.New("invalid calendar provider"))
			continue
		}
		days, err := provider.Source.TradingDays(ctx, from, through)
		if ctx.Err() != nil {
			return History{}, ctx.Err()
		}
		if err == nil {
			var history History
			history, err = NewHistory(provider.Name, from, through, days, now().In(market.Shanghai))
			if err == nil {
				return history, nil
			}
		}
		failures = append(failures, fmt.Errorf("%s: %w", provider.Name, err))
	}
	return History{}, fmt.Errorf("all trading calendar sources failed: %w", errors.Join(failures...))
}
