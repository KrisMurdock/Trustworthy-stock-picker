package fourlightstudy

import (
	"context"
	"errors"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

type HorizonReport struct {
	Version            int                   `json:"schema_version"`
	Policy             string                `json:"policy"`
	Status             string                `json:"status"`
	Manifest           Manifest              `json:"manifest"`
	SectorSnapshot     SectorContextMetadata `json:"sector_snapshot"`
	Windows            []int                 `json:"windows"`
	Groups             []HorizonStats        `json:"groups"`
	Daily              []HorizonStats        `json:"daily"`
	Monthly            []HorizonStats        `json:"monthly"`
	Comparisons        []HorizonComparison   `json:"comparisons"`
	MonthlyComparisons []HorizonComparison   `json:"monthly_comparisons"`
	Limitations        []string              `json:"limitations"`
}

func AnalyzeWaveHorizons(ctx context.Context, m Manifest, source Source, sectors SectorSnapshot, emit func(Event) error) (Report, *SectorContextReport, *TriggerTimingReport, *PriceEpisodeReport, *HorizonReport, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, nil, nil, nil, nil, err
	}
	if m.Version != 2 {
		return Report{}, nil, nil, nil, nil, errors.New("wave horizons require manifest version 2")
	}
	if err := m.Validate(); err != nil {
		return Report{}, nil, nil, nil, nil, err
	}
	dates, err := calendar.New(m.Config()).TradingDays(m.From, m.Through)
	if err != nil {
		return Report{}, nil, nil, nil, nil, err
	}
	r := &HorizonReport{Version: 1, Policy: strategy.WaveHorizonsModel, Manifest: m, Windows: append([]int(nil), horizonLengths...),
		Limitations: []string{
			"长窗口只描述路径，没有新的成功阈值或回撤否决；五日启动状态完整保留原upward_wave_no_drawdown_v1。",
			"20/40/60按交易日分别要求完整数据；未满窗口只保留已观测前缀，不能以46日末值冒充60日，也不能因后期缺口否定已完整早窗。",
			"净末端、净收盘峰值及回吐沿原入场成交、费用、滑点和股数；D5后涨幅为C[N]/C[5]-1价格收益，上涨比例分母N-5，不是增量净收益。",
			"startup_five分层是D5才可知的事后诊断，不是信号日候选；长期known不能决定分层。启动成功组长期表现包含已发生的前五日上涨，不能直接解释为后续预测增量。",
			"同日市场与同行业比较仅使用startup_five=all；行业逐窗口自排除，至少5个其他已知窗口，候选构成加权后日期等权。",
			"信号日的个股相对行业、行业相对市场和未知状态会分别形成诊断组；这些分层只描述事后路径，不是信号日买入条件。",
			"长期窗口高度重叠，未沿用不适配60日窗口的10日块区间；这里只给描述性点差，不代表显著性、独立交易或组合收益。",
			"保留原全市场观察上限与预留边界，七月、八月长窗不完整另列；不同窗口known组成不同，均值不可直接相减当作同一队列成长。",
			"伊利股份2026-08-20至10-30已在个案讨论中被观察，该个案不能再宣称未污染留出；本全市场运行仍截止原观察上限，不将该案例混入开发汇总。",
			"活跃股池、回溯复权、行业滞后及多轮开发观察偏差仍存在；四灯未对齐商业软件，未改变正式推荐。",
		}}
	total, daily, monthly := horizonGrid{}, horizonGrid{}, horizonGrid{}
	matching := horizonMatchGrid{}
	observer := func(e Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.Context == nil {
			return errors.New("wave horizons require sector context")
		}
		if err := validateHorizonEvent(e); err != nil {
			return err
		}
		names, err := horizonNamesForEvent(e)
		if err != nil {
			return err
		}
		total.add("", e.Date, names, e.Horizons)
		daily.add(e.Date, e.Date, names, e.Horizons)
		monthly.add(e.Date[:7], e.Date, names, e.Horizons)
		matching.add(e, names)
		return nil
	}
	base, sector, trigger, episode, err := analyzeEpisodeObserved(ctx, m, source, sectors, emit, observer, true)
	if err != nil {
		return Report{}, nil, nil, nil, nil, err
	}
	r.SectorSnapshot = sector.SectorSnapshot
	r.Groups = total.rows("", "")
	monthDates := map[string][]string{}
	for _, date := range dates {
		r.Daily = append(r.Daily, daily.rows(date, "daily")...)
		monthDates[date[:7]] = append(monthDates[date[:7]], date)
	}
	months := make([]string, 0, len(monthDates))
	for month := range monthDates {
		months = append(months, month)
	}
	sort.Strings(months)
	r.Comparisons = matching.compare(dates, "", daily)
	for _, month := range months {
		if err := ctx.Err(); err != nil {
			return Report{}, nil, nil, nil, nil, err
		}
		r.Monthly = append(r.Monthly, monthly.rows(month, "monthly")...)
		r.MonthlyComparisons = append(r.MonthlyComparisons, matching.compare(monthDates[month], month, daily)...)
	}
	if err := ctx.Err(); err != nil {
		return Report{}, nil, nil, nil, nil, err
	}
	r.Status = "complete"
	return base, sector, trigger, episode, r, nil
}
