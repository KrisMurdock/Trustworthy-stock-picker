// Package selection ports the existing trend rubric. It is opt-in, not an approved strategy.
package selection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"stock-watcher/internal/config"
	"stock-watcher/internal/indicators"
	"stock-watcher/internal/market"
	"stock-watcher/internal/strategy"
	"strings"
	"time"
)

type Metrics = indicators.Metrics
type Candidate struct {
	Code      string              `json:"code"`
	Name      string              `json:"name"`
	Sector    string              `json:"sector,omitempty"`
	Close     float64             `json:"close"`
	ChangePct float64             `json:"change_pct"`
	Metrics   Metrics             `json:"metrics"`
	Ranking   *strategy.Candidate `json:"ranking,omitempty"`
}
type Excluded struct {
	Code     string `json:"code"`
	Reason   string `json:"reason"`
	LastDate string `json:"last_date,omitempty"`
}
type Report struct {
	Version          int                  `json:"schema_version"`
	ID               string               `json:"id"`
	AsOf             string               `json:"as_of"`
	Target           string               `json:"target_trading_date"`
	GeneratedAt      time.Time            `json:"generated_at"`
	Strategy         string               `json:"strategy"`
	SelectionEnabled bool                 `json:"selection_enabled"`
	Adjust           string               `json:"adjust"`
	MinScore         int                  `json:"min_score"`
	TopN             int                  `json:"top_n"`
	PoolSize         int                  `json:"pool_size"`
	Eligible         int                  `json:"eligible"`
	Passed           int                  `json:"passed"`
	Rules            []string             `json:"rules"`
	Selected         []Candidate          `json:"selected"`
	Excluded         []Excluded           `json:"excluded"`
	Filtered         []Excluded           `json:"filtered"`
	Ranking          []strategy.Candidate `json:"ranking,omitempty"`
	Research         *strategy.Research   `json:"research,omitempty"`
}

func Score(bars []market.Bar) (Metrics, error) { return indicators.Score(bars) }

func Build(c config.Config, histories []market.History, asof, target string, now time.Time) (Report, error) {
	if c.Strategy.Enabled {
		return Report{}, errors.New("short-horizon strategy requires raw execution histories")
	}
	r := Report{Version: 1, AsOf: asof, Target: target, GeneratedAt: now.In(market.Shanghai), Strategy: "leader_daily_v1", SelectionEnabled: c.Selection.Enabled, Adjust: c.Daily.Adjust, MinScore: c.Selection.MinScore, TopN: c.Selection.TopN, PoolSize: len(c.Stocks), Selected: []Candidate{}, Excluded: []Excluded{}, Filtered: []Excluded{}, Rules: []string{
		"收盘价 > MA20 且 MA5 > MA10 > MA20",
		fmt.Sprintf("量比 ≥ %.2f 且 MACD DIF > DEA", c.Selection.MinVolumeRatio),
		fmt.Sprintf("RSI14 %.0f～%.0f，日涨跌幅 %.1f%%～%.1f%%，排除 ST / 停牌", c.Selection.RSIMin, c.Selection.RSIMax, c.Selection.MinDayChange, c.Selection.MaxDayChange),
	}}
	byCode := map[string]market.History{}
	for _, h := range histories {
		if err := h.Validate(); err != nil {
			return r, fmt.Errorf("invalid persisted history for %s: %w", h.Code, err)
		}
		if _, exists := byCode[h.Code]; exists {
			return r, errors.New("duplicate stock history")
		}
		byCode[h.Code] = h
	}
	ranked := []Candidate{}
	for _, s := range c.Stocks {
		h, ok := byCode[s.Code]
		if !ok {
			r.Excluded = append(r.Excluded, Excluded{Code: s.Code, Reason: "missing_data"})
			continue
		}
		if h.Version != 1 || h.Adjust != c.Daily.Adjust || h.AsOf != asof {
			r.Excluded = append(r.Excluded, Excluded{Code: s.Code, Reason: "invalid_or_stale_data", LastDate: lastDate(h)})
			continue
		}
		if len(h.Bars) == 0 {
			r.Excluded = append(r.Excluded, Excluded{Code: s.Code, Reason: "no_history"})
			continue
		}
		last := h.Bars[len(h.Bars)-1]
		if last.Date != asof {
			r.Excluded = append(r.Excluded, Excluded{Code: s.Code, Reason: "stale_or_suspended", LastDate: last.Date})
			continue
		}
		if last.Volume == 0 {
			r.Excluded = append(r.Excluded, Excluded{Code: s.Code, Reason: "no_trading_volume", LastDate: last.Date})
			continue
		}
		m, err := Score(h.Bars)
		if err != nil {
			r.Excluded = append(r.Excluded, Excluded{Code: s.Code, Reason: "insufficient_or_invalid_history", LastDate: last.Date})
			continue
		}
		r.Eligible++
		change := (last.Close/h.Bars[len(h.Bars)-2].Close - 1) * 100
		name := strings.ToUpper(strings.TrimSpace(h.Name))
		reason := ""
		switch {
		case strings.HasPrefix(name, "ST") || strings.HasPrefix(name, "*ST") || strings.HasPrefix(name, "S*ST") || strings.HasPrefix(name, "SST") || strings.Contains(name, "退"):
			reason = "special_treatment"
		case !(last.Close > m.MA20 && m.MA5 > m.MA10 && m.MA10 > m.MA20):
			reason = "no_bullish_alignment"
		case m.VolumeRatio < c.Selection.MinVolumeRatio || m.DIF <= m.DEA:
			reason = "insufficient_volume_or_macd"
		case m.RSI14 < c.Selection.RSIMin || m.RSI14 > c.Selection.RSIMax || change < c.Selection.MinDayChange || change > c.Selection.MaxDayChange:
			reason = "overheat_or_weakness"
		case m.Score < c.Selection.MinScore:
			reason = "score_below_threshold"
		}
		if reason != "" {
			r.Filtered = append(r.Filtered, Excluded{Code: s.Code, Reason: reason})
			continue
		}
		if c.Selection.Enabled {
			ranked = append(ranked, Candidate{Code: s.Code, Name: h.Name, Sector: s.Sector, Close: last.Close, ChangePct: change, Metrics: m})
		}
	}
	if r.Eligible == 0 {
		return r, errors.New("no stocks have complete eligible daily data")
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Metrics.Score == ranked[j].Metrics.Score {
			return ranked[i].Code < ranked[j].Code
		}
		return ranked[i].Metrics.Score > ranked[j].Metrics.Score
	})
	r.Passed = len(ranked)
	r.Selected = append(r.Selected, ranked[:min(len(ranked), c.Selection.TopN)]...)
	// ID includes configuration, selected results and data date, not wall-clock generation time.
	identity := r
	identity.GeneratedAt = time.Time{}
	identity.ID = ""
	data, _ := json.Marshal(identity)
	sum := sha256.Sum256(data)
	r.ID = hex.EncodeToString(sum[:12])
	return r, nil
}

func lastDate(h market.History) string {
	if len(h.Bars) == 0 {
		return ""
	}
	return h.Bars[len(h.Bars)-1].Date
}

// BuildWithExecution keeps legacy configuration and persisted v1 reports readable.
// Short-horizon reports have their own v2 contract; histories remain v1.
func BuildWithExecution(ctx context.Context, c config.Config, histories, raw []market.History, asof, target string, now time.Time) (Report, error) {
	return buildWithExecution(ctx, c, histories, raw, nil, asof, target, now)
}

// BuildWithExecutionMinutes adds the Chan gate when saved 5-minute histories
// are available. A nil or empty slice preserves the daily-only behavior used
// by callers without an intraday store.
func BuildWithExecutionMinutes(ctx context.Context, c config.Config, histories, raw []market.History, minutes []market.MinuteHistory, asof, target string, now time.Time) (Report, error) {
	return buildWithExecution(ctx, c, histories, raw, minutes, asof, target, now)
}

func buildWithExecution(ctx context.Context, c config.Config, histories, raw []market.History, minutes []market.MinuteHistory, asof, target string, now time.Time) (Report, error) {
	if !c.Strategy.Enabled {
		return Build(c, histories, asof, target, now)
	}
	var result strategy.Result
	var err error
	if minutes == nil {
		result, err = strategy.Analyze(ctx, c, histories, raw, asof)
	} else {
		result, err = strategy.AnalyzeWithChan(ctx, c, histories, raw, minutes, asof)
	}
	if err != nil {
		return Report{}, err
	}
	r := Report{Version: 2, AsOf: asof, Target: target, GeneratedAt: now.In(market.Shanghai), Strategy: "short_horizon_v1", SelectionEnabled: c.Selection.Enabled, Adjust: c.Daily.Adjust, TopN: c.Selection.TopN, PoolSize: len(c.Stocks), Eligible: len(result.Ranking), Ranking: result.Ranking, Research: &result.Research, Selected: []Candidate{}, Excluded: []Excluded{}, Filtered: []Excluded{}, Rules: []string{
		fmt.Sprintf("次日开盘模拟买入；净止盈 %.1f%% / 净止损 %.1f%%", c.Strategy.TargetNetPct, c.Strategy.StopNetPct),
		fmt.Sprintf("买入当日不卖出；买入后第 %d 个交易日收盘到期退出", c.Strategy.MaxHoldingDays),
		"历史验收硬条件：单笔扣费后净亏损严格低于投入的3%，超限或未决不通过；止损触发价不保证实盘成交价",
		"实验软目标：可卖出持仓窗口内最高价扣除成本与滑点后触达2.5%或3%，首次触达越快越好；收盘回落不取消触达，触达不等于实际成交",
		"机会分结合价量特征和已完成历史交易；不足证据不推荐；参考卖价需按实际买入成本重算",
	}}
	for _, stock := range c.Stocks {
		if reason, ok := result.Excluded[stock.Code]; ok {
			r.Excluded = append(r.Excluded, Excluded{Code: stock.Code, Reason: reason})
		}
	}
	if r.Eligible == 0 {
		return r, errors.New("no stocks have complete eligible strategy data")
	}
	for _, x := range r.Ranking {
		if !x.Recommended {
			r.Filtered = append(r.Filtered, Excluded{Code: x.Code, Reason: recommendationFilterReason(x.Risks)})
			continue
		}
		r.Passed++
		if !c.Selection.Enabled || len(r.Selected) >= r.TopN {
			continue
		}
		copy := x
		for _, h := range histories {
			if h.Code == x.Code {
				r.Selected = append(r.Selected, Candidate{Code: x.Code, Name: x.Name, Close: h.Bars[len(h.Bars)-1].Close, ChangePct: x.Features.Return1, Metrics: x.Features.Metrics, Ranking: &copy})
				break
			}
		}
	}
	identity := r
	identity.GeneratedAt = time.Time{}
	identity.ID = ""
	data, err := json.Marshal(identity)
	if err != nil {
		return r, err
	}
	sum := sha256.Sum256(data)
	r.ID = hex.EncodeToString(sum[:12])
	return r, nil
}

func recommendationFilterReason(risks []string) string {
	if hasRisk(risks, "chan_filtered") {
		return "chan_filtered"
	}
	if hasRisk(risks, "chan_unknown") {
		return "chan_unknown"
	}
	return "strategy_evidence_or_risk_gate"
}

func hasRisk(risks []string, want string) bool {
	for _, risk := range risks {
		if risk == want {
			return true
		}
	}
	return false
}
