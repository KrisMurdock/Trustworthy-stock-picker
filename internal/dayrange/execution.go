package dayrange

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const (
	executionDeliveryLedgerPath = "state/premarket-execution-delivery.json"
	executionMaxRows            = 30
)

// ReportSource is the read-only market report surface used to join the
// premarket forecast with the latest second-stage ranking. storage.MarketDB
// implements it; test and offline sources may omit it.
type ReportSource interface {
	LoadReport(context.Context, string) (selection.Report, error)
}

type ExecutionCardInput struct {
	SignalDate  string
	TargetDate  string
	GeneratedAt time.Time
	Report      selection.Report
	Predictions []Prediction
}

type executionPlan struct {
	plan   *strategy.Plan
	formal bool
}

// ConfigureExecutionDelivery loads the independent execution-card ledger.
// The caller passes all enabled channels; only the premarket_execution route is
// selected here, so a failure in the dayrange or second-stage bot cannot block
// the execution card.
func (s *Service) ConfigureExecutionDelivery(store DeliveryStore, channels []notify.Channel) error {
	if s == nil {
		return errors.New("dayrange service is incomplete")
	}
	if store == nil {
		return errors.New("premarket execution delivery requires a state store")
	}
	s.executionMu.Lock()
	defer s.executionMu.Unlock()
	s.executionStore = store
	s.executionChannels = append([]notify.Channel(nil), channels...)
	var ledger notify.Ledger
	if err := store.Read(executionDeliveryLedgerPath, &ledger); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		ledger = notify.NewLedger()
	}
	if ledger.Version != 2 {
		return errors.New("unsupported premarket execution delivery ledger")
	}
	if err := ledger.Validate(); err != nil {
		return err
	}
	changed := ledger.Normalize(s.now())
	s.executionLedger = ledger
	if changed {
		return s.saveExecutionLedgerLocked()
	}
	return nil
}

func (s *Service) publishExecutionCard(ctx context.Context, target string, predictions []Prediction, now time.Time) error {
	if s == nil || !s.Config.DayRange.Enabled {
		return nil
	}
	s.executionMu.Lock()
	defer s.executionMu.Unlock()
	if s.executionStore == nil || len(s.executionChannelsLocked()) == 0 {
		return nil
	}
	report, err := s.loadExecutionReport(ctx, target, now)
	if err != nil {
		return err
	}
	if report == nil {
		s.Log.Debug("Premarket execution card skipped", "target_date", target, "reason", "report_missing")
		return nil
	}
	card, ok, err := BuildExecutionCard(ExecutionCardInput{
		SignalDate:  report.AsOf,
		TargetDate:  target,
		GeneratedAt: now,
		Report:      *report,
		Predictions: predictions,
	})
	if err != nil {
		return err
	}
	if !ok {
		s.Log.Debug("Premarket execution card skipped", "target_date", target, "reason", "no_candidates_or_predictions")
		return nil
	}
	channelNames := s.executionChannelsLocked()
	if err := s.executionLedger.EnqueuePart(target, card, channelNames, "execution", 0, ""); err != nil {
		return err
	}
	if err := s.saveExecutionLedgerLocked(); err != nil {
		return err
	}
	return s.flushExecutionLocked(ctx, now)
}

func (s *Service) loadExecutionReport(ctx context.Context, target string, now time.Time) (*selection.Report, error) {
	source, ok := s.Market.(ReportSource)
	if !ok {
		return nil, nil
	}
	if now.IsZero() {
		now = s.now()
	}
	signal, err := calendar.New(s.Config).Completed(now)
	if err != nil {
		return nil, err
	}
	report, err := source.LoadReport(ctx, signal)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if report.AsOf != signal || report.Target != target {
		return nil, nil
	}
	return &report, nil
}

func (s *Service) executionChannelsLocked() []string {
	available := map[string]bool{}
	for _, channel := range s.executionChannels {
		if channel != nil && channel.Name() != "" {
			available[channel.Name()] = true
		}
	}
	names := make([]string, 0)
	for _, name := range s.Config.ChannelNamesForRoute(config.NotifyRoutePremarketExecution) {
		if available[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (s *Service) saveExecutionLedgerLocked() error {
	if s.executionStore == nil {
		return nil
	}
	if s.executionLedger.Version == 0 {
		s.executionLedger.Version = 2
	}
	if s.executionLedger.Events == nil {
		s.executionLedger.Events = map[string]notify.Event{}
	}
	return s.executionStore.Write(executionDeliveryLedgerPath, s.executionLedger)
}

func (s *Service) flushExecutionLocked(ctx context.Context, now time.Time) error {
	if s.executionStore == nil || len(s.executionLedger.Events) == 0 {
		return nil
	}
	allowed := map[string]bool{}
	for _, name := range s.executionChannelsLocked() {
		allowed[name] = true
	}
	channels := map[string]notify.Channel{}
	for _, channel := range s.executionChannels {
		if channel != nil && channel.Name() != "" && allowed[channel.Name()] {
			channels[channel.Name()] = channel
		}
	}
	for _, delivery := range s.executionLedger.Due(now) {
		channel, ok := channels[delivery.Channel]
		if !ok {
			_ = s.executionLedger.MarkFailure(delivery.EventID, delivery.Channel, &notify.DeliveryError{Kind: notify.Permanent, Reason: "channel_missing"}, now)
			continue
		}
		if !s.executionLedger.CanDeliver(delivery.EventID, delivery.Channel) {
			continue
		}
		if err := s.executionStore.Check(); err != nil {
			return err
		}
		if err := s.executionLedger.MarkInFlight(delivery.EventID, delivery.Channel, now); err != nil {
			continue
		}
		if err := s.saveExecutionLedgerLocked(); err != nil {
			return err
		}
		remaining := delivery.Message.ExpiresAt.Sub(s.now())
		if remaining <= 0 {
			s.executionLedger.Expire(s.now())
			continue
		}
		timeout := 10 * time.Second
		if remaining < timeout {
			timeout = remaining
		}
		sendCtx, cancel := context.WithTimeout(ctx, timeout)
		err := channel.Send(sendCtx, delivery.Message)
		cancel()
		if err == nil {
			_ = s.executionLedger.MarkDelivered(delivery.EventID, delivery.Channel)
		} else {
			failure := &notify.DeliveryError{Kind: notify.Retryable, Reason: "send_failed"}
			if classified, ok := err.(*notify.DeliveryError); ok {
				failure = classified
			}
			_ = s.executionLedger.MarkFailure(delivery.EventID, delivery.Channel, failure, s.now())
		}
		if err := s.saveExecutionLedgerLocked(); err != nil {
			return err
		}
	}
	s.executionLedger.Expire(now)
	s.executionLedger.Prune(now, 30*24*time.Hour)
	return s.saveExecutionLedgerLocked()
}

// BuildExecutionCard joins the second-stage Top-N ranking with the already
// frozen premarket high/low forecasts. It never creates a new forecast and
// never changes the ranking. The boolean result is false when the report has no
// usable candidates or none of the candidates has a prediction.
func BuildExecutionCard(input ExecutionCardInput) (notify.Message, bool, error) {
	target := strings.TrimSpace(input.TargetDate)
	parsed, err := time.ParseInLocation(time.DateOnly, target, shanghai)
	if err != nil || parsed.Format(time.DateOnly) != target {
		return notify.Message{}, false, errors.New("premarket execution card requires a valid target date")
	}
	report := input.Report
	if report.SecondStage == nil || report.SecondStage.Status != secondstage.StatusOK || len(report.SecondStage.Candidates) == 0 {
		return notify.Message{}, false, nil
	}
	predictions := map[string]Prediction{}
	matched := 0
	for _, prediction := range input.Predictions {
		if prediction.TargetDate != target {
			continue
		}
		predictions[prediction.Code] = prediction
	}
	plans := executionPlans(report)
	rows := make([][]notify.TableCell, 0, min(len(report.SecondStage.Candidates), executionMaxRows))
	maintain, observe, abandon, missing := 0, 0, 0, 0
	for _, candidate := range report.SecondStage.Candidates {
		if len(rows) >= executionMaxRows {
			break
		}
		prediction, ok := predictions[candidate.Code]
		var predictionPtr *Prediction
		if ok {
			predictionPtr = &prediction
			matched++
		} else {
			missing++
		}
		entry := plans[candidate.Code]
		status, _ := executionStatus(predictionPtr, entry.plan, entry.formal)
		switch status {
		case "可执行":
			maintain++
		case "放弃":
			abandon++
		default:
			observe++
		}
		rows = append(rows, executionRow(candidate, predictionPtr, status, len(rows)+1))
	}
	if matched == 0 {
		return notify.Message{}, false, nil
	}
	expires := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 9, 30, 0, 0, shanghai)
	lines := []string{
		"状态只比较盘前预测与参考计划；09:25 仍需用实际开盘价复核，高开超过参考买入价 3% 或已到目标价时放弃。",
		"可执行=正式推荐且预测高低覆盖目标/止损；观察=无正式推荐或可能先触止损；放弃=预测高不足以覆盖净目标。",
		fmt.Sprintf("状态统计：可执行 %d · 观察 %d · 放弃 %d。", maintain, observe, abandon),
		"盘前研究输出，不是成功概率，不构成投资建议。",
	}
	if report.ExecutionMode == strategy.StructureGuardShadow || !report.SelectionEnabled {
		lines = append(lines, "结构影子期或筛选未启用：不产生可执行指令；放弃仅表示不按参考计划执行，候选仍在观察池。")
	}
	if report.SecondStage.Degraded {
		lines = append(lines, "精排处于降级状态，排序和状态只作观察参考。")
	}
	if missing > 0 {
		lines = append(lines, fmt.Sprintf("%d 只候选缺少盘前预测，按观察处理。", missing))
	}
	if len(report.SecondStage.Candidates) > executionMaxRows {
		lines = append(lines, fmt.Sprintf("仅展示前 %d 只，完整名单见精排文章。", executionMaxRows))
	}
	inputThrough := executionInputThrough(input.Predictions)
	fields := []notify.Field{
		{Label: "目标交易日", Value: target},
		{Label: "信号日", Value: valueOrDash(input.SignalDate)},
		{Label: "数据截止", Value: valueOrDash(inputThrough)},
		{Label: "精排候选", Value: fmt.Sprintf("%d 只", len(report.SecondStage.Candidates))},
		{Label: "正式推荐", Value: fmt.Sprintf("%d 只", len(report.Selected))},
		{Label: "预测覆盖", Value: fmt.Sprintf("%d/%d", matched, len(report.SecondStage.Candidates))},
		{Label: "模型", Value: modelSummary(input.Predictions)},
	}
	message := notify.Message{
		Version:        2,
		EventID:        fmt.Sprintf("premarket-execution:%s", target),
		DisplayEventID: fmt.Sprintf("premarket-execution:%s", target),
		PartLabel:      "盘前执行确认",
		Title:          "盘前执行确认 · " + target,
		Subtitle:       fmt.Sprintf("盘前执行确认 · 生成于 %s · 精排 Top%d", input.GeneratedAt.In(shanghai).Format("2006-01-02 15:04"), len(report.SecondStage.Candidates)),
		HeaderIcon:     "insert-chart_outlined",
		WidthMode:      "fill",
		Template:       "blue",
		TrimPolicy:     notify.TrimNone,
		ExpiresAt:      expires,
		Sections: []notify.Section{
			{Fields: fields},
			{
				Heading: "精排候选 · 盘前预测",
				Lines:   lines,
				Table: &notify.Table{
					PageSize:          executionMaxRows,
					RowHeight:         "middle",
					FreezeFirstColumn: true,
					Columns: []notify.TableColumn{
						{Key: "rank", Label: "精排", Kind: notify.TableText, Width: "5%"},
						{Key: "stock", Label: "标的", Kind: notify.TableText, Width: "22%"},
						{Key: "reference", Label: "参考", Kind: notify.TableText, Width: "9%", Align: "right"},
						{Key: "high", Label: "预测高", Kind: notify.TableChange, Width: "11%", Align: "right"},
						{Key: "low", Label: "预测低", Kind: notify.TableChange, Width: "11%", Align: "right"},
						{Key: "high_range", Label: "高区间", Kind: notify.TableText, Width: "14%", Align: "right"},
						{Key: "low_range", Label: "低区间", Kind: notify.TableText, Width: "14%", Align: "right"},
						{Key: "status", Label: "状态", Kind: notify.TableStatus, Width: "14%"},
					},
					Rows: rows,
				},
			},
		},
	}
	return message, true, nil
}

func executionPlans(report selection.Report) map[string]executionPlan {
	out := map[string]executionPlan{}
	for _, candidate := range report.Ranking {
		if candidate.Plan != nil {
			out[candidate.Code] = executionPlan{plan: candidate.Plan}
		}
	}
	for _, candidate := range report.Selected {
		entry := out[candidate.Code]
		if candidate.Ranking != nil && candidate.Ranking.Plan != nil {
			entry.plan = candidate.Ranking.Plan
		}
		if entry.plan != nil && report.SelectionEnabled && report.ExecutionMode != strategy.StructureGuardShadow {
			entry.formal = true
		}
		out[candidate.Code] = entry
	}
	return out
}

func executionStatus(prediction *Prediction, plan *strategy.Plan, formal bool) (string, string) {
	if prediction == nil || prediction.ReferencePrice <= 0 || prediction.PointHigh <= 0 || prediction.PointLow <= 0 {
		return "观察", "盘前预测缺失"
	}
	if plan == nil || plan.EntryPrice <= 0 || plan.TakeProfit <= 0 || plan.StopPrice <= 0 {
		return "观察", "无正式推荐"
	}
	if prediction.PointHigh <= plan.EntryPrice {
		return "放弃", "预测高未高于参考买入价"
	}
	if prediction.PointHigh < plan.TakeProfit {
		return "放弃", "预测高低于净目标价"
	}
	if !formal {
		return "观察", "无正式推荐"
	}
	if prediction.PointLow < plan.StopPrice {
		return "观察", "预测低低于净止损价"
	}
	return "可执行", "预测高低覆盖参考目标与止损"
}

func executionRow(candidate secondstage.Candidate, prediction *Prediction, status string, fallbackRank int) []notify.TableCell {
	rank := candidate.Rank
	if rank <= 0 {
		rank = fallbackRank
	}
	name := strings.TrimSpace(candidate.Name)
	if name == "" {
		name = candidate.Code
	}
	if prediction == nil {
		return []notify.TableCell{
			{Text: fmt.Sprintf("%d", rank)},
			{Text: name},
			{Text: "—"},
			{Text: "—"},
			{Text: "—"},
			{Text: "—"},
			{Text: "—"},
			{Text: status},
		}
	}
	highPct := (prediction.PointHigh/prediction.ReferencePrice - 1) * 100
	lowPct := (prediction.PointLow/prediction.ReferencePrice - 1) * 100
	return []notify.TableCell{
		{Text: fmt.Sprintf("%d", rank)},
		{Text: name},
		{Text: fmt.Sprintf("%.2f", prediction.ReferencePrice)},
		{Text: fmt.Sprintf("%.2f (%+.2f%%)", prediction.PointHigh, highPct), Tone: toneFor(highPct)},
		{Text: fmt.Sprintf("%.2f (%+.2f%%)", prediction.PointLow, lowPct), Tone: toneFor(lowPct)},
		{Text: intervalText(prediction.IntervalHigh)},
		{Text: intervalText(prediction.IntervalLow)},
		{Text: status},
	}
}

func executionInputThrough(predictions []Prediction) string {
	latest := ""
	for _, prediction := range predictions {
		if prediction.InputThrough > latest {
			latest = prediction.InputThrough
		}
	}
	return latest
}
