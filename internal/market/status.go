package market

import (
	"context"
	"errors"
	"time"

	"stock-watcher/internal/config"
)

type SessionState string

const (
	SessionTraded    SessionState = "traded"
	SessionSuspended SessionState = "suspended"
	SessionUnknown   SessionState = "unknown"
)

func ValidSessionState(state SessionState) bool {
	return state == SessionTraded || state == SessionSuspended || state == SessionUnknown
}

// SessionStatus is the per-trading-day answer to "did this stock trade?".
// It is deliberately separate from History: a suspended day has no bar.
type SessionStatus struct {
	Code      string       `json:"code"`
	Date      string       `json:"date"`
	State     SessionState `json:"state"`
	Source    string       `json:"source"`
	FetchedAt time.Time    `json:"fetched_at"`
	Detail    string       `json:"detail,omitempty"`
}

type StatusWindow struct {
	Code      string          `json:"code"`
	Start     string          `json:"start"`
	End       string          `json:"end"`
	Source    string          `json:"source"`
	FetchedAt time.Time       `json:"fetched_at"`
	Statuses  []SessionStatus `json:"statuses"`
	// Full marks a source that enumerates every exchange session in the range,
	// including explicit traded days. Sparse suspension feeds must leave it
	// false so they never erase earlier authoritative answers.
	Full bool `json:"full,omitempty"`
}

func (w StatusWindow) Validate() error {
	if !config.ValidCode(w.Code) || w.Source == "" || w.FetchedAt.IsZero() || w.Start == "" || w.End == "" || w.Start > w.End {
		return errors.New("invalid status window metadata")
	}
	previous := ""
	for _, s := range w.Statuses {
		if s.Code != w.Code || s.Date < w.Start || s.Date > w.End || s.Date <= previous || !ValidSessionState(s.State) || s.Source == "" || s.FetchedAt.IsZero() {
			return errors.New("invalid session status")
		}
		previous = s.Date
	}
	return nil
}

type StatusSource interface {
	FetchStatus(context.Context, config.Stock, string, string) (StatusWindow, error)
}

type TradingCalendarSource interface {
	FetchTradingDays(context.Context, string, string) ([]string, error)
}
