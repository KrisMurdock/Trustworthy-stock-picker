package fourlightstudy

import (
	"context"
	"errors"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
)

var episodePartition = []string{"episode_first", "episode_repeat", "episode_unknown"}
var episodeAgeSixPartition = []string{"age6_episode_first", "age6_episode_repeat", "age6_episode_unknown"}

func episodeGroupNames() []string {
	names := append([]string{"baseline", "first_all_red"}, episodePartition...)
	names = append(names, "first_price_age_6_plus")
	return append(names, episodeAgeSixPartition...)
}

type PriceEpisodeAudit struct {
	AllRedEvents      int `json:"all_red_events"`
	FirstAllRed       int `json:"first_all_red"`
	EpisodeFirst      int `json:"episode_first"`
	EpisodeRepeat     int `json:"episode_repeat"`
	EpisodeUnknown    int `json:"episode_unknown"`
	StartCensored     int `json:"start_censored"`
	PriorCountInexact int `json:"prior_count_inexact"`
	// Segment counts include only segments containing selected first_all_red events.
	ObservedSegments   int            `json:"observed_segments"`
	KnownStartSegments int            `json:"known_start_segments"`
	UnknownReasons     map[string]int `json:"unknown_reasons"`
}

type PriceEpisodeMonth struct {
	Month         string                    `json:"month"`
	Market        []Comparison              `json:"market"`
	FirstAllRed   []Comparison              `json:"first_all_red"`
	EpisodeFirst  []Comparison              `json:"episode_first"`
	MatchedSector []SectorMatchedComparison `json:"matched_sector"`
}

type PriceEpisodeReport struct {
	Version                  int                       `json:"schema_version"`
	Policy                   string                    `json:"policy"`
	Status                   string                    `json:"status"`
	Manifest                 Manifest                  `json:"manifest"`
	SectorSnapshot           SectorContextMetadata     `json:"sector_snapshot"`
	Audit                    PriceEpisodeAudit         `json:"audit"`
	Groups                   []GroupStats              `json:"groups"`
	Daily                    []GroupStats              `json:"daily"`
	Monthly                  []GroupStats              `json:"monthly"`
	MarketComparisons        []Comparison              `json:"market_comparisons"`
	FirstComparisons         []Comparison              `json:"first_comparisons"`
	EpisodeFirstComparisons  []Comparison              `json:"episode_first_comparisons"`
	MatchedSectorComparisons []SectorMatchedComparison `json:"matched_sector_comparisons"`
	MonthlyComparisons       []PriceEpisodeMonth       `json:"monthly_comparisons"`
	Limitations              []string                  `json:"limitations"`
}

// AnalyzePriceEpisodeStudy partitions the existing first-all-red events by
// past-only price-episode evidence. It preserves every prior report and event.
func AnalyzePriceEpisodeStudy(ctx context.Context, m Manifest, source Source, sectors SectorSnapshot, emit func(Event) error) (Report, *SectorContextReport, *TriggerTimingReport, *PriceEpisodeReport, error) {
	return analyzeEpisodeObserved(ctx, m, source, sectors, emit, nil, false)
}

func analyzeEpisodeObserved(ctx context.Context, m Manifest, source Source, sectors SectorSnapshot, emit, extraObserver func(Event) error, waveHorizons bool) (Report, *SectorContextReport, *TriggerTimingReport, *PriceEpisodeReport, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, nil, nil, nil, err
	}
	if m.Version != 2 {
		return Report{}, nil, nil, nil, errors.New("price episodes require manifest version 2")
	}
	if err := m.Validate(); err != nil {
		return Report{}, nil, nil, nil, err
	}
	dates, err := calendar.New(m.Config()).TradingDays(m.From, m.Through)
	if err != nil {
		return Report{}, nil, nil, nil, err
	}
	names := episodeGroupNames()
	comparisons := names[1:]
	r := &PriceEpisodeReport{Version: 1, Policy: PriceEpisodePolicy, Manifest: m,
		Audit: PriceEpisodeAudit{UnknownReasons: map[string]int{}},
		Limitations: []string{
			"价格段沿完整可用历史与冻结交易日扫描，不在研究起点、月份或60日处重置；首次可判已全红不证明段起点。",
			"缺日或价格灯未知切断可确认连续性；量能未知不切价格段，但累计触发次数与最近触发间隔分别保留不确定性。",
			"此前同一确认连续段内观察过全红即可确认再次触发，即使此前转换未知；首次须起点及此前状态完整，余下另列unknown。",
			"episode_first最多保留同一股票同一已知价格段的一次原首次全红；未确认转换的早期全红不补造为候选，也不以未来收益决定保留。",
			"再次触发仍是事件统计，长价格段可贡献多次、未来窗口可重叠；首次组不同价格段间也可能窗口重叠，不代表独立交易或组合收益。",
			"6+仅拆解上轮既有灯龄组，不新增阈值；全期、分月、同行业和有效覆盖并列，重复对首次仅比较共同有效日期。",
			"开发区间已反复观察；同日同行业与移动块区间不消除持续时间、触发类型、存活股池和事后复权偏差，不构成因果或独立验证。",
			"波段不设回撤否决，四灯公式未修改，原预留区间未读取，七月保留。",
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
	segments, firsts := map[string]bool{}, map[string]bool{}
	observer := func(e Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.Context == nil {
			return errors.New("price episodes require sector context")
		}
		groups, err := episodeNamesForEvent(e)
		if err != nil {
			return err
		}
		total.add(e.Date, groups, e.Wave)
		daily[e.Date].add(e.Date, groups, e.Wave)
		monthly[e.Date[:7]].add(e.Date, groups, e.Wave)
		matching.add(e.Date, e.Context.SectorCode, groups, e.Wave)
		if extraObserver != nil {
			if err := extraObserver(e); err != nil {
				return err
			}
		}
		if e.FourLight.AllRed {
			r.Audit.AllRedEvents++
		}
		if !hasSectorGroup(e.Groups, "first_all_red") {
			return nil
		}
		c := e.Episode
		r.Audit.FirstAllRed++
		key := e.Code + "/" + c.ObservedStartDate
		if !segments[key] {
			segments[key] = true
			r.Audit.ObservedSegments++
			if c.StartKnown {
				r.Audit.KnownStartSegments++
			}
		}
		if !c.StartKnown {
			r.Audit.StartCensored++
		}
		if c.ExactPriorTriggers == nil {
			r.Audit.PriorCountInexact++
		}
		switch c.Group {
		case "first":
			if firsts[key] {
				return errors.New("duplicate first trigger in one price episode")
			}
			firsts[key] = true
			r.Audit.EpisodeFirst++
		case "repeat":
			r.Audit.EpisodeRepeat++
		case "unknown":
			r.Audit.EpisodeUnknown++
			r.Audit.UnknownReasons[c.GroupReason]++
		}
		return nil
	}
	base, sector, trigger, err := analyzeTriggerObserved(ctx, m, source, sectors, emit, observer, studyFeatures{priceEpisodes: true, waveHorizons: waveHorizons})
	if err != nil {
		return Report{}, nil, nil, nil, err
	}
	r.SectorSnapshot = sector.SectorSnapshot
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
	if r.MarketComparisons, err = compareNamedGroups(ctx, dates, r.Daily, comparisons, "baseline"); err != nil {
		return Report{}, nil, nil, nil, err
	}
	if r.FirstComparisons, err = compareNamedGroups(ctx, dates, r.Daily, comparisons, "first_all_red"); err != nil {
		return Report{}, nil, nil, nil, err
	}
	if r.EpisodeFirstComparisons, err = compareNamedGroups(ctx, dates, r.Daily, []string{"episode_repeat"}, "episode_first"); err != nil {
		return Report{}, nil, nil, nil, err
	}
	if r.MatchedSectorComparisons, err = matching.compare(ctx, dates, comparisons, ""); err != nil {
		return Report{}, nil, nil, nil, err
	}
	for _, month := range months {
		monthDates := []string{}
		for _, date := range dates {
			if date[:7] == month {
				monthDates = append(monthDates, date)
			}
		}
		row := PriceEpisodeMonth{Month: month}
		if row.Market, err = compareNamedGroups(ctx, monthDates, r.Daily, comparisons, "baseline"); err != nil {
			return Report{}, nil, nil, nil, err
		}
		if row.FirstAllRed, err = compareNamedGroups(ctx, monthDates, r.Daily, comparisons, "first_all_red"); err != nil {
			return Report{}, nil, nil, nil, err
		}
		if row.EpisodeFirst, err = compareNamedGroups(ctx, monthDates, r.Daily, []string{"episode_repeat"}, "episode_first"); err != nil {
			return Report{}, nil, nil, nil, err
		}
		if row.MatchedSector, err = matching.compare(ctx, monthDates, comparisons, month); err != nil {
			return Report{}, nil, nil, nil, err
		}
		r.MonthlyComparisons = append(r.MonthlyComparisons, row)
	}
	if err := ctx.Err(); err != nil {
		return Report{}, nil, nil, nil, err
	}
	r.Status = "complete"
	return base, sector, trigger, r, nil
}

func episodeNamesForEvent(e Event) ([]string, error) {
	names := []string{"baseline"}
	if !hasSectorGroup(e.Groups, "first_all_red") {
		return names, nil
	}
	if e.Episode == nil || e.Trigger == nil {
		return nil, errors.New("missing first-all-red episode or timing context")
	}
	switch e.Episode.Group {
	case "first", "repeat", "unknown":
	default:
		return nil, errors.New("invalid first-all-red episode group")
	}
	names = append(names, "first_all_red", "episode_"+e.Episode.Group)
	if e.Trigger.PriceAgeGroup == "6+" {
		names = append(names, "first_price_age_6_plus", "age6_episode_"+e.Episode.Group)
	}
	return names, nil
}
