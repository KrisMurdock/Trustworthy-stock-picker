package fourlightstudy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const (
	bootstrapBlock       = 10
	bootstrapRepetitions = 2000
	bootstrapSeed        = int64(20261102)
)

var groupNames = []string{"baseline", "first_all_red", "all_red", "continuing_all_red", "flip_all", "first_energy_only", "first_price_involved", "all_red_transition_unknown", "non_all_red"}

type StockProblem struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// Audit accounts for every universe member and requested signal session. Only
// a known current four-light state enters baseline; no future path condition
// may remove it from a group's denominator.
type Audit struct {
	Stocks             int            `json:"stocks"`
	SignalDates        int            `json:"signal_dates"`
	StockDates         int            `json:"stock_dates"`
	BeforeListing      int            `json:"before_listing"`
	ListingDateUnknown int            `json:"listing_date_unknown_stocks"`
	SignalMissing      int            `json:"signal_missing"`
	WarmupInsufficient int            `json:"warmup_insufficient"`
	StateUnknown       int            `json:"state_unknown"`
	StateKnown         int            `json:"state_known"`
	TransitionUnknown  int            `json:"transition_unknown"`
	AllRedEventRows    int            `json:"all_red_event_rows"`
	StockProblems      []StockProblem `json:"stock_problems"`
}

type GroupStats struct {
	Name  string                      `json:"name"`
	Date  string                      `json:"date,omitempty"`
	Month string                      `json:"month,omitempty"`
	Wave  *strategy.UpwardWaveSummary `json:"upward_wave"`
}

// Comparison is an equal-date average of group minus same-date baseline hit
// rates. Differences and interval endpoints are ratios, not percentage points.
type Comparison struct {
	Group                string   `json:"group"`
	Days                 int      `json:"days"`
	PairedDates          int      `json:"paired_dates"`
	UnpairedDates        int      `json:"unpaired_dates"`
	MeanDifference       *float64 `json:"mean_difference"`
	Lower95              *float64 `json:"lower_95"`
	Upper95              *float64 `json:"upper_95"`
	BootstrapBlockDays   int      `json:"bootstrap_block_days"`
	BootstrapRepetitions int      `json:"bootstrap_repetitions"`
	BootstrapValidDraws  int      `json:"bootstrap_valid_draws"`
	BootstrapSeed        int64    `json:"bootstrap_seed"`
	IntervalStatus       string   `json:"interval_status"`
}

type Report struct {
	Version     int          `json:"schema_version"`
	Engine      string       `json:"engine"`
	Status      string       `json:"status"`
	Manifest    Manifest     `json:"manifest"`
	Audit       Audit        `json:"audit"`
	Groups      []GroupStats `json:"groups"`
	Daily       []GroupStats `json:"daily"`
	Monthly     []GroupStats `json:"monthly"`
	Comparisons []Comparison `json:"comparisons"`
	Limitations []string     `json:"limitations"`
}

type groupAccumulators map[string]*strategy.UpwardWaveAccumulator

func newGroupAccumulators() groupAccumulators {
	result := groupAccumulators{}
	for _, group := range groupNames {
		result[group] = &strategy.UpwardWaveAccumulator{}
	}
	return result
}

func (groups groupAccumulators) add(date string, names []string, wave *strategy.UpwardWave) {
	for _, name := range names {
		groups[name].Add(date, wave)
	}
}

func (groups groupAccumulators) summarize(date, month string) []GroupStats {
	rows := make([]GroupStats, 0, len(groupNames))
	for _, name := range groupNames {
		rows = append(rows, GroupStats{Name: name, Date: date, Month: month, Wave: groups[name].Summary()})
	}
	return rows
}

// Analyze streams one stock's histories at a time. The optional sink receives
// all-red observations in stable code/date order; it never receives an invented
// rank or interprets overlapping groups as a portfolio.
func Analyze(ctx context.Context, manifest Manifest, source Source, emit func(Event) error) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if err := manifest.Validate(); err != nil {
		return Report{}, err
	}
	if source == nil {
		return Report{}, errors.New("market-study source is required")
	}
	c := manifest.Config()
	cal := calendar.New(c)
	evaluator, err := strategy.NewOpeningWaveEvaluator(c)
	if err != nil {
		return Report{}, err
	}
	dates, err := cal.TradingDays(manifest.From, manifest.Through)
	if err != nil {
		return Report{}, err
	}
	previous := previousDates(manifest.Calendar.Days, dates)
	stocks, err := source.Stocks(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("read study universe: %w", err)
	}
	stocks = append([]config.Stock(nil), stocks...)
	sort.Slice(stocks, func(i, j int) bool { return stocks[i].Code < stocks[j].Code })
	if err := validateStocks(stocks); err != nil {
		return Report{}, err
	}
	report := Report{Version: 1, Engine: Engine, Manifest: manifest,
		Audit:  Audit{Stocks: len(stocks), SignalDates: len(dates), StockProblems: []StockProblem{}},
		Groups: []GroupStats{}, Daily: []GroupStats{}, Monthly: []GroupStats{}, Comparisons: []Comparison{},
		Limitations: []string{
			"当前活跃沪深股票集合，含存活者与当前股池偏差；不等同于逐日历史全市场。",
			"四灯为冻结价量近似 four_light_approx_v1，未对齐商业软件；未来波段只作事后评价。",
			"历史 ST 身份未完整冻结，入场模拟不代表真实可成交；净收盘估值不代表已实现收益。",
			"基线覆盖当前四灯可判的股票日；预热不足、缺当日行情与未知灯色单列审计，未冒充有效基线。",
			"同一股票相邻信号及同日股票相关，分组有重叠；结果是事件统计，不是独立交易或组合收益。",
			"日期等权差值仅使用组与同日基准均有有效入场窗口的日期；覆盖率和保守率另报，不能把未知当失败。",
			"固定十交易日移动块重采样区间仅为描述性不确定性；没有做多重比较校正，不构成独立验证或策略晋级。",
			"观察范围截止于预留区间之前；不足未来窗口仍保留分母，预留区间未消费。",
		}}
	total := newGroupAccumulators()
	daily, monthly := map[string]groupAccumulators{}, map[string]groupAccumulators{}
	for _, date := range dates {
		daily[date] = newGroupAccumulators()
		if monthly[date[:7]] == nil {
			monthly[date[:7]] = newGroupAccumulators()
		}
	}
	for _, stock := range stocks {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		if stock.ListedFrom == "" {
			report.Audit.ListingDateUnknown++
		}
		pair, err := source.Load(ctx, stock)
		if err != nil {
			return Report{}, fmt.Errorf("read study stock %s: %w", stock.Code, err)
		}
		if err := validateSourceDates(pair, manifest.ObservationThrough); err != nil {
			return Report{}, fmt.Errorf("study stock %s: %w", stock.Code, err)
		}
		if pair.Problem != "" {
			report.Audit.StockProblems = append(report.Audit.StockProblems, StockProblem{Code: stock.Code, Reason: pair.Problem})
		}
		index := sort.Search(len(pair.Adjusted), func(i int) bool { return pair.Adjusted[i].Date >= manifest.From })
		for _, date := range dates {
			if err := ctx.Err(); err != nil {
				return Report{}, err
			}
			report.Audit.StockDates++
			if stock.ListedFrom != "" && date < stock.ListedFrom {
				report.Audit.BeforeListing++
				continue
			}
			for index < len(pair.Adjusted) && pair.Adjusted[index].Date < date {
				index++
			}
			if index >= len(pair.Adjusted) || pair.Adjusted[index].Date != date {
				report.Audit.SignalMissing++
				continue
			}
			evidence := strategy.AnalyzeFourLight(pair.Adjusted[:index+1])
			if evidence.UnknownLights > 0 {
				if index+1 < 60 {
					report.Audit.WarmupInsufficient++
				} else {
					report.Audit.StateUnknown++
				}
				continue
			}
			report.Audit.StateKnown++
			if index == 0 || pair.Adjusted[index-1].Date != previous[date] {
				markPreviousUnknown(&evidence)
			}
			if evidence.PreviousUnknown > 0 {
				report.Audit.TransitionUnknown++
			}
			groups := classify(evidence)
			wave, err := evaluator.Evaluate(ctx, stock.Code, date, pair.Adjusted, pair.Raw, manifest.ReservedFrom, manifest.AsOf)
			if err != nil {
				return Report{}, fmt.Errorf("evaluate study stock %s at %s: %w", stock.Code, date, err)
			}
			total.add(date, groups, wave)
			daily[date].add(date, groups, wave)
			monthly[date[:7]].add(date, groups, wave)
			if evidence.AllRed {
				report.Audit.AllRedEventRows++
				if emit != nil {
					if err := emit(Event{Code: stock.Code, Name: stock.Name, Date: date, Groups: groups, FourLight: evidence, Wave: wave}); err != nil {
						return Report{}, fmt.Errorf("write study event %s at %s: %w", stock.Code, date, err)
					}
				}
			}
		}
	}
	report.Groups = total.summarize("", "")
	for _, date := range dates {
		report.Daily = append(report.Daily, daily[date].summarize(date, "")...)
	}
	months := make([]string, 0, len(monthly))
	for month := range monthly {
		months = append(months, month)
	}
	sort.Strings(months)
	for _, month := range months {
		report.Monthly = append(report.Monthly, monthly[month].summarize("", month)...)
	}
	report.Comparisons, err = compareGroups(ctx, dates, report.Daily)
	if err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	report.Status = "complete"
	return report, nil
}

func validateStocks(stocks []config.Stock) error {
	if len(stocks) == 0 {
		return errors.New("market-study universe is empty")
	}
	for i, stock := range stocks {
		if !config.ValidCode(stock.Code) || (i > 0 && stock.Code == stocks[i-1].Code) {
			return errors.New("market-study universe contains invalid or duplicate codes")
		}
		if stock.ListedFrom != "" {
			if parsed, err := time.Parse(time.DateOnly, stock.ListedFrom); err != nil || parsed.Format(time.DateOnly) != stock.ListedFrom {
				return errors.New("market-study universe contains invalid listing date")
			}
		}
	}
	return nil
}

func validateSourceDates(pair Pair, through string) error {
	for _, bars := range [][]market.Bar{pair.Adjusted, pair.Raw} {
		previous := ""
		for _, bar := range bars {
			if parsed, err := time.Parse(time.DateOnly, bar.Date); err != nil || parsed.Format(time.DateOnly) != bar.Date || bar.Date <= previous {
				return errors.New("source bars must have valid strictly increasing dates")
			}
			if bar.Date > through {
				return errors.New("source returned bars beyond frozen observation boundary")
			}
			previous = bar.Date
		}
	}
	return nil
}

func previousDates(calendarDays, dates []string) map[string]string {
	result := make(map[string]string, len(dates))
	for _, date := range dates {
		i := sort.SearchStrings(calendarDays, date)
		if i > 0 {
			result[date] = calendarDays[i-1]
		}
	}
	return result
}

func markPreviousUnknown(evidence *strategy.FourLightEvidence) {
	evidence.PreviousState = strategy.FourLightState{Trend: strategy.FourLightUnknown, Energy: strategy.FourLightUnknown, MidTerm: strategy.FourLightUnknown, ShortTerm: strategy.FourLightUnknown}
	evidence.PreviousUnknown = 4
	evidence.Transitions = strategy.FourLightTransitions{}
	evidence.FlipCount, evidence.AllFlipped = 0, false
	evidence.DataStatus = strategy.FourLightDataPartial
	evidence.Reason += "；上一交易日日线缺失，转换未知"
}

func classify(evidence strategy.FourLightEvidence) []string {
	if evidence.UnknownLights > 0 {
		return nil
	}
	groups := []string{"baseline"}
	if !evidence.AllRed {
		return append(groups, "non_all_red")
	}
	groups = append(groups, "all_red")
	if evidence.PreviousUnknown > 0 {
		return append(groups, "all_red_transition_unknown")
	}
	p := evidence.PreviousState
	if p.Trend == strategy.FourLightRed && p.Energy == strategy.FourLightRed && p.MidTerm == strategy.FourLightRed && p.ShortTerm == strategy.FourLightRed {
		return append(groups, "continuing_all_red")
	}
	groups = append(groups, "first_all_red")
	if evidence.AllFlipped {
		groups = append(groups, "flip_all")
	}
	t := evidence.Transitions
	if t.Trend || t.MidTerm || t.ShortTerm {
		groups = append(groups, "first_price_involved")
	} else if t.Energy {
		groups = append(groups, "first_energy_only")
	}
	return groups
}
