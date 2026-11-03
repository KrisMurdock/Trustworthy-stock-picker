package valuetrapped

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/lowvaluation"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/trappedscan"
)

type Options struct {
	TopN               int
	MaxTrappedRatio    float64
	MinHistorySessions int
	MinAmountCNY       float64
	Now                func() time.Time
}

// Build intersects the complete low-valuation candidate set with the trapped
// observations returned for the same codes and as-of date. It never fills an
// empty intersection from either side.
func Build(low lowvaluation.Recommendation, trapped trappedscan.Result, options Options) (Recommendation, error) {
	if low.Version != lowvaluation.SchemaVersion || low.Market != lowvaluation.Market || low.ID == "" {
		return Recommendation{}, errors.New("invalid low valuation recommendation identity")
	}
	if _, err := time.Parse("2006-01-02", low.AsOf); err != nil {
		return Recommendation{}, errors.New("invalid value-trapped as-of date")
	}
	target, err := time.Parse("2006-01-02", low.Target)
	if err != nil || target.Format("2006-01-02") <= low.AsOf {
		return Recommendation{}, errors.New("invalid value-trapped target date")
	}
	if trapped.AsOf != low.AsOf {
		return Recommendation{}, errors.New("low valuation and trapped snapshot dates differ")
	}
	if options.TopN <= 0 {
		options.TopN = DefaultTopN
	}
	if options.TopN < 1 || options.TopN > 50 {
		return Recommendation{}, errors.New("value-trapped top_n must be between 1 and 50")
	}
	if options.MaxTrappedRatio == 0 {
		options.MaxTrappedRatio = DefaultMaxTrappedRatio
	}
	if math.IsNaN(options.MaxTrappedRatio) || math.IsInf(options.MaxTrappedRatio, 0) || options.MaxTrappedRatio <= 0 || options.MaxTrappedRatio > 1 {
		return Recommendation{}, errors.New("value-trapped max ratio must be between 0 and 1")
	}
	if options.MinHistorySessions <= 0 {
		options.MinHistorySessions = DefaultMinHistorySessions
	}
	if options.MinHistorySessions < strategy.TrappedLookbackSessions {
		return Recommendation{}, fmt.Errorf("value-trapped minimum history must be at least %d", strategy.TrappedLookbackSessions)
	}
	if options.MinAmountCNY < 0 {
		return Recommendation{}, errors.New("value-trapped minimum amount must not be negative")
	}
	if options.MinAmountCNY == 0 {
		options.MinAmountCNY = DefaultMinAmountCNY
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}

	lowCandidates := low.AllCandidates
	if lowCandidates == nil {
		lowCandidates = low.Candidates
	}
	trappedByCode := make(map[string]trappedscan.Candidate, len(trapped.Top))
	for _, candidate := range trapped.Top {
		if candidate.Trapped != nil {
			trappedByCode[candidate.Code] = candidate
		}
	}

	filtered := map[string]int{}
	for _, item := range low.Filtered {
		filtered[item.Reason] += item.Count
	}
	for reason, count := range trapped.Excluded {
		filtered[reason] += count
	}
	candidates := make([]Candidate, 0, len(lowCandidates))
	for _, lowCandidate := range lowCandidates {
		trappedCandidate, ok := trappedByCode[lowCandidate.Code]
		if !ok {
			continue
		}
		signal := trappedCandidate.Trapped
		if signal == nil || signal.Status == strategy.TrappedStatusUnknown {
			continue
		}
		if signal.TrappedVolumeRatio > options.MaxTrappedRatio+1e-12 {
			filtered["trapped_ratio_above_limit"]++
			continue
		}
		if lowCandidate.Close <= 0 || lowCandidate.Close >= lowCandidate.Estimate.Lower || lowCandidate.PriceDate != low.AsOf {
			filtered["low_valuation_inconsistent"]++
			continue
		}
		candidates = append(candidates, Candidate{
			Code: lowCandidate.Code, Name: lowCandidate.Name, Sector: lowCandidate.Sector, PriceDate: lowCandidate.PriceDate,
			Close: lowCandidate.Close, GapToLowerPct: lowCandidate.GapToLowerPct, UpsideToCentralPct: lowCandidate.UpsideToCentralPct,
			Estimate: lowCandidate.Estimate, AvgAmount20CNY: trappedCandidate.AvgAmount20CNY, Trapped: signal,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].GapToLowerPct != candidates[j].GapToLowerPct {
			return candidates[i].GapToLowerPct < candidates[j].GapToLowerPct
		}
		if candidates[i].Trapped.TrappedVolumeRatio != candidates[j].Trapped.TrappedVolumeRatio {
			return candidates[i].Trapped.TrappedVolumeRatio < candidates[j].Trapped.TrappedVolumeRatio
		}
		if math.Abs(candidates[i].AvgAmount20CNY-candidates[j].AvgAmount20CNY) > 1 {
			return candidates[i].AvgAmount20CNY > candidates[j].AvgAmount20CNY
		}
		return candidates[i].Code < candidates[j].Code
	})
	rec := Recommendation{
		Version: SchemaVersion, ID: fmt.Sprintf("%s-%s-%d", Market, low.AsOf, options.TopN), Market: Market,
		AsOf: low.AsOf, Target: low.Target, GeneratedAt: now().In(time.FixedZone("Asia/Shanghai", 8*60*60)),
		UniverseTotal: low.UniverseTotal, Priced: low.Priced, Valued: low.Valued,
		LowValuationEligible: low.Eligible, TrappedValid: trapped.ValidSignals, Eligible: len(candidates),
		TopN: options.TopN, MaxTrappedRatioPct: options.MaxTrappedRatio * 100,
		MinHistorySessions: options.MinHistorySessions, MinAmountCNY: options.MinAmountCNY,
		SectorCoveragePct: low.SectorCoveragePct, TrappedUnknownReasons: copyCounts(trapped.UnknownReasons),
		Candidates: []Candidate{}, Filtered: filteredSlice(filtered),
		Limitations: append([]string{}, low.Limitations...),
	}
	if len(candidates) > options.TopN {
		candidates = candidates[:options.TopN]
	}
	for index := range candidates {
		candidates[index].Rank = index + 1
	}
	rec.Candidates = candidates
	rec.Selected = len(candidates)
	rec.Limitations = append(rec.Limitations,
		"低估值与低套牢的交集只是数据观察，不代表上涨、收益或成功概率。",
		"套牢量由不复权成交量和换手率推算，不等同于真实持仓成本；流通股本校验只是代理口径。",
		fmt.Sprintf("只纳入套牢信号有效且上方套牢量不超过 %.0f%% 的股票；unknown、历史不足 %d 日或 20 日成交额低于 %.0f 万元时剔除。", options.MaxTrappedRatio*100, options.MinHistorySessions, options.MinAmountCNY/10_000),
	)
	return rec, nil
}

func copyCounts(values map[string]int) map[string]int {
	out := make(map[string]int, len(values))
	for key, value := range values {
		if value > 0 {
			out[key] = value
		}
	}
	return out
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
