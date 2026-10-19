package strategy

import (
	"errors"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

const chiNextPriceLimitReform = "2020-08-24"

// ErrPriceLimitUnknown reports that no board/date rule can be established.
var ErrPriceLimitUnknown = errors.New("daily price limit is unknown")

// STStatus describes whether a stock carried special-treatment status on a day.
// Historical ST status is not available in the stored market data.
type STStatus uint8

const (
	STStatusUnknown STStatus = iota
	STStatusNormal
	STStatusSpecial
)

// LimitRule is the daily price-limit band for a stock and trading day.
// STStatus is deliberately unknown: current names must not be used to infer
// historical ST status.
type LimitRule struct {
	Pct      float64
	Known    bool
	STStatus STStatus
}

// PriceLimitFor returns the board/date price-limit rule for a supported code.
// ChiNext (sz30) used 10% before 2020-08-24 and 20% from that day onward.
func PriceLimitFor(code, tradingDay string) LimitRule {
	if !config.ValidCode(code) {
		return LimitRule{}
	}
	if _, err := time.Parse("2006-01-02", tradingDay); err != nil {
		return LimitRule{}
	}
	rule := LimitRule{Pct: 10, Known: true, STStatus: STStatusUnknown}
	switch {
	case strings.HasPrefix(code, "sh68"):
		rule.Pct = 20
	case strings.HasPrefix(code, "sz30") && tradingDay >= chiNextPriceLimitReform:
		rule.Pct = 20
	}
	return rule
}
