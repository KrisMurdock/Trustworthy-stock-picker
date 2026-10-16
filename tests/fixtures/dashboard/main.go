// Command dashboard creates deterministic, credential-free browser fixtures using
// the same SQLite transaction and schema as the production synchronizer.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"stock-watcher/internal/bottomstudy"
	"stock-watcher/internal/config"
	"stock-watcher/internal/experiment"
	"stock-watcher/internal/market"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/storage"
	"stock-watcher/internal/strategy"
	"time"
)

func main() {
	root := flag.String("data", "", "existing fixture data directory")
	empty := flag.Bool("empty", false, "initialize without published snapshots")
	partial := flag.Bool("partial", false, "save market data without a report after coverage failure")
	flag.Parse()
	if err := seed(*root, *empty, *partial); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !*empty {
		if err := seedExperiments(*root); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func seed(root string, empty, partial bool) error {
	db, err := storage.InitMarket(root, false)
	if err != nil {
		return err
	}
	defer db.Close()
	if empty {
		return nil
	}
	stocks := []config.Stock{
		{Code: "sz000001", Name: "测试银行", Sector: "银行"},
		{Code: "sh600519", Name: "测试消费", Sector: "消费"},
		{Code: "sz000002", Name: "测试缺失", Sector: "地产"},
	}
	for _, stock := range []config.Stock{stocks[0], stocks[2]} {
		if err := seedMinutes(db, stock); err != nil {
			return err
		}
	}
	for _, day := range []string{"2026-10-12", "2026-10-13"} {
		at, err := time.ParseInLocation("2006-01-02", day, market.Shanghai)
		if err != nil {
			return err
		}
		at = at.Add(19 * time.Hour)
		items := []storage.SyncItem{}
		for i, stock := range stocks[:2] {
			adjusted := history(stock, day, at, i, 80)
			raw := history(stock, day, at, i, 2700)
			if i == 1 {
				adjusted.Source, raw.Source = "baostock", "baostock"
			}
			raw.Adjust, raw.DataField = "none", "day"
			for j := range raw.Bars {
				bar := &raw.Bars[j]
				bar.Open, bar.Close, bar.High, bar.Low = bar.Open*2, bar.Close*2, bar.High*2, bar.Low*2
			}
			items = append(items, storage.SyncItem{Stock: stock, Adjusted: &adjusted, Raw: &raw, Status: "ok"})
		}
		items = append(items, storage.SyncItem{Stock: stocks[2], Status: "isolated", Reason: "missing_data"})
		report := selection.Report{
			Version: 1, ID: "dashboard-" + day, AsOf: day, Target: at.AddDate(0, 0, 1).Format("2006-01-02"),
			GeneratedAt: at, Strategy: "leader_daily_v1", SelectionEnabled: true, Adjust: "qfq",
			TopN: 5, PoolSize: 3, Eligible: 2, Passed: 1,
			Rules:    []string{"浏览器测试快照"},
			Selected: []selection.Candidate{{Code: stocks[1].Code, Name: stocks[1].Name, Sector: stocks[1].Sector, Close: 198, ChangePct: -1, Metrics: selection.Metrics{Score: 77}}},
			Excluded: []selection.Excluded{{Code: stocks[2].Code, Reason: "missing_data"}}, Filtered: []selection.Excluded{},
		}
		if day == "2026-10-13" {
			report.Version, report.Strategy = 2, "short_horizon_v1"
			report.Ranking = []strategy.Candidate{
				{Code: stocks[0].Code, Name: stocks[0].Name, Rank: 1, Score: 88.5, Recommended: true, Reasons: []string{"证据充分"}, Risks: []string{}, Rules: []string{"trend"}},
				{Code: stocks[1].Code, Name: stocks[1].Name, Rank: 2, Score: 62.5, Recommended: false, Reasons: []string{"历史样本不足"}, Risks: []string{"evidence"}, Rules: []string{}},
			}
			report.Selected = []selection.Candidate{{Code: stocks[0].Code, Name: stocks[0].Name, Sector: stocks[0].Sector, Close: 10.25, ChangePct: 2.5, Metrics: selection.Metrics{Score: 85}, Ranking: &report.Ranking[0]}}
			report.Research = &strategy.Research{Version: 1, Strategy: report.Strategy, AsOf: day, SampleCount: 120, ValidationPassed: false, Limitations: []string{"仅用于浏览器测试"}}
		}
		if partial {
			if day != "2026-10-13" {
				continue
			}
			return db.CommitMarketData(context.Background(), storage.SyncRun{ID: "partial-run", AsOf: day, StartedAt: at.Add(-time.Minute), FinishedAt: at, CoveragePct: 200.0 / 3}, items, "qfq")
		}
		if err := db.CommitSnapshot(context.Background(), storage.SyncRun{ID: "dashboard-run-" + day, AsOf: day, StartedAt: at.Add(-time.Minute), FinishedAt: at, CoveragePct: 200.0 / 3}, items, report); err != nil {
			return err
		}
	}
	return nil
}

func history(stock config.Stock, day string, at time.Time, index, count int) market.History {
	var bars []market.Bar
	for date := at; len(bars) < count; date = date.AddDate(0, 0, -1) {
		if date.Weekday() == time.Saturday || date.Weekday() == time.Sunday {
			continue
		}
		close := 10.0
		if index == 1 {
			close = 200
		}
		if len(bars) == 0 {
			if index == 0 {
				close = 10.25
			} else {
				close = 198
			}
		}
		bars = append(bars, market.Bar{Date: date.Format("2006-01-02"), Open: close * (0.99 + .02*float64(index)), Close: close, High: close * 1.01, Low: close * .99, Volume: float64((index + 1) * 1000)})
	}
	for left, right := 0, len(bars)-1; left < right; left, right = left+1, right-1 {
		bars[left], bars[right] = bars[right], bars[left]
	}
	return market.History{Version: 1, Code: stock.Code, Name: stock.Name, Source: "tencent", Adjust: "qfq", DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: day, FetchedAt: at, Bars: bars}
}

func seedExperiments(root string) error {
	ctx := context.Background()
	db, err := storage.OpenExperiments(ctx, root, false, true)
	if err != nil {
		return err
	}
	defer db.Close()
	c := config.Defaults()
	c.Calendar.ValidFrom = "2025-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试银行", Sector: "银行"}, {Code: "sh600519", Name: "测试消费", Sector: "消费"}, {Code: "sz000002", Name: "测试缺失", Sector: "地产"}}
	at, _ := time.Parse("2006-01-02", "2026-10-13")
	adjusted, raw := []market.History{}, []market.History{}
	for index, stock := range c.Stocks[:2] {
		h := history(stock, "2026-10-13", at, index, 240)
		for i := range h.Bars {
			p := 20 + float64(i)*.04 + .2*math.Sin(float64(i)*.4)
			b := &h.Bars[i]
			b.Open = p * .995
			b.Close = p
			b.Low = p * .99
			b.High = p * 1.003
			if index == 0 {
				b.High = p * 1.03
			}
		}
		r := h
		r.Adjust, r.DataField = "none", "day"
		r.Bars = append([]market.Bar(nil), h.Bars...)
		for i := range r.Bars {
			b := &r.Bars[i]
			b.Open *= 2
			b.High *= 2
			b.Low *= 2
			b.Close *= 2
		}
		adjusted = append(adjusted, h)
		raw = append(raw, r)
	}
	snapshot, err := experiment.NewSnapshot(c, "2026-10-13", adjusted, raw)
	if err != nil {
		return err
	}
	var parent *experiment.Record
	for _, policy := range []string{"baseline", "upper-wick", "next-day-baseline"} {
		spec, _ := experiment.Specification(policy)
		r, err := experiment.NewRecord(snapshot, spec, parent, "fixture-v1")
		if err != nil {
			return err
		}
		if err = db.Start(ctx, snapshot, r); err != nil {
			return err
		}
		if err = experiment.Evaluate(ctx, snapshot, &r, parent); err != nil {
			return err
		}
		if err = db.Finish(ctx, r); err != nil {
			return err
		}
		parent = &r
	}
	minutes := []market.MinuteHistory{}
	for _, h := range raw {
		m := market.MinuteHistory{Version: 1, Code: h.Code, Name: h.Name, Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: at.Add(16 * time.Hour)}
		for _, b := range h.Bars[len(h.Bars)-13:] {
			day, _ := time.ParseInLocation("2006-01-02", b.Date, market.Shanghai)
			for i := 0; i < 48; i++ {
				minute := 575 + i*5
				if i >= 24 {
					minute = 785 + (i-24)*5
				}
				bar := market.MinuteBar{End: day.Add(time.Duration(minute) * time.Minute), Open: b.Open, Close: b.Open, High: b.Open, Low: b.Open, Volume: 100, Final: true}
				if i == 0 {
					bar.Low = b.Low
					bar.High = b.High
					bar.Close = b.Low + .75*(b.Open-b.Low)
				}
				if i == 1 {
					bar.Open = b.Low + .75*(b.Open-b.Low)
					bar.Low = bar.Open
				}
				if i == 47 {
					bar.Close = b.Close
					bar.High = max(bar.High, b.Close)
					bar.Low = min(bar.Low, b.Close)
				}
				m.Bars = append(m.Bars, bar)
			}
		}
		minutes = append(minutes, m)
	}
	snapshot, err = experiment.WithMinutes(snapshot, minutes)
	if err != nil {
		return err
	}
	spec, _ := experiment.Specification("conditional-entry")
	spec.Range = &strategy.BacktestRange{From: raw[0].Bars[len(raw[0].Bars)-14].Date, Through: raw[0].Bars[len(raw[0].Bars)-5].Date}
	r, err := experiment.NewRecord(snapshot, spec, nil, "fixture-v1")
	if err != nil {
		return err
	}
	if err = db.Start(ctx, snapshot, r); err != nil {
		return err
	}
	if err = experiment.Evaluate(ctx, snapshot, &r, nil); err != nil {
		return err
	}
	if err = db.Finish(ctx, r); err != nil {
		return err
	}
	return seedResultSnapshot(ctx, db, snapshot)
}

func seedResultSnapshot(ctx context.Context, db *storage.ExperimentDB, data experiment.Snapshot) error {
	spec := experiment.Spec{Title: "测试无止损 · 持仓时间结果", Direction: "验证结果快照", Change: "两组固定参数对照", Criteria: "训练选优，后段复查", Range: &strategy.BacktestRange{From: "2026-01-01", Through: "2026-10-12"}}
	r, err := experiment.NewRecord(data, spec, nil, "fixture-v1")
	if err != nil {
		return err
	}
	r.Engine = bottomstudy.SpeedEngine
	if err = db.Start(ctx, data, r); err != nil {
		return err
	}
	s := bottomstudy.SpeedStats{ComparisonStats: bottomstudy.ComparisonStats{TradeStats: bottomstudy.TradeStats{Samples: 19, Entries: 19, Resolved: 19, Targets: 18, Rate: 18.0 / 19, MeanDays: 7.37, MeanNet: 2.55, WorstNet: -8.21}}, SuccessDays: 6.67, PenalizedDays: 7.42}
	p := bottomstudy.SpeedPair{ID: "fixture-pair", Policy: bottomstudy.TradePolicy{Rule: "macd_contraction", Batches: 2, Horizon: 20, Target: 3}, Confirmation: "none", Stock: bottomstudy.SpeedArm{Training: s, Later: s}, Combined: bottomstudy.SpeedArm{Training: s, Later: s}}
	report := bottomstudy.SpeedReport{Pairs: []bottomstudy.SpeedPair{p}, Winners: []bottomstudy.SpeedWinner{{ID: p.ID}}}
	now := time.Now().UTC()
	r.FinishedAt, r.Status, r.Verdict = &now, "completed", "research"
	r.ResultSnapshot = bottomstudy.SpeedResultSnapshot(report, r)
	return db.Finish(ctx, r)
}

func seedMinutes(db *storage.MarketDB, stock config.Stock) error {
	at := time.Date(2026, 10, 13, 15, 10, 0, 0, market.Shanghai)
	h := market.MinuteHistory{Version: 1, Code: stock.Code, Name: stock.Name, Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: at}
	for day := at.AddDate(0, 0, -11); !day.After(at); day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		for _, start := range []int{570, 780} {
			for n := start + 5; n <= start+120; n += 5 {
				end := time.Date(day.Year(), day.Month(), day.Day(), n/60, n%60, 0, 0, market.Shanghai)
				h.Bars = append(h.Bars, market.MinuteBar{End: end, Open: 10, High: 12, Low: 9, Close: 11, Volume: 100, Final: true})
			}
		}
	}
	if err := db.SaveMinutes(context.Background(), stock, h.Bars[len(h.Bars)-1].End, at, &h, "ok", "", true); err != nil {
		return err
	}
	h.Bars = nil
	h.FetchedAt = time.Date(2026, 10, 14, 10, 1, 0, 0, market.Shanghai)
	for n := 575; n <= 605; n += 5 {
		end := time.Date(2026, 10, 14, n/60, n%60, 0, 0, market.Shanghai)
		h.Bars = append(h.Bars, market.MinuteBar{End: end, Open: 10, High: 12, Low: 9, Close: 11.5, Volume: 200, Final: end.Before(h.FetchedAt)})
	}
	return db.SaveMinutes(context.Background(), stock, h.Bars[len(h.Bars)-1].End, h.FetchedAt, &h, "ok", "", false)
}
