package lowvaluation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	defaultWorkers          = 4
	maxWorkers              = 8
	minCoveragePriced       = 100
	minValuationCoveragePct = 50.0
)

type QuoteSource interface {
	LatestQuotes(context.Context, string) ([]Quote, error)
}

type FundamentalSource interface {
	FetchFundamentals(context.Context, config.Stock) (market.FundamentalSnapshot, error)
}

type Options struct {
	AsOf     string
	Target   string
	TopN     int
	Workers  int
	Codes    []string
	Sectors  map[string]string
	Now      func() time.Time
	Progress func(done, total int)
}

type stockEvaluation struct {
	index     int
	candidate *Candidate
	valued    bool
	reason    string
	fatal     error
}

// Scan fetches one TTM snapshot per priced stock and retains only stocks whose
// close is strictly below the conservative lower bound. The ranking is stable:
// smaller (Lower - Close) / Lower first, then higher central upside, then code.
func Scan(ctx context.Context, quotes QuoteSource, fundamentals FundamentalSource, options Options) (Recommendation, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	rec := Recommendation{
		Version: SchemaVersion, Market: Market, AsOf: options.AsOf, Target: options.Target,
		GeneratedAt: now().In(market.Shanghai), TopN: options.TopN,
		Candidates: []Candidate{}, Filtered: []Filtered{},
		Limitations: []string{
			"全市场范围沿用沪深 A 股股票池，排除 ST、退市整理、北交所和 B 股；这不是交易所全部证券。",
			"区间来自 TTM EPS 与每股净资产的相对估值，是财务研究区间，不是目标价或收益预测。",
			"只保留现价低于保守下沿的股票；距下沿折价越小越靠前，代表价格越接近区间。",
			"估值低估不等于短期上涨；停牌、退市风险、周期高点利润和会计口径差异仍需单独核查。",
			"行业快照只用于金融/非金融估值边界，不用于构造历史同业分位。",
		},
	}
	if err := validateOptions(rec.AsOf, rec.Target, rec.TopN, options.Workers); err != nil {
		return rec, err
	}
	if quotes == nil || fundamentals == nil {
		return rec, errors.New("low valuation dependencies are unavailable")
	}
	rec.ID = fmt.Sprintf("low_valuation-%s-%d", rec.AsOf, rec.TopN)
	allQuotes, err := quotes.LatestQuotes(ctx, rec.AsOf)
	if err != nil {
		return rec, err
	}
	sort.Slice(allQuotes, func(i, j int) bool { return allQuotes[i].Code < allQuotes[j].Code })
	rec.UniverseTotal = len(allQuotes)
	allowed := make(map[string]bool, len(options.Codes))
	for _, code := range options.Codes {
		allowed[code] = true
	}
	filtered := map[string]int{}
	priced := make([]Quote, 0, len(allQuotes))
	for _, quote := range allQuotes {
		if len(allowed) > 0 && !allowed[quote.Code] {
			continue
		}
		if quote.Code == "" || quote.Close <= 0 || quote.PriceDate != rec.AsOf {
			filtered["stale_or_suspended"]++
			continue
		}
		if quote.Volume <= 0 {
			filtered["no_trading_volume"]++
			continue
		}
		priced = append(priced, quote)
	}
	rec.Priced = len(priced)
	sectorKnown := 0
	for _, quote := range priced {
		if strings.TrimSpace(options.Sectors[quote.Code]) != "" {
			sectorKnown++
		}
	}
	if rec.Priced > 0 {
		rec.SectorCoveragePct = float64(sectorKnown) / float64(rec.Priced) * 100
	}
	if len(priced) == 0 {
		rec.Filtered = filteredSlice(filtered)
		return rec, errors.New("low valuation has no fresh priced stocks")
	}

	workers := options.Workers
	if workers <= 0 {
		workers = defaultWorkers
	}
	jobs := make(chan int)
	results := make(chan stockEvaluation, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				quote := priced[index]
				stock := config.Stock{Code: quote.Code, Name: quote.Name, Sector: options.Sectors[quote.Code]}
				snapshot, err := fundamentals.FetchFundamentals(ctx, stock)
				if err != nil {
					results <- stockEvaluation{index: index, reason: "fundamentals_error", fatal: ctx.Err()}
					continue
				}
				estimate := fairvalue.Calculate(stock, quote.Close, rec.AsOf, snapshot)
				if !estimate.Available() {
					results <- stockEvaluation{index: index, reason: "valuation_unavailable"}
					continue
				}
				if quote.Close >= estimate.Lower {
					results <- stockEvaluation{index: index, valued: true, reason: "not_below_lower"}
					continue
				}
				gap := (estimate.Lower - quote.Close) / estimate.Lower * 100
				upside := (estimate.Central - quote.Close) / quote.Close * 100
				candidate := Candidate{
					Code: quote.Code, Name: quote.Name, Sector: stock.Sector, PriceDate: quote.PriceDate,
					Close: quote.Close, GapToLowerPct: gap, UpsideToCentralPct: upside, Estimate: estimate,
				}
				results <- stockEvaluation{index: index, candidate: &candidate, valued: true}
			}
		}()
	}
	go func() {
		for index := range priced {
			jobs <- index
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	candidates := make([]Candidate, 0, len(priced))
	valued := 0
	completed := 0
	for evaluation := range results {
		if evaluation.fatal != nil {
			// Drain remaining workers before returning the cancellation.
			for range results {
			}
			return rec, evaluation.fatal
		}
		if evaluation.valued {
			valued++
		}
		completed++
		if options.Progress != nil && (completed%100 == 0 || completed == len(priced)) {
			options.Progress(completed, len(priced))
		}
		if evaluation.reason != "" {
			filtered[evaluation.reason]++
			continue
		}
		if evaluation.candidate == nil {
			continue
		}
		candidates = append(candidates, *evaluation.candidate)
	}
	if err := ctx.Err(); err != nil {
		return rec, err
	}
	rec.Valued = valued
	if rec.Priced >= minCoveragePriced && float64(rec.Valued)*100 < float64(rec.Priced)*minValuationCoveragePct {
		rec.Filtered = filteredSlice(filtered)
		return rec, fmt.Errorf("low valuation fundamentals coverage %.1f%% is below %.0f%%",
			float64(rec.Valued)/float64(rec.Priced)*100, minValuationCoveragePct)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].GapToLowerPct != candidates[j].GapToLowerPct {
			return candidates[i].GapToLowerPct < candidates[j].GapToLowerPct
		}
		if candidates[i].UpsideToCentralPct != candidates[j].UpsideToCentralPct {
			return candidates[i].UpsideToCentralPct > candidates[j].UpsideToCentralPct
		}
		return candidates[i].Code < candidates[j].Code
	})
	rec.Eligible = len(candidates)
	for index := range candidates {
		candidates[index].Rank = index + 1
	}
	rec.AllCandidates = append([]Candidate{}, candidates...)
	if len(candidates) > rec.TopN {
		candidates = candidates[:rec.TopN]
	}
	rec.Candidates = candidates
	rec.Selected = len(candidates)
	rec.Filtered = filteredSlice(filtered)
	return rec, nil
}

func validateOptions(asOf, target string, topN, workers int) error {
	first, err := time.Parse("2006-01-02", asOf)
	if err != nil {
		return errors.New("invalid low valuation as-of date")
	}
	next, err := time.Parse("2006-01-02", target)
	if err != nil || !next.After(first) {
		return errors.New("invalid low valuation target date")
	}
	if topN < 1 || topN > 50 {
		return errors.New("low valuation top_n must be between 1 and 50")
	}
	if workers < 0 || workers > maxWorkers {
		return fmt.Errorf("low valuation workers must be between 1 and %d", maxWorkers)
	}
	return nil
}

func filteredSlice(values map[string]int) []Filtered {
	out := make([]Filtered, 0, len(values))
	for reason, count := range values {
		if count > 0 {
			out = append(out, Filtered{Reason: reason, Count: count})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reason < out[j].Reason })
	return out
}
