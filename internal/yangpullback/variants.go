package yangpullback

import (
	"fmt"
	"math"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

// ExitRule is one pre-registered execution variant. All rules keep the frozen
// pattern definition and differ only in holding period, exit thresholds or the
// entry gap cap.
type ExitRule struct {
	Name        string  `json:"name"`
	Horizon     int     `json:"horizon"`
	TakeProfit  float64 `json:"take_profit_net_pct,omitempty"`
	StopLoss    float64 `json:"stop_loss_net_pct,omitempty"`
	Trail       float64 `json:"trailing_drawdown_net_pct,omitempty"`
	EntryGapCap float64 `json:"entry_gap_cap_pct,omitempty"`
}

// DefaultVariants freezes the exploratory rule list before the holdout is read.
// The primary evaluation remains fixed_5d.
func DefaultVariants() []ExitRule {
	return []ExitRule{
		{Name: "fixed_5d", Horizon: 5},
		{Name: "fixed_3d", Horizon: 3},
		{Name: "fixed_10d", Horizon: 10},
		{Name: "tp3_5d", Horizon: 5, TakeProfit: 3},
		{Name: "sl3_5d", Horizon: 5, StopLoss: -3},
		{Name: "tp3_sl3_5d", Horizon: 5, TakeProfit: 3, StopLoss: -3},
		{Name: "tp5_sl3_10d", Horizon: 10, TakeProfit: 5, StopLoss: -3},
		{Name: "trail3_10d", Horizon: 10, Trail: 3},
		{Name: "gap_cap3_5d", Horizon: 5, EntryGapCap: 3},
	}
}

type Variant struct {
	Name        string          `json:"name"`
	Rule        ExitRule        `json:"rule"`
	Development Stats           `json:"development"`
	Holdout     Stats           `json:"holdout"`
	Control     Stats           `json:"holdout_control"`
	Purged      int             `json:"purged"`
	Bootstrap   BootstrapResult `json:"bootstrap"`
	Verdict     string          `json:"verdict"`
	Gates       []string        `json:"gate_failures"`
}

func (r ExitRule) validate() error {
	if r.Name == "" {
		return fmt.Errorf("yang-pullback variant requires a name")
	}
	if r.Horizon < 1 || r.Horizon > 10 {
		return fmt.Errorf("yang-pullback variant %s horizon must be between 1 and 10", r.Name)
	}
	if r.TakeProfit < 0 || r.TakeProfit > 20 {
		return fmt.Errorf("yang-pullback variant %s take profit must be between 0 and 20", r.Name)
	}
	if r.StopLoss > 0 || r.StopLoss < -20 {
		return fmt.Errorf("yang-pullback variant %s stop loss must be between -20 and 0", r.Name)
	}
	if r.Trail < 0 || r.Trail > 20 {
		return fmt.Errorf("yang-pullback variant %s trailing drawdown must be between 0 and 20", r.Name)
	}
	if r.EntryGapCap < 0 || r.EntryGapCap > 20 {
		return fmt.Errorf("yang-pullback variant %s entry gap cap must be between 0 and 20", r.Name)
	}
	if r.TakeProfit > 0 && r.Trail > 0 {
		return fmt.Errorf("yang-pullback variant %s cannot combine take profit and trailing exit", r.Name)
	}
	return nil
}

func variantTrades(pairs map[string]pairedHistory, signals []Signal, s config.Strategy, cal *calendar.Calendar, rule ExitRule) []Trade {
	trades := make([]Trade, 0, len(signals))
	for _, signal := range signals {
		pair, ok := pairs[signal.Code]
		if !ok {
			trades = append(trades, Trade{Signal: signal, Status: "missing_history"})
			continue
		}
		trades = append(trades, variantTrade(pair, signal, s, cal, rule))
	}
	sortTrades(trades)
	return trades
}

func variantTrade(pair pairedHistory, signal Signal, s config.Strategy, cal *calendar.Calendar, rule ExitRule) Trade {
	if err := rule.validate(); err != nil {
		return Trade{Signal: signal, Status: "invalid_rule"}
	}
	trade := Trade{Signal: signal, Status: "unresolved"}
	_, entryIndex, entry, fill, shares, buyCost, trade, ok := enterPosition(pair, signal, s, cal)
	if !ok {
		return trade
	}
	signalIndex := pair.indexByDate[signal.Date]
	if rule.EntryGapCap > 0 {
		signalClose := pair.raw.Bars[signalIndex].Close
		if signalClose > 0 && (entry.Open/signalClose-1)*100 > rule.EntryGapCap {
			trade.EntrySkipped = true
			trade.Status = "entry_gap_skipped"
			return trade
		}
	}

	takeProfit, stopPrice := variantExitPrices(s, shares, buyCost, fill, rule)
	maxHigh := entry.High
	for day := 1; day <= rule.Horizon && entryIndex+day < len(pair.raw.Bars); day++ {
		maxHigh = math.Max(maxHigh, pair.raw.Bars[entryIndex+day].High)
	}
	trade.MaxHighPct = (maxHigh/fill - 1) * 100
	trade.PriceTouch = maxHigh > fill
	mfe := 0.0
	mae := math.Min(0, fixedNetPct(s, shares, buyCost, entry.Low))
	peakCloseNet := fixedNetPct(s, shares, buyCost, fixedTickDown(entry.Close*(1-s.SlippageRate)))

	previous := entry
	for day := 1; day <= rule.Horizon; day++ {
		expected, nextErr := cal.Next(previous.Date)
		if nextErr != nil || entryIndex+day >= len(pair.raw.Bars) || pair.raw.Bars[entryIndex+day].Date != expected {
			trade.Status = "missing_exit_session"
			return trade
		}
		bar := pair.raw.Bars[entryIndex+day]
		if bar.Volume == 0 {
			trade.Status = "missing_exit_session"
			return trade
		}
		if corporateActionChanged(pair, signalIndex, entryIndex+day) {
			trade.Status = "corporate_action"
			return trade
		}
		previousClose := previous.Close
		previous = bar
		mfe = math.Max(mfe, fixedNetPct(s, shares, buyCost, bar.High))
		mae = math.Min(mae, fixedNetPct(s, shares, buyCost, bar.Low))

		if mainBoardSTLockedDown(pair.code, previousClose, bar) {
			// A possible ST one-price limit-down bar cannot be sold. Defer the
			// exit; only the horizon bar is reported as unresolved.
			if day == rule.Horizon {
				trade.Status = "limit_status_unknown"
				return trade
			}
			continue
		}
		if lockedDown(pair.code, previousClose, bar) {
			continue
		}
		limit := strategy.PriceLimitFor(pair.code, bar.Date)
		if !limit.Known {
			trade.Status = "limit_status_unknown"
			return trade
		}
		upper := math.Round(previousClose*(1+limit.Pct/100)*100) / 100
		lower := math.Round(previousClose*(1-limit.Pct/100)*100) / 100
		marketFill := func(price float64) float64 {
			return math.Max(bar.Low, fixedTickDown(price*(1-s.SlippageRate)))
		}

		targetHit := takeProfit > 0 && bar.High >= takeProfit+0.01-1e-8 && takeProfit <= upper+0.005
		stopHit := stopPrice > 0 && bar.Low <= stopPrice && stopPrice >= lower-0.005
		exitPrice, reason := 0.0, ""
		switch {
		case stopPrice > 0 && bar.Open <= stopPrice && stopPrice >= lower-0.005:
			exitPrice, reason = marketFill(bar.Open), "stop_gap"
		case takeProfit > 0 && bar.Open >= takeProfit && takeProfit <= upper+0.005:
			exitPrice, reason = math.Max(takeProfit, marketFill(bar.Open)), "target_gap"
		case stopHit:
			exitPrice, reason = marketFill(stopPrice), "stop"
			if targetHit {
				trade.Ambiguous = true
			}
		case targetHit:
			exitPrice, reason = takeProfit, "target"
		}
		if reason != "" {
			trade.EntryDate = entry.Date
			trade.ExitDate = bar.Date
			trade.HoldingDays = day
			trade.NetPct = fixedNetPct(s, shares, buyCost, exitPrice)
			trade.Resolved = true
			trade.Status = reason
			trade.TargetFirst = reason == "target" || reason == "target_gap"
			trade.StopFirst = reason == "stop" || reason == "stop_gap"
			trade.MFE = mfe
			trade.MAE = mae
			return trade
		}

		closeNet := fixedNetPct(s, shares, buyCost, marketFill(bar.Close))
		peakCloseNet = math.Max(peakCloseNet, closeNet)
		if rule.Trail > 0 && peakCloseNet-closeNet >= rule.Trail {
			trade.EntryDate = entry.Date
			trade.ExitDate = bar.Date
			trade.HoldingDays = day
			trade.NetPct = closeNet
			trade.Resolved = true
			trade.Status = "trail"
			trade.MFE = mfe
			trade.MAE = mae
			return trade
		}
		if day == rule.Horizon {
			trade.EntryDate = entry.Date
			trade.ExitDate = bar.Date
			trade.HoldingDays = day
			trade.NetPct = closeNet
			trade.Resolved = true
			trade.Status = "fixed_hold"
			trade.MFE = mfe
			trade.MAE = mae
			return trade
		}
	}
	trade.Status = "exit_locked"
	return trade
}

// variantExitPrices converts net-return goals into trigger prices using the
// same fee, slippage and tick model as the primary fixed-hold trade.
func variantExitPrices(s config.Strategy, shares int, buyCost, fill float64, rule ExitRule) (takeProfit, stopPrice float64) {
	if rule.TakeProfit > 0 {
		takeProfit = fixedTickUp(priceForNet(s, shares, buyCost, fill, rule.TakeProfit))
	}
	if rule.StopLoss < 0 {
		raw := priceForNet(s, shares, buyCost, fill, rule.StopLoss)
		stopPrice = fixedTickUp(raw / (1 - s.SlippageRate))
		for fixedNetPct(s, shares, buyCost, fixedTickDown(stopPrice*(1-s.SlippageRate))) <= rule.StopLoss+1e-9 {
			stopPrice = fixedTickUp(stopPrice + 0.01)
		}
	}
	return takeProfit, stopPrice
}

func priceForNet(s config.Strategy, shares int, buyCost, fill, goal float64) float64 {
	lo, hi := 0.01, fill*3
	for i := 0; i < 10 && fixedNetPct(s, shares, buyCost, hi) < goal; i++ {
		hi *= 2
	}
	for i := 0; i < 80; i++ {
		mid := (lo + hi) / 2
		if fixedNetPct(s, shares, buyCost, mid) >= goal {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

func appendVariants(result *Evaluation, pairsByCode map[string]pairedHistory, patternSignals, holdSignals []Signal, s config.Strategy, cal *calendar.Calendar, opts EvalOptions, boundary string, dataYears float64) error {
	result.Variants = make([]Variant, 0, len(DefaultVariants()))
	for _, rule := range DefaultVariants() {
		patternTrades := variantTrades(pairsByCode, patternSignals, s, cal, rule)
		development, holdout, purged, variantBoundary := splitTrades(patternTrades, opts.Split, cal, rule.Horizon)
		if variantBoundary != boundary {
			return fmt.Errorf("yang-pullback variant %s boundary %s does not match primary %s", rule.Name, variantBoundary, boundary)
		}
		holdoutStats := summarizeTrades(holdout, 0)
		controlTrades := tradesAtOrAfter(variantTrades(pairsByCode, holdSignals, s, cal, rule), boundary)
		control := summarizeTrades(controlTrades, 0)
		bootstrap := bootstrapComparison(holdout, controlTrades, opts.Bootstrap, opts.Seed, "hold_only", rule.Horizon+2)
		verdict, gates := evaluateVerdict(holdoutStats, control, bootstrap, dataYears)
		variant := Variant{
			Name:        rule.Name,
			Rule:        rule,
			Development: summarizeTrades(development, purged),
			Holdout:     holdoutStats,
			Control:     control,
			Purged:      purged,
			Bootstrap:   bootstrap,
			Verdict:     verdict,
			Gates:       gates,
		}
		if rule.Name == "fixed_5d" {
			if err := matchesPrimary(variant.Development, result.Development); err != nil {
				return fmt.Errorf("fixed_5d development does not match primary: %w", err)
			}
			if err := matchesPrimary(variant.Holdout, result.Holdout); err != nil {
				return fmt.Errorf("fixed_5d holdout does not match primary: %w", err)
			}
			if err := matchesPrimary(variant.Control, baselineStats(result.Baselines, "hold_only")); err != nil {
				return fmt.Errorf("fixed_5d control does not match primary hold_only: %w", err)
			}
		}
		result.Variants = append(result.Variants, variant)
	}
	return nil
}

func matchesPrimary(got, want Stats) error {
	if got.Signals != want.Signals || got.Resolved != want.Resolved || got.Wins != want.Wins {
		return fmt.Errorf("counts differ: got signals=%d resolved=%d wins=%d, want signals=%d resolved=%d wins=%d",
			got.Signals, got.Resolved, got.Wins, want.Signals, want.Resolved, want.Wins)
	}
	if math.Abs(got.MeanNet-want.MeanNet) > 1e-9 || math.Abs(got.MedianNet-want.MedianNet) > 1e-9 {
		return fmt.Errorf("returns differ: got mean=%.12f median=%.12f, want mean=%.12f median=%.12f",
			got.MeanNet, got.MedianNet, want.MeanNet, want.MedianNet)
	}
	return nil
}

// VariantCandidateNames returns names whose holdout passed every gate. The
// caller must still treat them as exploratory because the holdout has already
// been read by the primary evaluation.
func VariantCandidateNames(variants []Variant) []string {
	out := make([]string, 0)
	for _, variant := range variants {
		if variant.Verdict == verdictCandidate {
			out = append(out, variant.Name)
		}
	}
	sort.Strings(out)
	return out
}
