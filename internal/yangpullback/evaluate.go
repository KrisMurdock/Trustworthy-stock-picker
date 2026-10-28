package yangpullback

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const (
	evaluationWinRateGap = 0.10
	evaluationMinSignals = 100
	evaluationMinDates   = 30
	evaluationMinStocks  = 20
	evaluationMinPF      = 1.2
	evaluationMaxStock   = 0.20
	evaluationMaxYear    = 0.40

	verdictInsufficient = "insufficient"
	verdictRejected     = "rejected"
	verdictCandidate    = "candidate"
)

// EvalOptions freezes the Phase 0 execution and validation choices.
type EvalOptions struct {
	Options   Options
	Horizon   int
	Split     float64
	Bootstrap int
	Seed      int64
}

func DefaultEvalOptions() EvalOptions {
	return EvalOptions{
		Options:   DefaultOptions(),
		Horizon:   5,
		Split:     0.70,
		Bootstrap: 1000,
		Seed:      1,
	}
}

type Baseline struct {
	Name  string `json:"name"`
	Stats Stats  `json:"stats"`
}

type Sensitivity struct {
	Name  string `json:"name"`
	Stats Stats  `json:"stats"`
}

type Evaluation struct {
	Version     int            `json:"schema_version"`
	Rule        string         `json:"rule"`
	AsOf        string         `json:"as_of"`
	Definition  map[string]any `json:"definition"`
	Data        map[string]any `json:"data"`
	Development Stats          `json:"development"`
	Holdout     Stats          `json:"holdout"`
	Baselines   []Baseline     `json:"baselines"`
	Sensitivity []Sensitivity  `json:"sensitivity"`
	Variants    []Variant      `json:"variants"`
	Subgroups   []Subgroup     `json:"subgroups"`
	Robustness  Robustness     `json:"robustness"`
	Purged      int            `json:"purged"`
	Verdict     string         `json:"verdict"`
	Limitations []string       `json:"limitations"`
}

type pairedHistory struct {
	code        string
	adjusted    market.History
	raw         market.History
	indexByDate map[string]int
}

// Evaluate runs the frozen research definition without network or storage
// access. Adjusted bars drive detection; raw bars drive execution.
func Evaluate(c config.Config, cal *calendar.Calendar, adjusted, raw []market.History, asof string, opts EvalOptions) (Evaluation, error) {
	normalized, err := normalizeEvalOptions(opts)
	if err != nil {
		return Evaluation{}, err
	}
	if cal == nil {
		return Evaluation{}, errors.New("yang-pullback evaluation requires a trading calendar")
	}
	if _, err := time.Parse("2006-01-02", asof); err != nil {
		return Evaluation{}, errors.New("yang-pullback evaluation requires a valid as-of date")
	}
	covered, err := cal.Covers(asof, asof)
	if err != nil || !covered {
		return Evaluation{}, errors.New("as-of date is outside the frozen exchange calendar")
	}

	pairs, skipped := alignHistories(adjusted, raw, asof)
	if len(pairs) == 0 {
		return Evaluation{}, errors.New("no adjusted/raw histories share the requested as-of date")
	}
	pairsByCode := make(map[string]pairedHistory, len(pairs))
	for _, pair := range pairs {
		pairsByCode[pair.code] = pair
	}

	scanOptions := normalized.Options
	scanOptions.AsOf = asof
	patternSignals, err := scanAll(pairs, scanOptions, ModePattern)
	if err != nil {
		return Evaluation{}, err
	}
	patternTrades := fixedHoldTrades(pairsByCode, patternSignals, c.Strategy, cal, normalized.Horizon)
	patternTrades = attachDiagnostics(patternTrades, pairsByCode, cal, c.Strategy)
	dataFrom, dataThrough := historyRange(pairs)

	development, holdout, purged, boundary := splitTrades(patternTrades, normalized.Split, cal, normalized.Horizon)
	result := Evaluation{
		Version:     Version,
		Rule:        Rule,
		AsOf:        asof,
		Definition:  evaluationDefinition(normalized, scanOptions, c.Strategy),
		Development: summarizeTrades(development, purged),
		Holdout:     summarizeTrades(holdout, 0),
		Purged:      purged,
		Verdict:     verdictInsufficient,
	}

	baselineTrades := map[string][]Trade{}
	controlTrades := map[Mode][]Trade{}
	controlSignals := map[Mode][]Signal{}
	for _, mode := range []Mode{ModePullbackOnly, ModeHoldOnly} {
		signals, scanErr := scanAll(pairs, scanOptions, mode)
		if scanErr != nil {
			return Evaluation{}, scanErr
		}
		controlSignals[mode] = signals
		trades := fixedHoldTrades(pairsByCode, signals, c.Strategy, cal, normalized.Horizon)
		controlTrades[mode] = trades
		baselineTrades[string(mode)] = tradesAtOrAfter(trades, boundary)
	}
	pool := poolSignals(pairs)
	baselineTrades["pool_all"] = tradesAtOrAfter(fixedHoldTrades(pairsByCode, pool, c.Strategy, cal, normalized.Horizon), boundary)
	for _, name := range []string{"pool_all", "pullback_only", "hold_only"} {
		result.Baselines = append(result.Baselines, Baseline{Name: name, Stats: summarizeTrades(baselineTrades[name], 0)})
	}

	if err := appendSensitivity(&result, pairs, pairsByCode, patternSignals, scanOptions, c.Strategy, cal, normalized); err != nil {
		return Evaluation{}, err
	}
	bootstrap := bootstrapComparison(holdout, baselineTrades["hold_only"], normalized.Bootstrap, normalized.Seed, "hold_only", normalized.Horizon+2)

	baselineScope := "holdout"
	if boundary == "" {
		baselineScope = "all_signals_no_pattern_split"
	}
	holdOnly := baselineStats(result.Baselines, "hold_only")
	result.Data = map[string]any{
		"adjusted_histories":              len(adjusted),
		"raw_histories":                   len(raw),
		"aligned_stocks":                  len(pairs),
		"skipped_stale_histories":         skipped,
		"data_from":                       dataFrom,
		"data_through":                    dataThrough,
		"data_years":                      yearSpan(dataFrom, dataThrough),
		"pattern_signals":                 len(patternSignals),
		"pattern_signal_dates":            uniqueSignalDates(patternSignals),
		"split_date":                      boundary,
		"baseline_scope":                  baselineScope,
		"baseline_pool_all_signals":       len(baselineTrades["pool_all"]),
		"baseline_pullback_signals":       len(baselineTrades["pullback_only"]),
		"baseline_hold_only_signals":      len(baselineTrades["hold_only"]),
		"holdout_control":                 "hold_only",
		"holdout_control_win_rate":        holdOnly.WinRate,
		"independent_holdout_gap_vs_hold": result.Holdout.WinRate - holdOnly.WinRate,
		"control_signal_dates": map[string]int{
			"pullback_only": uniqueSignalDates(controlSignals[ModePullbackOnly]),
			"hold_only":     uniqueSignalDates(controlSignals[ModeHoldOnly]),
		},
		"control_comparison_paired": false,
		"control_comparison_method": "independent_cohorts_descriptive_only",
		"control_timing":            "pullback_only/hold_only signal on H+2; pattern signals on the confirmation day (H+2..H+5)",
		"strategy_parameters":       strategyParameters(c.Strategy),
		"bootstrap_block_length":    normalized.Horizon + 2,
		"purge_horizon_sessions":    normalized.Horizon + 1,
		"secondary_outcome":         fmt.Sprintf("strategy.Simulate horizon=%d, diagnostic only", normalized.Horizon),
		"bootstrap":                 bootstrap,
	}
	result.Verdict, result.Data["gate_failures"] = evaluateVerdict(result.Holdout, holdOnly, bootstrap, yearSpan(dataFrom, dataThrough))
	if err := appendVariants(&result, pairsByCode, patternSignals, controlSignals[ModeHoldOnly], c.Strategy, cal, normalized, boundary, yearSpan(dataFrom, dataThrough)); err != nil {
		return Evaluation{}, err
	}
	result.Data["variant_count"] = len(result.Variants)
	result.Data["variant_candidates"] = VariantCandidateNames(result.Variants)
	result.Data["variant_scope"] = "pre_registered_exit_rules; exploratory because the primary evaluation already read this holdout"
	result.Data["variant_control"] = "hold_only with the same exit rule"
	appendSubgroups(&result, pairs, pairsByCode, patternTrades, controlTrades[ModeHoldOnly], cal, normalized, boundary)
	appendRobustness(&result, pairsByCode, patternSignals, controlSignals[ModeHoldOnly], patternTrades, controlTrades[ModeHoldOnly], c.Strategy, cal, normalized)
	result.Limitations = []string{
		"Phase 0 research only; this is not a production recommendation, a probability of success, or a return promise.",
		"Primary win means fixed-horizon net return is strictly positive; unresolved and skipped entries count as non-wins.",
		"Adjusted prices are used only for pattern detection; raw execution returns do not model dividends or corporate-action cash flows.",
		"Target-first, stop-first, MFE, and MAE come from strategy.Simulate and do not change the primary win definition.",
		"Historical ST status is unavailable. Main-board bars locked at the 5% band are marked limit_status_unknown and treated as unresolved/non-wins; their fills are not modeled as known ST or normal-limit executions.",
		"pullback_only and hold_only signal at H+2 while pattern signals at confirmation. The control cohorts are independent, not paired, and their contrast is descriptive only.",
		"The moving-block bootstrap resamples signal-date clusters with horizon+2 sessions, so overlapping fixed-horizon labels are not treated as independent observations.",
		"pool_all contains overlapping fixed-hold observations and is descriptive only.",
		"Execution variants are pre-registered and differ only in holding period, exit thresholds or entry gap cap; the primary fixed_5d verdict is unchanged.",
		"Variant results reuse the same holdout that the primary evaluation already read, so they are exploratory and cannot serve as a fresh out-of-sample confirmation.",
		"Subgroups are seven pre-registered single-condition filters; one is selected on the development period and the holdout result is exploratory because that holdout was already read.",
		"Calendar-year and 1..10 day horizon tables are descriptive robustness views over the same data; overlapping trades are not independent observations.",
	}
	return result, nil
}

func normalizeEvalOptions(in EvalOptions) (EvalOptions, error) {
	defaults := DefaultEvalOptions()
	if in.Options.HighWindow == 0 && in.Options.PullbackMin == 0 && in.Options.PullbackMax == 0 && in.Options.PhaseWindow == 0 && in.Options.AsOf == "" && !in.Options.RequirePhase {
		in.Options = defaults.Options
	} else {
		normalized, err := in.Options.Normalize()
		if err != nil {
			return EvalOptions{}, err
		}
		in.Options = normalized
	}
	if in.Horizon == 0 {
		in.Horizon = defaults.Horizon
	}
	if in.Split == 0 {
		in.Split = defaults.Split
	}
	if in.Bootstrap == 0 {
		in.Bootstrap = defaults.Bootstrap
	}
	if in.Seed == 0 {
		in.Seed = defaults.Seed
	}
	if in.Horizon < 1 {
		return EvalOptions{}, errors.New("yang-pullback horizon must be positive")
	}
	if math.IsNaN(in.Split) || math.IsInf(in.Split, 0) || in.Split <= 0 || in.Split >= 1 {
		return EvalOptions{}, errors.New("yang-pullback split must be between zero and one")
	}
	if in.Bootstrap < 0 {
		return EvalOptions{}, errors.New("yang-pullback bootstrap iterations cannot be negative")
	}
	in.Options.AsOf = ""
	return in, nil
}

func alignHistories(adjusted, raw []market.History, asof string) ([]pairedHistory, int) {
	rawByCode := make(map[string]market.History, len(raw))
	for _, history := range raw {
		if history.Code != "" {
			rawByCode[history.Code] = history
		}
	}
	adjustedByCode := make(map[string]market.History, len(adjusted))
	for _, history := range adjusted {
		if history.Code != "" {
			adjustedByCode[history.Code] = history
		}
	}
	codes := make([]string, 0, len(adjustedByCode))
	for code := range adjustedByCode {
		if _, ok := rawByCode[code]; ok {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)

	out := make([]pairedHistory, 0, len(codes))
	skipped := 0
	for _, code := range codes {
		adjustedHistory := adjustedByCode[code]
		rawHistory := rawByCode[code]
		adjustedBars := barsByDate(adjustedHistory.Bars, asof)
		rawBars := barsByDate(rawHistory.Bars, asof)
		commonDates := make([]string, 0, min(len(adjustedBars), len(rawBars)))
		for date := range adjustedBars {
			if _, ok := rawBars[date]; ok {
				commonDates = append(commonDates, date)
			}
		}
		sort.Strings(commonDates)
		if len(commonDates) == 0 || commonDates[len(commonDates)-1] != asof {
			skipped++
			continue
		}

		alignedAdjusted := make([]market.Bar, 0, len(commonDates))
		alignedRaw := make([]market.Bar, 0, len(commonDates))
		indexMap := make(map[string]int, len(commonDates))
		for index, date := range commonDates {
			alignedAdjusted = append(alignedAdjusted, adjustedBars[date])
			alignedRaw = append(alignedRaw, rawBars[date])
			indexMap[date] = index
		}
		adjustedHistory.Bars = alignedAdjusted
		adjustedHistory.AsOf = asof
		rawHistory.Bars = alignedRaw
		rawHistory.AsOf = asof
		out = append(out, pairedHistory{
			code:        code,
			adjusted:    adjustedHistory,
			raw:         rawHistory,
			indexByDate: indexMap,
		})
	}
	return out, skipped
}

func barsByDate(bars []market.Bar, asof string) map[string]market.Bar {
	out := make(map[string]market.Bar, len(bars))
	for _, bar := range bars {
		if bar.Date != "" && bar.Date <= asof {
			out[bar.Date] = bar
		}
	}
	return out
}

func scanAll(pairs []pairedHistory, options Options, mode Mode) ([]Signal, error) {
	signals := make([]Signal, 0)
	for _, pair := range pairs {
		if len(pair.adjusted.Bars) < 60 {
			continue
		}
		found, err := Scan(pair.adjusted, options, mode)
		if err != nil {
			return nil, fmt.Errorf("scan %s in %s mode: %w", pair.code, mode, err)
		}
		signals = append(signals, found...)
	}
	sortSignals(signals)
	return signals, nil
}

func sortSignals(signals []Signal) {
	sort.Slice(signals, func(i, j int) bool {
		if signals[i].Date != signals[j].Date {
			return signals[i].Date < signals[j].Date
		}
		return signals[i].Code < signals[j].Code
	})
}

func sortTrades(trades []Trade) {
	sort.Slice(trades, func(i, j int) bool {
		if trades[i].Signal.Date != trades[j].Signal.Date {
			return trades[i].Signal.Date < trades[j].Signal.Date
		}
		return trades[i].Signal.Code < trades[j].Signal.Code
	})
}

func fixedHoldTrades(pairs map[string]pairedHistory, signals []Signal, s config.Strategy, cal *calendar.Calendar, horizon int) []Trade {
	trades := make([]Trade, 0, len(signals))
	for _, signal := range signals {
		pair, ok := pairs[signal.Code]
		if !ok {
			trades = append(trades, Trade{Signal: signal, Status: "missing_history"})
			continue
		}
		trades = append(trades, fixedHoldTrade(pair, signal, s, cal, horizon))
	}
	sortTrades(trades)
	return trades
}

func fixedHoldTrade(pair pairedHistory, signal Signal, s config.Strategy, cal *calendar.Calendar, horizon int) Trade {
	signalIndex, entryIndex, entry, fill, shares, buyCost, trade, ok := enterPosition(pair, signal, s, cal)
	if !ok {
		return trade
	}

	previousDate := entry.Date
	for day := 1; day <= horizon; day++ {
		expected, nextErr := cal.Next(previousDate)
		if nextErr != nil || entryIndex+day >= len(pair.raw.Bars) || pair.raw.Bars[entryIndex+day].Date != expected {
			trade.Status = "missing_exit_session"
			return trade
		}
		previousDate = expected
	}
	exit := pair.raw.Bars[entryIndex+horizon]
	if exit.Volume == 0 {
		trade.Status = "missing_exit_session"
		return trade
	}
	previousExit := pair.raw.Bars[entryIndex+horizon-1]
	if mainBoardSTLockedDown(pair.code, previousExit.Close, exit) {
		trade.Status = "limit_status_unknown"
		return trade
	}
	if lockedDown(pair.code, previousExit.Close, exit) {
		trade.Status = "exit_locked"
		return trade
	}
	if corporateActionChanged(pair, signalIndex, entryIndex+horizon) {
		trade.Status = "corporate_action"
		return trade
	}
	sale := math.Max(exit.Low, fixedTickDown(exit.Close*(1-s.SlippageRate)))
	trade.EntryDate = entry.Date
	trade.ExitDate = exit.Date
	trade.HoldingDays = horizon
	trade.NetPct = fixedNetPct(s, shares, buyCost, sale)
	high := entry.High
	mfe := 0.0
	mae := math.Min(0, fixedNetPct(s, shares, buyCost, entry.Low))
	for day := 1; day <= horizon; day++ {
		bar := pair.raw.Bars[entryIndex+day]
		high = math.Max(high, bar.High)
		mfe = math.Max(mfe, fixedNetPct(s, shares, buyCost, bar.High))
		mae = math.Min(mae, fixedNetPct(s, shares, buyCost, bar.Low))
	}
	trade.MFE = mfe
	trade.MAE = mae
	trade.MaxHighPct = (high/fill - 1) * 100
	trade.PriceTouch = high > fill
	trade.Resolved = true
	trade.Status = "fixed_hold"
	return trade
}

func enterPosition(pair pairedHistory, signal Signal, s config.Strategy, cal *calendar.Calendar) (signalIndex, entryIndex int, entry market.Bar, fill float64, shares int, buyCost float64, trade Trade, ok bool) {
	trade = Trade{Signal: signal, Status: "unresolved"}
	signalIndex, ok = pair.indexByDate[signal.Date]
	if !ok {
		trade.Status = "missing_signal_session"
		return signalIndex, entryIndex, entry, fill, shares, buyCost, trade, false
	}
	entryIndex = signalIndex + 1
	expectedEntry, err := cal.Next(signal.Date)
	if err != nil || entryIndex >= len(pair.raw.Bars) || pair.raw.Bars[entryIndex].Date != expectedEntry {
		trade.Status = "missing_entry_session"
		return signalIndex, entryIndex, entry, fill, shares, buyCost, trade, false
	}
	entry = pair.raw.Bars[entryIndex]
	previous := pair.raw.Bars[signalIndex]
	if entry.Volume == 0 {
		trade.EntrySkipped = true
		trade.Status = "entry_skipped"
		return signalIndex, entryIndex, entry, fill, shares, buyCost, trade, false
	}
	if mainBoardSTLockedUp(pair.code, previous.Close, entry) {
		trade.EntrySkipped = true
		trade.Status = "limit_status_unknown"
		return signalIndex, entryIndex, entry, fill, shares, buyCost, trade, false
	}
	if entry.High-entry.Low < 0.005 {
		trade.EntrySkipped = true
		trade.Status = "entry_skipped"
		return signalIndex, entryIndex, entry, fill, shares, buyCost, trade, false
	}
	if lockedUp(pair.code, previous.Close, entry) {
		trade.EntrySkipped = true
		trade.Status = "entry_skipped"
		return signalIndex, entryIndex, entry, fill, shares, buyCost, trade, false
	}
	fill = fixedTickUp(entry.Open * (1 + s.SlippageRate))
	if fill <= 0 || fill > entry.High+0.005 {
		trade.EntrySkipped = true
		trade.Status = "entry_skipped"
		return signalIndex, entryIndex, entry, fill, shares, buyCost, trade, false
	}
	shares, buyCost, ok = fixedBuy(s, pair.code, fill)
	if !ok {
		trade.EntrySkipped = true
		trade.Status = "cash_insufficient"
		return signalIndex, entryIndex, entry, fill, shares, buyCost, trade, false
	}
	return signalIndex, entryIndex, entry, fill, shares, buyCost, trade, true
}

func lockedUp(code string, previousClose float64, bar market.Bar) bool {
	rule := strategy.PriceLimitFor(code, bar.Date)
	if !rule.Known || previousClose <= 0 {
		return false
	}
	limit := math.Round(previousClose*(1+rule.Pct/100)*100) / 100
	return bar.Low >= limit-0.005 && bar.Close >= limit-0.005
}

func lockedDown(code string, previousClose float64, bar market.Bar) bool {
	rule := strategy.PriceLimitFor(code, bar.Date)
	if !rule.Known || previousClose <= 0 {
		return false
	}
	limit := math.Round(previousClose*(1-rule.Pct/100)*100) / 100
	return bar.High <= limit+0.005 && bar.Close <= limit+0.005
}

func mainBoardSTLockedUp(code string, previousClose float64, bar market.Bar) bool {
	if !mainBoardCode(code) || previousClose <= 0 {
		return false
	}
	limit := math.Round(previousClose*1.05*100) / 100
	return bar.Low >= limit-0.005 && bar.High <= limit+0.005 && bar.Close <= limit+0.005
}

func mainBoardSTLockedDown(code string, previousClose float64, bar market.Bar) bool {
	if !mainBoardCode(code) || previousClose <= 0 {
		return false
	}
	limit := math.Round(previousClose*0.95*100) / 100
	return bar.High <= limit+0.005 && bar.Low >= limit-0.005 && bar.Close >= limit-0.005
}

func mainBoardCode(code string) bool {
	return strings.HasPrefix(code, "sh60") || strings.HasPrefix(code, "sz00")
}

func corporateActionChanged(pair pairedHistory, signalIndex, exitIndex int) bool {
	if signalIndex < 0 || exitIndex >= len(pair.adjusted.Bars) || exitIndex >= len(pair.raw.Bars) {
		return true
	}
	signalFactor := pair.adjusted.Bars[signalIndex].Close / pair.raw.Bars[signalIndex].Close
	for index := signalIndex + 1; index <= exitIndex; index++ {
		if pair.raw.Bars[index].Close <= 0 {
			return true
		}
		factor := pair.adjusted.Bars[index].Close / pair.raw.Bars[index].Close
		if math.Abs(factor/signalFactor-1) > 0.002 {
			return true
		}
	}
	return false
}

func fixedBuy(s config.Strategy, code string, fill float64) (int, float64, bool) {
	lot, minimum := 100, 100
	if strings.HasPrefix(code, "sh68") {
		lot, minimum = 1, 200
	}
	if fill <= 0 || s.TradeCash <= 0 {
		return 0, 0, false
	}
	shares := int(s.TradeCash/fill) / lot * lot
	for shares >= minimum {
		cost := fixedBuyCost(s, fill, shares)
		if cost <= s.TradeCash+1e-9 {
			return shares, cost, true
		}
		shares -= lot
	}
	return 0, 0, false
}

func fixedBuyCost(s config.Strategy, fill float64, shares int) float64 {
	amount := fill * float64(shares)
	return amount + fixedFee(amount, s.CommissionRate, s.MinCommission) + fixedFee(amount, s.TransferRate, 0)
}

func fixedNetPct(s config.Strategy, shares int, buyCost, sale float64) float64 {
	amount := sale * float64(shares)
	proceeds := amount - fixedFee(amount, s.CommissionRate, s.MinCommission) - fixedFee(amount, s.TransferRate, 0) - fixedFee(amount, s.SellStampRate, 0)
	if buyCost <= 0 {
		return 0
	}
	return (proceeds/buyCost - 1) * 100
}

func fixedFee(amount, rate, minimum float64) float64 {
	return math.Ceil((math.Max(amount*rate, minimum)-1e-9)*100) / 100
}

func fixedTickUp(value float64) float64 {
	return math.Ceil(value*100-1e-8) / 100
}

func fixedTickDown(value float64) float64 {
	return math.Floor(value*100+1e-8) / 100
}

func attachDiagnostics(trades []Trade, pairs map[string]pairedHistory, cal *calendar.Calendar, s config.Strategy) []Trade {
	for index := range trades {
		pair, ok := pairs[trades[index].Signal.Code]
		if !ok {
			continue
		}
		signalIndex, ok := pair.indexByDate[trades[index].Signal.Date]
		if !ok {
			continue
		}
		parameters := s
		if parameters.MaxHoldingDays < 5 {
			parameters.MaxHoldingDays = 5
		}
		outcome, err := strategy.Simulate(parameters, cal, pair.code, pair.raw.Bars[signalIndex], pair.raw.Bars[signalIndex+1:], 5)
		if err != nil {
			continue
		}
		trades[index].Ambiguous = outcome.Ambiguous
		if !outcome.Ambiguous {
			switch outcome.Status {
			case "target":
				trades[index].TargetFirst = true
			case "stop", "stop_gap":
				trades[index].StopFirst = true
			}
		}
	}
	return trades
}

func poolSignals(pairs []pairedHistory) []Signal {
	signals := make([]Signal, 0)
	for _, pair := range pairs {
		for index := 59; index < len(pair.adjusted.Bars); index++ {
			signals = append(signals, Signal{
				Version: Version,
				Rule:    Rule,
				Code:    pair.code,
				Date:    pair.adjusted.Bars[index].Date,
			})
		}
	}
	sortSignals(signals)
	return signals
}

func appendSensitivity(result *Evaluation, pairs []pairedHistory, pairsByCode map[string]pairedHistory, patternSignals []Signal, scanOptions Options, s config.Strategy, cal *calendar.Calendar, opts EvalOptions) error {
	highOptions := scanOptions
	highOptions.HighWindow = 20
	highSignals, err := scanAll(pairs, highOptions, ModePattern)
	if err != nil {
		return err
	}
	result.Sensitivity = append(result.Sensitivity, Sensitivity{
		Name:  "pattern_high_window_20",
		Stats: summarizeTrades(fixedHoldTrades(pairsByCode, highSignals, s, cal, opts.Horizon), 0),
	})

	maOptions := scanOptions
	maOptions.RequirePhase = false
	maSignals, err := scanAll(pairs, maOptions, ModePattern)
	if err != nil {
		return err
	}
	trendSignals := filterMA20Trend(maSignals, pairsByCode)
	result.Sensitivity = append(result.Sensitivity, Sensitivity{
		Name:  "pattern_ma20_trend",
		Stats: summarizeTrades(fixedHoldTrades(pairsByCode, trendSignals, s, cal, opts.Horizon), 0),
	})
	for _, horizon := range []int{1, 3, 10} {
		result.Sensitivity = append(result.Sensitivity, Sensitivity{
			Name:  fmt.Sprintf("fixed_hold_%d", horizon),
			Stats: summarizeTrades(fixedHoldTrades(pairsByCode, patternSignals, s, cal, horizon), 0),
		})
	}
	return nil
}

func filterMA20Trend(signals []Signal, pairs map[string]pairedHistory) []Signal {
	out := make([]Signal, 0, len(signals))
	for _, signal := range signals {
		pair, ok := pairs[signal.Code]
		if !ok {
			continue
		}
		index, ok := pair.indexByDate[signal.Date]
		if !ok || index < 20 {
			continue
		}
		current := meanClose(pair.adjusted.Bars[index-19 : index+1])
		previous := meanClose(pair.adjusted.Bars[index-20 : index])
		if pair.adjusted.Bars[index].Close > current && current > previous {
			out = append(out, signal)
		}
	}
	return out
}

func meanClose(bars []market.Bar) float64 {
	if len(bars) == 0 {
		return 0
	}
	sum := 0.0
	for _, bar := range bars {
		sum += bar.Close
	}
	return sum / float64(len(bars))
}

func splitTrades(trades []Trade, split float64, cal *calendar.Calendar, horizon int) (development, holdout []Trade, purged int, boundary string) {
	dates := uniqueTradeDates(trades)
	if len(dates) == 0 {
		return nil, nil, 0, ""
	}
	index := int(math.Floor(float64(len(dates)) * split))
	if index < 0 {
		index = 0
	}
	if index >= len(dates) {
		index = len(dates) - 1
	}
	boundary = dates[index]
	development, holdout, purged = splitAtBoundary(trades, boundary, cal, horizon)
	return development, holdout, purged, boundary
}

func splitAtBoundary(trades []Trade, boundary string, cal *calendar.Calendar, horizon int) (development, holdout []Trade, purged int) {
	for _, trade := range trades {
		date := trade.Signal.Date
		if date == "" {
			continue
		}
		if date < boundary {
			labelEnd, ok := expectedLabelEnd(cal, date, horizon)
			if !ok || labelEnd >= boundary {
				purged++
				continue
			}
			development = append(development, trade)
		} else {
			holdout = append(holdout, trade)
		}
	}
	return development, holdout, purged
}

func expectedLabelEnd(cal *calendar.Calendar, signalDate string, horizon int) (string, bool) {
	if cal == nil || horizon < 1 {
		return "", false
	}
	day, err := cal.Next(signalDate)
	if err != nil {
		return "", false
	}
	for held := 0; held < horizon; held++ {
		day, err = cal.Next(day)
		if err != nil {
			return "", false
		}
	}
	return day, true
}

func uniqueTradeDates(trades []Trade) []string {
	set := map[string]bool{}
	for _, trade := range trades {
		if trade.Signal.Date != "" {
			set[trade.Signal.Date] = true
		}
	}
	dates := make([]string, 0, len(set))
	for date := range set {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	return dates
}

func uniqueSignalDates(signals []Signal) int {
	set := map[string]bool{}
	for _, signal := range signals {
		if signal.Date != "" {
			set[signal.Date] = true
		}
	}
	return len(set)
}

func historyRange(pairs []pairedHistory) (string, string) {
	from, through := "", ""
	for _, pair := range pairs {
		if len(pair.adjusted.Bars) == 0 {
			continue
		}
		first := pair.adjusted.Bars[0].Date
		last := pair.adjusted.Bars[len(pair.adjusted.Bars)-1].Date
		if from == "" || first < from {
			from = first
		}
		if through == "" || last > through {
			through = last
		}
	}
	return from, through
}

func yearSpan(from, through string) float64 {
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return 0
	}
	end, err := time.Parse("2006-01-02", through)
	if err != nil || end.Before(start) {
		return 0
	}
	return end.Sub(start).Hours() / 24 / 365.25
}

func tradesAtOrAfter(trades []Trade, boundary string) []Trade {
	if boundary == "" {
		return trades
	}
	out := make([]Trade, 0, len(trades))
	for _, trade := range trades {
		if trade.Signal.Date >= boundary {
			out = append(out, trade)
		}
	}
	return out
}

func baselineStats(baselines []Baseline, name string) Stats {
	for _, baseline := range baselines {
		if baseline.Name == name {
			return baseline.Stats
		}
	}
	return Stats{ByYear: map[string]int{}}
}

func evaluationDefinition(opts EvalOptions, scanOptions Options, s config.Strategy) map[string]any {
	definition := map[string]any{
		"status":                    "phase0_research",
		"pattern_mode":              string(ModePattern),
		"high_window":               scanOptions.HighWindow,
		"pullback_min":              scanOptions.PullbackMin,
		"pullback_max":              scanOptions.PullbackMax,
		"phase_window":              scanOptions.PhaseWindow,
		"require_phase":             scanOptions.RequirePhase,
		"horizon_trading_days":      opts.Horizon,
		"entry":                     "next trading-day open with adverse slippage",
		"exit":                      "fixed-horizon close with adverse slippage",
		"win":                       "net return strictly greater than zero",
		"costs":                     "commission, minimum commission, transfer fee, sell stamp duty, slippage",
		"development_split":         opts.Split,
		"purge_horizon_sessions":    opts.Horizon + 1,
		"bootstrap_iterations":      opts.Bootstrap,
		"bootstrap_seed":            opts.Seed,
		"bootstrap_method":          "independent_signal_date_moving_block_bootstrap",
		"bootstrap_block_length":    opts.Horizon + 2,
		"control_comparison_paired": false,
		"control_comparison":        "independent cohorts; descriptive only; pattern and controls are not paired",
		"control_timing":            "pullback_only/hold_only signal on H+2; pattern signals on the confirmation day (H+2..H+5)",
		"verdict_scope":             "holdout",
		"secondary_diagnostics":     "strategy.Simulate",
		"production_recommendation": false,
	}
	for key, value := range strategyParameters(s) {
		definition[key] = value
	}
	return definition
}

func strategyParameters(s config.Strategy) map[string]any {
	return map[string]any{
		"cash":                 s.TradeCash,
		"commission_rate":      s.CommissionRate,
		"min_commission":       s.MinCommission,
		"transfer_fee_rate":    s.TransferRate,
		"sell_stamp_duty_rate": s.SellStampRate,
		"slippage_rate":        s.SlippageRate,
		"max_holding_days":     s.MaxHoldingDays,
	}
}

func evaluateVerdict(holdout, baseline Stats, bootstrap BootstrapResult, dataYears float64) (string, []string) {
	failures := []string{}
	if dataYears < 2 {
		failures = append(failures, "data_window_under_two_years")
	}
	if holdout.Signals < evaluationMinSignals {
		failures = append(failures, "holdout_signals_below_minimum")
	}
	if holdout.Dates < evaluationMinDates {
		failures = append(failures, "holdout_dates_below_minimum")
	}
	if holdout.Stocks < evaluationMinStocks {
		failures = append(failures, "holdout_stocks_below_minimum")
	}
	if !bootstrap.Available || !bootstrap.BaselineAvailable || !bootstrap.DifferenceAvailable {
		failures = append(failures, "bootstrap_unavailable")
	}
	if baseline.Signals == 0 {
		failures = append(failures, "baseline_empty")
	}
	if holdout.WinRateResolved+1e-12 < baseline.WinRateResolved+evaluationWinRateGap {
		failures = append(failures, "win_rate_gap_below_10pp")
	}
	if bootstrap.DifferenceLower <= 0 {
		failures = append(failures, "independent_control_difference_not_positive")
	}
	if holdout.MeanNet <= 0 || holdout.MedianNet <= 0 {
		failures = append(failures, "net_return_not_positive")
	}
	if holdout.ProfitFactor != nil && *holdout.ProfitFactor < evaluationMinPF {
		failures = append(failures, "profit_factor_below_1_2")
	}
	if holdout.MaxStockShare > evaluationMaxStock {
		failures = append(failures, "stock_concentration_above_20pct")
	}
	if dataYears >= 2 && holdout.MaxYearShare > evaluationMaxYear {
		failures = append(failures, "year_concentration_above_40pct")
	}
	if len(failures) == 0 {
		return verdictCandidate, failures
	}
	if dataYears < 2 {
		return verdictInsufficient, failures
	}
	return verdictRejected, failures
}
