// Command dashboard creates deterministic, credential-free browser fixtures using
// the same SQLite transaction and schema as the production synchronizer.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/bottomstudy"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"math"
	"os"
	"time"
)

func main() {
	root := flag.String("data", "", "existing fixture data directory")
	empty := flag.Bool("empty", false, "initialize without published snapshots")
	partial := flag.Bool("partial", false, "save market data without a report after coverage failure")
	gates := flag.Bool("gates", false, "seed rejected strategy rules and an empty recommendation holdout")
	flag.Parse()
	if err := seed(*root, *empty, *partial, *gates); err != nil {
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

func seed(root string, empty, partial, gates bool) error {
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
		{Code: "sz000004", Name: "测试停牌", Sector: "运输"},
		{Code: "sz000005", Name: "测试缺口", Sector: "工业"},
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
		items = append(items, storage.SyncItem{Stock: stocks[3], Status: "isolated", Reason: "complete_not_analyzable", Sessions: 80, Suspended: 1, Complete: true})
		gapAdjusted, gapRaw := history(stocks[4], day, at, 0, 80), history(stocks[4], day, at, 0, 2700)
		gapRaw.Adjust, gapRaw.DataField = "none", "day"
		items = append(items, storage.SyncItem{Stock: stocks[4], Adjusted: &gapAdjusted, Raw: &gapRaw, Status: "ok", RawStatus: "ok", Sessions: 80, Gaps: 1, Analyzable: true, RawAnalyzable: true})
		report := selection.Report{
			Version: 1, ID: "dashboard-" + day, AsOf: day, Target: at.AddDate(0, 0, 1).Format("2006-01-02"),
			GeneratedAt: at, Strategy: "leader_daily_v1", SelectionEnabled: true, Adjust: "qfq",
			TopN: 5, PoolSize: 5, Eligible: 3, Passed: 1,
			Rules:    []string{"浏览器测试快照"},
			Selected: []selection.Candidate{{Code: stocks[1].Code, Name: stocks[1].Name, Sector: stocks[1].Sector, Close: 198, ChangePct: -1, Metrics: selection.Metrics{Score: 77}}},
			Excluded: []selection.Excluded{{Code: stocks[2].Code, Reason: "missing_data"}}, Filtered: []selection.Excluded{},
		}
		if day == "2026-10-13" {
			report.Version, report.Strategy = 2, "short_horizon_v1"
			report.Ranking = []strategy.Candidate{
				{Code: stocks[0].Code, Name: stocks[0].Name, Rank: 1, Score: 88.5, Recommended: true, Reasons: []string{"证据充分"}, Risks: []string{}, Rules: []string{"trend"}, Continuation: &strategy.ContinuationStats{Version: 1, Label: strategy.ContinuationLabel, Rule: "momentum", Samples: 120, Dates: 40, Hits: 66, Rate: .55, LowerBound: .46, Score: 46, Status: "ready"}},
				{Code: stocks[1].Code, Name: stocks[1].Name, Rank: 2, Score: 62.5, Recommended: false, Reasons: []string{"历史样本不足"}, Risks: []string{"evidence"}, Rules: []string{}, Continuation: &strategy.ContinuationStats{Version: 1, Label: strategy.ContinuationLabel, Rule: "baseline", Status: "not_applicable"}},
			}
			report.Selected = []selection.Candidate{{Code: stocks[0].Code, Name: stocks[0].Name, Sector: stocks[0].Sector, Close: 10.25, ChangePct: 2.5, Metrics: selection.Metrics{Score: 85}, Ranking: &report.Ranking[0]}}
			report.Research = &strategy.Research{Version: 1, Strategy: report.Strategy, AsOf: day, SampleCount: 120, ValidationPassed: false, Limitations: []string{"仅用于浏览器测试"}}
			if gates {
				report.Passed, report.Selected = 0, nil
				report.Ranking[0].Recommended = false
				report.Ranking[0].Rules = []string{"baseline", "momentum"}
				report.Ranking[0].Reasons = []string{"rising_ma20"}
				report.Ranking[0].Risks = []string{"historical_net_loss_limit_breached", "insufficient_or_unprofitable_evidence", "holdout_validation_not_passed"}
				report.Ranking[1].Rules = []string{"baseline"}
				report.Ranking[1].Reasons = []string{"rising_ma20"}
				report.Ranking[1].Risks = []string{"insufficient_or_unprofitable_evidence", "holdout_validation_not_passed"}
				report.Research.Parameters = config.DefaultStrategy()
				report.Research.Holdout = strategy.Evaluation{Method: "frozen_holdout", From: "2026-08-06", Through: "2026-10-08", SignalDates: 40}
				report.Research.Rules = []strategy.Stats{
					{Rule: "baseline", Target: 3, Horizon: 5, HitRate: .458},
					{Rule: "momentum", Target: 3, Horizon: 5, Samples: 1095, Dates: 154, Resolved: 1041, Unresolved: 54, HitRate: .474, MeanNet: .36, WorstNet: -15.3766, LossBreaches: 209, LossLimitPct: 3},
				}
			}
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
	at, _ := time.ParseInLocation("2006-01-02", "2026-10-13", market.Shanghai)
	at = at.Add(19 * time.Hour)
	suspended := stocks[3]
	if err := db.SaveStatusWindow(context.Background(), suspended, market.StatusWindow{
		Code: suspended.Code, Start: "2026-10-13", End: "2026-10-13", Source: "baostock", FetchedAt: at, Full: true,
		Statuses: []market.SessionStatus{{Code: suspended.Code, Date: "2026-10-13", State: market.SessionSuspended, Source: "baostock", FetchedAt: at}},
	}); err != nil {
		return err
	}
	return seedMarketContext(root)
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
	contrast, _ := experiment.Specification("conditional-guard")
	contrast.Range = spec.Range
	child, err := experiment.NewRecord(snapshot, contrast, &r, "fixture-v1")
	if err != nil {
		return err
	}
	if err = db.Start(ctx, snapshot, child); err != nil {
		return err
	}
	if err = experiment.Evaluate(ctx, snapshot, &child, &r); err != nil {
		return err
	}
	if err = db.Finish(ctx, child); err != nil {
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

func seedMarketContext(root string) error {
	db, err := storage.InitContext(root, false)
	if err != nil {
		return err
	}
	defer db.Close()
	items := []storage.ContextInstrument{
		{Code: "sh000001", Name: "上证指数", Kind: "index", Category: "index", Source: "tencent"},
		{Code: "sz399001", Name: "深证成指", Kind: "index", Category: "index", Source: "tencent"},
		{Code: "sz399006", Name: "创业板指", Kind: "index", Category: "index", Source: "tencent"},
		{Code: "sh000300", Name: "沪深300", Kind: "index", Category: "index", Source: "tencent"},
		{Code: "sh000688", Name: "科创50", Kind: "index", Category: "index", Source: "tencent"},
		{Code: "BK1036", Name: "半导体", Kind: "board", Category: "industry,concept", Source: "eastmoney"},
		{Code: "BK1137", Name: "存储芯片", Kind: "board", Category: "concept", Source: "eastmoney"},
		{Code: "BK1152", Name: "高带宽内存", Kind: "board", Category: "concept", Source: "eastmoney"},
	}
	at := time.Date(2026, 10, 13, 19, 0, 0, 0, market.Shanghai)
	for index, item := range items {
		bars := []storage.ContextBar{}
		close := 1000.0 + float64(index)*137
		for day := at.AddDate(0, 0, -30); !day.After(at); day = day.AddDate(0, 0, 1) {
			if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
				continue
			}
			close *= 1 + 0.004*math.Sin(float64(index+len(bars)))
			bars = append(bars, storage.ContextBar{Code: item.Code, Date: day.Format("2006-01-02"), Open: close * .995, Close: close, High: close * 1.01, Low: close * .99, Volume: 1000 + float64(index*100), Amount: close * 1000})
		}
		item.UpdatedAt = at
		if err := db.SaveInstrument(context.Background(), item, bars); err != nil {
			return err
		}
	}
	return db.SaveSyncStatus(context.Background(), storage.ContextSyncStatus{AsOf: "2026-10-13", StartedAt: at.Add(-time.Minute), FinishedAt: at, Status: "complete", Total: len(items), Succeeded: len(items)})
}
