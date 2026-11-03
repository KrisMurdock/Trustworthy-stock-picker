package fourlightstudy

import (
	"context"
	"errors"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
)

var triggerAgeGroups = []string{"first_price_age_0", "first_price_age_1_2", "first_price_age_3_5", "first_price_age_6_plus", "first_price_age_unknown"}
var triggerPathGroups = []string{"first_prior_both_positive", "first_prior_positive_then_nonpositive", "first_prior_nonpositive_then_positive", "first_prior_both_nonpositive", "first_prior_path_unknown"}

func triggerGroupNames() []string {
	names := []string{"baseline", "first_all_red", moderateVolumeGroup, "first_energy_only", "first_price_involved"}
	names = append(names, triggerAgeGroups...)
	return append(names, triggerPathGroups...)
}

type TriggerTimingMonth struct {
	Month         string                    `json:"month"`
	Market        []Comparison              `json:"market"`
	FirstAllRed   []Comparison              `json:"first_all_red"`
	EnergyOnly    []Comparison              `json:"energy_only"`
	MatchedSector []SectorMatchedComparison `json:"matched_sector"`
}

type TriggerTimingAudit struct {
	AllRedEvents       int            `json:"all_red_events"`
	FirstAllRed        int            `json:"first_all_red"`
	AgeUnknown         int            `json:"age_unknown"`
	PathUnknown        int            `json:"path_unknown"`
	AgeCensored        int            `json:"age_censored"`
	AgeUnknownReasons  map[string]int `json:"age_unknown_reasons"`
	PathUnknownReasons map[string]int `json:"path_unknown_reasons"`
}

// TriggerTimingReport is a separate exploratory view. It never changes the
// base or sector reports, or interprets an observed run as a trading gate.
type TriggerTimingReport struct {
	Version                  int                       `json:"schema_version"`
	Policy                   string                    `json:"policy"`
	Status                   string                    `json:"status"`
	Manifest                 Manifest                  `json:"manifest"`
	SectorSnapshot           SectorContextMetadata     `json:"sector_snapshot"`
	RedRunLookbackSessions   int                       `json:"red_run_lookback_sessions"`
	PriceAgeBins             []string                  `json:"price_age_bins"`
	PriorPathDefinition      string                    `json:"prior_path_definition"`
	Audit                    TriggerTimingAudit        `json:"audit"`
	Groups                   []GroupStats              `json:"groups"`
	Daily                    []GroupStats              `json:"daily"`
	Monthly                  []GroupStats              `json:"monthly"`
	MarketComparisons        []Comparison              `json:"market_comparisons"`
	FirstComparisons         []Comparison              `json:"first_comparisons"`
	EnergyComparisons        []Comparison              `json:"energy_comparisons"`
	MatchedSectorComparisons []SectorMatchedComparison `json:"matched_sector_comparisons"`
	MonthlyComparisons       []TriggerTimingMonth      `json:"monthly_comparisons"`
	Limitations              []string                  `json:"limitations"`
}

func AnalyzeTriggerTiming(ctx context.Context, m Manifest, source Source, sectors SectorSnapshot, emit func(Event) error) (Report, *SectorContextReport, *TriggerTimingReport, error) {
	return analyzeTriggerObserved(ctx, m, source, sectors, emit, nil, false)
}

func analyzeTriggerObserved(ctx context.Context, m Manifest, source Source, sectors SectorSnapshot, emit, extraObserver func(Event) error, priceEpisodes bool) (Report, *SectorContextReport, *TriggerTimingReport, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, nil, nil, err
	}
	if m.Version != 2 {
		return Report{}, nil, nil, errors.New("trigger timing requires manifest version 2")
	}
	if err := m.Validate(); err != nil {
		return Report{}, nil, nil, err
	}
	dates, err := calendar.New(m.Config()).TradingDays(m.From, m.Through)
	if err != nil {
		return Report{}, nil, nil, err
	}
	names := triggerGroupNames()
	compareNames := names[1:]
	energyNames := triggerAgeGroups[1:4]
	r := &TriggerTimingReport{Version: 1, Policy: TriggerContextPolicy, Manifest: m,
		RedRunLookbackSessions: TriggerRedLookbackSessions, PriceAgeBins: []string{"0", "1-2", "3-5", "6+", "unknown"},
		PriorPathDefinition: "20-session and 5-session close returns ending at signal minus one session; zero is nonpositive; independent from red-run bins",
		Audit:               TriggerTimingAudit{AgeUnknownReasons: map[string]int{}, PathUnknownReasons: map[string]int{}},
		Limitations: []string{
			"灯龄截至信号前一交易日，不包含触发当日；0/1-2/3-5需已知非全红终点，6+可由至少6日的已观测下界证明，截断天数不冒充精确天数。",
			"灯龄正数组属于量能补红，除全体首次全红外另与同日量能补红比较；年龄0属于价格灯参与，不能把触发类型构成误当延迟效果。",
			"前置5日/20日收益截至t-1，两个窗口重叠；四象限是价格描述，不证明反转、启动或反抽。信号当日涨跌不参与四象限定义。",
			"年龄和前置收益分别评价，不交叉、不与量比拼条件；未知仍在原selected分母，非首次全红不纳入候选。",
			"历史行业基准与原sector_context同口径，自排除、按候选已知窗口行业构成加权、日期等权；不足5个其他已知窗口单列。",
			"本轮使用反复观察的开发期，不构成独立验证；各组需同时报告有效覆盖、可比日期和月份，不能因七月表现删除七月。",
			"未来波段仍不设回撤否决，四灯公式与原报告保持冻结；当前股池、回溯qfq、行业滞后和未知非随机限制仍存在。",
		}}
	total := newGroupAccumulators(names, m.waveModel())
	daily, monthly := map[string]groupAccumulators{}, map[string]groupAccumulators{}
	for _, date := range dates {
		daily[date] = newGroupAccumulators(names, m.waveModel())
		if monthly[date[:7]] == nil {
			monthly[date[:7]] = newGroupAccumulators(names, m.waveModel())
		}
	}
	matching := sectorMatchGrid{}
	observer := func(event Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if event.Context == nil {
			return errors.New("trigger timing requires sector context on each observation")
		}
		groups, err := triggerNamesForEvent(event)
		if err != nil {
			return err
		}
		total.add(event.Date, groups, event.Wave)
		daily[event.Date].add(event.Date, groups, event.Wave)
		monthly[event.Date[:7]].add(event.Date, groups, event.Wave)
		matching.add(event.Date, event.Context.SectorCode, groups, event.Wave)
		if event.FourLight.AllRed {
			r.Audit.AllRedEvents++
		}
		if hasSectorGroup(event.Groups, "first_all_red") {
			r.Audit.FirstAllRed++
			if event.Trigger.PriceAgeGroup == "unknown" {
				r.Audit.AgeUnknown++
				r.Audit.AgeUnknownReasons[event.Trigger.PriceAgeReason]++
			}
			if event.Trigger.PriorPathGroup == "unknown" {
				r.Audit.PathUnknown++
				r.Audit.PathUnknownReasons[event.Trigger.PriorPathReason]++
			}
			if event.Trigger.PriorRedRuns.PriceAllRed.Censored {
				r.Audit.AgeCensored++
			}
		}
		if extraObserver != nil {
			return extraObserver(event)
		}
		return nil
	}
	base, sectorReport, err := analyzeSectorObserved(ctx, m, source, sectors, emit, observer, studyFeatures{triggerTiming: true, priceEpisodes: priceEpisodes})
	if err != nil {
		return Report{}, nil, nil, err
	}
	r.SectorSnapshot = sectorReport.SectorSnapshot
	r.Groups = total.summarize(names, "", "")
	for _, date := range dates {
		r.Daily = append(r.Daily, daily[date].summarize(names, date, "")...)
	}
	months := make([]string, 0, len(monthly))
	for month := range monthly {
		months = append(months, month)
	}
	sort.Strings(months)
	for _, month := range months {
		r.Monthly = append(r.Monthly, monthly[month].summarize(names, "", month)...)
	}
	if r.MarketComparisons, err = compareNamedGroups(ctx, dates, r.Daily, compareNames, "baseline"); err != nil {
		return Report{}, nil, nil, err
	}
	if r.FirstComparisons, err = compareNamedGroups(ctx, dates, r.Daily, compareNames, "first_all_red"); err != nil {
		return Report{}, nil, nil, err
	}
	if r.EnergyComparisons, err = compareNamedGroups(ctx, dates, r.Daily, energyNames, "first_energy_only"); err != nil {
		return Report{}, nil, nil, err
	}
	if r.MatchedSectorComparisons, err = matching.compare(ctx, dates, compareNames, ""); err != nil {
		return Report{}, nil, nil, err
	}
	for _, month := range months {
		monthDates := []string{}
		for _, date := range dates {
			if date[:7] == month {
				monthDates = append(monthDates, date)
			}
		}
		row := TriggerTimingMonth{Month: month}
		if row.Market, err = compareNamedGroups(ctx, monthDates, r.Daily, compareNames, "baseline"); err != nil {
			return Report{}, nil, nil, err
		}
		if row.FirstAllRed, err = compareNamedGroups(ctx, monthDates, r.Daily, compareNames, "first_all_red"); err != nil {
			return Report{}, nil, nil, err
		}
		if row.EnergyOnly, err = compareNamedGroups(ctx, monthDates, r.Daily, energyNames, "first_energy_only"); err != nil {
			return Report{}, nil, nil, err
		}
		if row.MatchedSector, err = matching.compare(ctx, monthDates, compareNames, month); err != nil {
			return Report{}, nil, nil, err
		}
		r.MonthlyComparisons = append(r.MonthlyComparisons, row)
	}
	if err := ctx.Err(); err != nil {
		return Report{}, nil, nil, err
	}
	r.Status = "complete"
	return base, sectorReport, r, nil
}

func triggerNamesForEvent(event Event) ([]string, error) {
	names := []string{"baseline"}
	if !hasSectorGroup(event.Groups, "first_all_red") {
		return names, nil
	}
	if event.Trigger == nil {
		return nil, errors.New("missing first-all-red trigger features")
	}
	names = append(names, "first_all_red")
	for _, name := range []string{moderateVolumeGroup, "first_energy_only", "first_price_involved"} {
		if hasSectorGroup(event.Groups, name) {
			names = append(names, name)
		}
	}
	age := map[string]string{"0": triggerAgeGroups[0], "1-2": triggerAgeGroups[1], "3-5": triggerAgeGroups[2], "6+": triggerAgeGroups[3], "unknown": triggerAgeGroups[4]}
	path := map[string]string{"both_positive": triggerPathGroups[0], "positive_then_nonpositive": triggerPathGroups[1], "nonpositive_then_positive": triggerPathGroups[2], "both_nonpositive": triggerPathGroups[3], "unknown": triggerPathGroups[4]}
	a, aok := age[event.Trigger.PriceAgeGroup]
	p, pok := path[event.Trigger.PriorPathGroup]
	if !aok || !pok {
		return nil, errors.New("invalid first-all-red trigger classification")
	}
	return append(names, a, p), nil
}
