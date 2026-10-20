package predictor

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
)

type CardData struct {
	Names map[string]string
}

type previousComparison struct {
	Prediction Prediction
	Actual     Actual
	Resolved   bool
}

func BuildCard(date string, targetEnd time.Time, model ModelInfo, preds []Prediction, data CardData) (notify.Message, error) {
	if len(preds) == 0 {
		return notify.Message{}, errors.New("predictor card requires at least one prediction")
	}
	window := predictionWindow(preds, targetEnd)
	sections := []notify.Section{
		{
			Fields: predictionSummaryFields(preds, window),
		},
		{
			Heading: "📊 本轮预测",
			Table: &notify.Table{
				PageSize:          20,
				RowHeight:         "low",
				FreezeFirstColumn: true,
				Columns: []notify.TableColumn{
					{Key: "name", Label: "名称", Kind: notify.TableText, Width: "22%"},
					{Key: "price", Label: "现价", Kind: notify.TableText, Width: "12%", Align: "right"},
					{Key: "high", Label: "预测高", Kind: notify.TableChange, Width: "18%", Align: "right"},
					{Key: "low", Label: "预测低", Kind: notify.TableChange, Width: "18%", Align: "right"},
					{Key: "close", Label: "预测收", Kind: notify.TableChange, Width: "18%", Align: "right"},
					{Key: "signal", Label: "信号", Kind: notify.TableChange, Width: "12%", Align: "center"},
				},
				Rows: predictionRows(preds, data.Names),
			},
		},
	}
	sections = append(sections, modelSection(model))
	return notify.Message{
		Version:        2,
		EventID:        fmt.Sprintf("intraday:%s:%d", date, targetEnd.Unix()),
		DisplayEventID: fmt.Sprintf("intraday:%s", date),
		PartLabel:      "5分钟预测",
		Title:          "5 分钟价格预测",
		Subtitle:       fmt.Sprintf("%s %s · 龙头池 · 实验性", date, window),
		HeaderIcon:     "insert-chart_outlined",
		WidthMode:      "fill",
		Template:       "orange",
		TrimPolicy:     notify.TrimNone,
		ExpiresAt:      targetEnd,
		Sections:       sections,
	}, nil
}

func predictionWindow(preds []Prediction, targetEnd time.Time) string {
	target := targetEnd.Format("15:04")
	if len(preds) > 0 && !preds[0].InputEnd.IsZero() {
		return preds[0].InputEnd.Format("15:04") + " → " + target
	}
	return target
}

func BuildDeviationCard(date string, targetEnd, expiresAt time.Time, model ModelInfo, previous []Prediction, actuals []Actual, names map[string]string) (notify.Message, error) {
	if len(previous) == 0 {
		return notify.Message{}, errors.New("deviation card requires at least one previous prediction")
	}
	comparisons := matchPrevious(previous, actuals)
	if len(comparisons) == 0 {
		return notify.Message{}, errors.New("deviation card has no rows")
	}
	if expiresAt.IsZero() {
		expiresAt = targetEnd.Add(5 * time.Minute)
	}
	confirmed, meanCloseError := previousSummary(comparisons)
	window := predictionWindow(previous, targetEnd)
	return notify.Message{
		Version:        2,
		EventID:        fmt.Sprintf("intraday:%s:%d:actual", date, targetEnd.Unix()),
		DisplayEventID: fmt.Sprintf("intraday:%s:actual", date),
		PartLabel:      "上轮偏差",
		Title:          "上轮预测偏差",
		Subtitle:       fmt.Sprintf("%s %s · 龙头池 · 实验性", date, window),
		HeaderIcon:     "insert-chart_outlined",
		WidthMode:      "fill",
		Template:       "orange",
		TrimPolicy:     notify.TrimNone,
		ExpiresAt:      expiresAt,
		Sections: []notify.Section{
			{
				Fields: deviationSummaryFields(comparisons, confirmed, meanCloseError, window),
			},
			{
				Heading: "✅ 上一根实际对比",
				Lines:   []string{fmt.Sprintf("%s 实际 vs 预测 · 偏差 = 实际 − 预测", targetEnd.Format("15:04"))},
				Table: &notify.Table{
					PageSize:          20,
					RowHeight:         "low",
					FreezeFirstColumn: true,
					Columns: []notify.TableColumn{
						{Key: "name", Label: "名称", Kind: notify.TableText, Width: "18%"},
						{Key: "actual_high", Label: "实际高", Kind: notify.TableText, Width: "13%", Align: "right"},
						{Key: "actual_low", Label: "实际低", Kind: notify.TableText, Width: "13%", Align: "right"},
						{Key: "actual_close", Label: "实际收", Kind: notify.TableText, Width: "13%", Align: "right"},
						{Key: "high_diff", Label: "高偏差", Kind: notify.TableChange, Width: "14%", Align: "right"},
						{Key: "low_diff", Label: "低偏差", Kind: notify.TableChange, Width: "14%", Align: "right"},
						{Key: "close_diff", Label: "收偏差", Kind: notify.TableChange, Width: "15%", Align: "right"},
					},
					Rows: previousResultRows(comparisons, names),
				},
			},
			modelSection(model),
		},
	}, nil
}

func deviationSummaryFields(comparisons []previousComparison, confirmed int, meanCloseError float64, window string) []notify.Field {
	errorText := "待确认"
	if confirmed > 0 {
		errorText = fmt.Sprintf("%.2fpp", meanCloseError*100)
	}
	return []notify.Field{
		{Label: "对比窗口", Value: window},
		{Label: "对比标的", Value: fmt.Sprintf("%d 只", len(comparisons))},
		{Label: "已确认", Value: fmt.Sprintf("%d / %d", confirmed, len(comparisons))},
		{Label: "平均收盘误差", Value: errorText},
	}
}

func predictionSummaryFields(preds []Prediction, window string) []notify.Field {
	return []notify.Field{
		{Label: "预测窗口", Value: window},
		{Label: "预测标的", Value: fmt.Sprintf("%d 只", len(preds))},
	}
}

func predictionRows(preds []Prediction, names map[string]string) [][]notify.TableCell {
	rows := make([][]notify.TableCell, 0, len(preds))
	for _, prediction := range preds {
		high := prediction.BasePrice * (1 + prediction.HighPct)
		low := prediction.BasePrice * (1 + prediction.LowPct)
		closePrice := prediction.BasePrice * (1 + prediction.ClosePct)
		signal := prediction.Trend
		if prediction.Confidence != "" {
			if signal != "" {
				signal += " · "
			}
			signal += prediction.Confidence
		}
		if signal == "" {
			signal = "—"
		}
		rows = append(rows, []notify.TableCell{
			{Text: stockName(prediction.Code, names)},
			{Text: fmt.Sprintf("%.2f", prediction.BasePrice)},
			{Text: fmt.Sprintf("%.2f (%s)", high, pct(prediction.HighPct)), Tone: tone(prediction.HighPct)},
			{Text: fmt.Sprintf("%.2f (%s)", low, pct(prediction.LowPct)), Tone: tone(prediction.LowPct)},
			{Text: fmt.Sprintf("%.2f (%s)", closePrice, pct(prediction.ClosePct)), Tone: tone(prediction.ClosePct)},
			{Text: signal, Tone: trendTone(prediction.Trend)},
		})
	}
	return rows
}

func matchPrevious(previous []Prediction, actuals []Actual) []previousComparison {
	actualByKey := make(map[string]Actual, len(actuals))
	for _, actual := range actuals {
		actualByKey[predictionKey(actual.Code, actual.ModelID)] = actual
	}
	out := make([]previousComparison, 0, len(previous))
	for _, prediction := range previous {
		actual, ok := actualByKey[predictionKey(prediction.Code, prediction.ModelID)]
		out = append(out, previousComparison{
			Prediction: prediction,
			Actual:     actual,
			Resolved:   ok && actual.Status == "resolved" && prediction.BasePrice > 0,
		})
	}
	return out
}

func previousSummary(comparisons []previousComparison) (int, float64) {
	confirmed := 0
	sum := 0.0
	for _, comparison := range comparisons {
		if comparison.Resolved {
			confirmed++
			sum += math.Abs(comparison.Actual.ClosePct - comparison.Prediction.ClosePct)
		}
	}
	if confirmed == 0 {
		return 0, 0
	}
	return confirmed, sum / float64(confirmed)
}

func previousResultRows(comparisons []previousComparison, names map[string]string) [][]notify.TableCell {
	rows := make([][]notify.TableCell, 0, len(comparisons))
	for _, comparison := range comparisons {
		prediction := comparison.Prediction
		actual := comparison.Actual
		if !comparison.Resolved {
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

func modelSection(model ModelInfo) notify.Section {
	samples := "—"
	mae := "—"
	if model.Metrics.Samples > 0 {
		samples = fmt.Sprintf("%d", model.Metrics.Samples)
		mae = fmt.Sprintf("高 %.2f%% · 低 %.2f%% · 收 %.2f%%",
			model.Metrics.MAE.High*100, model.Metrics.MAE.Low*100, model.Metrics.MAE.Close*100)
	}
	lines := []string{"基准价=上一根 5 分钟收盘；预测为下一根 K 线的点估计。"}
	if model.Metrics.Samples > 0 {
		lines = append(lines, fmt.Sprintf("基线 MAE：高 %.2f%% · 低 %.2f%% · 收 %.2f%%",
			model.Metrics.BaselineLastMAE.High*100, model.Metrics.BaselineLastMAE.Low*100, model.Metrics.BaselineLastMAE.Close*100))
	}
	if warning := modelWarning(model); warning != "" {
		lines = append(lines, warning)
	}
	lines = append(lines, "实验性预测，不构成投资建议；T+1 手动交易。")
	return notify.Section{
		Heading:  "⚙️ 模型状态",
		Optional: true,
		Fields: []notify.Field{
			{Label: "模型", Value: valueOrDash(model.ModelID)},
			{Label: "训练截止", Value: valueOrDash(model.DataTo)},
			{Label: "训练样本", Value: samples},
			{Label: "验证 MAE", Value: mae},
		},
		Lines: lines,
	}
}

func modelWarning(model ModelInfo) string {
	if model.Metrics.Samples <= 0 {
		return ""
	}
	warnings := []string{}
	if !betterThanBaseline(model.Metrics.MAE.High, model.Metrics.BaselineLastMAE.High) ||
		!betterThanBaseline(model.Metrics.MAE.Low, model.Metrics.BaselineLastMAE.Low) ||
		!betterThanBaseline(model.Metrics.MAE.Close, model.Metrics.BaselineLastMAE.Close) {
		warnings = append(warnings, "⚠️ 未优于基线")
	}
	if model.Metrics.Samples < 5000 {
		warnings = append(warnings, "⚠️ 样本不足")
	}
	return strings.Join(warnings, " · ")
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
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
