package report

import (
	"fmt"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/articlechart"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const articleChartTokenPrefix = "[[chart:"

func writeArticleChart(out *strings.Builder, candidate *strategy.Candidate) {
	if candidate == nil || candidate.Chart == nil {
		return
	}
	out.WriteString(articleChartTokenPrefix + candidate.Chart.ID + "]]\n\n")
}

func articleCharts(candidates []*strategy.Candidate) []articlechart.Spec {
	charts := make([]articlechart.Spec, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate == nil || candidate.Chart == nil || seen[candidate.Chart.ID] {
			continue
		}
		spec := *candidate.Chart
		spec.Caption = articleChartCaption(spec)
		charts = append(charts, spec)
		seen[spec.ID] = true
	}
	return charts
}

func articleChartCaption(spec articlechart.Spec) string {
	adjust := map[string]string{"qfq": "前复权", "hfq": "后复权", "none": "不复权"}[strings.ToLower(spec.Adjust)]
	if adjust == "" {
		adjust = articleValue(spec.Adjust, "复权口径未记录")
	}
	note := ""
	if adjust != "不复权" {
		note = "；复权价可能与文中参考价不同"
	}
	return fmt.Sprintf("%s 日K结构图（%s）；橙线缠论笔，蓝区笔重叠区，紫线波浪价位%s。", spec.Code, adjust, note)
}

func stripArticleChartTokens(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "#### 结构图谱" || strings.HasPrefix(trimmed, articleChartTokenPrefix) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
