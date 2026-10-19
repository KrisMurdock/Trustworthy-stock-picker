package strategy

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"math"
	"time"
)

const ConditionalEntryMode = "pullback_rebound_v1"

type EntryZone struct {
	Lower      float64 `json:"lower"`
	Upper      float64 `json:"upper"`
	ReboundPct float64 `json:"rebound_pct"`
	LastEntry  string  `json:"last_entry"`
}

// Entry prices are fixed from the previous completed day's raw close.
func ConditionalZone(previousClose float64) EntryZone {
	return EntryZone{Lower: tickUp(previousClose * .98), Upper: tickDown(previousClose * .995), ReboundPct: .3, LastEntry: "14:30"}
}

func completeMinuteDay(bars []market.MinuteBar, daily market.Bar) bool {
	if len(bars) != 48 {
		return false
	}
	day, err := time.ParseInLocation("2006-01-02", daily.Date, market.Shanghai)
	if err != nil {
		return false
	}
	for i, b := range bars {
		minute := 575 + i*5
		if i >= 24 {
			minute = 785 + (i-24)*5
		}
		if !b.End.Equal(day.Add(time.Duration(minute)*time.Minute)) || !b.Final || b.Volume < 0 || b.Low <= 0 || b.High < math.Max(b.Open, b.Close) || b.Low > math.Min(b.Open, b.Close) || b.Low < daily.Low-.015 || b.High > daily.High+.015 {
			return false
		}
	}
	return math.Abs(bars[len(bars)-1].Close-daily.Close) < .015
}

// SimulateConditional inspects completed five-minute candles for the trigger,
// then fills at the NEXT candle's open. It never buys at the hindsight daily low
// or removes an entry because its later close is negative.
func SimulateConditional(s config.Strategy, cal *calendar.Calendar, code string, signal market.Bar, future []market.Bar, entryBars, exitBars []market.MinuteBar) (Outcome, error) {
	out := Outcome{Status: "entry_path_unknown"}
	if err := s.Validate(); err != nil {
		return out, err
	}
	entryDate, err := cal.Next(signal.Date)
	if err != nil {
		return out, err
	}
	if len(future) == 0 || future[0].Date != entryDate || !completeMinuteDay(entryBars, future[0]) {
		return out, nil
	}
	zone := ConditionalZone(signal.Close)
	low := math.Inf(1)
	fillIndex := -1
	for i, b := range entryBars {
		localEnd := b.End.In(market.Shanghai)
		if localEnd.Hour()*60+localEnd.Minute() > 14*60+30 {
			break
		}
		low = math.Min(low, b.Low)
		if b.Close < zone.Lower {
			out.Status = "entry_invalidated"
			return out, nil
		}
		if b.Volume <= 0 || b.Close < zone.Lower || b.Close > zone.Upper || b.Close < low*(1+zone.ReboundPct/100) {
			continue
		}
		next := entryBars[i+1]
		// Never carry a pre-lunch trigger over the 90-minute market break.
		if next.End.Sub(b.End) != 5*time.Minute {
			continue
		}
		fill := tickUp(next.Open * (1 + s.SlippageRate))
		if next.Volume <= 0 || fill < zone.Lower || fill > zone.Upper || fill > next.High+.005 {
			out.Status = "entry_price_outside_zone"
			return out, nil
		}
		p, err := NewPlan(s, code, fill)
		if err != nil {
			out.Status = "risk_budget_insufficient"
			return out, nil
		}
		fillIndex = i + 1
		out.EntryDate = entryDate
		out.EntryDay = entryDayValue(s, p, future[0].Close, next.End.Add(-5*time.Minute).Format(time.RFC3339), ConditionalEntryMode)
		out.HighTouch = &HighTouch{Version: 1}
		out.MAE = 0
		for _, held := range entryBars[fillIndex:] {
			out.MAE = math.Min(out.MAE, netPct(s, p, tickDown(held.Low*(1-s.SlippageRate))))
		}
		out.Status = "missing_exit_session"
		exitDate, err := cal.Next(entryDate)
		if err != nil {
			return out, err
		}
		if !PriceLimitFor(code, exitDate).Known {
			return out, ErrPriceLimitUnknown
		}
		if len(future) < 2 || future[1].Date != exitDate || !completeMinuteDay(exitBars, future[1]) {
			return out, nil
		}
		if lockedDown(future[1], future[0].Close, code) {
			out.Status = "unresolved"
			out.HoldingDays = 1
			return out, nil
		}
		out.HoldingDays = 1
		out.Status = "unresolved"
		// Conservative ordering is needed only inside a single five-minute candle.
		for j, sell := range exitBars {
			if sell.Volume <= 0 {
				out.Status = "missing_exit_session"
				return out, nil
			}
			if lockedDown(market.Bar{Date: exitDate, Open: sell.Open, High: sell.High, Low: sell.Low, Close: sell.Close, Volume: sell.Volume}, future[0].Close, code) {
				continue
			}
			out.MAE = math.Min(out.MAE, netPct(s, p, tickDown(sell.Low*(1-s.SlippageRate))))
			out.MFE = math.Max(out.MFE, netPct(s, p, tickDown(sell.High*(1-s.SlippageRate))))
			marketFill := func(price float64) float64 { return math.Max(sell.Low, tickDown(price*(1-s.SlippageRate))) }
			price, reason := 0.0, ""
			stop := sell.Low <= p.StopPrice
			target := sell.High >= p.TakeProfit+.01-1e-8
			switch {
			case sell.Open <= p.StopPrice:
				price, reason = marketFill(sell.Open), "stop_gap"
			case sell.Open >= p.TakeProfit && target:
				price, reason = math.Max(p.TakeProfit, marketFill(sell.Open)), "target"
			case stop:
				price, reason = marketFill(p.StopPrice), "stop"
				out.Ambiguous = target
			case target:
				price, reason = p.TakeProfit, "target"
			case j == len(exitBars)-1:
				price, reason = marketFill(sell.Close), "timeout"
			}
			high := sell.High
			if reason == "stop_gap" || reason == "target" && sell.Open >= p.TakeProfit {
				high = sell.Open
			}
			// Preserve confirmations from earlier candles even if the exit candle's
			// own high could occur after the exit.
			had25, had30 := out.HighTouch.Day25 > 0, out.HighTouch.Day30 > 0
			out.HighTouch.observe(1, netPct(s, p, tickDown(high*(1-s.SlippageRate))))
			out.HighTouch.ObservedDays = 1
			if reason != "" {
				out.Status, out.ExitDate, out.ExitPrice = reason, exitDate, price
				out.NetPct = netPct(s, p, price)
				intraday := reason == "stop" || reason == "target" && sell.Open < p.TakeProfit
				out.HighTouch.Day25Unknown = intraday && !had25 && out.HighTouch.Day25 == 1 && out.NetPct < 2.5
				out.HighTouch.Day30Unknown = intraday && !had30 && out.HighTouch.Day30 == 1 && out.NetPct < 3
				out.HighTouch.Ambiguous = out.HighTouch.Day25Unknown || out.HighTouch.Day30Unknown
				return out, nil
			}
		}
		return out, nil
	}
	out.Status = "entry_not_triggered"
	return out, nil
}
