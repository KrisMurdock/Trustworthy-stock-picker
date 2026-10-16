package bottomstudy

import (
	"math"
	"sort"
)

func resolvedTrade(t Trade) bool {
	return t.Status == "target" || t.Status == "stop" || t.Status == "stop_gap" || t.Status == "timeout"
}

func tradeStats(trades []Trade, split string, baseline map[string]Trade) TradeStats {
	var s TradeStats
	dates, stocks := map[string]bool{}, map[string]bool{}
	nets := []float64{}
	baselineTargets := 0
	for _, t := range trades {
		if t.Split != split {
			continue
		}
		s.Opportunities++
		if b, ok := baseline[t.ID]; ok && b.TargetHit {
			baselineTargets++
			if t.TargetHit {
				s.RetainedTargets++
			}
		}
		if t.EntryDate == "" {
			if t.Status == "entry_skipped" {
				s.Skipped++
			} else {
				s.UnknownEntries++
			}
			continue
		}
		s.Entries++
		dates[t.SignalDate], stocks[t.Code] = true, true
		s.MeanBatches += float64(t.BatchesFilled)
		s.MeanInvested += t.Invested
		if t.EntryCloseNetPct > 0 {
			s.EntryPositive++
		}
		if t.Ambiguous {
			s.Ambiguous++
		}
		if t.BlockedByT1 {
			s.StopBlockedT1++
		}
		if t.TargetBlockedT1 {
			s.TargetBlockedT1++
		}
		if t.StopTriggered {
			s.Stops++
		}
		if !resolvedTrade(t) {
			s.Unresolved++
			continue
		}
		s.Resolved++
		if t.TargetHit {
			s.Targets++
			if t.TargetDay == 1 {
				s.NextDay++
				if t.EntryCloseNetPct > 0 {
					s.JointNextDay++
				}
			}
		}
		if t.NetPct > 0 {
			s.Positive++
		}
		if t.StopExceeded {
			s.StopExceeded++
		}
		nets = append(nets, t.NetPct)
		s.MeanNet += t.NetPct
		s.MeanNetCash += t.Invested * t.NetPct / 100
		s.MeanDays += float64(t.HoldingDays)
	}
	s.Samples, s.Dates, s.Stocks = s.Entries+s.UnknownEntries, len(dates), len(stocks)
	if s.Samples > 0 {
		s.Rate = float64(s.Targets) / float64(s.Samples)
		s.ProfitRate = float64(s.Positive) / float64(s.Samples)
	}
	if s.Entries > 0 {
		s.MeanBatches /= float64(s.Entries)
		s.MeanInvested /= float64(s.Entries)
	}
	if s.Resolved > 0 {
		s.MeanNet /= float64(s.Resolved)
		s.MeanNetCash /= float64(s.Resolved)
		s.MeanDays /= float64(s.Resolved)
		sort.Float64s(nets)
		s.WorstNet = nets[0]
		s.P05Net = nets[int(math.Floor(.05*float64(len(nets)-1)))]
		tail := max(1, int(math.Ceil(.05*float64(len(nets)))))
		for _, net := range nets[:tail] {
			s.TailMeanNet += net
		}
		s.TailMeanNet /= float64(tail)
	}
	s.LostTargets = baselineTargets - s.RetainedTargets
	if baselineTargets > 0 {
		s.Retention = float64(s.RetainedTargets) / float64(baselineTargets)
	}
	return s
}
