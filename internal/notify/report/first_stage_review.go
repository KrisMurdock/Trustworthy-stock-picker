package report

import (
	"fmt"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
)

func FirstStageReviewCard(report firststage.ReviewReport) (notify.Message, error) {
	expires, err := time.ParseInLocation("2006-01-02 15:04", report.EvaluationDate+" 23:59", market.Shanghai)
	if err != nil {
		return notify.Message{}, err
	}
	expires = expires.Add(72 * time.Hour)
	monthDay := report.EvaluationDate
	if len(monthDay) > 5 {
		monthDay = monthDay[len(monthDay)-5:]
	}
	title := fmt.Sprintf("A股龙头池 · %s 初筛回看 · 3日触达召回率", monthDay)
	windowStart := report.WindowStart
	if windowStart == "" {
		windowStart = report.EvaluationDate
	}
	lines := []string{
		fmt.Sprintf("初筛日 %s · %d日窗口 %s～%s · 评估范围 Top %d", report.SignalDate, report.HorizonDays, windowStart, report.EvaluationDate, report.Selected),
		fmt.Sprintf("判定口径：窗口内最高价相对初筛日收盘涨幅 ≥ %.2f%%；期末收盘涨幅仅作参考，不决定命中。", report.ThresholdPct),
		recallLine(report),
		fmt.Sprintf("Top %d 窗口命中率：%.2f%%（%d/%d）", report.Selected, report.SelectionHitRatePct, report.Hits, report.Evaluated),
	}
	if report.Unavailable > 0 {
		lines = append(lines, fmt.Sprintf("Top %d 中 %d 只因信号日或窗口行情缺失未计入命中率。", report.Selected, report.Unavailable))
	}
	lines = append(lines, "结果仅为第一层初筛复盘，不构成交易建议。")

	section := notify.Section{Heading: "3日窗口回看", Lines: lines}
	rows := make([][]notify.TableCell, 0, report.Misses+report.Unavailable)
	for _, candidate := range report.Candidates {
		if candidate.Correct && candidate.Status == "hit" {
			continue
		}
		windowReturn, closeReturn := "-", "-"
		if candidate.Status == "hit" || candidate.Status == "miss" {
			windowReturn = fmt.Sprintf("%.2f%%", candidate.WindowHighReturnPct)
			closeReturn = fmt.Sprintf("%.2f%%", candidate.HorizonCloseReturn)
		}
		rows = append(rows, []notify.TableCell{
			{Text: fmt.Sprintf("%d", candidate.Rank)},
			{Text: escape(candidate.Name)},
			{Text: candidate.Code},
			{Text: windowReturn},
			{Text: closeReturn},
			{Text: reviewStatusText(candidate.Status)},
		})
	}
	if len(rows) > 0 {
		section.Table = &notify.Table{
			PageSize:          30,
			RowHeight:         "middle",
			FreezeFirstColumn: true,
			Columns: []notify.TableColumn{
				{Key: "rank", Label: "排名", Kind: notify.TableText, Width: "8%"},
				{Key: "stock", Label: "标的", Kind: notify.TableText, Width: "30%"},
				{Key: "code", Label: "代码", Kind: notify.TableText, Width: "17%"},
				{Key: "window", Label: "3日最高", Kind: notify.TableChange, Width: "16%"},
				{Key: "close", Label: "末日收盘", Kind: notify.TableChange, Width: "16%"},
				{Key: "status", Label: "结果", Kind: notify.TableStatus, Width: "13%"},
			},
			Rows: rows,
		}
	}

	eventID := fmt.Sprintf("first-stage-review:%s:%s", report.SignalDate, report.EvaluationDate)
	return notify.Message{
		Version:        2,
		EventID:        eventID,
		DisplayEventID: eventID,
		PartLabel:      "3日初筛回看",
		Title:          title,
		Sections:       []notify.Section{section},
		Template:       "blue",
		TrimPolicy:     notify.TrimNone,
		ExpiresAt:      expires,
	}, nil
}

func recallLine(report firststage.ReviewReport) string {
	if !report.RecallAvailable {
		return fmt.Sprintf("3日触达召回率：不适用（全池 %d 只均未在窗口内达到 +%.2f%%）", report.PoolEvaluated, report.ThresholdPct)
	}
	return fmt.Sprintf("3日触达召回率：%.2f%%（Top %d 命中 %d / 全池达标 %d）", report.RecallPct, report.Selected, report.Hits, report.PoolHits)
}

func reviewStatusText(status string) string {
	switch status {
	case "hit":
		return "命中"
	case "miss":
		return "未命中"
	case "missing_signal":
		return "缺信号日"
	case "missing_horizon":
		return "缺窗口行情"
	case "adjustment_mismatch":
		return "复权不符"
	default:
		return "缺行情"
	}
}
