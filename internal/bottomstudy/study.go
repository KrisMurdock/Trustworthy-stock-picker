package bottomstudy

import (
	"context"
	"errors"
	"fmt"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"sort"
	"time"
)

const Engine = "daily_bottom_signals_v1"
const horizon = 20

type Options struct {
	From    string `json:"from"`
	Through string `json:"through"`
}

type Episode struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Rule        string   `json:"rule"`
	Target      float64  `json:"target_pct"`
	Start       string   `json:"start"`
	End         string   `json:"end"`
	Reference   float64  `json:"reference_close"`
	Available   int      `json:"available_bars"`
	HitDay      int      `json:"hit_day"`
	DrawdownDay int      `json:"drawdown_3_day"`
	Order       string   `json:"barrier_order"`
	Status      string   `json:"status"`
	SignalDates []string `json:"signal_dates"`
	ActiveDays  int      `json:"active_signal_days"`
}

type Summary struct {
	Rule           string      `json:"rule"`
	Target         float64     `json:"target_pct"`
	Episodes       int         `json:"episodes"`
	Mature         int         `json:"mature"`
	Tail           int         `json:"incomplete_followup"`
	TailHits       int         `json:"incomplete_observed_hits"`
	Hits           int         `json:"hits"`
	NoHit          int         `json:"no_hit"`
	HitByDay       map[int]int `json:"hit_by_day"`
	HitByCount     map[int]int `json:"hit_by_signal_count"`
	NoHitByCount   map[int]int `json:"no_hit_by_signal_count"`
	TargetFirst    int         `json:"target_before_drawdown"`
	DrawdownFirst  int         `json:"hit_after_drawdown"`
	OrderUnknown   int         `json:"hit_same_day_as_drawdown"`
	MedianHitBars  float64     `json:"median_hit_bars"`
	MedianSignals  float64     `json:"median_signals_on_hit"`
	StockCount     int         `json:"stock_count"`
	StartDateCount int         `json:"start_date_count"`
}

type Conditional struct {
	Occurrence    int     `json:"occurrence"`
	Bars          int     `json:"bars"`
	Target        float64 `json:"target_pct"`
	Observed      int     `json:"observed"`
	Eligible      int     `json:"eligible"`
	Hits          int     `json:"hits"`
	TargetFirst   int     `json:"target_before_drawdown"`
	DrawdownFirst int     `json:"hit_after_drawdown"`
	OrderUnknown  int     `json:"hit_same_day_as_drawdown"`
}

type StockSignals struct {
	Code    string   `json:"code"`
	Name    string   `json:"name"`
	Signals []Signal `json:"signals"`
}

type Report struct {
	Version     int            `json:"schema_version"`
	Engine      string         `json:"engine"`
	Options     Options        `json:"options"`
	Stocks      int            `json:"stocks"`
	SignalDays  int            `json:"signal_days"`
	FirstSignal string         `json:"first_signal"`
	LastSignal  string         `json:"last_signal"`
	Summary     []Summary      `json:"summary"`
	Conditional []Conditional  `json:"conditional"`
	Episodes    []Episode      `json:"episodes"`
	Signals     []StockSignals `json:"stock_signals"`
}

func Analyze(ctx context.Context, histories []market.History, options Options) (Report, error) {
	r := Report{Version: 1, Engine: Engine, Options: options, Summary: []Summary{}, Episodes: []Episode{}, Signals: []StockSignals{}}
	for _, d := range []string{options.From, options.Through} {
		if _, err := time.Parse(time.DateOnly, d); err != nil {
			return r, errors.New("from and through must be ISO dates")
		}
	}
	if options.From > options.Through || len(histories) == 0 {
		return r, errors.New("empty history or reversed interval")
	}
	for n := 1; n <= 4; n++ {
		for _, days := range []int{1, 3, 5, 10, 20} {
			for _, target := range []float64{2.5, 3} {
				r.Conditional = append(r.Conditional, Conditional{Occurrence: n, Bars: days, Target: target})
			}
		}
	}
	seen := map[string]bool{}
	ordered := append([]market.History(nil), histories...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Code < ordered[j].Code })
	for _, h := range ordered {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		if h.Adjust != "qfq" || h.DataField != "qfqday" || h.Validate() != nil || seen[h.Code] || options.Through > h.AsOf {
			return r, fmt.Errorf("invalid, duplicate or stale qfq history: %s", h.Code)
		}
		seen[h.Code] = true
		end := sort.Search(len(h.Bars), func(i int) bool { return h.Bars[i].Date > options.Through })
		bars := h.Bars[:end] // Never calculate indicators or outcomes beyond cutoff.
		if len(bars) < warmup {
			return r, fmt.Errorf("insufficient warmup: %s", h.Code)
		}
		signals, err := detect(ctx, bars)
		if err != nil {
			return r, err
		}
		r.Stocks++
		stock := StockSignals{Code: h.Code, Name: h.Name, Signals: []Signal{}}
		index := map[string]int{}
		for i, s := range signals {
			index[s.Date] = i
			if s.Date < options.From || len(s.Events) == 0 {
				continue
			}
			r.SignalDays++
			if r.FirstSignal == "" || s.Date < r.FirstSignal {
				r.FirstSignal = s.Date
			}
			if s.Date > r.LastSignal {
				r.LastSignal = s.Date
			}
			stock.Signals = append(stock.Signals, s)
		}
		r.Signals = append(r.Signals, stock)
		for _, rule := range rules {
			for _, target := range []float64{2.5, 3} {
				episodes := episodesFor(bars, signals, options.From, rule, target)
				for _, e := range episodes {
					e.Code, e.Name = h.Code, h.Name
					r.Episodes = append(r.Episodes, e)
					if rule != "any" || target != 3 {
						continue
					}
					for n, date := range e.SignalDates {
						if n >= 4 {
							break
						}
						i := index[date]
						for k := range r.Conditional {
							c := &r.Conditional[k]
							if c.Occurrence != n+1 {
								continue
							}
							c.Observed++
							if i+c.Bars >= len(bars) {
								continue
							}
							c.Eligible++
							hit, _, order := response(bars, i, c.Bars, c.Target)
							if hit > 0 {
								c.Hits++
								addOrder(order, &c.TargetFirst, &c.DrawdownFirst, &c.OrderUnknown)
							}
						}
					}
				}
			}
		}
	}
	for _, rule := range rules {
		for _, target := range []float64{2.5, 3} {
			r.Summary = append(r.Summary, summarize(r.Episodes, rule, target))
		}
	}
	return r, ctx.Err()
}

// response starts on the NEXT bar. A same-bar high/low does not prove the order
// of target and adverse excursion, even if an optimistic fill is conceivable.
func response(bars []market.Bar, start, days int, target float64) (hit, drawdown int, order string) {
	ref := bars[start].Close
	for d := 1; d <= days && start+d < len(bars); d++ {
		b := bars[start+d]
		if drawdown == 0 && b.Low <= ref*0.97 {
			drawdown = d
		}
		if b.High >= ref*(1+target/100) {
			hit = d
			break
		}
	}
	order = "neither"
	if hit > 0 && drawdown == 0 {
		order = "target_first"
	} else if drawdown > 0 && (hit == 0 || drawdown < hit) {
		order = "drawdown_first"
	} else if hit > 0 && drawdown == hit {
		order = "same_day_unknown"
	}
	return
}

func episodesFor(bars []market.Bar, signals []Signal, from, rule string, target float64) []Episode {
	out := []Episode{}
	for start := warmup - 1; start < len(bars); {
		if bars[start].Date < from || !has(signals[start].Events, rule) {
			start++
			continue
		}
		available := min(horizon, len(bars)-start-1)
		hit, drawdown, order := response(bars, start, available, target)
		end, status := start+available, "no_hit"
		if hit > 0 {
			end, status = start+hit, "hit"
		} else if available < horizon {
			status = "censored"
		}
		e := Episode{Rule: rule, Target: target, Start: bars[start].Date, End: bars[end].Date, Reference: bars[start].Close, Available: available, HitDay: hit, DrawdownDay: drawdown, Order: order, Status: status, SignalDates: []string{bars[start].Date}, ActiveDays: 1}
		for i := start + 1; i <= end; i++ {
			if hit > 0 && i == end {
				break // A signal confirmed at the close cannot precede this high.
			}
			if has(signals[i].Events, rule) {
				e.SignalDates = append(e.SignalDates, bars[i].Date)
			}
			if has(signals[i].States, rule) {
				e.ActiveDays++
			}
		}
		out = append(out, e)
		if hit > 0 {
			start = end // This close can initiate a NEW, non-overlapping episode.
		} else {
			start = end + 1
		}
	}
	return out
}

func summarize(episodes []Episode, rule string, target float64) Summary {
	s := Summary{Rule: rule, Target: target, HitByDay: map[int]int{}, HitByCount: map[int]int{}, NoHitByCount: map[int]int{}}
	days, counts := []int{}, []int{}
	stocks, dates := map[string]bool{}, map[string]bool{}
	for _, e := range episodes {
		if e.Rule != rule || e.Target != target {
			continue
		}
		s.Episodes++
		if e.Available < horizon {
			s.Tail++
			if e.HitDay > 0 {
				s.TailHits++
			}
			continue
		}
		s.Mature++
		stocks[e.Code], dates[e.Start] = true, true
		count := len(e.SignalDates)
		if e.HitDay == 0 {
			s.NoHit++
			s.NoHitByCount[min(count, 4)]++
			continue
		}
		s.Hits++
		days, counts = append(days, e.HitDay), append(counts, count)
		s.HitByCount[min(count, 4)]++
		for _, d := range []int{1, 3, 5, 10, 20} {
			if e.HitDay <= d {
				s.HitByDay[d]++
			}
		}
		addOrder(e.Order, &s.TargetFirst, &s.DrawdownFirst, &s.OrderUnknown)
	}
	s.MedianHitBars, s.MedianSignals = median(days), median(counts)
	s.StockCount, s.StartDateCount = len(stocks), len(dates)
	return s
}

func addOrder(order string, target, drawdown, unknown *int) {
	switch order {
	case "target_first":
		*target++
	case "drawdown_first":
		*drawdown++
	case "same_day_unknown":
		*unknown++
	}
}

func median(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Ints(values)
	n := len(values)
	return float64(values[(n-1)/2]+values[n/2]) / 2
}
