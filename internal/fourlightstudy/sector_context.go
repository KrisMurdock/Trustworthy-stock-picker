package fourlightstudy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const (
	SectorContextPolicy       = "published_sector_relative20_v1"
	sectorReturnSessions      = 20
	sectorMinimumOthers       = 5
	stockOutperformSector     = "first_stock_outperform_sector"
	stockNotOutperformSector  = "first_stock_not_outperform_sector"
	sectorOutperformMarket    = "first_sector_outperform_market"
	sectorNotOutperformMarket = "first_sector_not_outperform_market"
	contextUnknown            = "first_context_unknown"
)

var sectorContextGroups = []string{"baseline", "first_all_red", moderateVolumeGroup,
	stockOutperformSector, stockNotOutperformSector, sectorOutperformMarket, sectorNotOutperformMarket, contextUnknown}
var sectorOutputGroups = []string{"baseline", "first_all_red", moderateVolumeGroup}
var sectorComparisonGroups = []string{"first_all_red", moderateVolumeGroup,
	stockOutperformSector, stockNotOutperformSector, sectorOutperformMarket, sectorNotOutperformMarket, contextUnknown}

// SectorSignalContext contains signal-time observations only. A current-only
// classification may describe a past signal but cannot activate candidates.
type SectorSignalContext struct {
	Policy                 string   `json:"policy"`
	SectorCode             string   `json:"sector_code,omitempty"`
	SectorName             string   `json:"sector_name,omitempty"`
	ClassificationStatus   string   `json:"classification_status"`
	ClassificationReason   string   `json:"classification_reason,omitempty"`
	HistoricalEligible     bool     `json:"historical_eligible"`
	Return20Pct            *float64 `json:"return20_pct"`
	SectorOtherReturn20Pct *float64 `json:"sector_other_return20_pct"`
	MarketOtherReturn20Pct *float64 `json:"market_other_return20_pct"`
	StockMinusSectorPct    *float64 `json:"stock_minus_sector_pct"`
	SectorMinusMarketPct   *float64 `json:"sector_minus_market_pct"`
	SectorOtherStocks      int      `json:"sector_other_stocks"`
	MarketOtherStocks      int      `json:"market_other_stocks"`
	StockStage             string   `json:"stock_stage"` // outperform / not_outperform / unknown
	SectorStage            string   `json:"sector_stage"`
	StockStageReason       string   `json:"stock_stage_reason,omitempty"`
	SectorStageReason      string   `json:"sector_stage_reason,omitempty"`
}

type SectorContextParameters struct {
	ReturnSessions          int     `json:"return_sessions"`
	MinimumOtherPriceStocks int     `json:"minimum_other_price_stocks"`
	MinimumOtherKnownWaves  int     `json:"minimum_other_known_waves"`
	ExcludeSelf             bool    `json:"exclude_self"`
	PriceUniverse           string  `json:"price_universe"`
	PriceWeighting          string  `json:"price_weighting"`
	OutcomeWeighting        string  `json:"outcome_weighting"`
	DateWeighting           string  `json:"date_weighting"`
	PositiveThresholdPct    float64 `json:"positive_threshold_pct"`
}

// Metadata intentionally omits the potentially large membership payload.
type SectorContextMetadata struct {
	Version      int            `json:"schema_version"`
	Basis        string         `json:"basis"`
	Taxonomy     string         `json:"taxonomy"`
	Level        string         `json:"level"`
	SnapshotAsOf string         `json:"snapshot_as_of"`
	PublishedOn  string         `json:"published_on,omitempty"`
	ObservedAt   string         `json:"observed_at"`
	Sources      []SectorSource `json:"sources"`
	Memberships  int            `json:"memberships"`
}

type SectorContextAudit struct {
	StockDates            int            `json:"stock_dates"`
	FirstAllRed           int            `json:"first_all_red"`
	Classified            int            `json:"classified"`
	ClassificationUnknown int            `json:"classification_unknown"`
	HistoricalEligible    int            `json:"historical_eligible"`
	Return20Known         int            `json:"return20_known"`
	StockStageKnown       int            `json:"stock_stage_known"`
	SectorStageKnown      int            `json:"sector_stage_known"`
	UnknownReasons        map[string]int `json:"unknown_reasons"`
	FirstUnknownReasons   map[string]int `json:"first_unknown_reasons"`
}

type SectorGroupStats struct {
	SectorCode           string                      `json:"sector_code,omitempty"`
	SectorName           string                      `json:"sector_name,omitempty"`
	ClassificationStatus string                      `json:"classification_status"`
	Name                 string                      `json:"name"`
	Date                 string                      `json:"date,omitempty"`
	Month                string                      `json:"month,omitempty"`
	Wave                 *strategy.UpwardWaveSummary `json:"upward_wave"`
}

// SectorMatchedComparison matches each known candidate to the other known
// outcomes in its own industry on the same signal date. Unknown classification
// and insufficient peers remain visible in selected/known/unmatched counts.
type SectorMatchedComparison struct {
	Group                     string         `json:"group"`
	Days                      int            `json:"days"`
	Month                     string         `json:"month,omitempty"`
	Selected                  int            `json:"selected"`
	CandidateKnown            int            `json:"candidate_known"`
	MatchedKnown              int            `json:"matched_known"`
	UnmatchedKnown            int            `json:"unmatched_known"`
	UnmatchedReasons          map[string]int `json:"unmatched_reasons"`
	KnownMatchingCoverage     *float64       `json:"known_matching_coverage"`
	EffectiveMatchingCoverage *float64       `json:"effective_matching_coverage"`
	PairedDates               int            `json:"paired_dates"`
	UnpairedDates             int            `json:"unpaired_dates"`
	MeanDifference            *float64       `json:"mean_difference"`
	Lower95                   *float64       `json:"lower_95"`
	Upper95                   *float64       `json:"upper_95"`
	BootstrapBlockDays        int            `json:"bootstrap_block_days"`
	BootstrapRepetitions      int            `json:"bootstrap_repetitions"`
	BootstrapValidDraws       int            `json:"bootstrap_valid_draws"`
	BootstrapSeed             int64          `json:"bootstrap_seed"`
	IntervalStatus            string         `json:"interval_status"`
}

type SectorMonthComparisons struct {
	Month         string                    `json:"month"`
	Market        []Comparison              `json:"market"`
	FirstAllRed   []Comparison              `json:"first_all_red"`
	MatchedSector []SectorMatchedComparison `json:"matched_sector"`
}

type SectorContextReport struct {
	Version                  int                       `json:"schema_version"`
	Policy                   string                    `json:"policy"`
	Status                   string                    `json:"status"`
	Manifest                 Manifest                  `json:"manifest"`
	SectorSnapshot           SectorContextMetadata     `json:"sector_snapshot"`
	HistoricalEligible       bool                      `json:"historical_eligible"`
	EligibleSignalDates      int                       `json:"eligible_signal_dates"`
	Parameters               SectorContextParameters   `json:"parameters"`
	Audit                    SectorContextAudit        `json:"audit"`
	Groups                   []GroupStats              `json:"groups"`
	Daily                    []GroupStats              `json:"daily"`
	Monthly                  []GroupStats              `json:"monthly"`
	Sectors                  []SectorGroupStats        `json:"sectors"`
	SectorDaily              []SectorGroupStats        `json:"sector_daily"`
	SectorMonthly            []SectorGroupStats        `json:"sector_monthly"`
	CandidateRetention       []CandidateRetention      `json:"candidate_retention"`
	MarketComparisons        []Comparison              `json:"market_comparisons"`
	FirstComparisons         []Comparison              `json:"first_comparisons"`
	MatchedSectorComparisons []SectorMatchedComparison `json:"matched_sector_comparisons"`
	MonthlyComparisons       []SectorMonthComparisons  `json:"monthly_comparisons"`
	Limitations              []string                  `json:"limitations"`
}

type sectorPriorFeature struct {
	selected bool
	known    bool
	value    float64
	reason   string
}

// Anchor-relative sums preserve exact equality for identical observations,
// avoiding an artificial >0 stage from subtracting rounded identical means.
type sectorMean struct {
	count              int
	anchor, deviations float64
}

func (a *sectorMean) add(value float64) {
	if a.count == 0 {
		a.anchor = value
	}
	a.deviations += value - a.anchor
	a.count++
}
func (a sectorMean) excluding(feature sectorPriorFeature) (*float64, int) {
	n, sum := a.count, a.deviations
	if feature.known {
		n--
		sum -= feature.value - a.anchor
	}
	if n < sectorMinimumOthers {
		return nil, max(n, 0)
	}
	v := a.anchor + sum/float64(n)
	return &v, n
}

type sectorPriorDay struct {
	market  sectorMean
	sectors map[string]*sectorMean
}
type sectorPrior struct {
	byCode    map[string][]sectorPriorFeature
	days      map[string]*sectorPriorDay
	dateIndex map[string]int
}

type sectorFrozenSource struct {
	Source
	stocks []config.Stock
}

func (s sectorFrozenSource) Stocks(ctx context.Context) ([]config.Stock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]config.Stock(nil), s.stocks...), nil
}

// AnalyzeSectorContext adds an independent research report. The returned base
// report is the unchanged v2 evaluation, and no legacy gate or light is edited.
func AnalyzeSectorContext(ctx context.Context, m Manifest, source Source, sectors SectorSnapshot, emit func(Event) error) (Report, *SectorContextReport, error) {
	return analyzeSectorObserved(ctx, m, source, sectors, emit, nil, studyFeatures{})
}

func analyzeSectorObserved(ctx context.Context, m Manifest, source Source, sectors SectorSnapshot, emit, extraObserver func(Event) error, features studyFeatures) (Report, *SectorContextReport, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, nil, err
	}
	if m.Version != 2 {
		return Report{}, nil, errors.New("sector context requires market-study manifest schema 2")
	}
	if err := m.Validate(); err != nil {
		return Report{}, nil, err
	}
	if err := sectors.Validate(); err != nil {
		return Report{}, nil, err
	}
	if source == nil {
		return Report{}, nil, errors.New("sector context requires a price source")
	}
	stocks, err := source.Stocks(ctx)
	if err != nil {
		return Report{}, nil, err
	}
	stocks = append([]config.Stock(nil), stocks...)
	sort.Slice(stocks, func(i, j int) bool { return stocks[i].Code < stocks[j].Code })
	if err := validateStocks(stocks); err != nil {
		return Report{}, nil, err
	}
	dates, err := calendar.New(m.Config()).TradingDays(m.From, m.Through)
	if err != nil {
		return Report{}, nil, err
	}
	prior, err := prepareSectorPrior(ctx, m, source, stocks, sectors, dates)
	if err != nil {
		return Report{}, nil, err
	}
	r := newSectorContextReport(m, sectors, dates)
	total := newGroupAccumulators(sectorContextGroups, m.waveModel())
	daily, monthly := map[string]groupAccumulators{}, map[string]groupAccumulators{}
	for _, date := range dates {
		daily[date] = newGroupAccumulators(sectorContextGroups, m.waveModel())
		if monthly[date[:7]] == nil {
			monthly[date[:7]] = newGroupAccumulators(sectorContextGroups, m.waveModel())
		}
	}
	sectorTotals := map[string]groupAccumulators{}
	sectorDaily, sectorMonthly := map[string]map[string]groupAccumulators{}, map[string]map[string]groupAccumulators{}
	sectorNames := map[string]string{}
	matching := sectorMatchGrid{}
	observer := func(event Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		index, ok := prior.dateIndex[event.Date]
		features := prior.byCode[event.Code]
		if !ok || index >= len(features) || !features[index].selected {
			return errors.New("source changed between sector-context passes")
		}
		contextValue := buildSectorSignalContext(sectors, event.Code, event.Date, features[index], prior.days[event.Date])
		event.Context = &contextValue
		names := sectorNamesForEvent(event)
		total.add(event.Date, names, event.Wave)
		daily[event.Date].add(event.Date, names, event.Wave)
		monthly[event.Date[:7]].add(event.Date, names, event.Wave)
		code := contextValue.SectorCode
		sectorNames[code] = contextValue.SectorName
		addSectorSummary(sectorTotals, code, m.waveModel(), event.Date, names, event.Wave)
		if sectorDaily[event.Date] == nil {
			sectorDaily[event.Date] = map[string]groupAccumulators{}
		}
		addSectorSummary(sectorDaily[event.Date], code, m.waveModel(), event.Date, names, event.Wave)
		if sectorMonthly[event.Date[:7]] == nil {
			sectorMonthly[event.Date[:7]] = map[string]groupAccumulators{}
		}
		addSectorSummary(sectorMonthly[event.Date[:7]], code, m.waveModel(), event.Date, names, event.Wave)
		matching.add(event.Date, code, names, event.Wave)
		addSectorContextAudit(&r.Audit, event, contextValue)
		if extraObserver != nil {
			if err := extraObserver(event); err != nil {
				return err
			}
		}
		if emit != nil && event.FourLight.AllRed {
			return emit(event)
		}
		return nil
	}
	base, err := analyzeObserved(ctx, m, sectorFrozenSource{Source: source, stocks: stocks}, nil, observer, features)
	if err != nil {
		return Report{}, nil, err
	}
	r.Groups = total.summarize(sectorContextGroups, "", "")
	for _, date := range dates {
		r.Daily = append(r.Daily, daily[date].summarize(sectorContextGroups, date, "")...)
		r.SectorDaily = append(r.SectorDaily, sectorSummaryRows(sectorDaily[date], sectorNames, date, "")...)
	}
	months := make([]string, 0, len(monthly))
	for month := range monthly {
		months = append(months, month)
	}
	sort.Strings(months)
	for _, month := range months {
		r.Monthly = append(r.Monthly, monthly[month].summarize(sectorContextGroups, "", month)...)
		r.SectorMonthly = append(r.SectorMonthly, sectorSummaryRows(sectorMonthly[month], sectorNames, "", month)...)
	}
	r.Sectors = sectorSummaryRows(sectorTotals, sectorNames, "", "")
	r.CandidateRetention = sectorContextRetention(r.Groups)
	if r.MarketComparisons, err = compareNamedGroups(ctx, dates, r.Daily, sectorComparisonGroups, "baseline"); err != nil {
		return Report{}, nil, err
	}
	if r.FirstComparisons, err = compareNamedGroups(ctx, dates, r.Daily, sectorComparisonGroups, "first_all_red"); err != nil {
		return Report{}, nil, err
	}
	if r.MatchedSectorComparisons, err = matching.compare(ctx, dates, sectorComparisonGroups, ""); err != nil {
		return Report{}, nil, err
	}
	for _, month := range months {
		monthDates := []string{}
		for _, date := range dates {
			if date[:7] == month {
				monthDates = append(monthDates, date)
			}
		}
		row := SectorMonthComparisons{Month: month}
		if row.Market, err = compareNamedGroups(ctx, monthDates, r.Daily, sectorComparisonGroups, "baseline"); err != nil {
			return Report{}, nil, err
		}
		if row.FirstAllRed, err = compareNamedGroups(ctx, monthDates, r.Daily, sectorComparisonGroups, "first_all_red"); err != nil {
			return Report{}, nil, err
		}
		if row.MatchedSector, err = matching.compare(ctx, monthDates, sectorComparisonGroups, month); err != nil {
			return Report{}, nil, err
		}
		r.MonthlyComparisons = append(r.MonthlyComparisons, row)
	}
	if err := ctx.Err(); err != nil {
		return Report{}, nil, err
	}
	r.Status = "complete"
	return base, r, nil
}

func newSectorContextReport(m Manifest, s SectorSnapshot, dates []string) *SectorContextReport {
	r := &SectorContextReport{Version: 1, Policy: SectorContextPolicy, Manifest: m,
		SectorSnapshot: SectorContextMetadata{Version: s.Version, Basis: s.Basis, Taxonomy: s.Taxonomy, Level: s.Level, SnapshotAsOf: s.SnapshotAsOf, PublishedOn: s.PublishedOn, ObservedAt: s.ObservedAt, Sources: append([]SectorSource(nil), s.Sources...), Memberships: len(s.Memberships)},
		Parameters:     SectorContextParameters{ReturnSessions: sectorReturnSessions, MinimumOtherPriceStocks: sectorMinimumOthers, MinimumOtherKnownWaves: sectorMinimumOthers, ExcludeSelf: true, PriceUniverse: "current_four_light_known_with_exact_21_sessions", PriceWeighting: "equal_stock", OutcomeWeighting: "candidate_known_industry_composition", DateWeighting: "equal_signal_date", PositiveThresholdPct: 0},
		Audit:          SectorContextAudit{UnknownReasons: map[string]int{}, FirstUnknownReasons: map[string]int{}},
		Groups:         []GroupStats{}, Daily: []GroupStats{}, Monthly: []GroupStats{}, Sectors: []SectorGroupStats{}, SectorDaily: []SectorGroupStats{}, SectorMonthly: []SectorGroupStats{}, MonthlyComparisons: []SectorMonthComparisons{},
		Limitations: []string{
			"固定采用一份已公开的行业分类快照；snapshot_as_of仅是报告期末，不是逐股分类生效日，也不代表期间没有行业变更。",
			"current_only分类只允许回溯描述，不能启用历史候选；只有日期的发布记录从次日起可用。缺失分类不并成可比较行业。",
			"20日强弱仅用信号日及之前21个完整交易日价格；行业成员按该固定分类取值，行业与市场收益均为股票等权并排除观察股票。",
			"行业匹配按当天可匹配候选已知窗口的行业构成加权，再按日期等权；这不是全体selected构成，未知、缺分类与不足5个其他已知窗口另报。",
			"两个相对强弱条件独立诊断，不与量比或彼此叠加；first_context_unknown表示至少一个条件不可用，可能与另一项已知分组重叠。",
			"当前活跃股池和回溯qfq窗口仍有存活者及历史数据版本偏差；行业发布时间可追溯不等于完整历史可交易验证。",
			"结果复用开发期，移动块区间仅描述不确定性；各比较必须核对paired/unpaired日期，不能跨不同日期集合直接相减。",
		}}
	for _, date := range dates {
		if s.Basis == "published_snapshot" && date > s.PublishedOn {
			r.EligibleSignalDates++
		}
	}
	r.HistoricalEligible = len(dates) > 0 && r.EligibleSignalDates == len(dates)
	return r
}

func prepareSectorPrior(ctx context.Context, m Manifest, source Source, stocks []config.Stock, s SectorSnapshot, dates []string) (*sectorPrior, error) {
	p := &sectorPrior{byCode: map[string][]sectorPriorFeature{}, days: map[string]*sectorPriorDay{}, dateIndex: map[string]int{}}
	calendarIndex := map[string]int{}
	for i, date := range m.Calendar.Days {
		calendarIndex[date] = i
	}
	for i, date := range dates {
		p.dateIndex[date] = i
		p.days[date] = &sectorPriorDay{sectors: map[string]*sectorMean{}}
	}
	for _, stock := range stocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pair, err := source.Load(ctx, stock)
		if err != nil {
			return nil, fmt.Errorf("sector prior %s: %w", stock.Code, err)
		}
		if err := validateSourceDates(pair, m.ObservationThrough); err != nil {
			return nil, err
		}
		features := make([]sectorPriorFeature, len(dates))
		index := sort.Search(len(pair.Adjusted), func(i int) bool { return pair.Adjusted[i].Date >= m.From })
		for di, date := range dates {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if stock.ListedFrom != "" && date < stock.ListedFrom {
				continue
			}
			for index < len(pair.Adjusted) && pair.Adjusted[index].Date < date {
				index++
			}
			if index >= len(pair.Adjusted) || pair.Adjusted[index].Date != date {
				continue
			}
			if strategy.AnalyzeFourLight(pair.Adjusted[:index+1]).UnknownLights > 0 {
				continue
			}
			f := sectorPriorFeature{selected: true}
			f.value, f.reason = sectorReturn20(pair.Adjusted[:index+1], m.Calendar.Days, calendarIndex[date])
			f.known = f.reason == ""
			features[di] = f
			if !f.known {
				continue
			}
			day := p.days[date]
			day.market.add(f.value)
			member, reason := s.Lookup(stock.Code, date)
			if reason != "" {
				continue
			}
			if day.sectors[member.SectorCode] == nil {
				day.sectors[member.SectorCode] = &sectorMean{}
			}
			day.sectors[member.SectorCode].add(f.value)
		}
		p.byCode[stock.Code] = features
	}
	return p, nil
}

func sectorReturn20(bars []market.Bar, calendarDays []string, end int) (float64, string) {
	if len(bars) < sectorReturnSessions+1 || end < sectorReturnSessions || end >= len(calendarDays) {
		return 0, "return20_history_missing"
	}
	window := bars[len(bars)-sectorReturnSessions-1:]
	for i, bar := range window {
		if bar.Date != calendarDays[end-sectorReturnSessions+i] {
			return 0, "return20_session_missing"
		}
		for _, value := range []float64{bar.Open, bar.Close, bar.High, bar.Low} {
			if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				return 0, "return20_price_invalid"
			}
		}
		if bar.High < math.Max(bar.Open, bar.Close) || bar.Low > math.Min(bar.Open, bar.Close) {
			return 0, "return20_price_invalid"
		}
	}
	value := (window[sectorReturnSessions].Close/window[0].Close - 1) * 100
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, "return20_price_invalid"
	}
	return value, ""
}

func buildSectorSignalContext(s SectorSnapshot, code, date string, f sectorPriorFeature, day *sectorPriorDay) SectorSignalContext {
	c := SectorSignalContext{Policy: SectorContextPolicy, ClassificationStatus: "unknown", StockStage: "unknown", SectorStage: "unknown"}
	member, reason := s.Lookup(code, date)
	if reason != "" {
		c.ClassificationReason = reason
		c.StockStageReason = reason
		c.SectorStageReason = reason
	} else {
		c.SectorCode, c.SectorName = member.SectorCode, member.SectorName
		c.ClassificationStatus = "classified"
		c.HistoricalEligible = s.Basis == "published_snapshot"
	}
	if f.known {
		value := f.value
		c.Return20Pct = &value
	}
	if day != nil {
		c.MarketOtherReturn20Pct, c.MarketOtherStocks = day.market.excluding(f)
		if reason == "" {
			if aggregate := day.sectors[member.SectorCode]; aggregate != nil {
				c.SectorOtherReturn20Pct, c.SectorOtherStocks = aggregate.excluding(f)
			}
		}
	}
	if c.Return20Pct != nil && c.SectorOtherReturn20Pct != nil {
		difference := *c.Return20Pct - *c.SectorOtherReturn20Pct
		c.StockMinusSectorPct = &difference
	}
	if c.SectorOtherReturn20Pct != nil && c.MarketOtherReturn20Pct != nil {
		difference := *c.SectorOtherReturn20Pct - *c.MarketOtherReturn20Pct
		c.SectorMinusMarketPct = &difference
	}
	if reason != "" {
		return c
	}
	if !c.HistoricalEligible {
		c.StockStageReason, c.SectorStageReason = "classification_current_only", "classification_current_only"
		return c
	}
	switch {
	case !f.known:
		c.StockStageReason = f.reason
	case c.SectorOtherReturn20Pct == nil:
		c.StockStageReason = "insufficient_sector_price_peers"
	default:
		c.StockStage = relativeSectorStage(*c.StockMinusSectorPct)
	}
	switch {
	case c.SectorOtherReturn20Pct == nil:
		c.SectorStageReason = "insufficient_sector_price_peers"
	case c.MarketOtherReturn20Pct == nil:
		c.SectorStageReason = "insufficient_market_price_peers"
	default:
		c.SectorStage = relativeSectorStage(*c.SectorMinusMarketPct)
	}
	return c
}

func relativeSectorStage(difference float64) string {
	if difference > 0 {
		return "outperform"
	}
	return "not_outperform"
}
func hasSectorGroup(names []string, name string) bool {
	for _, value := range names {
		if value == name {
			return true
		}
	}
	return false
}
func sectorNamesForEvent(event Event) []string {
	names := []string{"baseline"}
	if !hasSectorGroup(event.Groups, "first_all_red") {
		return names
	}
	names = append(names, "first_all_red")
	if hasSectorGroup(event.Groups, moderateVolumeGroup) {
		names = append(names, moderateVolumeGroup)
	}
	c := event.Context
	if c == nil {
		return append(names, contextUnknown)
	}
	switch c.StockStage {
	case "outperform":
		names = append(names, stockOutperformSector)
	case "not_outperform":
		names = append(names, stockNotOutperformSector)
	}
	switch c.SectorStage {
	case "outperform":
		names = append(names, sectorOutperformMarket)
	case "not_outperform":
		names = append(names, sectorNotOutperformMarket)
	}
	if c.StockStage == "unknown" || c.SectorStage == "unknown" {
		names = append(names, contextUnknown)
	}
	return names
}

func addSectorSummary(groups map[string]groupAccumulators, code, model, date string, names []string, wave *strategy.UpwardWave) {
	if groups[code] == nil {
		groups[code] = newGroupAccumulators(sectorOutputGroups, model)
	}
	for _, name := range sectorOutputGroups {
		if hasSectorGroup(names, name) {
			groups[code][name].Add(date, wave)
		}
	}
}
func sectorSummaryRows(groups map[string]groupAccumulators, names map[string]string, date, month string) []SectorGroupStats {
	keys := make([]string, 0, len(groups))
	for code := range groups {
		keys = append(keys, code)
	}
	sort.Strings(keys)
	rows := []SectorGroupStats{}
	for _, code := range keys {
		status := "classified"
		if code == "" {
			status = "unknown"
		}
		for _, row := range groups[code].summarize(sectorOutputGroups, date, month) {
			rows = append(rows, SectorGroupStats{SectorCode: code, SectorName: names[code], ClassificationStatus: status, Name: row.Name, Date: date, Month: month, Wave: row.Wave})
		}
	}
	return rows
}
func addSectorContextAudit(a *SectorContextAudit, event Event, c SectorSignalContext) {
	a.StockDates++
	first := hasSectorGroup(event.Groups, "first_all_red")
	if first {
		a.FirstAllRed++
	}
	if c.ClassificationStatus == "classified" {
		a.Classified++
	} else {
		a.ClassificationUnknown++
	}
	if c.HistoricalEligible {
		a.HistoricalEligible++
	}
	if c.Return20Pct != nil {
		a.Return20Known++
	}
	if c.StockStage != "unknown" {
		a.StockStageKnown++
	}
	if c.SectorStage != "unknown" {
		a.SectorStageKnown++
	}
	reasons := map[string]bool{}
	for _, reason := range []string{c.ClassificationReason, c.StockStageReason, c.SectorStageReason} {
		if reason != "" {
			reasons[reason] = true
		}
	}
	for reason := range reasons {
		a.UnknownReasons[reason]++
		if first {
			a.FirstUnknownReasons[reason]++
		}
	}
}

func sectorRatio(n, d int) *float64 {
	if d == 0 {
		return nil
	}
	value := float64(n) / float64(d)
	return &value
}
func sectorContextRetention(rows []GroupStats) []CandidateRetention {
	byName := map[string]*strategy.UpwardWaveSummary{}
	for _, row := range rows {
		byName[row.Name] = row.Wave
	}
	base := byName["first_all_red"]
	result := []CandidateRetention{}
	for _, name := range sectorComparisonGroups {
		if name == "first_all_red" {
			continue
		}
		for i, w := range byName[name].Windows {
			b := base.Windows[i]
			result = append(result, CandidateRetention{Group: name, Reference: "first_all_red", Days: w.Days, SelectedFraction: sectorRatio(w.Selected, b.Selected), KnownFraction: sectorRatio(w.Known, b.Known), EffectiveCoverage: sectorRatio(w.Known, w.Selected), EntryUnknownFraction: sectorRatio(w.EntryUnknown, w.Selected)})
		}
	}
	return result
}

type sectorMatchCount struct {
	selected       int
	known, success [2]int
}
type sectorMatchGrid map[string]map[string]map[string]*sectorMatchCount

func (grid sectorMatchGrid) add(date, sector string, names []string, wave *strategy.UpwardWave) {
	if grid[date] == nil {
		grid[date] = map[string]map[string]*sectorMatchCount{}
	}
	if grid[date][sector] == nil {
		grid[date][sector] = map[string]*sectorMatchCount{}
	}
	for _, name := range names {
		if grid[date][sector][name] == nil {
			grid[date][sector][name] = &sectorMatchCount{}
		}
		count := grid[date][sector][name]
		count.selected++
		if wave == nil || wave.Model != strategy.UpwardWaveNoDrawdownModel || wave.EntryStatus != "entered" || len(wave.Windows) != 2 {
			continue
		}
		for i, days := range []int{5, 10} {
			w := wave.Windows[i]
			if w.Days != days || w.Full == nil || w.GivebackPct == nil || (w.Status != "success" && w.Status != "failed") || (w.Status == "success" && w.Segment == nil) {
				continue
			}
			count.known[i]++
			if w.Status == "success" {
				count.success[i]++
			}
		}
	}
}

func (grid sectorMatchGrid) compare(ctx context.Context, dates, names []string, month string) ([]SectorMatchedComparison, error) {
	rows := []SectorMatchedComparison{}
	for _, name := range names {
		for wi, days := range []int{5, 10} {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			row := SectorMatchedComparison{Group: name, Days: days, Month: month, UnmatchedReasons: map[string]int{}, BootstrapBlockDays: bootstrapBlock, BootstrapRepetitions: bootstrapRepetitions, BootstrapSeed: bootstrapSeed}
			deltas := make([]*float64, len(dates))
			sum := 0.0
			for di, date := range dates {
				matched := 0
				difference := 0.0
				keys := make([]string, 0, len(grid[date]))
				for code := range grid[date] {
					keys = append(keys, code)
				}
				sort.Strings(keys)
				for _, sector := range keys {
					groups := grid[date][sector]
					candidate := groups[name]
					if candidate == nil {
						continue
					}
					row.Selected += candidate.selected
					c, s := candidate.known[wi], candidate.success[wi]
					row.CandidateKnown += c
					if c == 0 {
						continue
					}
					if sector == "" {
						row.UnmatchedKnown += c
						row.UnmatchedReasons["sector_classification_unknown"] += c
						continue
					}
					base := groups["baseline"]
					if base == nil || base.known[wi]-1 < sectorMinimumOthers {
						row.UnmatchedKnown += c
						row.UnmatchedReasons["insufficient_known_sector_peers"] += c
						continue
					}
					n, b := base.known[wi], base.success[wi]
					if c > n || s > b {
						return nil, errors.New("sector candidate is not a subset of its baseline")
					}
					matched += c
					difference += float64(s) - (float64(c)*float64(b)-float64(s))/float64(n-1)
				}
				row.MatchedKnown += matched
				if matched == 0 {
					row.UnpairedDates++
					continue
				}
				value := difference / float64(matched)
				deltas[di] = &value
				sum += value
				row.PairedDates++
			}
			row.KnownMatchingCoverage = sectorRatio(row.MatchedKnown, row.CandidateKnown)
			row.EffectiveMatchingCoverage = sectorRatio(row.MatchedKnown, row.Selected)
			if row.PairedDates > 0 {
				value := sum / float64(row.PairedDates)
				row.MeanDifference = &value
			}
			var err error
			row.Lower95, row.Upper95, row.BootstrapValidDraws, err = movingBlockInterval(ctx, deltas, bootstrapBlock, bootstrapRepetitions, bootstrapSeed)
			if err != nil {
				return nil, err
			}
			switch {
			case row.PairedDates == 0:
				row.IntervalStatus = "no_paired_dates"
			case row.PairedDates < 2:
				row.IntervalStatus = "insufficient_paired_dates"
			case len(dates) < 2*bootstrapBlock:
				row.IntervalStatus = "insufficient_date_blocks"
			case row.BootstrapValidDraws == 0:
				row.IntervalStatus = "no_valid_bootstrap_draws"
			default:
				row.IntervalStatus = "descriptive_only"
			}
			rows = append(rows, row)
		}
	}
	return rows, nil
}
