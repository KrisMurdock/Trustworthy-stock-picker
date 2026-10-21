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
	title := fmt.Sprintf("A股龙头池 · %s 初筛回看 · 收盘召回率", monthDay)
	lines := []string{
		fmt.Sprintf("初筛日 %s · 评估日 %s · 评估范围 Top %d", report.SignalDate, report.EvaluationDate, report.Selected),
		fmt.Sprintf("判定口径：评估日收盘价相对初筛日收盘价涨幅 ≥ %.2f%%；盘中最高价触达仅作参考。", report.ThresholdPct),
		recallLine(report),
		fmt.Sprintf("Top %d 收盘命中率：%.2f%%（%d/%d）", report.Selected, report.SelectionHitRatePct, report.Hits, report.Evaluated),
		touchLine(report),
	}
	if report.Unavailable > 0 {
		lines = append(lines, fmt.Sprintf("Top %d 中 %d 只因信号日或评估日行情缺失未计入命中率。", report.Selected, report.Unavailable))
	}
	lines = append(lines, "结果仅为第一层初筛复盘，不构成交易建议。")

	section := notify.Section{Heading: "次日收盘回看", Lines: lines}
	rows := make([][]notify.TableCell, 0, report.Misses+report.Unavailable)
	for _, candidate := range report.Candidates {
		if candidate.Correct && candidate.Status == "hit" {
			continue
		}
		closeReturn, highReturn := "-", "-"
		if candidate.Status == "hit" || candidate.Status == "miss" {
			closeReturn = fmt.Sprintf("%.2f%%", candidate.CloseReturnPct)
			highReturn = fmt.Sprintf("%.2f%%", candidate.HighReturnPct)
		}
		rows = append(rows, []notify.TableCell{
			{Text: fmt.Sprintf("%d", candidate.Rank)},
			{Text: escape(candidate.Name)},
			{Text: candidate.Code},
			{Text: closeReturn},
			{Text: highReturn},
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
				{Key: "close", Label: "收盘涨幅", Kind: notify.TableChange, Width: "16%"},
				{Key: "high", Label: "最高涨幅", Kind: notify.TableChange, Width: "16%"},
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
		PartLabel:      "初筛回看",
		Title:          title,
		Sections:       []notify.Section{section},
		Template:       "blue",
		TrimPolicy:     notify.TrimNone,
		ExpiresAt:      expires,
	}, nil
}

func recallLine(report firststage.ReviewReport) string {
	if !report.RecallAvailable {
		return fmt.Sprintf("收盘召回率：不适用（全池 %d 只均未达到收盘 +%.2f%%）", report.PoolEvaluated, report.ThresholdPct)
	}
	return fmt.Sprintf("收盘召回率：%.2f%%（命中 %d / 全池收盘达标 %d）", report.RecallPct, report.Hits, report.PoolHits)
}

func touchLine(report firststage.ReviewReport) string {
	if !report.TouchRecallAvailable {
		return fmt.Sprintf("盘中触达参考：%d/%d（全池无最高价触达）", report.TouchHits, report.Evaluated)
	}
	return fmt.Sprintf("盘中触达参考：%d/%d；触达召回率 %.2f%%（全池触达 %d）", report.TouchHits, report.Evaluated, report.TouchRecallPct, report.PoolTouchHits)
}

func reviewStatusText(status string) string {
	switch status {
	case "hit":
		return "命中"
	case "miss":
		return "未命中"
	case "missing_signal":
		return "缺信号日"
	case "missing_evaluation":
		return "缺评估日"
	case "adjustment_mismatch":
		return "复权不符"
	default:
		return "缺行情"
	}
}
