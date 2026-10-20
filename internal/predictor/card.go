package predictor

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
)

func BuildCard(date string, targetEnd time.Time, model ModelInfo, preds []Prediction) (notify.Message, error) {
	if len(preds) == 0 {
		return notify.Message{}, errors.New("predictor card requires at least one prediction")
	}
	rows := make([][]notify.TableCell, 0, len(preds))
	for _, p := range preds {
		high := p.BasePrice * (1 + p.HighPct)
		low := p.BasePrice * (1 + p.LowPct)
		closePrice := p.BasePrice * (1 + p.ClosePct)
		rows = append(rows, []notify.TableCell{
			{Text: p.Code},
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
	return notify.Message{
		Version:        2,
		EventID:        fmt.Sprintf("intraday:%s:%d", date, targetEnd.Unix()),
		DisplayEventID: fmt.Sprintf("intraday:%s", date),
		PartLabel:      "5分钟预测",
		Title:          fmt.Sprintf("A股龙头池 · %s 5分钟预测（实验）", targetEnd.Format("15:04")),
		Template:       "orange",
		TrimPolicy:     notify.TrimNone,
		ExpiresAt:      targetEnd,
		Sections: []notify.Section{
			{
				Lines: []string{meta},
				Table: &notify.Table{
					PageSize:          20,
					RowHeight:         "low",
					FreezeFirstColumn: true,
					Columns: []notify.TableColumn{
						{Key: "code", Label: "代码", Kind: notify.TableText, Width: "100px"},
						{Key: "price", Label: "现价", Kind: notify.TableText, Width: "64px"},
						{Key: "high", Label: "预测高", Kind: notify.TableChange, Width: "110px"},
						{Key: "low", Label: "预测低", Kind: notify.TableChange, Width: "110px"},
						{Key: "close", Label: "预测收", Kind: notify.TableChange, Width: "110px"},
						{Key: "trend", Label: "趋势", Kind: notify.TableStatus, Width: "64px"},
						{Key: "confidence", Label: "置信", Kind: notify.TableStatus, Width: "56px"},
					},
					Rows: rows,
				},
			},
			{
				Heading:  "模型状态",
				Optional: true,
				Lines: []string{
					mae,
					"实验性预测，不构成投资建议；T+1 手动交易。",
					"预测为点估计，不代表保证区间；数据延迟或模型过期时不推送。",
				},
			},
		},
	}, nil
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
