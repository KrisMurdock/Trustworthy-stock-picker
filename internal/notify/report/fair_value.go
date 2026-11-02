package report

import (
	"fmt"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
)

func articleFairValue(r selection.Report, code string) (fairvalue.Estimate, bool) {
	if r.FairValues == nil || code == "" {
		return fairvalue.Estimate{}, false
	}
	estimate, ok := r.FairValues[code]
	if !ok || estimate.Code != "" && estimate.Code != code {
		return fairvalue.Estimate{}, false
	}
	return estimate, true
}

func articleFairValueSummary(r selection.Report, code string) string {
	estimate, ok := articleFairValue(r, code)
	if !ok {
		return "暂缺（估值缺失）"
	}
	if !estimate.Available() {
		return "暂缺（" + articleValue(estimate.Reason, "财务估值条件不足") + "）"
	}
	summary := fmt.Sprintf("%.2f~%.2f 元（中枢 %.2f，%s", estimate.Lower, estimate.Upper, estimate.Central, articleFairValueUpside(estimate.UpsidePct))
	if estimate.Confidence == "low" {
		summary += "，低置信度"
	}
	return summary + "）"
}

func articleFairValueCompactSummary(r selection.Report, code string) string {
	estimate, ok := articleFairValue(r, code)
	if !ok || !estimate.Available() {
		return "暂缺"
	}
	summary := fmt.Sprintf("%.2f~%.2f", estimate.Lower, estimate.Upper)
	if estimate.Confidence == "low" {
		summary += "（低）"
	}
	return summary
}

func articleFairValueUpside(value float64) string {
	if value >= 0 {
		return fmt.Sprintf("较收盘 +%.1f%%", value)
	}
	return fmt.Sprintf("较收盘 %.1f%%", value)
}

func writeArticleFairValue(out *strings.Builder, r selection.Report, code string) {
	out.WriteString("#### 财务研究区间\n")
	estimate, ok := articleFairValue(r, code)
	if !ok || !estimate.Available() {
		reason := "财务估值快照未生成"
		if ok {
			reason = articleValue(estimate.Reason, "财务估值条件不足")
		}
		out.WriteString("- **区间**：暂缺（" + reason + "）。\n")
		out.WriteString("- **边界**：估值使用最近四季度财报快照；数据缺失或盈利为负时不补造区间。\n\n")
		return
	}
	out.WriteString("- **区间**：" + fmt.Sprintf("%.2f~%.2f 元；中枢 %.2f 元；%s。\n", estimate.Lower, estimate.Upper, estimate.Central, articleFairValueUpside(estimate.UpsidePct)))
	growth := "暂缺"
	if estimate.GrowthKnown {
		growth = fmt.Sprintf("%+.1f%%", estimate.GrowthPct)
	}
	confidence := "中"
	if estimate.Confidence == "low" {
		confidence = "低"
	}
	method := "盈利倍数与净资产倍数各占一半，按 TTM ROE 与盈利增速调整；宽度随增速和缺失数据扩大。"
	if estimate.BookValueAnchored {
	} else {
		method = "净资产或 ROE 条件不足，本次仅使用盈利倍数并按低置信度加宽。"
	}
	out.WriteString(fmt.Sprintf("- **财务口径**：TTM EPS %.2f 元；每股净资产 %.2f 元；ROE %.1f%%；盈利同比 %s；当前 PE %.1f 倍、PB %.1f 倍；置信度 %s。%s\n",
		estimate.EPS, estimate.BookValuePerShare, estimate.ROEPct, growth, estimate.PE, estimate.PB, confidence, method))
	out.WriteString("- **边界**：财务相对估值研究区间，不是目标价、收益预测或买卖指令。\n\n")
}
