// Command dashboard creates deterministic, credential-free browser fixtures using
// the same SQLite transaction and schema as the production synchronizer.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"stock-watcher/internal/config"
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
