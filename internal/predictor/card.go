package predictor

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
)

type CardData struct {
	Names    map[string]string
	Previous []Prediction
	Actuals  []Actual
}

func BuildCard(date string, targetEnd time.Time, model ModelInfo, preds []Prediction, data CardData) (notify.Message, error) {
	if len(preds) == 0 {
		return notify.Message{}, errors.New("predictor card requires at least one prediction")
	}
	rows := make([][]notify.TableCell, 0, len(preds))
	for _, p := range preds {
		high := p.BasePrice * (1 + p.HighPct)
		low := p.BasePrice * (1 + p.LowPct)
		closePrice := p.BasePrice * (1 + p.ClosePct)
		rows = append(rows, []notify.TableCell{
			{Text: stockName(p.Code, data.Names)},
			{Text: fmt.Sprintf("%.2f", p.BasePrice)},
			{Text: fmt.Sprintf("%.2f (%s)", high, pct(p.HighPct)), Tone: tone(p.HighPct)},
			{Text: fmt.Sprintf("%.2f (%s)", low, pct(p.LowPct)), Tone: tone(p.LowPct)},
			{Text: fmt.Sprintf("%.2f (%s)", closePrice, pct(p.ClosePct)), Tone: tone(p.ClosePct)},
			{Text: p.Trend, Tone: trendTone(p.Trend)},
			{Text: p.Confidence},
		})
	}
	warning := ""
	if model.Metrics.Samples > 0 && (!betterThanBaseline(model.Metrics.MAE.High, model.Metrics.BaselineLastMAE.High) ||
		!betterThanBaseline(model.Metrics.MAE.Low, model.Metrics.BaselineLastMAE.Low) ||
		!betterThanBaseline(model.Metrics.MAE.Close, model.Metrics.BaselineLastMAE.Close)) {
		warning = " ⚠ 未优于基线"
	}
	if model.Metrics.Samples > 0 && model.Metrics.Samples < 5000 {
		warning += " ⚠ 样本不足"
	}
	meta := fmt.Sprintf("基准价=上一根5分钟收盘 · 模型 %s · 训练至 %s · 样本 %d%s",
		model.ModelID, model.DataTo, model.Metrics.Samples, warning)
	mae := fmt.Sprintf("上根 MAE：高 %.2f%% / 低 %.2f%% / 收 %.2f%%（基线 高 %.2f%% / 低 %.2f%% / 收 %.2f%%）",
		model.Metrics.MAE.High*100, model.Metrics.MAE.Low*100, model.Metrics.MAE.Close*100,
		model.Metrics.BaselineLastMAE.High*100, model.Metrics.BaselineLastMAE.Low*100, model.Metrics.BaselineLastMAE.Close*100)
	sections := []notify.Section{
		{
			Lines: []string{meta},
			Table: &notify.Table{
				PageSize:          20,
				RowHeight:         "low",
				FreezeFirstColumn: true,
				Columns: []notify.TableColumn{
					{Key: "name", Label: "名称", Kind: notify.TableText, Width: "14%"},
					{Key: "price", Label: "现价", Kind: notify.TableText, Width: "10%"},
					{Key: "high", Label: "预测高", Kind: notify.TableChange, Width: "16%"},
					{Key: "low", Label: "预测低", Kind: notify.TableChange, Width: "16%"},
					{Key: "close", Label: "预测收", Kind: notify.TableChange, Width: "16%"},
					{Key: "trend", Label: "趋势", Kind: notify.TableChange, Width: "14%"},
					{Key: "confidence", Label: "置信", Kind: notify.TableStatus, Width: "14%"},
				},
				Rows: rows,
			},
		},
	}
	if previous := previousResultRows(data.Previous, data.Actuals, data.Names); len(previous) > 0 {
		target := data.Previous[0].TargetEnd.Format("15:04")
		sections = append(sections, notify.Section{
			Heading: "上一根实际对比",
			Lines:   []string{fmt.Sprintf("%s 实际值与该根预测对比；偏差=实际-预测", target)},
			Table: &notify.Table{
				PageSize:          20,
				RowHeight:         "low",
				FreezeFirstColumn: true,
				Columns: []notify.TableColumn{
					{Key: "name", Label: "名称", Kind: notify.TableText, Width: "16%"},
					{Key: "actual_high", Label: "实际高", Kind: notify.TableText, Width: "12%"},
					{Key: "actual_low", Label: "实际低", Kind: notify.TableText, Width: "12%"},
					{Key: "actual_close", Label: "实际收", Kind: notify.TableText, Width: "12%"},
					{Key: "high_diff", Label: "高偏差", Kind: notify.TableChange, Width: "16%"},
					{Key: "low_diff", Label: "低偏差", Kind: notify.TableChange, Width: "16%"},
					{Key: "close_diff", Label: "收偏差", Kind: notify.TableChange, Width: "16%"},
				},
				Rows: previous,
			},
		})
	}
	sections = append(sections, notify.Section{
		Heading:  "模型状态",
		Optional: true,
		Lines: []string{
			mae,
			"实验性预测，不构成投资建议；T+1 手动交易。",
			"预测为点估计，不代表保证区间；数据延迟或模型过期时不推送。",
		},
	})
	return notify.Message{
		Version:        2,
		EventID:        fmt.Sprintf("intraday:%s:%d", date, targetEnd.Unix()),
		DisplayEventID: fmt.Sprintf("intraday:%s", date),
		PartLabel:      "5分钟预测",
		Title:          fmt.Sprintf("A股龙头池 · %s 5分钟预测（实验）", targetEnd.Format("15:04")),
		Template:       "orange",
		TrimPolicy:     notify.TrimNone,
		ExpiresAt:      targetEnd,
		Sections:       sections,
	}, nil
}

func previousResultRows(previous []Prediction, actuals []Actual, names map[string]string) [][]notify.TableCell {
	if len(previous) == 0 {
		return nil
	}
	actualByKey := make(map[string]Actual, len(actuals))
	for _, actual := range actuals {
		actualByKey[predictionKey(actual.Code, actual.ModelID)] = actual
	}
	rows := make([][]notify.TableCell, 0, len(previous))
	for _, prediction := range previous {
		actual, ok := actualByKey[predictionKey(prediction.Code, prediction.ModelID)]
		if !ok || actual.Status != "resolved" || prediction.BasePrice <= 0 {
			rows = append(rows, []notify.TableCell{
				{Text: stockName(prediction.Code, names)},
				{Text: "待确认"},
				{Text: "—"}, {Text: "—"},
				{Text: "—"}, {Text: "—"}, {Text: "—"},
			})
			continue
		}
		predictedHigh := prediction.BasePrice * (1 + prediction.HighPct)
		predictedLow := prediction.BasePrice * (1 + prediction.LowPct)
		predictedClose := prediction.BasePrice * (1 + prediction.ClosePct)
		actualHigh := prediction.BasePrice * (1 + actual.HighPct)
		actualLow := prediction.BasePrice * (1 + actual.LowPct)
		actualClose := prediction.BasePrice * (1 + actual.ClosePct)
		rows = append(rows, []notify.TableCell{
			{Text: stockName(prediction.Code, names)},
			{Text: fmt.Sprintf("%.2f (%s)", actualHigh, pct(actual.HighPct))},
			{Text: fmt.Sprintf("%.2f (%s)", actualLow, pct(actual.LowPct))},
			{Text: fmt.Sprintf("%.2f (%s)", actualClose, pct(actual.ClosePct))},
			{Text: priceDeviation(actualHigh-predictedHigh, actual.HighPct-prediction.HighPct)},
			{Text: priceDeviation(actualLow-predictedLow, actual.LowPct-prediction.LowPct)},
			{Text: priceDeviation(actualClose-predictedClose, actual.ClosePct-prediction.ClosePct)},
		})
	}
	return rows
}

func predictionKey(code, modelID string) string {
	return code + "\x00" + modelID
}

func priceDeviation(price, points float64) string {
	return fmt.Sprintf("%s (%s)", signedNumber(price), signedPercentPoints(points))
}

func signedNumber(value float64) string {
	if value > 0 {
		return fmt.Sprintf("+%.2f", value)
	}
	return fmt.Sprintf("%.2f", value)
}

func signedPercentPoints(value float64) string {
	if value > 0 {
		return fmt.Sprintf("+%.2fpp", value*100)
	}
	return fmt.Sprintf("%.2fpp", value*100)
}

func stockName(code string, names map[string]string) string {
	if name := strings.TrimSpace(names[code]); name != "" {
		return name
	}
	return code
}

func pct(value float64) string {
	sign := ""
	if value > 0 {
		sign = "+"
	}
	return fmt.Sprintf("%s%.2f%%", sign, value*100)
}

func tone(value float64) notify.TableTone {
	switch {
	case value > 0:
		return notify.TonePositive
	case value < 0:
		return notify.ToneNegative
	default:
		return notify.ToneDefault
	}
}

func trendTone(trend string) notify.TableTone {
	switch {
	case strings.Contains(trend, "强"):
		return notify.TonePositive
	case strings.Contains(trend, "弱"):
		return notify.ToneNegative
	default:
		return notify.ToneDefault
	}
}

func betterThanBaseline(actual, baseline float64) bool {
	if baseline <= 0 {
		return true
	}
	return actual < baseline
}
