// Package experiment owns immutable, reproducible development experiment records.
package experiment

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"sort"
	"strings"
	"time"
)

const Engine = "indicator_selection_v7"

type Calendar struct {
	Version  int      `json:"schema_version,omitempty"`
	From     string   `json:"from"`
	Through  string   `json:"through"`
	Holidays []string `json:"holidays"`
	Days     []string `json:"days,omitempty"`
	Source   string   `json:"source,omitempty"`
	Digest   string   `json:"digest,omitempty"`
}
type Excluded struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}
type DataSummary struct {
	ID            string          `json:"id"`
	AsOf          string          `json:"as_of"`
	From          string          `json:"from"`
	PoolSize      int             `json:"pool_size"`
	Included      int             `json:"included"`
	Excluded      []Excluded      `json:"excluded"`
	Calendar      Calendar        `json:"calendar"`
	Parameters    config.Strategy `json:"parameters"`
	TopN          int             `json:"top_n"`
	MinuteFrom    string          `json:"minute_from,omitempty"`
	MinuteThrough string          `json:"minute_through,omitempty"`
	MinuteStocks  int             `json:"minute_stocks,omitempty"`
}
type Snapshot struct {
	Version int `json:"schema_version"`
	DataSummary
	Stocks   []config.Stock         `json:"stocks"`
	Adjusted []market.History       `json:"adjusted"`
	Raw      []market.History       `json:"raw"`
	Minutes  []market.MinuteHistory `json:"minutes,omitempty"`
}
type Spec struct {
	Title           string                  `json:"title"`
	Direction       string                  `json:"direction"`
	Hypothesis      string                  `json:"hypothesis"`
	Change          string                  `json:"change"`
	RequireEvidence bool                    `json:"require_risk_evidence,omitempty"`
	MaxUpperWick    *float64                `json:"max_upper_wick,omitempty"`
	NextDayRule     string                  `json:"next_day_rule,omitempty"`
	EntryMode       string                  `json:"entry_mode,omitempty"`
	SignalRule      string                  `json:"signal_rule,omitempty"`
	Range           *strategy.BacktestRange `json:"signal_range,omitempty"`
	Criteria        string                  `json:"criteria"`
}
type Finding struct {
	Kind     string   `json:"kind"`
	Text     string   `json:"text"`
	Evidence []string `json:"evidence"`
}
type Group struct {
	Kind  string         `json:"kind"`
	Name  string         `json:"name"`
	Stats strategy.Stats `json:"stats"`
}
type Results struct {
	Development strategy.DevelopmentResult `json:"development"`
	Groups      []Group                    `json:"groups"`
	Findings    []Finding                  `json:"findings"`
	Next        []string                   `json:"next"`
}
type Holdout struct {
	From         string `json:"from"`
	Through      string `json:"through"`
	Contaminated bool   `json:"contaminated,omitempty"`
}
type Record struct {
	Version        int             `json:"schema_version"`
	ID             string          `json:"id"`
	Stage          string          `json:"stage,omitempty"`
	ParentID       string          `json:"parent_id,omitempty"`
	BaselineID     string          `json:"baseline_id"`
	Engine         string          `json:"engine"`
	CodeVersion    string          `json:"code_version"`
	CreatedAt      time.Time       `json:"created_at"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
	Status         string          `json:"status"`
	Verdict        string          `json:"verdict"`
	Error          string          `json:"error,omitempty"`
	Spec           Spec            `json:"spec"`
	Data           DataSummary     `json:"data"`
	Result         *Results        `json:"result,omitempty"`
	Review         *Review         `json:"current_review,omitempty"`
	ResultSnapshot *ResultSnapshot `json:"result_snapshot,omitempty"`
	Holdout        *Holdout        `json:"holdout,omitempty"`
}

func ValidID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && id == hex.EncodeToString(b)
}
func Specification(policy string) (Spec, error) {
	if policy == "conditional-entry" {
		return conditionalSpecification("")
	}
	if strings.HasPrefix(policy, "conditional-") {
		return conditionalSpecification(strings.TrimPrefix(policy, "conditional-"))
	}
	if strings.HasPrefix(policy, "next-day-") {
		return nextDaySpecification(strings.TrimPrefix(policy, "next-day-"))
	}
	s := Spec{Direction: "建立基线", Title: "现有机会排序 · 开发期基线", Hypothesis: "测量现有机会排序在同一数据快照上的目标触达、实际退出收益、风险与样本覆盖，作为后续对照。", Change: "沿用现有机会评分与证据规则，每个信号日选择前 N 名。", Criteria: "硬条件：每笔扣费后净亏损严格低于实际投入的 3%，等于 3% 也否决；未决交易不能视为达标。至少 30 个完整信号日和 30 笔完成交易后，按可卖出持仓窗口内最高价扣除成本及滑点是否触达 2.5%／3% 验证盈利机会，不要求收盘价达标；触达率越高、首次触达天数越短越好。固定 Top N 槽位不足按现金 0，数据未知使该信号日不完整。日内先止损还是先触达不明时单独标记且不计触达成功，触达不等于实际成交。实际退出平均净收益须为正；软指标相互退让时标为取舍，不抵消风险超限。仅作开发候选，最终区间未验证。"}
	if policy == "baseline" {
		return s, nil
	}
	if policy == "risk-qualified" {
		s.RequireEvidence = true
		s.Direction = "先通过历史亏损与证据门槛"
		s.Title = "亏损硬约束 · 历史证据过滤"
		s.Hypothesis = "只入选当时已完成历史样本未出现净亏损达到 3%、没有未决、且满足证据门槛的候选，检验能否降低后续超限。"
		s.Change = "在原 Top N 排序前先要求历史推荐门槛通过；证据仅来自信号日前已完整结束的交易，不使用本次交易未来结果筛选。无合格候选时空仓。"
		return s, nil
	}
	if policy != "upper-wick" {
		return s, errors.New("experiment policy must be baseline, upper-wick or risk-qualified")
	}
	v := 0.3
	s.MaxUpperWick = &v
	s.Direction = "减少长上影线信号"
	s.Title = "上影线过滤 · 单变量对照"
	s.Hypothesis = "剔除上影线占当日振幅超过 30% 的信号，可能减少冲高回落造成的亏损。"
	s.Change = "仅新增上影线占比 ≤ 0.30 的入选条件；不足前 N 名时按原排序顺延，其余规则与交易假设不变。"
	return s, nil
}
func NewSnapshot(c config.Config, asof string, adjusted, raw []market.History) (Snapshot, error) {
	s := Snapshot{Version: 1, DataSummary: DataSummary{AsOf: asof, PoolSize: len(c.Stocks), Excluded: []Excluded{}, Calendar: Calendar{From: c.Calendar.ValidFrom, Through: c.Calendar.ValidThrough, Holidays: append([]string{}, c.Calendar.Holidays...)}, Parameters: c.Strategy, TopN: c.Selection.TopN}, Stocks: append([]config.Stock{}, c.Stocks...), Adjusted: []market.History{}, Raw: []market.History{}}
	if c.Daily.Adjust != "qfq" {
		return s, errors.New("experiments require saved qfq and raw histories")
	}
	s.Parameters.Enabled = true
	s.Parameters.Version = 1
	if err := s.Parameters.Validate(); err != nil {
		return s, err
	}
	a, b := map[string]market.History{}, map[string]market.History{}
	for _, h := range adjusted {
		a[h.Code] = h
	}
	for _, h := range raw {
		b[h.Code] = h
	}
	sort.Slice(s.Stocks, func(i, j int) bool { return s.Stocks[i].Code < s.Stocks[j].Code })
	for _, stock := range s.Stocks {
		h, ok := a[stock.Code]
		r, found := b[stock.Code]
		reason := ""
		if !ok || !found {
			reason = "missing_saved_pair"
		} else if h.Validate() != nil || r.Validate() != nil || h.AsOf != asof || r.AsOf != asof || h.Adjust != "qfq" || h.DataField != "qfqday" || r.Adjust != "none" {
			reason = "invalid_saved_pair"
		} else {
			bars := []market.Bar{}
			start := h.Bars[0].Date
			for _, bar := range r.Bars {
				if bar.Date >= start && bar.Date <= asof {
					bars = append(bars, bar)
				}
			}
			r.Bars = bars
			if len(h.Bars) != len(r.Bars) {
				reason = "unaligned_saved_pair"
			} else {
				for i := range h.Bars {
					if h.Bars[i].Date != r.Bars[i].Date {
						reason = "unaligned_saved_pair"
						break
					}
				}
			}
		}
		if reason != "" {
			s.Excluded = append(s.Excluded, Excluded{stock.Code, stock.Name, reason})
			continue
		}
		s.Adjusted = append(s.Adjusted, h)
		s.Raw = append(s.Raw, r)
		if s.From == "" || h.Bars[0].Date > s.From {
			s.From = h.Bars[0].Date
		}
	}
	s.Included = len(s.Adjusted)
	// A newly created snapshot freezes the configured exchange calendar when it
	// covers the full saved window. Production-scale history extends beyond the
	// hand-maintained calendar, so the CLI replaces this with a provider-backed
	// history before evaluation.
	if days, err := calendar.New(c).TradingDays(s.From, asof); err == nil {
		if history, err := calendar.NewHistory("configured", s.Calendar.From, s.Calendar.Through, days, time.Now()); err == nil {
			s.Calendar.Version = 1
			s.Calendar.Days = days
			s.Calendar.Source = "configured"
			s.Calendar.Digest = history.Digest
		}
	}
	// The digest includes actual bars, sources, fetch metadata, universe, calendar,
	// parameters and protocol version. Children reuse this payload without reloading.
	data, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	hash := sha256.Sum256(data)
	s.ID = hex.EncodeToString(hash[:])
	return s, nil
}
func NewRecord(snapshot Snapshot, spec Spec, parent *Record, version string) (Record, error) {
	if spec.EntryMode != "" && len(snapshot.Minutes) == 0 {
		return Record{}, errors.New("conditional entry requires a snapshot with saved five-minute bars; start a new experiment without -parent")
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return Record{}, err
	}
	r := Record{Version: 1, ID: hex.EncodeToString(token), Stage: "development", Engine: Engine, CodeVersion: version, CreatedAt: time.Now().UTC(), Status: "running", Verdict: "pending", Spec: spec, Data: snapshot.DataSummary}
	r.BaselineID = r.ID
	if parent != nil {
		if parent.Status != "completed" || parent.Data.ID != snapshot.ID {
			return r, errors.New("parent must be a completed experiment on the same snapshot")
		}
		r.ParentID = parent.ID
		r.BaselineID = BaselineFor(r, *parent)
	}
	return r, nil
}

// NewHoldoutRecord creates a child that consumes the parent's reserved range
// with the exact frozen rule. The parent development record is never mutated.
func NewHoldoutRecord(snapshot Snapshot, parent Record, contaminated bool) (Record, error) {
	if parent.Status != "completed" || parent.Result == nil || parent.Result.Development.HoldoutFrom == "" || parent.Result.Development.HoldoutThrough == "" {
		return Record{}, errors.New("parent experiment has no reserved holdout interval")
	}
	if parent.Stage == "holdout" {
		return Record{}, errors.New("a holdout experiment cannot reserve another holdout")
	}
	if parent.Data.ID != snapshot.ID {
		return Record{}, errors.New("holdout snapshot differs from its parent")
	}
	if parent.Spec.EntryMode != "" && len(snapshot.Minutes) == 0 {
		return Record{}, errors.New("conditional holdout requires the parent snapshot's saved minute paths")
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return Record{}, err
	}
	spec := parent.Spec
	spec.Range = nil
	baseline := parent.BaselineID
	if baseline == "" {
		baseline = parent.ID
	}
	return Record{
		Version: 1, ID: hex.EncodeToString(token), Stage: "holdout",
		ParentID: parent.ID, BaselineID: baseline, Engine: parent.Engine,
		CodeVersion: parent.CodeVersion, CreatedAt: time.Now().UTC(),
		Status: "running", Verdict: "pending", Spec: spec,
		Data:    snapshot.DataSummary,
		Holdout: &Holdout{From: parent.Result.Development.HoldoutFrom, Through: parent.Result.Development.HoldoutThrough, Contaminated: contaminated},
	}, nil
}

// BaselineFor keeps a new execution/engine baseline separate while preserving
// the parent snapshot and provenance. Storage uses this same lineage contract.
func BaselineFor(record, parent Record) string {
	if record.Stage == "holdout" {
		if parent.BaselineID != "" {
			return parent.BaselineID
		}
		return parent.ID
	}
	if record.Spec.NextDayRule == "baseline" && parent.Spec.NextDayRule == "" {
		return record.ID
	}
	if record.Spec.EntryMode == strategy.ConditionalEntryMode && record.Spec.SignalRule == "" &&
		(parent.Engine != record.Engine || parent.CodeVersion != record.CodeVersion || parent.Spec.EntryMode != record.Spec.EntryMode || parent.Spec.SignalRule != "" || !sameRange(record.Spec.Range, parent.Spec.Range)) {
		return record.ID
	}
	return parent.BaselineID
}

func Comparable(a, b Record) bool {
	return a.Engine == b.Engine && a.CodeVersion == b.CodeVersion && a.Data.ID == b.Data.ID && a.Status == "completed" && b.Status == "completed" && (a.Spec.NextDayRule != "") == (b.Spec.NextDayRule != "") && a.Spec.EntryMode == b.Spec.EntryMode && sameRange(a.Spec.Range, b.Spec.Range)
}
func Evaluate(ctx context.Context, snapshot Snapshot, record *Record, parent *Record) error {
	if record.Stage == "holdout" {
		return EvaluateHoldout(ctx, snapshot, record, parent)
	}
	c := config.Defaults()
	c.Strategy = snapshot.Parameters
	c.Stocks = snapshot.Stocks
	c.Selection.TopN = snapshot.TopN
	c.Calendar.ValidFrom = snapshot.Calendar.From
	c.Calendar.ValidThrough = snapshot.Calendar.Through
	c.Calendar.Holidays = snapshot.Calendar.Holidays
	c.Calendar.TradingDays = snapshot.Calendar.Days
	c.Calendar.Source = snapshot.Calendar.Source
	c.Calendar.Digest = snapshot.Calendar.Digest
	var x strategy.DevelopmentResult
	var err error
	if record.Spec.EntryMode == strategy.ConditionalEntryMode {
		x, err = strategy.AnalyzeConditional(ctx, c, snapshot.Adjusted, snapshot.Raw, snapshot.Minutes, snapshot.AsOf, record.Spec.Range, record.Spec.SignalRule)
	} else {
		x, err = strategy.AnalyzeDevelopment(ctx, c, snapshot.Adjusted, snapshot.Raw, snapshot.AsOf, strategy.EntryPolicy{MaxUpperWick: record.Spec.MaxUpperWick, RequireEvidence: record.Spec.RequireEvidence, NextDayRule: record.Spec.NextDayRule, Range: record.Spec.Range})
	}
	if err != nil {
		return err
	}
	result := &Results{Development: x, Groups: []Group{}, Findings: []Finding{}, Next: []string{}}
	stockNames := map[string]config.Stock{}
	for _, s := range snapshot.Stocks {
		stockNames[s.Code] = s
	}
	groups := map[string][]strategy.ExperimentTrade{}
	for _, t := range x.Trades {
		if !t.Selected {
			continue
		}
		s := stockNames[t.Code]
		for _, k := range []string{"month/" + t.Date[:7], "stock/" + t.Code, "sector/" + s.Sector} {
			groups[k] = append(groups[k], t)
		}
	}
	keys := []string{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		split := 0
		for k[split] != '/' {
			split++
		}
		result.Groups = append(result.Groups, Group{k[:split], k[split+1:], strategy.SummarizeExperimentTradesWithCohort(groups[k], x.Research.Parameters, snapshot.TopN)})
	}
	stats := x.Research.Development.TopRanked
	record.Result = result
	record.Status = "completed"
	record.Verdict = "baseline"
	losses := append([]strategy.ExperimentTrade{}, x.Trades...)
	sort.Slice(losses, func(i, j int) bool { return losses[i].Outcome.NetPct < losses[j].Outcome.NetPct })
	evidence := []string{}
	for _, t := range losses {
		if t.Selected && strategy.ResolvedOutcome(t.Outcome) && t.Outcome.NetPct < 0 {
			evidence = append(evidence, t.ID)
			if len(evidence) == 3 {
				break
			}
		}
	}
	result.Findings = append(result.Findings, Finding{"measured", fmt.Sprintf("开发期共 %d 个信号日期、%d 笔完成交易、%d 笔未决交易、%d 次跳过入场。", stats.Dates, stats.Resolved, stats.Unresolved, stats.SkippedEntries), []string{}})
	if stats.HighTouchSamples > 0 {
		result.Findings = append(result.Findings, Finding{"measured", fmt.Sprintf("已确认持仓内最高价扣费后触达 2.5%% 的样本 %d 笔，触达 3%% 的样本 %d 笔；%d 笔退出日内先后顺序不明，%d 笔触达证据缺失或未决。触达与实际退出盈亏分别统计，不以收盘价判断目标触达。", stats.HighTouch25, stats.HighTouch30, stats.HighTouchAmbiguous, stats.HighTouchUnknown), []string{}})
	}
	if len(evidence) > 0 {
		result.Findings = append(result.Findings, Finding{"measured", "损失最大的已完成样本需要复查入场形态、跳空及止损执行。", evidence})
	}
	result.Findings = append(result.Findings, Finding{"limitation", "样本来自当前股池及成功保存的数据；存在股池选择偏差。日线模拟并非实际成交，单笔统计不等于资金组合收益。", []string{}})
	record.Verdict = developmentVerdict(*record, parent)
	if record.Spec.EntryMode != "" {
		if selection := x.Selection; selection != nil {
			result.Findings = append(result.Findings, Finding{"measured", fmt.Sprintf("信号日候选 %d 条，信号规则通过 %d 条，合格池内预先选入 %d 条；原全池排序前N中被过滤 %d 条。未触发不按次日行情补选。", selection.Candidates, selection.Eligible, selection.Selected, selection.FilteredOriginalTop), []string{}})
		}
		result.Findings = append(result.Findings, Finding{"measured", fmt.Sprintf("买入日收盘扣成本浮盈为正 %d 笔，不为正 %d 笔，未知 %d 笔；入场路径缺失 %d 条。买入日为正且第三天触达2.5%% / 3%%的联合成功样本 %d / %d，入场分母 %d。", stats.EntryDayPositive, stats.EntryDayNonPositive, stats.EntryDayUnknown, stats.UnknownEntries, stats.Joint25, stats.Joint30, stats.Samples), []string{}})
		result.Findings = append(result.Findings, Finding{"limitation", "本轮为固定分钟规则试运行。早期分钟日期可能与旧日线留出期重叠，已查看日期不得再声称是旧策略未见样本；后续最终验证须重新冻结新数据。", []string{}})
		result.Next = append(result.Next, "核对未触发、跌破区间、入场跳价和缺失分钟的原因；买入日亏损保留为失败，不按收盘结果删样本。", "冻结入场规则并积累更多完整分钟交易日，分别检查买入日正浮盈率与两档联合成功率；没有合格证据不发布买入推荐。")
	}
	if record.Spec.NextDayRule != "" {
		if stats.Samples == 0 || stats.HighTouchSamples == 0 {
			result.Findings = append(result.Findings, Finding{"measured", "没有可核实的次日触达样本，成功率和距 80% 的差距暂不可计算；不能把空仓或未知证据记为 0% 或 100%。", []string{}})
		} else {
			result.Findings = append(result.Findings, Finding{"measured", fmt.Sprintf("次日确认触达净盈利 2.5%%：%d / %d（%.2f%%）；3%%：%d / %d（%.2f%%）。相对 80%% 目标分别差 %.2f / %.2f 个百分点；全部入场（含未决）为分母，买入日及更晚交易日不计成功。", stats.NextDay25, stats.Samples, stats.NextDay25Rate*100, stats.NextDay30, stats.Samples, stats.NextDay30Rate*100, (strategy.NextDayGoalRate-stats.NextDay25Rate)*100, (strategy.NextDayGoalRate-stats.NextDay30Rate)*100), []string{}})
		}
		result.Next = append(result.Next, "分别核对次日未触达、跳空超限和日内顺序不明样本；只用信号日前已知特征提出下一轮规则，不按未来收益剔除交易。", "80% 是观察成功率目标；开发数据筛选过的最优规则仍需冻结后独立验证，不能把多轮挑选后的最高值当作未来胜率。")
	}
	switch record.Verdict {
	case "risk_rejected":
		result.Findings = append(result.Findings, Finding{"measured", fmt.Sprintf("%d 笔完成交易净亏损达到或超过投入的 3%%，本轮风险不达标；平均收益不能抵消超限。", stats.LossBreaches), evidence})
		result.Next = append(result.Next, "优先检查跳空、无法卖出及成本预留造成的超限；只有历史风险门槛通过后再比较盈利目标与持仓时间。")
	case "risk_unverified":
		if stats.Samples == 0 {
			result.Next = append(result.Next, "本轮没有实际入场样本，不能把空仓当作达标策略。先检查历史超限来源与证据覆盖，再用独立实验验证可提前识别的风险特征。")
		} else {
			result.Next = append(result.Next, "先补齐未决交易及数据证据；零笔已完成或无法确定最终亏损时不宣称风险达标。")
		}
	case "insufficient":
		result.Next = append(result.Next, "积累更多独立信号日期，保持本轮规则不变后再验证。")
	case "not_comparable":
		result.Next = append(result.Next, "代码或数据口径已变化，需要在当前引擎下重新运行基线。")
	case "tradeoff":
		result.Next = append(result.Next, "最高价触达率与首次触达周期存在取舍；保留各项原始指标，下一轮单独验证更快触达是否牺牲触达比例。")
	}
	if parent != nil && parent.Result != nil && Comparable(*record, *parent) {
		before := parent.Result.Development.Research.Development.TopRanked
		result.Findings = append(result.Findings, Finding{"measured", fmt.Sprintf("相对父实验：平均净收益变化 %+.4f 个百分点，入场样本变化 %+d，最差单笔变化 %+.4f 个百分点，平均持有天数变化 %+.2f。", stats.MeanNet-before.MeanNet, stats.Samples-before.Samples, stats.WorstNet-before.WorstNet, stats.MeanDays-before.MeanDays), []string{}})
		result.Next = append(result.Next, "检查按月、行业与个股分组，确认差异是否集中在少数日期或股票；未验证前不继续收紧阈值。")
	}
	if record.Spec.SignalRule == strategy.ChanSignalRule {
		result.Findings = append(result.Findings, Finding{"limitation", "缠论分型、笔和中枢只描述已完成的价格结构，不预测未来方向；三级同时通过不代表独立证据或未来成功概率，须核对样本覆盖和未通过样本。", []string{}})
		result.Next = append(result.Next, "在固定区间内与无过滤基线对照：收益变化来自结构过滤还是样本减少；保持 5/15/30 分钟参数冻结，积累新交易日后重新验证。")
	} else if record.Spec.SignalRule != "" {
		result.Findings = append(result.Findings, Finding{"limitation", "RSI、MACD和布林线都是同一价格历史的变换，组合满足不代表独立证据或未来成功概率；同时核对样本覆盖和遗漏的机会。", []string{}})
		result.Next = append(result.Next, "在固定区间内与无过滤基线对照：收益变化来自指标过滤还是样本减少；保持参数冻结，积累新交易日后再验证稳定性。")
	} else if record.Spec.RequireEvidence {
		result.Findings = append(result.Findings, Finding{"hypothesis", "后续可单独检验入场前的波动、跳空频率与流动性是否能够识别超限风险；本轮尚未验证这些解释。", evidence})
	} else {
		result.Findings = append(result.Findings, Finding{"hypothesis", "可在后续独立实验中检查上影线过滤是否受波动水平影响；本轮尚未验证该解释。", evidence})
	}
	if len(result.Next) == 0 {
		result.Next = append(result.Next, "以当前基线为父实验，单独验证上影线 ≤ 0.30 的入选过滤。")
	}
	if record.Spec.Range != nil {
		result.Findings = append(result.Findings, Finding{"limitation", "本轮使用显式信号日期区间 " + record.Spec.Range.From + " 至 " + record.Spec.Range.Through + "，没有声明独立留出区间；已查看区间不能再次作为未见验证集。", []string{}})
		result.Next = append(result.Next, "候选冻结后使用新的未查看区间进行独立验证；不能把本轮显式回测区间的最高结果当作未来成功率。")
	} else {
		result.Next = append(result.Next, "最终保留区间未计算收益；冻结候选后再单独设计最终验证。")
	}
	now := time.Now().UTC()
	record.FinishedAt = &now
	return nil
}

// EvaluateHoldout evaluates only the reserved interval with frozen training
// evidence. A repeated consumption stays inspectable but cannot pass validation.
func EvaluateHoldout(ctx context.Context, snapshot Snapshot, record *Record, parent *Record) error {
	if record.Stage != "holdout" || record.Holdout == nil || parent == nil {
		return errors.New("invalid holdout evaluation")
	}
	if snapshot.Calendar.Version == 0 || len(snapshot.Calendar.Days) == 0 {
		return errors.New("holdout requires a snapshot with a frozen trading calendar")
	}
	c := config.Defaults()
	c.Strategy = snapshot.Parameters
	c.Stocks = snapshot.Stocks
	c.Selection.TopN = snapshot.TopN
	c.Calendar.ValidFrom = snapshot.Calendar.From
	c.Calendar.ValidThrough = snapshot.Calendar.Through
	c.Calendar.Holidays = snapshot.Calendar.Holidays
	c.Calendar.TradingDays = snapshot.Calendar.Days
	c.Calendar.Source = snapshot.Calendar.Source
	c.Calendar.Digest = snapshot.Calendar.Digest
	var x strategy.DevelopmentResult
	var err error
	if record.Spec.EntryMode == strategy.ConditionalEntryMode {
		interval := &strategy.BacktestRange{From: record.Holdout.From, Through: record.Holdout.Through}
		x, err = strategy.AnalyzeConditional(ctx, c, snapshot.Adjusted, snapshot.Raw, snapshot.Minutes, snapshot.AsOf, interval, record.Spec.SignalRule)
		if err == nil {
			hold := x.Research.Development
			hold.Method = "frozen_holdout"
			x.Research.Holdout = hold
			x.Research.Development = strategy.Evaluation{Method: "frozen_training"}
			x.Research.ValidationPassed = !record.Holdout.Contaminated && x.Research.Calendar.Complete && len(strategy.HoldoutFailures(hold)) == 0
			if record.Holdout.Contaminated {
				x.Research.Flags = append(x.Research.Flags, "contaminated")
			}
			x.Research.Verdict = strategy.HoldoutVerdict(x.Research, record.Holdout.Contaminated)
		}
	} else {
		policy := strategy.EntryPolicy{MaxUpperWick: parent.Spec.MaxUpperWick, RequireEvidence: parent.Spec.RequireEvidence, NextDayRule: parent.Spec.NextDayRule}
		x, err = strategy.AnalyzeHoldout(ctx, c, snapshot.Adjusted, snapshot.Raw, snapshot.AsOf, policy, record.Holdout.From, record.Holdout.Through, record.Holdout.Contaminated)
	}
	if err != nil {
		return err
	}
	result := &Results{Development: x, Groups: []Group{}, Findings: []Finding{}, Next: []string{}}
	stockNames := map[string]config.Stock{}
	for _, s := range snapshot.Stocks {
		stockNames[s.Code] = s
	}
	groups := map[string][]strategy.ExperimentTrade{}
	for _, t := range x.Trades {
		if !t.Selected {
			continue
		}
		s := stockNames[t.Code]
		for _, k := range []string{"month/" + t.Date[:7], "stock/" + t.Code, "sector/" + s.Sector} {
			groups[k] = append(groups[k], t)
		}
	}
	keys := []string{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		split := 0
		for k[split] != '/' {
			split++
		}
		result.Groups = append(result.Groups, Group{k[:split], k[split+1:], strategy.SummarizeExperimentTradesWithCohort(groups[k], x.Research.Parameters, snapshot.TopN)})
	}
	hold := x.Research.Holdout
	result.Findings = append(result.Findings, Finding{"measured", fmt.Sprintf("保留区间 %s 至 %s 共 %d 个信号日、%d 个计划槽位、%d 个已成交槽位；平均净收益 %.2f%%。", hold.From, hold.Through, hold.Cohort.SignalDates, hold.Cohort.PlannedSlots, hold.Cohort.FilledSlots, hold.Cohort.MeanNet), []string{}})
	if x.Research.ValidationPassed {
		result.Findings = append(result.Findings, Finding{"measured", "冻结规则在保留区间通过最低证据、均值收益、相对基线与亏损约束。", []string{}})
	} else {
		result.Findings = append(result.Findings, Finding{"limitation", "保留区间未通过验证；结果保留为失败证据，不能据此发布推荐。", []string{}})
	}
	if record.Holdout.Contaminated {
		result.Findings = append(result.Findings, Finding{"limitation", "该快照的保留区间此前已被查看；本次结果不具备独立样本外含义。", []string{}})
	}
	result.Findings = append(result.Findings, Finding{"limitation", "当前股池与行业名称来自今天，历史回溯存在幸存者偏差；即使通过保留区间，也只能标为当前股池下的探索性验证。", []string{}})
	result.Next = append(result.Next, "只有未污染且通过验证的冻结规则才可进入发布评估；失败后应提出新假设并冻结新的未查看区间。")
	record.Result = result
	record.Status = "completed"
	record.Verdict = x.Research.Verdict
	if record.Holdout.Contaminated {
		record.Verdict = "contaminated"
	}
	now := time.Now().UTC()
	record.FinishedAt = &now
	return nil
}

func sameRange(a, b *strategy.BacktestRange) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func ValidateSnapshot(s Snapshot) error {
	id := s.ID
	s.ID = ""
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(b)
	if s.Version != 1 || id != hex.EncodeToString(hash[:]) {
		return errors.New("experiment snapshot digest mismatch")
	}
	if s.Calendar.Version != 0 {
		if s.Calendar.Version != 1 || s.Calendar.From == "" || s.Calendar.Through == "" || s.Calendar.Source == "" || s.Calendar.Digest == "" {
			return errors.New("invalid experiment trading calendar")
		}
		history, err := calendar.NewHistory(s.Calendar.Source, s.Calendar.From, s.Calendar.Through, s.Calendar.Days, time.Now())
		if err != nil || history.Digest != s.Calendar.Digest {
			return errors.New("invalid experiment trading calendar digest")
		}
		previous := ""
		for _, day := range s.Calendar.Days {
			if day < s.Calendar.From || day > s.Calendar.Through || day <= previous {
				return errors.New("invalid experiment trading calendar days")
			}
			previous = day
		}
	}
	return nil
}

// WithTradingCalendar freezes the provider-backed calendar into the snapshot.
// The calendar must cover the whole warm-up and exit window; a partial history
// is rejected instead of silently falling back to the configured calendar.
func WithTradingCalendar(s Snapshot, history calendar.History) (Snapshot, error) {
	if err := history.Validate(); err != nil {
		return s, err
	}
	from, through, ok := snapshotRange(s)
	if !ok {
		return s, errors.New("snapshot has no saved market window")
	}
	covered, err := history.Covers(from, through)
	if err != nil || !covered {
		return s, errors.New("trading calendar does not cover the saved market window")
	}
	s.Calendar.Version = history.Version
	s.Calendar.From = history.From
	s.Calendar.Through = history.Through
	s.Calendar.Days = append([]string{}, history.Days...)
	s.Calendar.Source = history.Source
	s.Calendar.Digest = history.Digest
	s.ID = ""
	data, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	hash := sha256.Sum256(data)
	s.ID = hex.EncodeToString(hash[:])
	return s, nil
}

func snapshotRange(s Snapshot) (string, string, bool) {
	from, through := "", ""
	for _, h := range append(append([]market.History{}, s.Adjusted...), s.Raw...) {
		for _, bar := range h.Bars {
			if from == "" || bar.Date < from {
				from = bar.Date
			}
			if bar.Date > through {
				through = bar.Date
			}
		}
	}
	return from, through, from != "" && through != ""
}

// SnapshotRange exposes the saved warm-up through exit window for calendar
// resolution by the CLI without leaking the snapshot's internal layout.
func SnapshotRange(s Snapshot) (string, string, bool) {
	return snapshotRange(s)
}
