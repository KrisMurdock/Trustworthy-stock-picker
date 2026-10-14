// Package strategy owns short-horizon ranking and its conservative daily-bar research.
package strategy

import (
	"errors"
	"math"
	"stock-watcher/internal/calendar"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"strings"
)

type Plan struct {
	Version       int     `json:"schema_version"`
	EntryPrice    float64 `json:"reference_entry_price"`
	Shares        int     `json:"reference_shares"`
	BuyCost       float64 `json:"reference_buy_cost"`
	Target25      float64 `json:"target_net_2_5_price"`
	Target30      float64 `json:"target_net_3_price"`
	TakeProfit    float64 `json:"take_profit_price"`
	StopPrice     float64 `json:"stop_price"`
	ReferenceOnly bool    `json:"reference_only"`
}

type Outcome struct {
	Status      string  `json:"status"`
	EntryDate   string  `json:"entry_date,omitempty"`
	ExitDate    string  `json:"exit_date,omitempty"`
	ExitPrice   float64 `json:"exit_price,omitempty"`
	NetPct      float64 `json:"net_pct"`
	HoldingDays int     `json:"holding_days"`
	MFE         float64 `json:"max_favorable_net_pct"`
	MAE         float64 `json:"max_adverse_net_pct"`
	Ambiguous   bool    `json:"ambiguous"`
}

func tickUp(p float64) float64   { return math.Ceil(p*100-1e-8) / 100 }
func tickDown(p float64) float64 { return math.Floor(p*100+1e-8) / 100 }
func fee(amount, rate, minimum float64) float64 {
	return math.Ceil((math.Max(amount*rate, minimum)-1e-9)*100) / 100
}
func buyCost(s config.Strategy, price float64, shares int) float64 {
	amount := price * float64(shares)
	return amount + fee(amount, s.CommissionRate, s.MinCommission) + fee(amount, s.TransferRate, 0)
}
func netPct(s config.Strategy, p Plan, sale float64) float64 {
	amount := sale * float64(p.Shares)
	proceeds := amount - fee(amount, s.CommissionRate, s.MinCommission) - fee(amount, s.TransferRate, 0) - fee(amount, s.SellStampRate, 0)
	return (proceeds/p.BuyCost - 1) * 100
}

// NewPlan takes an actual fill price; pre-entry reference plans are labelled separately.
func NewPlan(s config.Strategy, code string, fill float64) (Plan, error) {
	if err := s.Validate(); err != nil {
		return Plan{}, err
	}
	if !config.ValidCode(code) || fill <= 0 || math.IsNaN(fill) || math.IsInf(fill, 0) {
		return Plan{}, errors.New("invalid trade price or code")
	}
	fill = tickUp(fill)
	minimum, step := 100, 100
	if strings.HasPrefix(code, "sh68") {
		minimum, step = 200, 1
	}
	shares := int(s.TradeCash/fill) / step * step
	for shares >= minimum && buyCost(s, fill, shares) > s.TradeCash {
		shares -= step
	}
	if shares < minimum {
		return Plan{}, errors.New("trade cash cannot fund the minimum order")
	}
	p := Plan{Version: 1, EntryPrice: fill, Shares: shares, BuyCost: buyCost(s, fill, shares)}
	priceFor := func(goal float64) float64 {
		lo, hi := fill*0.5, fill*2
		for i := 0; i < 64; i++ {
			mid := (lo + hi) / 2
			if netPct(s, p, mid) >= goal {
				hi = mid
			} else {
				lo = mid
			}
		}
		return hi
	}
	p.Target25 = tickUp(priceFor(2.5))
	p.Target30 = tickUp(priceFor(3))
	p.TakeProfit = tickUp(priceFor(s.TargetNetPct))
	p.StopPrice = tickDown(priceFor(s.StopNetPct))
	return p, nil
}

func limitPct(code string) float64 {
	if strings.HasPrefix(code, "sh68") || strings.HasPrefix(code, "sz30") {
		return 20
	}
	return 10
}
func lockedDown(b market.Bar, previous float64, code string) bool {
	limit := math.Round(previous*(1-limitPct(code)/100)*100) / 100
	return b.Volume == 0 || (b.High-b.Low < 0.005 && b.Low <= limit+0.005)
}

// Simulate uses raw execution prices. horizon=1 means the first sellable day after buying,
// not the buy day. Missing sessions, suspensions and corporate actions are censored.
func Simulate(s config.Strategy, cal *calendar.Calendar, code string, signal market.Bar, future []market.Bar, horizon int) (Outcome, error) {
	out := Outcome{Status: "unresolved"}
	if err := s.Validate(); err != nil {
		return out, err
	}
	if horizon < 1 || horizon > s.MaxHoldingDays {
		return out, errors.New("invalid simulation horizon")
	}
	next, err := cal.Next(signal.Date)
	if err != nil {
		return out, err
	}
	if len(future) == 0 || future[0].Date != next {
		out.Status = "missing_entry_session"
		return out, nil
	}
	entry := future[0]
	gap := (entry.Open/signal.Close - 1) * 100
	if entry.Volume == 0 || math.Abs(gap) > s.MaxEntryGapPct || entry.High-entry.Low < 0.005 {
		out.Status = "entry_skipped"
		return out, nil
	}
	p, err := NewPlan(s, code, tickUp(entry.Open*(1+s.SlippageRate)))
	if err != nil {
		out.Status = "cash_insufficient"
		return out, nil
	}
	if p.EntryPrice > entry.High+0.005 {
		out.Status = "entry_skipped"
		return out, nil
	}
	out.EntryDate = entry.Date
	out.MAE = math.Min(0, netPct(s, p, entry.Low))
	previous := entry
	for day := 1; day <= horizon; day++ {
		if len(future) <= day {
			return out, nil
		}
		b := future[day]
		expected, err := cal.Next(previous.Date)
		if err != nil {
			return out, err
		}
		if b.Date != expected {
			out.Status = "missing_exit_session"
			return out, nil
		}
		out.HoldingDays = day
		out.MFE = math.Max(out.MFE, netPct(s, p, b.High))
		out.MAE = math.Min(out.MAE, netPct(s, p, b.Low))
		if lockedDown(b, previous.Close, code) {
			previous = b
			continue
		}
		upper := math.Round(previous.Close*(1+limitPct(code)/100)*100) / 100
		lower := math.Round(previous.Close*(1-limitPct(code)/100)*100) / 100
		// Slippage cannot create a simulated fill outside the observed daily range.
		marketFill := func(price float64) float64 { return math.Max(b.Low, tickDown(price*(1-s.SlippageRate))) }
		exitPrice, reason := 0.0, ""
		stop := b.Low <= p.StopPrice && p.StopPrice >= lower-0.005
		// A mere high-price touch does not prove a limit fill. Require one tick through.
		target := b.High >= p.TakeProfit+0.01-1e-8 && p.TakeProfit <= upper+0.005
		switch {
		case b.Open <= p.StopPrice:
			exitPrice, reason = marketFill(b.Open), "stop_gap"
		case b.Open >= p.TakeProfit && target:
			exitPrice, reason = math.Max(p.TakeProfit, marketFill(b.Open)), "target"
		case stop:
			exitPrice, reason = marketFill(p.StopPrice), "stop"
			out.Ambiguous = target
		case target:
			exitPrice, reason = p.TakeProfit, "target"
		case day == horizon:
			exitPrice, reason = marketFill(b.Close), "timeout"
		}
		if reason != "" {
			out.Status = reason
			out.ExitDate = b.Date
			out.ExitPrice = exitPrice
			out.NetPct = netPct(s, p, exitPrice)
			return out, nil
		}
		previous = b
	}
	return out, nil
}
