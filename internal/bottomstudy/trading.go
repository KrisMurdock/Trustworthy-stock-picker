package bottomstudy

import (
	"math"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"strings"
)

type TradePolicy struct {
	Rule    string  `json:"rule"`
	Batches int     `json:"batches"`
	Horizon int     `json:"sellable_days"`
	Target  float64 `json:"target_net_pct"`
	Stop    float64 `json:"stop_loss_pct"` // Zero disables the stop, not the time exit.
}

type Fill struct {
	Date   string  `json:"date"`
	Side   string  `json:"side"`
	Shares int     `json:"shares"`
	Price  float64 `json:"price"`
	Cash   float64 `json:"cash"` // Buy cost / sell proceeds, including fees.
	Reason string  `json:"reason"`
}

type Trade struct {
	ID               string  `json:"id"`
	Code             string  `json:"code"`
	Name             string  `json:"name"`
	SignalDate       string  `json:"signal_date"`
	Split            string  `json:"split"`
	Status           string  `json:"status"`
	EntryDate        string  `json:"entry_date,omitempty"`
	ExitDate         string  `json:"exit_date,omitempty"`
	NetPct           float64 `json:"net_pct"`
	Invested         float64 `json:"invested"`
	BatchesFilled    int     `json:"batches_filled"`
	HoldingDays      int     `json:"holding_days"`
	EntryCloseNetPct float64 `json:"entry_close_net_pct"`
	TargetHit        bool    `json:"target_hit"`
	TargetDay        int     `json:"target_day"`
	StopTriggered    bool    `json:"stop_triggered"`
	StopTriggerDate  string  `json:"stop_trigger_date,omitempty"`
	Ambiguous        bool    `json:"ambiguous"`
	BlockedByT1      bool    `json:"stop_blocked_by_t1"`
	TargetBlockedT1  bool    `json:"target_blocked_by_t1"`
	StopExceeded     bool    `json:"stop_exceeded"`
	Fills            []Fill  `json:"fills"`
}

type opportunity struct {
	id, code, name, split string
	bars                  []market.Bar // Index 0: signal; 1: entry; 2: first sellable.
	events                []bool
	invalid               map[int]string
}

type lot struct{ shares, bought int }
type position struct {
	lots     []lot
	invested float64
	realized float64
}

func roundUp(v float64) float64   { return math.Ceil(v*100-1e-8) / 100 }
func roundDown(v float64) float64 { return math.Floor(v*100+1e-8) / 100 }
func charge(amount, rate, minimum float64) float64 {
	return math.Ceil((math.Max(amount*rate, minimum)-1e-9)*100) / 100
}
func purchaseCost(s config.Strategy, price float64, n int) float64 {
	a := price * float64(n)
	return a + charge(a, s.CommissionRate, s.MinCommission) + charge(a, s.TransferRate, 0)
}
func proceeds(s config.Strategy, price float64, n int) float64 {
	if n == 0 {
		return 0
	}
	a := price * float64(n)
	return a - charge(a, s.CommissionRate, s.MinCommission) - charge(a, s.TransferRate, 0) - charge(a, s.SellStampRate, 0)
}
func (p *position) shares(day int, sellable bool) int {
	n := 0
	for _, l := range p.lots {
		if !sellable || l.bought < day {
			n += l.shares
		}
	}
	return n
}
func (p *position) net(s config.Strategy, price float64) float64 {
	return ((p.realized+proceeds(s, price, p.shares(0, false)))/p.invested - 1) * 100
}
func (p *position) price(s config.Strategy, goal, reference float64) float64 {
	lo, hi := 0.0, reference*2
	for p.net(s, hi) < goal {
		hi *= 2
	}
	for n := 0; n < 48; n++ {
		mid := (lo + hi) / 2
		if p.net(s, mid) >= goal {
			hi = mid
		} else {
			lo = mid
		}
	}
	return roundUp(hi)
}

func (p *position) stopPrice(s config.Strategy, loss, reference float64) float64 {
	trigger := roundUp(p.price(s, -loss, reference) / (1 - s.SlippageRate))
	for p.net(s, roundDown(trigger*(1-s.SlippageRate))) <= -loss+1e-9 {
		trigger = roundUp(trigger + .01)
	}
	return trigger
}
func (p *position) buy(s config.Strategy, code string, b market.Bar, day int, budget float64) (Fill, bool) {
	fill := roundUp(b.Open * (1 + s.SlippageRate))
	minimum, step := 100, 100
	if strings.HasPrefix(code, "sh68") {
		minimum, step = 200, 1
	}
	n := int(budget/fill) / step * step
	for n >= minimum && purchaseCost(s, fill, n) > budget+1e-8 {
		n -= step
	}
	if n < minimum || fill > b.High+1e-8 {
		return Fill{}, false
	}
	cost := purchaseCost(s, fill, n)
	p.lots = append(p.lots, lot{n, day})
	p.invested += cost
	return Fill{b.Date, "buy", n, fill, cost, "signal"}, true
}
func (p *position) sell(s config.Strategy, b market.Bar, day int, price float64, reason string) Fill {
	n := p.shares(day, true)
	for i := range p.lots {
		if p.lots[i].bought < day {
			p.lots[i].shares = 0
		}
	}
	cash := proceeds(s, price, n)
	p.realized += cash
	return Fill{b.Date, "sell", n, price, cash, reason}
}

func onePrice(b market.Bar) bool { return b.High-b.Low < .005 }
func sellBlocked(b market.Bar, previous float64, code string) bool {
	limit := .10
	if strings.HasPrefix(code, "sh68") || strings.HasPrefix(code, "sz30") {
		limit = .20
	}
	return b.Volume <= 0 || onePrice(b) && b.Low <= math.Round(previous*(1-limit)*100)/100+.005
}

// simulateTrade models one position, including partial stop liquidation when a
// new lot is T+1 locked. Stop decisions latch: a later rebound never cancels them.
func simulateTrade(s config.Strategy, policy TradePolicy, o opportunity) Trade {
	t := Trade{ID: o.id, Code: o.code, Name: o.name, SignalDate: o.bars[0].Date, Split: o.split, Status: "entry_skipped", Fills: []Fill{}}
	var p position
	stopReason := "stop"
	deadline := policy.Horizon + 1
	finish := func(b market.Bar, i int, reason string) {
		t.Status, t.ExitDate, t.HoldingDays = reason, b.Date, i-1
		t.NetPct = p.net(s, b.Close) // All inventory has been sold.
		// A T+1-delayed stop can execute at a profitable next open. Its exit
		// reason must not erase an actually achieved net target.
		t.TargetHit = t.NetPct >= policy.Target-1e-9
		if t.TargetHit {
			t.TargetDay = i - 1
		}
		t.StopExceeded = policy.Stop > 0 && t.NetPct <= -policy.Stop+1e-9
	}
	for i := 1; i <= deadline && i < len(o.bars); i++ {
		b, previous := o.bars[i], o.bars[i-1]
		if reason := o.invalid[i]; reason != "" {
			t.Status = reason
			return t
		}
		marketFill := func(price float64) float64 { return math.Max(b.Low, roundDown(price*(1-s.SlippageRate))) }
		mark := func(price float64) float64 { return roundDown(price * (1 - s.SlippageRate)) }
		blocked := sellBlocked(b, previous.Close, o.code)
		all, available := p.shares(i, false), p.shares(i, true)
		if all > 0 {
			t.HoldingDays = i - 1
			// Decisions at the opening price precede any new purchase.
			if !t.StopTriggered && policy.Stop > 0 && b.Open <= p.stopPrice(s, policy.Stop, b.Open) {
				t.StopTriggered, t.StopTriggerDate, stopReason = true, b.Date, "stop_gap"
			}
			if t.StopTriggered {
				if !blocked && available > 0 {
					t.Fills = append(t.Fills, p.sell(s, b, i, marketFill(b.Open), stopReason))
				}
				if p.shares(i, false) == 0 {
					finish(b, i, stopReason)
					return t
				}
				continue
			}
			if !blocked && available == all && p.net(s, mark(b.Open)) >= policy.Target {
				t.TargetHit, t.TargetDay = true, i-1
				t.Fills = append(t.Fills, p.sell(s, b, i, marketFill(b.Open), "target"))
				finish(b, i, "target")
				return t
			}
		}
		if t.BatchesFilled < policy.Batches && (i == 1 || i < deadline && o.events[i-1]) {
			gap := math.Abs(b.Open/previous.Close-1) * 100
			if b.Volume > 0 && !onePrice(b) && gap <= s.MaxEntryGapPct {
				budget := math.Min(s.TradeCash/float64(policy.Batches), s.TradeCash-p.invested)
				if f, ok := p.buy(s, o.code, b, i, budget); ok {
					t.Fills = append(t.Fills, f)
					t.BatchesFilled++
					t.Invested = p.invested
					if t.EntryDate == "" {
						t.EntryDate = b.Date
						t.Status = "unresolved"
					}
				}
			}
			if i == 1 && t.EntryDate == "" {
				return t // Do not replace a failed initial order with a future signal.
			}
		}
		if i == 1 {
			t.EntryCloseNetPct = p.net(s, mark(b.Close))
		}
		all, available = p.shares(i, false), p.shares(i, true)
		highHit := p.net(s, mark(b.High)) >= policy.Target-1e-9
		trigger := 0.0
		if policy.Stop > 0 {
			trigger = p.stopPrice(s, policy.Stop, b.Open)
		}
		stopHit := policy.Stop > 0 && b.Low <= trigger
		if highHit && available < all {
			t.TargetBlockedT1 = true
		}
		if stopHit {
			t.StopTriggered, t.StopTriggerDate = true, b.Date
			t.Ambiguous = highHit && available == all
			t.BlockedByT1 = available < all
			// Include tick/slippage buffer so an ordinary trigger fill stays
			// strictly inside the nominal bound; gaps and T+1 can exceed it.
			if !blocked && available > 0 {
				t.Fills = append(t.Fills, p.sell(s, b, i, marketFill(math.Min(b.High, trigger)), stopReason))
			}
			if p.shares(i, false) == 0 {
				finish(b, i, stopReason)
				return t
			}
			continue
		}
		if !blocked && highHit && available == all {
			t.TargetHit, t.TargetDay = true, i-1
			price := p.price(s, policy.Target, b.Open)
			t.Fills = append(t.Fills, p.sell(s, b, i, price, "target"))
			finish(b, i, "target")
			return t
		}
		if i == deadline && !blocked && available == all {
			t.Fills = append(t.Fills, p.sell(s, b, i, marketFill(b.Close), "timeout"))
			finish(b, i, "timeout")
			return t
		}
	}
	return t
}
