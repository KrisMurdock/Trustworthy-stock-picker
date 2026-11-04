package maexec

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"time"
)

func finite(x float64) bool   { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func positive(x float64) bool { return finite(x) && x > 0 }
func DateOK(d string) bool {
	t, e := time.Parse("2006-01-02", d)
	return e == nil && t.Format("2006-01-02") == d
}
func (in Input) Validate(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if in.Version != 3 || in.PriceBasis != "qfq_proxy" || in.Model != "adjusted_return_proxy" {
		return errors.New("only schema 3 adjusted_return_proxy is supported")
	}
	if in.Provenance.Calendar == "" || in.Provenance.Rules == "" || in.Provenance.Assumptions == "" {
		return errors.New("explicit model provenance required")
	}
	c := in.Config
	if !positive(c.Capital) || !positive(c.Fraction) || c.Fraction > 1 || !positive(c.Participation) || c.Participation > 1 || !positive(c.Tick) || c.MaxPositions < 1 || c.MaxPositions > 10000 || c.Holding < 1 || c.Holding > 10000 || (c.Target != .03 && c.Target != .05) || c.Stop != .05 || !finite(c.Slippage) || c.Slippage < 0 || c.Slippage >= 1 || !finite(c.RiskFree) || c.RiskFree < 0 {
		return errors.New("invalid proxy configuration")
	}
	if len(in.Sessions) == 0 || len(in.Sessions) > 10000 {
		return errors.New("invalid session count")
	}
	dates := map[string]bool{}
	for i, d := range in.Sessions {
		if !DateOK(d) || (i > 0 && d <= in.Sessions[i-1]) {
			return errors.New("sessions must be unique ordered ISO dates")
		}
		dates[d] = true
	}
	codes := map[string]bool{}
	seen := map[string]bool{}
	for _, s := range in.Signals {
		k := s.Code + "/" + s.Date
		if s.Code == "" || !dates[s.Date] || seen[k] || !positive(s.ATR) {
			return errors.New("invalid or duplicate signal")
		}
		seen[k] = true
		codes[s.Code] = true
	}
	for _, d := range in.Sessions {
		if e := ctx.Err(); e != nil {
			return e
		}
		for code := range codes {
			b, ok := in.Bars[d][code]
			if !ok || !b.Assumed {
				return fmt.Errorf("missing assumed bar: %s %s", code, d)
			}
			for _, x := range []float64{b.Open, b.High, b.Low, b.Close, b.MA10, b.Lower, b.Upper} {
				if !positive(x) {
					return errors.New("invalid price or limit")
				}
			}
			if b.Lower >= b.Upper || b.Low > math.Min(b.Open, b.Close) || math.Max(b.Open, b.Close) > b.High {
				return errors.New("invalid OHLC or limits")
			}
			for _, x := range []float64{b.Commission, b.Minimum, b.Transfer, b.Stamp, b.ADV} {
				if !finite(x) || x < 0 {
					return errors.New("invalid fee or liquidity")
				}
			}
			if b.Commission >= 1 || b.Transfer >= 1 || b.Stamp >= 1 {
				return errors.New("fee rates must be fractions")
			}
			if b.Observation != "observed" && b.Observation != "carried_missing" && b.Observation != "unpriced_before_first" {
				return errors.New("explicit observation required")
			}
			if b.Observation != "observed" && (b.Buy || b.Sell || b.Intraday) {
				return errors.New("missing bars cannot be tradable")
			}
		}
	}
	return nil
}

// Match Decimal(str(float)) rounding in the frozen Python reference. A binary
// epsilon would change fees at exact-cent boundaries and hide reconciliation errors.
func rounded(v, tick float64, up bool) float64 {
	// Finite JSON values can still overflow during multiplication. Propagate an
	// invalid value to the caller's numeric invariant instead of dereferencing a
	// nil Rat returned by parsing infinity.
	if !finite(v) || !positive(tick) {
		return math.NaN()
	}
	a, _ := new(big.Rat).SetString(strconv.FormatFloat(v, 'g', -1, 64))
	b, _ := new(big.Rat).SetString(strconv.FormatFloat(tick, 'g', -1, 64))
	q := new(big.Rat).Quo(a, b)
	n, r := new(big.Int), new(big.Int)
	n.QuoRem(q.Num(), q.Denom(), r)
	if r.Sign() != 0 {
		if up && q.Sign() > 0 {
			n.Add(n, big.NewInt(1))
		}
		if !up && q.Sign() < 0 {
			n.Sub(n, big.NewInt(1))
		}
	}
	out, _ := new(big.Rat).Mul(new(big.Rat).SetInt(n), b).Float64()
	return out
}
func charges(amount float64, b Bar, sell bool) float64 {
	v := rounded(math.Max(amount*b.Commission, b.Minimum), .01, true) + rounded(amount*b.Transfer, .01, true)
	if sell {
		v += rounded(amount*b.Stamp, .01, true)
	}
	return v
}
func ptr(x float64) *float64 { return &x }

func Simulate(ctx context.Context, in Input) (Result, error) {
	r := Result{Version: 3, Model: in.Model, PriceBasis: in.PriceBasis, QuantityBasis: "fractional_synthetic_units", Config: in.Config, Decisions: []Decision{}, Orders: []Order{}, Trades: []Trade{}, NAV: []Equity{}, Open: map[string]*Position{}, Limitations: []string{"Adjusted-price returns and fractional synthetic units; not historical cash/share accounting.", "Availability, board limits, costs and capacity are assumptions. Corporate taxes, payment dates and partial fills are not modeled.", "Missing prices carry forward for valuation only; open positions are not force liquidated.", "Historical development evidence, not independent out-of-sample or production approval."}}
	if e := in.Validate(ctx); e != nil {
		return r, e
	}
	c := in.Config
	cash := c.Capital
	indices := map[string]int{}
	for i, d := range in.Sessions {
		indices[d] = i
	}
	signals := map[int][]Signal{}
	for _, s := range in.Signals {
		i := indices[s.Date] + 1
		if i == len(in.Sessions) {
			r.Decisions = append(r.Decisions, Decision{s, "unobserved_next_session"})
		} else {
			signals[i] = append(signals[i], s)
		}
	}
	// Position iteration follows insertion order, as in the reference Python dict.
	order := []string{}
	sell := func(code, day, reason string, reference float64, b Bar) error {
		p := r.Open[code]
		fill := rounded(reference*(1-c.Slippage), c.Tick, false)
		if !positive(fill) {
			return errors.New("sell fill rounds to zero")
		}
		if fill <= b.Lower || fill >= b.Upper {
			p.queue("sell_price_limit")
			return nil
		}
		amount := fill * p.Shares
		fees := charges(amount, b, true)
		proceeds := amount - fees
		cash += proceeds
		r.Trades = append(r.Trades, Trade{Code: code, SignalDate: p.SignalDate, EntryDate: p.EntryDate, ExitDate: day, Reason: reason, Shares: p.Shares, EntryPrice: p.Entry, ExitPrice: fill, BuyCost: p.Cost, Proceeds: proceeds, Fees: p.BuyFees + fees, PNL: proceeds - p.Cost, Return: proceeds/p.Cost - 1, Holding: indices[day] - p.EntryIndex + 1})
		r.Orders = append(r.Orders, Order{Date: day, Code: code, Side: "sell", Fill: fill, Shares: p.Shares, Fees: fees, Reason: reason})
		delete(r.Open, code)
		return nil
	}
	for i, day := range in.Sessions {
		if e := ctx.Err(); e != nil {
			return r, e
		}
		daily := in.Bars[day]
		prior := cash
		if len(r.NAV) > 0 {
			prior = r.NAV[len(r.NAV)-1].Equity
		}
		for _, code := range order {
			p := r.Open[code]
			if p == nil {
				continue
			}
			b := daily[code]
			if p.Pending != nil && b.Sell {
				if e := sell(code, day, *p.Pending, b.Open, b); e != nil {
					return r, e
				}
				continue
			}
			if b.Open <= p.Stop {
				if b.Sell {
					if e := sell(code, day, "stop_gap", b.Open, b); e != nil {
						return r, e
					}
				} else {
					p.queue("stop_blocked")
				}
			} else if b.Open >= p.Target && b.Sell {
				if e := sell(code, day, "target_gap", p.Target, b); e != nil {
					return r, e
				}
			}
		}
		// Remove sold entries before a same-code re-entry to preserve dict order.
		kept := order[:0]
		for _, code := range order {
			if r.Open[code] != nil {
				kept = append(kept, code)
			}
		}
		order = kept
		ss := signals[i]
		sort.SliceStable(ss, func(a, b int) bool {
			if ss[a].ATR != ss[b].ATR {
				return ss[a].ATR > ss[b].ATR
			}
			return ss[a].Code < ss[b].Code
		})
		for _, s := range ss {
			if e := ctx.Err(); e != nil {
				return r, e
			}
			b := daily[s.Code]
			reason := ""
			if r.Open[s.Code] != nil {
				reason = "already_held"
			} else if len(r.Open) >= c.MaxPositions {
				reason = "position_limit"
			} else if !b.Buy {
				reason = "entry_unavailable"
			}
			if reason != "" {
				r.Decisions = append(r.Decisions, Decision{s, reason})
				continue
			}
			fill := rounded(b.Open*(1+c.Slippage), c.Tick, true)
			if !positive(fill) {
				return r, errors.New("invalid buy fill after rounding")
			}
			if fill >= b.Upper {
				r.Decisions = append(r.Decisions, Decision{s, "entry_price_limit"})
				continue
			}
			budget := math.Min(cash, prior*c.Fraction)
			low, high := 0.0, math.Min(budget, b.ADV*c.Participation)
			for j := 0; j < 45; j++ {
				mid := (low + high) / 2
				if mid+charges(mid, b, false) <= budget {
					low = mid
				} else {
					high = mid
				}
			}
			if low < .01 {
				r.Decisions = append(r.Decisions, Decision{s, "cash_or_capacity"})
				continue
			}
			shares := low / fill
			fees := charges(shares*fill, b, false)
			cost := shares*fill + fees
			cash -= cost
			r.Open[s.Code] = &Position{Entry: fill, Shares: shares, Cost: cost, BuyFees: fees, EntryIndex: i, EntryDate: day, SignalDate: s.Date, Stop: fill * (1 - c.Stop), Target: fill * (1 + c.Target)}
			order = append(order, s.Code)
			r.Decisions = append(r.Decisions, Decision{s, "entered"})
			r.Orders = append(r.Orders, Order{Date: day, Code: s.Code, Side: "buy", Fill: fill, Shares: shares, Fees: fees})
		}
		for _, code := range order {
			p := r.Open[code]
			b := daily[code]
			stop, target := b.Low <= p.Stop, b.High >= p.Target
			if p.EntryIndex == i {
				if stop {
					p.queue("stop_t1")
				}
			} else if b.Intraday && p.Pending == nil {
				if stop && target {
					r.Metrics.Ambiguous++
				}
				if stop {
					if e := sell(code, day, "stop", math.Min(b.Open, p.Stop), b); e != nil {
						return r, e
					}
					continue
				}
				if target {
					if e := sell(code, day, "target", p.Target, b); e != nil {
						return r, e
					}
					continue
				}
			} else if stop {
				p.queue("stop_blocked")
			}
			if b.Close < b.MA10 {
				p.queue("ma10")
			}
			if i-p.EntryIndex+1 >= c.Holding {
				p.queue("time")
			}
		}
		kept = order[:0]
		value := 0.0
		for _, code := range order {
			if p := r.Open[code]; p != nil {
				kept = append(kept, code)
				value += p.Shares * daily[code].Close
				if daily[code].Observation != "observed" {
					r.Metrics.Stale++
				}
			}
		}
		order = kept
		if !finite(cash+value) || cash < -1e-7 {
			return r, errors.New("portfolio conservation invariant failed")
		}
		r.NAV = append(r.NAV, Equity{Date: day, Cash: cash, Value: value, Equity: cash + value, Positions: len(r.Open)})
	}
	summarize(&r)
	return r, nil
}
func summarize(r *Result) {
	m := &r.Metrics
	m.Closed = len(r.Trades)
	pnl, ret, wins := 0.0, 0.0, 0.0
	for _, t := range r.Trades {
		pnl += t.PNL
		ret += t.Return
		if t.PNL > 1e-8 {
			wins++
		}
		m.Gains += math.Max(t.PNL, 0)
		m.Losses += math.Max(-t.PNL, 0)
	}
	if m.Closed > 0 {
		n := float64(m.Closed)
		m.WinRate = ptr(wins / n)
		m.Expectancy = ptr(pnl / n)
		m.MeanReturn = ptr(ret / n)
	}
	if m.Losses > 0 {
		m.PF = ptr(m.Gains / m.Losses)
	}
	peak, previous := r.Config.Capital, r.Config.Capital
	returns := []float64{}
	mean := 0.0
	for _, n := range r.NAV {
		peak = math.Max(peak, n.Equity)
		m.Drawdown = math.Min(m.Drawdown, n.Equity/peak-1)
		v := n.Equity/previous - 1
		returns = append(returns, v)
		mean += v
		previous = n.Equity
	}
	m.Return = previous/r.Config.Capital - 1
	mean /= float64(len(returns))
	variance := 0.0
	for _, v := range returns {
		variance += (v - mean) * (v - mean)
	}
	if len(returns) > 1 && variance > 0 {
		sd := math.Sqrt(variance / float64(len(returns)-1))
		rf := math.Pow(1+r.Config.RiskFree, 1.0/252) - 1
		m.Sharpe = ptr((mean - rf) / sd * math.Sqrt(252))
	}
}
