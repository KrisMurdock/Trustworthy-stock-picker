package aris

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
)

// Inference freezes a descriptive, non-overlapping time-block bootstrap. All
// stocks sharing a signal date stay in one block. This is not OOS evidence or a
// correction for unrecorded earlier strategy searches.
type Inference struct {
	BlockSessions int     `json:"block_sessions"`
	MinBlocks     int     `json:"min_independent_blocks"`
	Samples       int     `json:"bootstrap_samples"`
	Confidence    float64 `json:"confidence"`
	Seed          int64   `json:"seed"`
}

func (c Inference) valid() bool {
	return c.BlockSessions >= 2 && c.BlockSessions <= 1000 && c.MinBlocks >= 4 && c.MinBlocks <= 1000 && c.Samples >= 100 && c.Samples <= 10000 && finite(c.Confidence) && c.Confidence >= .8 && c.Confidence < 1
}

type Interval struct {
	Blocks          int       `json:"blocks"`
	TradeBlocks     int       `json:"trade_blocks"`
	Trades          int       `json:"closed_trades"`
	Sessions        int       `json:"sessions"`
	ExpectancyLower float64   `json:"net_expectancy_lower_amount"`
	ExpectancyUpper float64   `json:"net_expectancy_upper_amount"`
	ExcessLower     float64   `json:"mean_daily_excess_lower"`
	ExcessUpper     float64   `json:"mean_daily_excess_upper"`
	Config          Inference `json:"config"`
}
type block struct {
	pnl, excess  float64
	trades, days int
}

func infer(ctx context.Context, r, base maexec.Result, c Inference) (Interval, error) {
	out := Interval{Config: c, Trades: len(r.Trades), Sessions: len(r.NAV)}
	if e := ctx.Err(); e != nil {
		return out, e
	}
	if !c.valid() || len(r.NAV) != len(base.NAV) {
		return out, errors.New("invalid inference configuration or paired dates")
	}
	// A final shorter block is kept and its actual denominator retained.
	blocks := make([]block, (len(r.NAV)+c.BlockSessions-1)/c.BlockSessions)
	dates := map[string]int{}
	previous, priorBase := r.Config.Capital, base.Config.Capital
	for i, n := range r.NAV {
		if n.Date != base.NAV[i].Date || previous <= 0 || priorBase <= 0 {
			return out, errors.New("invalid paired equity series")
		}
		j := i / c.BlockSessions
		dates[n.Date] = j
		blocks[j].excess += n.Equity/previous - base.NAV[i].Equity/priorBase
		blocks[j].days++
		previous = n.Equity
		priorBase = base.NAV[i].Equity
	}
	for _, tr := range r.Trades {
		j, ok := dates[tr.SignalDate]
		if !ok {
			return out, errors.New("trade signal outside inference window")
		}
		blocks[j].pnl += tr.PNL
		blocks[j].trades++
	}
	out.Blocks = len(blocks)
	for _, b := range blocks {
		if b.trades > 0 {
			out.TradeBlocks++
		}
	}
	if out.Blocks < c.MinBlocks || out.TradeBlocks < c.MinBlocks {
		return out, errors.New("insufficient nonempty time blocks")
	}
	rng := rand.New(rand.NewSource(c.Seed))
	expectancy := make([]float64, 0, c.Samples)
	excess := make([]float64, 0, c.Samples)
	for k := 0; k < c.Samples; k++ {
		if e := ctx.Err(); e != nil {
			return out, e
		}
		var sample block
		for range blocks {
			b := blocks[rng.Intn(len(blocks))]
			sample.pnl += b.pnl
			sample.excess += b.excess
			sample.trades += b.trades
			sample.days += b.days
		}
		if sample.trades == 0 {
			return out, errors.New("bootstrap sample has no closed trades")
		}
		expectancy = append(expectancy, sample.pnl/float64(sample.trades))
		excess = append(excess, sample.excess/float64(sample.days))
	}
	sort.Float64s(expectancy)
	sort.Float64s(excess)
	alpha := (1 - c.Confidence) / 2
	lower := int(math.Floor(alpha * float64(c.Samples-1)))
	upper := int(math.Ceil((1 - alpha) * float64(c.Samples-1)))
	out.ExpectancyLower, out.ExpectancyUpper = expectancy[lower], expectancy[upper]
	out.ExcessLower, out.ExcessUpper = excess[lower], excess[upper]
	return out, nil
}
