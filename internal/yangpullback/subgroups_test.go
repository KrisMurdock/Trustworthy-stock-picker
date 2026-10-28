package yangpullback

import (
	"math"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestPoolBreadthRequiresFiftyStocksAndUsesPriorBars(t *testing.T) {
	dates := testWeekdays(21)
	pairs := make([]pairedHistory, 0, 50)
	for stock := 0; stock < 50; stock++ {
		bars := make([]market.Bar, 0, len(dates))
		for index, date := range dates {
			close := 10.0
			if index == len(dates)-1 {
				if stock < 25 {
					close = 11
				} else {
					close = 9
				}
			}
			bars = append(bars, market.Bar{Date: date, Open: close, Close: close, High: close, Low: close, Volume: 1000})
		}
		pairs = append(pairs, pairedHistory{adjusted: market.History{Bars: bars}})
	}
	breadth := poolBreadth(pairs)
	if got := breadth[dates[len(dates)-1]]; math.Abs(got-0.5) > 1e-12 {
		t.Fatalf("breadth = %.4f, want 0.5", got)
	}
	if _, ok := breadth[dates[18]]; ok {
		t.Fatal("breadth was produced before 20 prior bars exist")
	}
}

func TestSubgroupFeaturesUseAnchorBodyTrendAndBreadth(t *testing.T) {
	dates := testWeekdays(61)
	bars := make([]market.Bar, 0, len(dates))
	for index, date := range dates {
		close := 10.0
		if index == 60 {
			close = 12
		}
		bars = append(bars, market.Bar{Date: date, Open: 10, Close: close, High: close, Low: 9.9, Volume: 1000})
	}
	pair := testPair("sz000001", bars)
	pair.adjusted.Bars[5].Open = 10
	pair.adjusted.Bars[5].Close = 10.5
	signal := Signal{
		Code:            "sz000001",
		Date:            dates[60],
		AnchorDate:      dates[5],
		PullbackDays:    3,
		PhaseConfidence: "high",
	}
	features := subgroupFeaturesFor(map[string]pairedHistory{"sz000001": pair}, signal, map[string]float64{dates[60]: 0.65})
	if features.pullbackDays != 3 || features.phaseConfidence != "high" {
		t.Fatalf("basic features = %+v", features)
	}
	if math.Abs(features.anchorBodyPct-5) > 1e-9 {
		t.Fatalf("anchor body = %.6f, want 5", features.anchorBodyPct)
	}
	if math.Abs(features.trend60Pct-20) > 1e-9 {
		t.Fatalf("60d return = %.6f, want 20", features.trend60Pct)
	}
	if math.Abs(features.breadthPct-65) > 1e-9 {
		t.Fatalf("breadth = %.6f, want 65", features.breadthPct)
	}
}

func TestSelectSubgroupUsesDevelopmentThenConfirmsOnHoldout(t *testing.T) {
	subgroups := []Subgroup{
		{Name: "eligible_but_slower", Development: subgroupStats(200, 0.52, 0.5, 0.2, 1.4), DevelopmentControl: subgroupControl(200, 0.50), Holdout: subgroupStats(200, 0.53, 0.5, 0.2, 1.4), HoldoutControl: subgroupControl(200, 0.51)},
		{Name: "best_development", Development: subgroupStats(200, 0.56, 0.8, 0.3, 1.5), DevelopmentControl: subgroupControl(200, 0.52), Holdout: subgroupStats(200, 0.54, 0.7, 0.2, 1.4), HoldoutControl: subgroupControl(200, 0.52)},
		{Name: "high_win_negative_mean", Development: subgroupStats(200, 0.61, -0.1, -0.2, 0.9), DevelopmentControl: subgroupControl(200, 0.50), Holdout: subgroupStats(200, 0.60, -0.1, -0.2, 0.9), HoldoutControl: subgroupControl(200, 0.50)},
	}
	name, confirmed := selectSubgroup(subgroups)
	if name != "best_development" || !confirmed {
		t.Fatalf("selection = %q confirmed=%v", name, confirmed)
	}
	if !subgroups[1].Selected || subgroups[0].Selected || subgroups[2].Selected {
		t.Fatalf("selected flags = %+v", subgroups)
	}

	subgroups[1].Holdout = subgroupStats(200, 0.45, 0.1, 0.1, 1.3)
	subgroups[1].HoldoutControl = subgroupControl(200, 0.50)
	name, confirmed = selectSubgroup(subgroups)
	if name != "best_development" || confirmed {
		t.Fatalf("unstable holdout = %q confirmed=%v", name, confirmed)
	}

	empty := []Subgroup{{Name: "too_few", Development: subgroupStats(10, 0.9, 1, 1, 2), DevelopmentControl: subgroupControl(10, 0.5), Holdout: subgroupStats(10, 0.9, 1, 1, 2), HoldoutControl: subgroupControl(10, 0.5)}}
	if name, confirmed := selectSubgroup(empty); name != "" || confirmed {
		t.Fatalf("empty selection = %q confirmed=%v", name, confirmed)
	}
}

func subgroupControl(resolved int, winRate float64) Stats {
	return Stats{Signals: resolved, Resolved: resolved, WinRateResolved: winRate, MeanNet: 0.5, MedianNet: 0.1, ProfitFactor: func() *float64 { value := 1.3; return &value }()}
}

func subgroupStats(resolved int, winRate, mean, median, pf float64) Stats {
	return Stats{
		Signals:         resolved,
		Resolved:        resolved,
		WinRateResolved: winRate,
		MeanNet:         mean,
		MedianNet:       median,
		ProfitFactor:    &pf,
	}
}
