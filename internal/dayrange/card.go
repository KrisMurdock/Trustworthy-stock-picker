package dayrange

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
)

const (
	premarketCardByteLimit = 16 * 1024
	premarketCardMaxRows   = 40
)

// CardInput freezes everything needed to render a premarket delivery card.
// Predictions are expected to belong to exactly one target trading day.
type CardInput struct {
	TargetDate   string
	GeneratedAt  time.Time
	InputThrough string
	PoolSize     int
	Predictions  []Prediction
	Names        map[string]string
	Skipped      []string
}

// BuildPremarketCards builds deterministic, page-sized Feishu messages. Event
// IDs contain only the target date and page number so retries and restarts do
// not send a second copy of the same page.
func BuildPremarketCards(input CardInput) ([]notify.Message, error) {
	if len(input.Predictions) == 0 {
		return nil, errors.New("dayrange card requires at least one prediction")
	}
	target, err := time.ParseInLocation(time.DateOnly, input.TargetDate, shanghai)
	if err != nil || target.Format(time.DateOnly) != input.TargetDate {
		return nil, errors.New("dayrange card target date is invalid")
	}
	if input.GeneratedAt.IsZero() {
		return nil, errors.New("dayrange card generation time is required")
	}
	if input.PoolSize <= 0 {
		input.PoolSize = len(input.Predictions)
	}
	if input.PoolSize < len(input.Predictions) {
		input.PoolSize = len(input.Predictions)
	}
	predictions := append([]Prediction(nil), input.Predictions...)
	sort.Slice(predictions, func(i, j int) bool { return predictions[i].Code < predictions[j].Code })
	if input.InputThrough == "" {
		for _, prediction := range predictions {
			if prediction.InputThrough > input.InputThrough {
				input.InputThrough = prediction.InputThrough
			}
		}
	}
	rows := make([][]notify.TableCell, 0, len(predictions))
	for _, prediction := range predictions {
		rows = append(rows, premarketRow(prediction, input.Names))
	}
	pages := make([][][]notify.TableCell, 0, len(rows)/premarketCardMaxRows+1)
	current := make([][]notify.TableCell, 0, premarketCardMaxRows)
	for _, row := range rows {
		if len(current) >= premarketCardMaxRows {
			pages = append(pages, current)
			current = make([][]notify.TableCell, 0, premarketCardMaxRows)
		}
		candidate := append(append([][]notify.TableCell(nil), current...), row)
		message := buildPremarketCard(input, candidate, len(pages)+1, 999)
		encoded, err := json.Marshal(message)
		if err != nil {
			return nil, err
		}
		if len(encoded) > premarketCardByteLimit && len(current) > 0 {
			pages = append(pages, current)
			current = [][]notify.TableCell{row}
			continue
		}
		if len(encoded) > premarketCardByteLimit {
			return nil, fmt.Errorf("dayrange card row for %s exceeds the payload limit", row[0].Text)
		}
		current = candidate
	}
	if len(current) > 0 {
		pages = append(pages, current)
	}
	if len(pages) == 0 {
		return nil, errors.New("dayrange card has no pages")
	}
	out := make([]notify.Message, 0, len(pages))
	for index, page := range pages {
		message := buildPremarketCard(input, page, index+1, len(pages))
		encoded, err := json.Marshal(message)
		if err != nil {
			return nil, err
		}
		if len(encoded) > premarketCardByteLimit {
			return nil, fmt.Errorf("dayrange card page %d exceeds the payload limit", index+1)
		}
		out = append(out, message)
	}
	return out, nil
}

func buildPremarketCard(input CardInput, rows [][]notify.TableCell, page, pages int) notify.Message {
	target, _ := time.ParseInLocation(time.DateOnly, input.TargetDate, shanghai)
	expiresAt := time.Date(target.Year(), target.Month(), target.Day(), 9, 30, 0, 0, shanghai)
	title := "全天高低价预测 · " + input.TargetDate
	if pages > 1 {
		title += fmt.Sprintf("（%d/%d）", page, pages)
	}
	unpublished := input.PoolSize - len(input.Predictions)
	if unpublished < 0 {
		unpublished = 0
	}
	lines := []string{
		"参考价为上一完成交易日不复权收盘；预测高/低为全天点估计，区间为高、低各自的边际区间，不代表两者联合覆盖。",
		"盘前研究输出，不是成功概率，不构成投资建议。",
	}
	if skipped := skippedSummary(input.Skipped); skipped != "" {
		lines = append(lines, skipped)
	}
	return notify.Message{
		Version:        2,
		EventID:        fmt.Sprintf("dayrange:premarket:%s:%d", input.TargetDate, page),
		DisplayEventID: "dayrange:premarket:" + input.TargetDate,
		PartLabel:      fmt.Sprintf("盘前预测 %d/%d", page, pages),
		Title:          title,
		Subtitle:       fmt.Sprintf("盘前预测 · 输入截止 %s · 生成于 %s", valueOrDash(input.InputThrough), input.GeneratedAt.In(shanghai).Format("2006-01-02 15:04")),
		HeaderIcon:     "insert-chart_outlined",
		WidthMode:      "fill",
		Template:       "blue",
		TrimPolicy:     notify.TrimNone,
		ExpiresAt:      expiresAt,
		Sections: []notify.Section{
			{
				Fields: []notify.Field{
					{Label: "目标交易日", Value: input.TargetDate},
					{Label: "数据截止", Value: valueOrDash(input.InputThrough)},
					{Label: "覆盖", Value: fmt.Sprintf("%d/%d", len(input.Predictions), input.PoolSize)},
					{Label: "未发布", Value: fmt.Sprintf("%d 只", unpublished)},
					{Label: "模型", Value: modelSummary(input.Predictions)},
					{Label: "本页", Value: fmt.Sprintf("第 %d/%d 页 · %d 只", page, pages, len(rows))},
				},
			},
			{
				Heading: "📈 全天高低价",
				Lines:   lines,
				Table: &notify.Table{
					PageSize:          premarketCardMaxRows,
					RowHeight:         "low",
					FreezeFirstColumn: true,
					Columns: []notify.TableColumn{
						{Key: "name", Label: "股票", Kind: notify.TableText, Width: "24%"},
						{Key: "reference", Label: "参考", Kind: notify.TableText, Width: "11%", Align: "right"},
						{Key: "high", Label: "预测高", Kind: notify.TableChange, Width: "13%", Align: "right"},
						{Key: "low", Label: "预测低", Kind: notify.TableChange, Width: "13%", Align: "right"},
						{Key: "high_range", Label: "高区间", Kind: notify.TableText, Width: "15%", Align: "right"},
						{Key: "low_range", Label: "低区间", Kind: notify.TableText, Width: "15%", Align: "right"},
						{Key: "amplitude", Label: "振幅", Kind: notify.TableText, Width: "9%", Align: "right"},
					},
					Rows: rows,
				},
			},
		},
	}
}

func premarketRow(prediction Prediction, names map[string]string) []notify.TableCell {
	name := strings.TrimSpace(names[prediction.Code])
	if name == "" {
		name = prediction.Code
	} else {
		name += " (" + prediction.Code + ")"
	}
	highPct := 0.0
	lowPct := 0.0
	amplitude := 0.0
	if prediction.ReferencePrice > 0 {
		highPct = (prediction.PointHigh/prediction.ReferencePrice - 1) * 100
		lowPct = (prediction.PointLow/prediction.ReferencePrice - 1) * 100
		amplitude = (prediction.PointHigh - prediction.PointLow) / prediction.ReferencePrice * 100
	}
	return []notify.TableCell{
		{Text: name},
		{Text: fmt.Sprintf("%.2f", prediction.ReferencePrice)},
		{Text: fmt.Sprintf("%.2f (%+.2f%%)", prediction.PointHigh, highPct), Tone: toneFor(highPct)},
		{Text: fmt.Sprintf("%.2f (%+.2f%%)", prediction.PointLow, lowPct), Tone: toneFor(lowPct)},
		{Text: intervalText(prediction.IntervalHigh)},
		{Text: intervalText(prediction.IntervalLow)},
		{Text: fmt.Sprintf("%.2f%%", amplitude)},
	}
}

func intervalText(interval *Interval) string {
	if interval == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f–%.2f", interval.Lower, interval.Upper)
}

func toneFor(value float64) notify.TableTone {
	switch {
	case value > 0:
		return notify.TonePositive
	case value < 0:
		return notify.ToneNegative
	default:
		return notify.ToneDefault
	}
}

func modelSummary(predictions []Prediction) string {
	modelCount := 0
	baselineCount := 0
	modelIDs := map[string]bool{}
	for _, prediction := range predictions {
		if prediction.Source == "model" {
			modelCount++
			if prediction.ModelID != "" {
				modelIDs[prediction.ModelID] = true
			}
		} else {
			baselineCount++
		}
	}
	switch {
	case modelCount == 0:
		return fmt.Sprintf("基线 %d 只", baselineCount)
	case baselineCount == 0 && len(modelIDs) == 1:
		for id := range modelIDs {
			if len(id) > 18 {
				id = id[:18]
			}
			return "树模型 " + id
		}
	case baselineCount == 0:
		return fmt.Sprintf("树模型 %d 只 · %d 个模型", modelCount, len(modelIDs))
	default:
		return fmt.Sprintf("树模型 %d 只 · 基线 %d 只", modelCount, baselineCount)
	}
	return "基线"
}

func skippedSummary(skipped []string) string {
	counts := map[string]int{}
	for _, item := range skipped {
		reason := "unknown"
		if index := strings.LastIndex(item, ":"); index >= 0 && index+1 < len(item) {
			reason = item[index+1:]
		}
		if reason == "already_published" {
			continue
		}
		counts[reason]++
	}
	if len(counts) == 0 {
		return ""
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%s %d", reason, counts[reason]))
	}
	return "本次跳过：" + strings.Join(parts, " · ")
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}
